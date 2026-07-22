/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	aiinfrav1alpha1 "github.com/bolin-dai/vllmservice-operator/api/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// VLLMServiceReconciler reconciles a VLLMService object
type VLLMServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const defaultProbePath = "/health"

// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
func (r *VLLMServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	vllmService := &aiinfrav1alpha1.VLLMService{}
	if err := r.Get(ctx, req.NamespacedName, vllmService); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vllmService.Name,
			Namespace: vllmService.Namespace,
		},
	}

	operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		selectorLabels := selectorLabelsForVLLMService(vllmService.Name)
		objectLabels := labelsForVLLMService(vllmService)

		deployment.Labels = objectLabels

		if deployment.Spec.Selector == nil {
			deployment.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			}
		}

		deployment.Spec.Replicas = replicasFor(vllmService)
		deployment.Spec.Template = buildPodTemplate(vllmService)
		deployment.Spec.RevisionHistoryLimit = int32Ptr(10)
		deployment.Spec.ProgressDeadlineSeconds = int32Ptr(600)

		return controllerutil.SetControllerReference(vllmService, deployment, r.Scheme)
	})

	if err != nil {
		logger.Error(err, "创建或更新 Deployment 失败")
		return ctrl.Result{}, err
	}

	logger.Info(
		"Deployment 同步完成",
		"operation", operation,
		"namespace", deployment.Namespace,
		"name", deployment.Name,
	)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vllmService.Name,
			Namespace: vllmService.Namespace,
		},
	}

	serviceOperation, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = labelsForVLLMService(vllmService)

		service.Spec.Type = corev1.ServiceTypeClusterIP
		service.Spec.Selector = selectorLabelsForVLLMService(vllmService.Name)
		service.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       portFor(vllmService),
				TargetPort: intstr.FromString("http"),
			},
		}

		return controllerutil.SetControllerReference(vllmService, service, r.Scheme)
	})

	if err != nil {
		logger.Error(err, "创建或更新 Service 失败")
		return ctrl.Result{}, err
	}

	logger.Info(
		"Service 同步完成",
		"operation", serviceOperation,
		"namespace", service.Namespace,
		"name", service.Name,
	)

	/*
		同步ServiceMonitor, 这里不立即返回monitoringErr
		而是先继续计算并更新MonitoringReady Condition.
		这样用户可以从status.conditions中看到失败原因。
	*/
	serviceMonitor, monitoringMessage, monitoringErr := r.reconcileServiceMonitor(ctx, vllmService, service)

	if monitoringErr != nil {
		logger.Error(
			monitoringErr,
			"同步ServiceMonitor失败",
		)
	}

	httpRoute, routeMessage, requeueAfter, err := r.reconcileHTTPRoute(ctx, vllmService, service)
	if err != nil {
		logger.Error(err, "同步 HTTPRoute 失败")
		return ctrl.Result{}, err
	}

	if err := r.updateVLLMServiceStatus(
		ctx,
		vllmService,
		deployment,
		service,
		httpRoute,
		serviceMonitor,
		routeMessage,
		monitoringMessage,
	); err != nil {
		logger.Error(err, "更新 VLLMService status 失败")
		return ctrl.Result{}, err
	}

	if monitoringErr != nil {
		return ctrl.Result{}, monitoringErr
	}

	if !apimeta.IsStatusConditionTrue(
		vllmService.Status.Conditions,
		aiinfrav1alpha1.VLLMServiceConditionAvailable,
	) {
		const statusRefreshInterval = 15 * time.Second

		if requeueAfter == 0 || requeueAfter > statusRefreshInterval {
			requeueAfter = statusRefreshInterval
		}
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func (r *VLLMServiceReconciler) reconcileHTTPRoute(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
	service *corev1.Service,
) (*gatewayv1.HTTPRoute, string, time.Duration, error) {
	logger := log.FromContext(ctx)

	if !gatewayRefEnabled(vllmService) {
		if err := r.deleteOwnedHTTPRouteIfExists(ctx, vllmService); err != nil {
			return nil, "", 0, err
		}
		return nil, "", 0, nil
	}

	gateway, routeMessage, requeueAfter, err := r.resolveGatewayRef(ctx, vllmService)
	if err != nil {
		return nil, "", 0, err
	}

	if gateway == nil {
		if err := r.deleteOwnedHTTPRouteIfExists(ctx, vllmService); err != nil {
			return nil, routeMessage, requeueAfter, err
		}
		return nil, routeMessage, requeueAfter, nil
	}

	httpRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vllmService.Name,
			Namespace: vllmService.Namespace,
		},
	}

	httpRouteOperation, err := controllerutil.CreateOrUpdate(ctx, r.Client, httpRoute, func() error {
		httpRoute.Labels = labelsForVLLMService(vllmService)

		sectionName := gatewayv1.SectionName(vllmService.Spec.GatewayRef.SectionName)

		parentRef := gatewayv1.ParentReference{
			Name:        gatewayv1.ObjectName(vllmService.Spec.GatewayRef.Name),
			SectionName: &sectionName,
		}

		if vllmService.Spec.GatewayRef.Namespace != "" {
			gatewayNamespace := gatewayv1.Namespace(vllmService.Spec.GatewayRef.Namespace)
			parentRef.Namespace = &gatewayNamespace
		}

		hostname := gatewayv1.Hostname(vllmService.Spec.GatewayRef.Host)
		backendPort := gatewayv1.PortNumber(portFor(vllmService))

		httpRoute.Spec = gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					parentRef,
				},
			},
			Hostnames: []gatewayv1.Hostname{
				hostname,
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(service.Name),
									Port: &backendPort,
								},
							},
						},
					},
				},
			},
		}

		return controllerutil.SetControllerReference(vllmService, httpRoute, r.Scheme)
	})

	if err != nil {
		return nil, "", 0, err
	}

	logger.Info(
		"HTTPRoute 同步完成",
		"operation", httpRouteOperation,
		"namespace", httpRoute.Namespace,
		"name", httpRoute.Name,
	)

	return httpRoute, "", 0, nil
}

