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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	aiinfrav1alpha1 "github.com/bolin-dai/vllmservice-operator/api/v1alpha1"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
)

// VLLMServiceReconciler reconciles a VLLMService object
type VLLMServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aiinfra.example.com,resources=vllmservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VLLMService object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
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
		logger.Error(err, "创建或更新deployment失败")
		return ctrl.Result{}, err
	}

	logger.Info(
		"Deployment同步完成", "operation", operation, "namespace", deployment.Namespace, "name", deployment.Name,
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
		logger.Error(err, "创建或更新Service失败")
		return ctrl.Result{}, err
	}

	logger.Info(
		"Service同步完成",
		"operation", serviceOperation,
		"namespace", service.Namespace,
		"name", service.Name,
	)

	if err := r.updateVLLMServiceStatus(ctx, vllmService, deployment, service); err != nil {
		logger.Error(err, "更新VLLMService status失败")
		return ctrl.Result{}, err
	}

	// TODO(user): your logic here

	return ctrl.Result{}, nil
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
		HostIPC:                       true,
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

func buildVLLMContainer(vllmservice *aiinfrav1alpha1.VLLMService) corev1.Container {
	port := portFor(vllmservice)

	return corev1.Container{
		Name:            "vllm",
		Image:           vllmservice.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,

		Args: []string{
			"--model", vllmservice.Spec.ModelPath,
			"--served-model-name", vllmservice.Spec.ModelName,
			"--host", "0.0.0.0",
			"--port", fmt.Sprintf("%d", port),
			"--dtype", "auto",
			"--max-model-len", "4096",
			"--gpu-memory-utilization", "0.75",
			"--max-num-seqs", "8",
		},

		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
		},

		Resources: vllmservice.Spec.Resources,
	}

}

func buildModelVolumesAndMounts(vllmservice *aiinfrav1alpha1.VLLMService) ([]corev1.Volume, []corev1.VolumeMount) {
	storage := vllmservice.Spec.Storage

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

func (r *VLLMServiceReconciler) updateVLLMServiceStatus(
	ctx context.Context,
	vllmservice *aiinfrav1alpha1.VLLMService,
	deployment *appsv1.Deployment,
	service *corev1.Service,
) error {

	phase, message := phaseAndMessageFromDeployment(deployment)

	serviceName := ""
	if service != nil {
		serviceName = service.Name
	}

	if vllmservice.Status.Phase == phase &&
		vllmservice.Status.ReadyReplicas == deployment.Status.ReadyReplicas &&
		vllmservice.Status.DeploymentName == deployment.Name &&
		vllmservice.Status.ServiceName == serviceName &&
		vllmservice.Status.Message == message {
		return nil
	}

	vllmservice.Status.Phase = phase
	vllmservice.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	vllmservice.Status.DeploymentName = deployment.Name
	vllmservice.Status.ServiceName = serviceName
	vllmservice.Status.Message = message

	return r.Status().Update(ctx, vllmservice)
}

func phaseAndMessageFromDeployment(deployment *appsv1.Deployment) (string, string) {
	desiredReplicas := int32(1)
	if deployment.Spec.Replicas != nil {
		desiredReplicas = *deployment.Spec.Replicas
	}

	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return "failed", fmt.Sprintf(
				"Deployment %s 副本创建失败： %s",
				deployment.Name,
				condition.Message,
			)
		}
	}

	if desiredReplicas > 0 && deployment.Status.ReadyReplicas >= desiredReplicas {
		return "Running", fmt.Sprintf(
			"Deployment %s 已就绪： readyReplicas = %d/%d",
			deployment.Name,
			deployment.Status.ReadyReplicas,
			desiredReplicas,
		)
	}

	return "Pending", fmt.Sprintf(
		"Deployment %s 正在启动： readyReplicas = %d/%d",
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

func portFor(vllmservice *aiinfrav1alpha1.VLLMService) int32 {
	if vllmservice.Spec.Port == 0 {
		return 8000
	}

	return vllmservice.Spec.Port
}

/*
selectorLabelsForVLLMService 生成稳定选择标签，用于 Deployment selector 和 Service selector。
*/
func selectorLabelsForVLLMService(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "vllmservice",
		"app.kubernetes.io/instance": name,
	}
}

/*
labelsForVLLMService 生成完整对象标签，用于 deployment的metadata.labels 和 PodTemplate labels。以及service的metadata.labels
*/
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
		Named("vllmservice").
		Complete(r)
}