func monitoringEnabled(
	vllmService *aiinfrav1alpha1.VLLMService,
) bool {
	return vllmService.Spec.Monitoring != nil &&
		vllmService.Spec.Monitoring.Enabled
}

func monitoringPathFor(
	vllmService *aiinfrav1alpha1.VLLMService,
) string {
	if vllmService.Spec.Monitoring == nil ||
		vllmService.Spec.Monitoring.Path == "" {
		return "/metrics"
	}

	return vllmService.Spec.Monitoring.Path

}

func monitoringIntervalFor(
	vllmService *aiinfrav1alpha1.VLLMService,
) string {
	if vllmService.Spec.Monitoring == nil ||
		vllmService.Spec.Monitoring.Interval == "" {
		return "30s"
	}

	return vllmService.Spec.Monitoring.Interval
}

func serviceMonitorLabelsForVLLMService(
	vllmService *aiinfrav1alpha1.VLLMService,
) map[string]string {
	labels := make(map[string]string)

	if vllmService.Spec.Monitoring != nil {
		for key, value := range vllmService.Spec.Monitoring.Labels {
			labels[key] = value
		}
	}

	labels["app.kubernetes.io/name"] = "vllmservice"
	labels["app.kubernetes.io/instance"] = vllmService.Name
	labels["app.kubernetes.io/managed-by"] = "vllmservice-operator"

	return labels
}

func (r *VLLMServiceReconciler) reconcileServiceMonitor(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
	service *corev1.Service,
) (*monitoringv1.ServiceMonitor, string, error) {
	logger := log.FromContext(ctx)

	/*
		只有monitoring.enabled=true 时才需要ServiceMonitor,
		如果用户把enabled从true改成false，或者直接删除monitoring配置，
		operator会删除之前由自己创建的ServiceMOnitor
	*/
	if !monitoringEnabled(vllmService) {
		if err := r.deleteOwnedServiceMonitorIfExists(
			ctx,
			vllmService,
		); err != nil {
			message := fmt.Sprintf(
				"删除不再需要的ServiceMonitor失败 %v",
				err,
			)
			return nil, message, err
		}
		return nil, "", nil
	}

	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vllmService.Name,
			Namespace: vllmService.Namespace,
		},
	}

	operation, err := controllerutil.CreateOrUpdate(
		ctx,
		r.Client,
		serviceMonitor,
		func() error {
			/*
				如果已经存在一个同名ServiceMonitor， 但不是当前VLLMService管理的资源，则禁止直接接管或覆盖它。
			*/
			if serviceMonitor.ResourceVersion != "" &&
				!metav1.IsControlledBy(serviceMonitor, vllmService) {
				return fmt.Errorf(
					"同名ServiceMonitor %s/%s 已存在，但不属于当前VLLMService",
					serviceMonitor.Namespace,
					serviceMonitor.Name,
				)
			}

			serviceMonitor.Labels = serviceMonitorLabelsForVLLMService(vllmService)

			serviceMonitor.Spec = monitoringv1.ServiceMonitorSpec{
				/*
					这里选择的是Service.metadata.Labels
				*/
				Selector: metav1.LabelSelector{
					MatchLabels: selectorLabelsForVLLMService(
						vllmService.Name,
					),
				},

				/*
					ServiceMonitor与service位于同一个命名空间

				*/
				NamespaceSelector: monitoringv1.NamespaceSelector{
					MatchNames: []string{
						service.Namespace,
					},
				},

				Endpoints: []monitoringv1.Endpoint{
					{
						/*
							Port填写的是Service port名称，不是数字端口8000
						*/
						Port: "http",

						Path: monitoringPathFor(vllmService),

						Scheme: "http",

						Interval: monitoringv1.Duration(
							monitoringIntervalFor(vllmService),
						),
					},
				},
			}

			return controllerutil.SetControllerReference(
				vllmService,
				serviceMonitor,
				r.Scheme,
			)

		},
	)

	if err != nil {
		message := fmt.Sprintf(
			"创建或更新 ServiceMonitor %s/%s 失败： %v",
			vllmService.Namespace,
			vllmService.Name,
			err,
		)
		return nil, message, err
	}

	logger.Info(
		"ServiceMonitor 同步完成",
		"operation", operation,
		"namespace", serviceMonitor.Namespace,
		"name", serviceMonitor.Name,
	)

	return serviceMonitor, "", nil

}

func (r *VLLMServiceReconciler) deleteOwnedServiceMonitorIfExists(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
) error {
	logger := log.FromContext(ctx)

	serviceMonitor := &monitoringv1.ServiceMonitor{}

	err := r.Get(
		ctx,
		client.ObjectKey{
			Name:      vllmService.Name,
			Namespace: vllmService.Namespace,
		},
		serviceMonitor,
	)

	if apierrors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if !metav1.IsControlledBy(serviceMonitor, vllmService) {
		logger.Info(
			"发现同名 ServiceMonitor, 但它不是当前VLLMService控制的资源,跳过删除",
			"namespace", serviceMonitor.Namespace,
			"name", serviceMonitor.Name,
		)
		return nil
	}

	if err := r.Delete(ctx, serviceMonitor); err != nil {
		return err
	}

	logger.Info(
		"ServiceMonitor 已删除",
		"namespace", serviceMonitor.Namespace,
		"name", serviceMonitor.Name,
	)

	return nil

}

func (r *VLLMServiceReconciler) resolveGatewayRef(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
) (*gatewayv1.Gateway, string, time.Duration, error) {
	gatewayNamespace := gatewayRefNamespaceFor(vllmService)
	gatewayName := vllmService.Spec.GatewayRef.Name
	sectionName := vllmService.Spec.GatewayRef.SectionName

	gateway := &gatewayv1.Gateway{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      gatewayName,
		Namespace: gatewayNamespace,
	}, gateway)

	if apierrors.IsNotFound(err) {
		message := fmt.Sprintf("引用的 Gateway 不存在：%s/%s", gatewayNamespace, gatewayName)
		return nil, message, time.Minute, nil
	}

	if err != nil {
		return nil, "", 0, err
	}

	listener, found := findGatewayListener(gateway, sectionName)
	if !found {
		message := fmt.Sprintf(
			"引用的 Gateway 存在，但找不到 listener：gateway=%s/%s sectionName=%s",
			gatewayNamespace,
			gatewayName,
			sectionName,
		)
		return nil, message, time.Minute, nil
	}

	if listener.Protocol != gatewayv1.HTTPProtocolType &&
		listener.Protocol != gatewayv1.HTTPSProtocolType {
		message := fmt.Sprintf(
			"引用的 Gateway listener 协议不是 HTTP/HTTPS：gateway=%s/%s sectionName=%s protocol=%s",
			gatewayNamespace,
			gatewayName,
			sectionName,
			listener.Protocol,
		)
		return nil, message, 0, nil
	}

	return gateway, "", 0, nil
}

func gatewayRefEnabled(vllmService *aiinfrav1alpha1.VLLMService) bool {
	return vllmService.Spec.GatewayRef != nil
}

func gatewayRefNamespaceFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.GatewayRef.Namespace == "" {
		return vllmService.Namespace
	}
	return vllmService.Spec.GatewayRef.Namespace
}

func findGatewayListener(gateway *gatewayv1.Gateway, sectionName string) (gatewayv1.Listener, bool) {
	for _, listener := range gateway.Spec.Listeners {
		if string(listener.Name) == sectionName {
			return listener, true
		}
	}

	return gatewayv1.Listener{}, false
}

func (r *VLLMServiceReconciler) deleteOwnedHTTPRouteIfExists(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
) error {
	logger := log.FromContext(ctx)

	httpRoute := &gatewayv1.HTTPRoute{}

	err := r.Get(ctx, client.ObjectKey{
		Name:      vllmService.Name,
		Namespace: vllmService.Namespace,
	}, httpRoute)

	if apierrors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if !metav1.IsControlledBy(httpRoute, vllmService) {
		logger.Info(
			"发现同名 HTTPRoute，但它不是当前 VLLMService 控制的资源，跳过删除",
			"namespace", httpRoute.Namespace,
			"name", httpRoute.Name,
		)
		return nil
	}

	if err := r.Delete(ctx, httpRoute); err != nil {
		return err
	}

	logger.Info(
		"HTTPRoute 已删除",
		"namespace", httpRoute.Namespace,
		"name", httpRoute.Name,
	)

	return nil
}

func buildPodTemplate(vllmService *aiinfrav1alpha1.VLLMService) corev1.PodTemplateSpec {
	objectLabels := labelsForVLLMService(vllmService)

	container := buildVLLMContainer(vllmService)

	volumes, volumeMounts := buildModelVolumesAndMounts(vllmService)
	container.VolumeMounts = volumeMounts

	schedulerName := corev1.DefaultSchedulerName
	if vllmService.Spec.SchedulerName != "" {
		schedulerName = vllmService.Spec.SchedulerName
	}

	podSpec := corev1.PodSpec{
		Containers:                    []corev1.Container{container},
		Volumes:                       volumes,
		RestartPolicy:                 corev1.RestartPolicyAlways,
		DNSPolicy:                     corev1.DNSClusterFirst,
		SchedulerName:                 schedulerName,
		TerminationGracePeriodSeconds: int64Ptr(30),
		EnableServiceLinks:            boolPtr(true),
		HostIPC:                       vllmService.Spec.HostIPC,
	}

	if vllmService.Spec.RuntimeClassName != "" {
		podSpec.RuntimeClassName = &vllmService.Spec.RuntimeClassName
	}

	if len(vllmService.Spec.NodeSelector) > 0 {
		podSpec.NodeSelector = vllmService.Spec.NodeSelector
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: objectLabels,
		},
		Spec: podSpec,
	}
}

func buildVLLMContainer(vllmService *aiinfrav1alpha1.VLLMService) corev1.Container {
	port := portFor(vllmService)

	container := corev1.Container{
		Name:            "vllm",
		Image:           vllmService.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--model", vllmService.Spec.ModelPath,
			"--served-model-name", vllmService.Spec.ModelName,
			"--host", "0.0.0.0",
			"--port", fmt.Sprintf("%d", port),
			"--dtype", dtypeFor(vllmService),
			"--max-model-len", fmt.Sprintf("%d", maxModelLenFor(vllmService)),
			"--gpu-memory-utilization", gpuMemoryUtilizationFor(vllmService),
			"--max-num-seqs", fmt.Sprintf("%d", maxNumSeqsFor(vllmService)),
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: vllmService.Spec.Resources,
	}

	if startupProbeEnabled(vllmService) {
		container.StartupProbe = buildVLLMStartupProbe(vllmService)
	}
	if livenessProbeEnabled(vllmService) {
		container.LivenessProbe = buildVLLMLivenessProbe(vllmService)
	}
	if readinessProbeEnabled(vllmService) {
		container.ReadinessProbe = buildVLLMReadinessProbe(vllmService)
	}

	return container

}

func startupProbeEnabled(vllmService *aiinfrav1alpha1.VLLMService) bool {
	return vllmService.Spec.StartupProbe != nil && vllmService.Spec.StartupProbe.Enabled
}

func startupProbePathFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.StartupProbe == nil || vllmService.Spec.StartupProbe.Path == "" {
		return defaultProbePath
	}

	return vllmService.Spec.StartupProbe.Path
}

func startupProbeInitialDelaySecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.StartupProbe == nil || vllmService.Spec.StartupProbe.InitialDelaySeconds == nil {
		return 30
	}
	return *vllmService.Spec.StartupProbe.InitialDelaySeconds
}

func startupProbePeriodSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.StartupProbe == nil || vllmService.Spec.StartupProbe.PeriodSeconds == nil {
		return 10
	}
	return *vllmService.Spec.StartupProbe.PeriodSeconds
}

func startupProbeTimeoutSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.StartupProbe == nil || vllmService.Spec.StartupProbe.TimeoutSeconds == nil {
		return 5
	}
	return *vllmService.Spec.StartupProbe.TimeoutSeconds
}

func startupProbeFailureThresholdFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.StartupProbe == nil || vllmService.Spec.StartupProbe.FailureThreshold == nil {
		return 60
	}
	return *vllmService.Spec.StartupProbe.FailureThreshold
}

func buildVLLMStartupProbe(vllmService *aiinfrav1alpha1.VLLMService) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: startupProbePathFor(vllmService),
				Port: intstr.FromString("http"),
			},
		},
		InitialDelaySeconds: startupProbeInitialDelaySecondsFor(vllmService),
		PeriodSeconds:       startupProbePeriodSecondsFor(vllmService),
		TimeoutSeconds:      startupProbeTimeoutSecondsFor(vllmService),
		FailureThreshold:    startupProbeFailureThresholdFor(vllmService),
	}
}

func buildVLLMLivenessProbe(vllmService *aiinfrav1alpha1.VLLMService) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: livenessProbePathFor(vllmService),
				Port: intstr.FromString("http"),
			},
		},
		InitialDelaySeconds: livenessProbeInitialDelaySecondsFor(vllmService),
		PeriodSeconds:       livenessProbePeriodSecondsFor(vllmService),
		TimeoutSeconds:      livenessProbeTimeoutSecondsFor(vllmService),
		FailureThreshold:    livenessProbeFailureThresholdFor(vllmService),
	}
}

func livenessProbeEnabled(vllmService *aiinfrav1alpha1.VLLMService) bool {
	return vllmService.Spec.LivenessProbe != nil && vllmService.Spec.LivenessProbe.Enabled
}

func livenessProbePathFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.LivenessProbe == nil || vllmService.Spec.LivenessProbe.Path == "" {
		return defaultProbePath
	}
	return vllmService.Spec.LivenessProbe.Path
}

func livenessProbeInitialDelaySecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.LivenessProbe == nil || vllmService.Spec.LivenessProbe.InitialDelaySeconds == nil {
		return 30
	}
	return *vllmService.Spec.LivenessProbe.InitialDelaySeconds
}

func livenessProbePeriodSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.LivenessProbe == nil || vllmService.Spec.LivenessProbe.PeriodSeconds == nil {
		return 30
	}
	return *vllmService.Spec.LivenessProbe.PeriodSeconds
}

func livenessProbeTimeoutSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.LivenessProbe == nil || vllmService.Spec.LivenessProbe.TimeoutSeconds == nil {
		return 5
	}
	return *vllmService.Spec.LivenessProbe.TimeoutSeconds
}

func livenessProbeFailureThresholdFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.LivenessProbe == nil || vllmService.Spec.LivenessProbe.FailureThreshold == nil {
		return 3
	}
	return *vllmService.Spec.LivenessProbe.FailureThreshold
}

func dtypeFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.EngineArgs == nil || vllmService.Spec.EngineArgs.Dtype == "" {
		return "auto"
	}
	return vllmService.Spec.EngineArgs.Dtype
}

func maxModelLenFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.EngineArgs == nil || vllmService.Spec.EngineArgs.MaxModelLen == nil {
		return 4096
	}
	return *vllmService.Spec.EngineArgs.MaxModelLen
}

func gpuMemoryUtilizationFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.EngineArgs == nil || vllmService.Spec.EngineArgs.GPUMemoryUtilization == "" {
		return "0.75"
	}
	return vllmService.Spec.EngineArgs.GPUMemoryUtilization
}

func maxNumSeqsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.EngineArgs == nil || vllmService.Spec.EngineArgs.MaxNumSeqs == nil {
		return 8
	}
	return *vllmService.Spec.EngineArgs.MaxNumSeqs
}

func readinessProbeEnabled(vllmService *aiinfrav1alpha1.VLLMService) bool {
	return vllmService.Spec.ReadinessProbe != nil && vllmService.Spec.ReadinessProbe.Enabled
}

func readinessProbePathFor(vllmService *aiinfrav1alpha1.VLLMService) string {
	if vllmService.Spec.ReadinessProbe == nil || vllmService.Spec.ReadinessProbe.Path == "" {
		return defaultProbePath
	}
	return vllmService.Spec.ReadinessProbe.Path
}

func readinessProbeInitialDelaySecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.ReadinessProbe == nil || vllmService.Spec.ReadinessProbe.InitialDelaySeconds == nil {
		return 30
	}
	return *vllmService.Spec.ReadinessProbe.InitialDelaySeconds
}

func readinessProbePeriodSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.ReadinessProbe == nil || vllmService.Spec.ReadinessProbe.PeriodSeconds == nil {
		return 30
	}
	return *vllmService.Spec.ReadinessProbe.PeriodSeconds
}

func readinessProbeTimeoutSecondsFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.ReadinessProbe == nil || vllmService.Spec.ReadinessProbe.TimeoutSeconds == nil {
		return 5
	}
	return *vllmService.Spec.ReadinessProbe.TimeoutSeconds
}

func readinessProbeFailureThresholdFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.ReadinessProbe == nil || vllmService.Spec.ReadinessProbe.FailureThreshold == nil {
		return 3
	}
	return *vllmService.Spec.ReadinessProbe.FailureThreshold
}

func buildVLLMReadinessProbe(vllmService *aiinfrav1alpha1.VLLMService) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: readinessProbePathFor(vllmService),
				Port: intstr.FromString("http"),
			},
		},
		InitialDelaySeconds: readinessProbeInitialDelaySecondsFor(vllmService),
		PeriodSeconds:       readinessProbePeriodSecondsFor(vllmService),
		TimeoutSeconds:      readinessProbeTimeoutSecondsFor(vllmService),
		FailureThreshold:    readinessProbeFailureThresholdFor(vllmService),
	}
}

func buildModelVolumesAndMounts(vllmService *aiinfrav1alpha1.VLLMService) ([]corev1.Volume, []corev1.VolumeMount) {
	storage := vllmService.Spec.Storage

	if storage.PVCName == "" {
		return nil, nil
	}

	mountPath := storage.MountPath
	if mountPath == "" {
		mountPath = "/data/models"
	}

	readOnly := readOnlyFor(storage)

	volumeName := "model-storage"

	volumes := []corev1.Volume{
		{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: storage.PVCName,
					ReadOnly:  readOnly,
				},
			},
		},
	}

	volumeMount := corev1.VolumeMount{
		Name:      volumeName,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	}

	if storage.SubPath != "" {
		volumeMount.SubPath = storage.SubPath
	}

	volumeMounts := []corev1.VolumeMount{volumeMount}

	return volumes, volumeMounts
}

func newVLLMServiceCondition(
	vllmService *aiinfrav1alpha1.VLLMService,
	conditionType string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) metav1.Condition {
	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: vllmService.Generation,
		Reason:             reason,
		Message:            message,
	}
}

func deploymentReadyCondition(
	vllmService *aiinfrav1alpha1.VLLMService,
	deployment *appsv1.Deployment,
) metav1.Condition {
	if deployment == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
			metav1.ConditionUnknown,
			"DeploymentNotObserved",
			"尚未获取到 Deployment",
		)
	}

	desiredReplicas := int32(1)
	if deployment.Spec.Replicas != nil {
		desiredReplicas = *deployment.Spec.Replicas
	}

	if deployment.Status.ObservedGeneration < deployment.Generation {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
			metav1.ConditionUnknown,
			"DeploymentStatusStale",
			fmt.Sprintf(
				"Deployment Controller 尚未处理最新配置：observedGeneration=%d, generation=%d",
				deployment.Status.ObservedGeneration,
				deployment.Generation,
			),
		)
	}

	deploymentAvailable := false

	for _, condition := range deployment.Status.Conditions {
		switch condition.Type {
		case appsv1.DeploymentReplicaFailure:
			if condition.Status == corev1.ConditionTrue {
				return newVLLMServiceCondition(
					vllmService,
					aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
					metav1.ConditionFalse,
					"DeploymentReplicaFailure",
					condition.Message,
				)
			}

		case appsv1.DeploymentProgressing:
			if condition.Status == corev1.ConditionFalse &&
				condition.Reason == "ProgressDeadlineExceeded" {
				return newVLLMServiceCondition(
					vllmService,
					aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
					metav1.ConditionFalse,
					"ProgressDeadlineExceeded",
					condition.Message,
				)
			}

		case appsv1.DeploymentAvailable:
			if condition.Status == corev1.ConditionTrue {
				deploymentAvailable = true
			}
		}
	}

	if deploymentAvailable &&
		deployment.Status.ReadyReplicas >= desiredReplicas &&
		deployment.Status.AvailableReplicas >= desiredReplicas {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
			metav1.ConditionTrue,
			"DeploymentAvailable",
			fmt.Sprintf(
				"Deployment 已达到期望副本数：readyReplicas=%d/%d, availableReplicas=%d/%d",
				deployment.Status.ReadyReplicas,
				desiredReplicas,
				deployment.Status.AvailableReplicas,
				desiredReplicas,
			),
		)
	}

	return newVLLMServiceCondition(
		vllmService,
		aiinfrav1alpha1.VLLMServiceConditionDeploymentReady,
		metav1.ConditionFalse,
		"DeploymentProgressing",
		fmt.Sprintf(
			"Deployment 正在启动或更新：readyReplicas=%d/%d, availableReplicas=%d/%d",
			deployment.Status.ReadyReplicas,
			desiredReplicas,
			deployment.Status.AvailableReplicas,
			desiredReplicas,
		),
	)
}

func (r *VLLMServiceReconciler) storageReadyCondition(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
) (metav1.Condition, error) {
	pvcName := vllmService.Spec.Storage.PVCName

	if pvcName == "" {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionFalse,
			"PVCNameMissing",
			"spec.storage.pvcName 不能为空",
		), nil
	}

	pvc := &corev1.PersistentVolumeClaim{}

	err := r.Get(ctx, client.ObjectKey{
		Name:      pvcName,
		Namespace: vllmService.Namespace,
	}, pvc)

	if apierrors.IsNotFound(err) {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionFalse,
			"PVCNotFound",
			fmt.Sprintf(
				"PVC %s/%s 不存在",
				vllmService.Namespace,
				pvcName,
			),
		), nil
	}

	if err != nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionUnknown,
			"PVCCheckFailed",
			fmt.Sprintf("读取 PVC 状态失败：%v", err),
		), err
	}

	switch pvc.Status.Phase {
	case corev1.ClaimBound:
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionTrue,
			"PVCBound",
			fmt.Sprintf(
				"PVC %s/%s 已绑定到 PV %s",
				pvc.Namespace,
				pvc.Name,
				pvc.Spec.VolumeName,
			),
		), nil

	case corev1.ClaimLost:
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionFalse,
			"PVCLost",
			fmt.Sprintf(
				"PVC %s/%s 已丢失其绑定的 PersistentVolume",
				pvc.Namespace,
				pvc.Name,
			),
		), nil

	default:
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionStorageReady,
			metav1.ConditionFalse,
			"PVCNotBound",
			fmt.Sprintf(
				"PVC %s/%s 尚未绑定，当前状态为 %s",
				pvc.Namespace,
				pvc.Name,
				pvc.Status.Phase,
			),
		), nil
	}
}

func findHTTPRouteParentStatus(
	vllmService *aiinfrav1alpha1.VLLMService,
	httpRoute *gatewayv1.HTTPRoute,
) *gatewayv1.RouteParentStatus {
	if vllmService.Spec.GatewayRef == nil {
		return nil
	}

	expectedGatewayName := vllmService.Spec.GatewayRef.Name
	expectedGatewayNamespace := gatewayRefNamespaceFor(vllmService)
	expectedSectionName := vllmService.Spec.GatewayRef.SectionName

	for i := range httpRoute.Status.Parents {
		parentStatus := &httpRoute.Status.Parents[i]

		if string(parentStatus.ParentRef.Name) != expectedGatewayName {
			continue
		}

		parentNamespace := httpRoute.Namespace
		if parentStatus.ParentRef.Namespace != nil {
			parentNamespace = string(*parentStatus.ParentRef.Namespace)
		}

		if parentNamespace != expectedGatewayNamespace {
			continue
		}

		if expectedSectionName != "" {
			if parentStatus.ParentRef.SectionName == nil {
				continue
			}

			if string(*parentStatus.ParentRef.SectionName) != expectedSectionName {
				continue
			}
		}

		return parentStatus
	}

	return nil
}

func routeReadyCondition(
	vllmService *aiinfrav1alpha1.VLLMService,
	httpRoute *gatewayv1.HTTPRoute,
	routeMessage string,
) metav1.Condition {
	if !gatewayRefEnabled(vllmService) {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionTrue,
			"RouteNotRequired",
			"未配置 gatewayRef，不需要创建 HTTPRoute",
		)
	}

	if routeMessage != "" {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionFalse,
			"GatewayReferenceInvalid",
			routeMessage,
		)
	}

	if httpRoute == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"HTTPRoutePending",
			"HTTPRoute 尚未创建或尚未获取到状态",
		)
	}

	parentStatus := findHTTPRouteParentStatus(vllmService, httpRoute)
	if parentStatus == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"RouteParentStatusPending",
			"HTTPRoute 尚未生成对应 Gateway 的 Parent Status",
		)
	}

	accepted := apimeta.FindStatusCondition(
		parentStatus.Conditions,
		string(gatewayv1.RouteConditionAccepted),
	)

	if accepted == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"RouteAcceptedStatusPending",
			"HTTPRoute 尚未生成 Accepted Condition",
		)
	}

	if accepted.ObservedGeneration < httpRoute.Generation {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"HTTPRouteStatusStale",
			fmt.Sprintf(
				"HTTPRoute Controller 尚未处理最新配置：observedGeneration=%d, generation=%d",
				accepted.ObservedGeneration,
				httpRoute.Generation,
			),
		)
	}

	if accepted.Status != metav1.ConditionTrue {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			accepted.Status,
			"RouteNotAccepted",
			fmt.Sprintf(
				"HTTPRoute 尚未被 Gateway 接受：reason=%s, message=%s",
				accepted.Reason,
				accepted.Message,
			),
		)
	}

	resolvedRefs := apimeta.FindStatusCondition(
		parentStatus.Conditions,
		string(gatewayv1.RouteConditionResolvedRefs),
	)

	if resolvedRefs == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"ResolvedRefsStatusPending",
			"HTTPRoute 尚未生成 ResolvedRefs Condition",
		)
	}

	if resolvedRefs.ObservedGeneration < httpRoute.Generation {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			metav1.ConditionUnknown,
			"HTTPRouteStatusStale",
			fmt.Sprintf(
				"HTTPRoute ResolvedRefs 状态尚未处理最新配置：observedGeneration=%d, generation=%d",
				resolvedRefs.ObservedGeneration,
				httpRoute.Generation,
			),
		)
	}

	if resolvedRefs.Status != metav1.ConditionTrue {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionRouteReady,
			resolvedRefs.Status,
			"RouteReferencesNotResolved",
			fmt.Sprintf(
				"HTTPRoute 后端引用未正确解析：reason=%s, message=%s",
				resolvedRefs.Reason,
				resolvedRefs.Message,
			),
		)
	}

	return newVLLMServiceCondition(
		vllmService,
		aiinfrav1alpha1.VLLMServiceConditionRouteReady,
		metav1.ConditionTrue,
		"RouteAccepted",
		fmt.Sprintf(
			"HTTPRoute %s/%s 已被 Gateway 接受，后端引用已正确解析",
			httpRoute.Namespace,
			httpRoute.Name,
		),
	)
}

func monitoringReadyCondition(
	vllmService *aiinfrav1alpha1.VLLMService,
	serviceMonitor *monitoringv1.ServiceMonitor,
	monitoringMessage string,
) metav1.Condition {
	/*
		优先判断Reconcile是否发生错误，
		即使当前monitoring已关闭，也可能出现删除旧ServiceMonitor失败的情况
	*/
	if monitoringMessage != "" {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionMonitoringReady,
			metav1.ConditionFalse,
			"ServiceMonitorReconcileFailed",
			monitoringMessage,
		)
	}

	/*
		monitoring.enabled未显式设置为true时， 不创建vllmService
	*/
	if !monitoringEnabled(vllmService) {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionMonitoringReady,
			metav1.ConditionTrue,
			"MonitoringDisabled",
			"monitoring.enabled未设置为true,不需要创建serviceMonitor",
		)
	}

	if serviceMonitor == nil {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionMonitoringReady,
			metav1.ConditionUnknown,
			"ServiceMonitoringPending",
			"监控已启用,但ServiceMonitor尚未创建或尚未获取到。",
		)
	}

	return newVLLMServiceCondition(
		vllmService,
		aiinfrav1alpha1.VLLMServiceConditionMonitoringReady,
		metav1.ConditionTrue,
		"ServiceMonitorConfigured",
		fmt.Sprintf(
			"ServiceMonitor %s/%s 已配置,port=http,path=%s,interval=%s",
			serviceMonitor.Namespace,
			serviceMonitor.Name,
			monitoringPathFor(vllmService),
			monitoringIntervalFor(vllmService),
		),
	)

}

func availableCondition(
	vllmService *aiinfrav1alpha1.VLLMService,
	deploymentCondition metav1.Condition,
	storageCondition metav1.Condition,
	routeCondition metav1.Condition,
	monitoringCondition metav1.Condition,
) metav1.Condition {
	componentConditions := []metav1.Condition{
		deploymentCondition,
		storageCondition,
		routeCondition,
		monitoringCondition,
	}

	var falseConditions []string
	var unknownConditions []string

	for _, condition := range componentConditions {
		switch condition.Status {
		case metav1.ConditionFalse:
			falseConditions = append(
				falseConditions,
				fmt.Sprintf("%s(%s)", condition.Type, condition.Reason),
			)

		case metav1.ConditionUnknown:
			unknownConditions = append(
				unknownConditions,
				fmt.Sprintf("%s(%s)", condition.Type, condition.Reason),
			)
		}
	}

	if len(falseConditions) > 0 {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionAvailable,
			metav1.ConditionFalse,
			"ComponentsNotReady",
			"以下组件尚未就绪："+strings.Join(falseConditions, ", "),
		)
	}

	if len(unknownConditions) > 0 {
		return newVLLMServiceCondition(
			vllmService,
			aiinfrav1alpha1.VLLMServiceConditionAvailable,
			metav1.ConditionUnknown,
			"ComponentStatusUnknown",
			"以下组件状态尚未确定："+strings.Join(unknownConditions, ", "),
		)
	}

	return newVLLMServiceCondition(
		vllmService,
		aiinfrav1alpha1.VLLMServiceConditionAvailable,
		metav1.ConditionTrue,
		"AllComponentsReady",
		"Deployment、Storage、Route 和 Monitoring 均已就绪",
	)
}

func (r *VLLMServiceReconciler) updateVLLMServiceStatus(
	ctx context.Context,
	vllmService *aiinfrav1alpha1.VLLMService,
	deployment *appsv1.Deployment,
	service *corev1.Service,
	httpRoute *gatewayv1.HTTPRoute,
	serviceMonitor *monitoringv1.ServiceMonitor,
	routeMessage string,
	monitoringMessage string,
) error {
	before := vllmService.DeepCopy()

	phase, message := phaseAndMessageFromDeployment(deployment)

	if routeMessage != "" {
		if message != "" {
			message = message + "; " + routeMessage
		} else {
			message = routeMessage
		}
	}

	serviceName := ""
	if service != nil {
		serviceName = service.Name
	}

	serviceMonitorName := ""
	if serviceMonitor != nil {
		serviceMonitorName = serviceMonitor.Name
	}

	gatewayRefName := ""
	gatewayRefNamespace := ""

	if vllmService.Spec.GatewayRef != nil {
		gatewayRefName = vllmService.Spec.GatewayRef.Name
		gatewayRefNamespace = gatewayRefNamespaceFor(vllmService)
	}

	httpRouteName := ""
	if httpRoute != nil {
		httpRouteName = httpRoute.Name
	}

	deploymentCondition := deploymentReadyCondition(
		vllmService,
		deployment,
	)

	storageCondition, storageErr := r.storageReadyCondition(
		ctx,
		vllmService,
	)

	routeCondition := routeReadyCondition(
		vllmService,
		httpRoute,
		routeMessage,
	)

	monitoringCondition := monitoringReadyCondition(
		vllmService,
		serviceMonitor,
		monitoringMessage,
	)

	overallAvailableCondition := availableCondition(
		vllmService,
		deploymentCondition,
		storageCondition,
		routeCondition,
		monitoringCondition,
	)

	vllmService.Status.ObservedGeneration = vllmService.Generation
	vllmService.Status.Phase = phase
	vllmService.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	vllmService.Status.DeploymentName = deployment.Name
	vllmService.Status.ServiceName = serviceName
	vllmService.Status.ServiceMonitorName = serviceMonitorName
	vllmService.Status.GatewayRefName = gatewayRefName
	vllmService.Status.GatewayRefNamespace = gatewayRefNamespace
	vllmService.Status.HTTPRouteName = httpRouteName
	vllmService.Status.Message = message

	apimeta.SetStatusCondition(
		&vllmService.Status.Conditions,
		deploymentCondition,
	)

	apimeta.SetStatusCondition(
		&vllmService.Status.Conditions,
		storageCondition,
	)

	apimeta.SetStatusCondition(
		&vllmService.Status.Conditions,
		routeCondition,
	)

	apimeta.SetStatusCondition(
		&vllmService.Status.Conditions,
		monitoringCondition,
	)

	apimeta.SetStatusCondition(
		&vllmService.Status.Conditions,
		overallAvailableCondition,
	)

	if apiequality.Semantic.DeepEqual(
		before.Status,
		vllmService.Status,
	) {
		return storageErr
	}

	if err := r.Status().Patch(
		ctx,
		vllmService,
		client.MergeFrom(before),
	); err != nil {
		return err
	}

	return storageErr
}

func phaseAndMessageFromDeployment(deployment *appsv1.Deployment) (string, string) {
	desiredReplicas := int32(1)
	if deployment.Spec.Replicas != nil {
		desiredReplicas = *deployment.Spec.Replicas
	}

	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentReplicaFailure &&
			condition.Status == corev1.ConditionTrue {
			return "failed", fmt.Sprintf(
				"Deployment %s 副本创建失败：%s",
				deployment.Name,
				condition.Message,
			)
		}
	}

	if desiredReplicas > 0 && deployment.Status.ReadyReplicas >= desiredReplicas {
		return "Running", fmt.Sprintf(
			"Deployment %s 已就绪：readyReplicas=%d/%d",
			deployment.Name,
			deployment.Status.ReadyReplicas,
			desiredReplicas,
		)
	}

	return "Pending", fmt.Sprintf(
		"Deployment %s 正在启动：readyReplicas=%d/%d",
		deployment.Name,
		deployment.Status.ReadyReplicas,
		desiredReplicas,
	)
}

func replicasFor(vllmService *aiinfrav1alpha1.VLLMService) *int32 {
	replicas := int32(1)
	if vllmService.Spec.Replicas != nil {
		replicas = *vllmService.Spec.Replicas
	}
	return &replicas
}

func portFor(vllmService *aiinfrav1alpha1.VLLMService) int32 {
	if vllmService.Spec.Port == 0 {
		return 8000
	}

	return vllmService.Spec.Port
}

func selectorLabelsForVLLMService(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "vllmservice",
		"app.kubernetes.io/instance": name,
	}
}

func labelsForVLLMService(vllmService *aiinfrav1alpha1.VLLMService) map[string]string {
	labels := make(map[string]string)

	for key, value := range vllmService.Spec.Labels {
		labels[key] = value
	}

	labels["app.kubernetes.io/name"] = "vllmservice"
	labels["app.kubernetes.io/instance"] = vllmService.Name
	labels["app.kubernetes.io/managed-by"] = "vllmservice-operator"

	return labels
}

func int32Ptr(v int32) *int32 {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func readOnlyFor(storage aiinfrav1alpha1.VLLMServiceStorageSpec) bool {
	if storage.ReadOnly == nil {
		return true
	}

	return *storage.ReadOnly
}

// SetupWithManager sets up the controller with the Manager.
func (r *VLLMServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiinfrav1alpha1.VLLMService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Owns(&monitoringv1.ServiceMonitor{}).
		Named("vllmservice").
		Complete(r)
}
