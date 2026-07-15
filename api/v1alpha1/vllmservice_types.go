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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	VLLMServiceConditionAvailable       = "Available"
	VLLMServiceConditionDeploymentReady = "DeploymentReady"
	VLLMServiceConditionStorageReady    = "StorageReady"
	VLLMServiceConditionRouteReady      = "RouteReady"
	VLLMServiceConditionMonitoringReady = "MonitoringReady"
)

type VLLMServiceEngineArgsSpec struct {
	// Dtype表示vllm加载模型权重和激活值时使用的数据类型。不填写时默认使用auto
	// +optional
	// +kubebuilder:validation:Enum=auto;half;float16;bfloat16;float;float32
	// +kubebuilder:default:=auto
	Dtype string `json:"dtype,omitempty"`

	// MaxModelLen 表示模型最大上下文长度，也就是输入token+输出token的总长度上限。不填写时默认使用4096
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=4096
	MaxModelLen *int32 `json:"maxModelLen,omitempty"`

	// GpuMemoryUtilization 表示当前vllm实例最多使用多少比例的GPU显存
	// 例如：0.75表示最多使用约75%的显存，不填写时默认使用0.75
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]+)?|1(\.0+)?)$`
	GPUMemoryUtilization string `json:"gpuMemoryUtilization,omitempty"`

	// MaxNumSeqs 表示vllm 一次调度迭代中最多处理多少个请求序列。可以粗略理解为推理batch size上限之一。 不填写时默认使用8
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=8
	MaxNumSeqs *int32 `json:"maxNumSeqs,omitempty"`
}

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.
type VLLMServiceStorageSpec struct {

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PVCName string `json:"pvcName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	MountPath string `json:"mountPath"`

	// +optional
	// +kubebuilder:default:=true
	ReadOnly *bool `json:"readOnly,omitempty"`

	// +optional
	SubPath string `json:"subPath,omitempty"`
}

/*
VLLMServiceGatewayRef 表示当前VLLMService要引用的已有的Gateway。
注意： 这里是引用Gateway,不是创建Gateway
Gateway通常是平台侧提前创建好的共享入口资源
*/
type VLLMServiceGatewayRef struct {
	// name是要引用的Gateway名称
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	//Namespace是Gateway所在命名空间。如果不填写，默认认为Gateway和当前VLLMService在同一个命名空间。
	// +optional
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace,omitempty"`

	// SectionName 是要绑定的Gateway listener名称。后面创建HTTPRoute时，会写入parentRefs.sectionName
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	SectionName string `json:"sectionName"`

	// Host是HTTPRoute要匹配的域名。后面创建HTTPRoute时，会写入spec.hostnames
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`
}

type VLLMServiceMonitoringSpec struct {
	// Enabled 表示是否创建serviceMonitor

	// 未填写、false或monitoring为空对象时，都不会启用监控
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Path 表示vllm暴露Prometheus指标的HTTP路径
	// enabled=true且未填写时，Controller默认使用 /metrics
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern:="^/.*"
	Path string `json:"path,omitempty"`

	// Interval表示Prometheus住区指标的时间间隔
	// enabled=true 且未填写时，Controller默认使用30s
	// 支持30s、1m、1h30m 登Prometheus duration格式
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern:="^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$"
	Interval string `json:"interval,omitempty"`

	// Labels 会添加到ServiceMonitor.metadata.labels
	//当Prometheus.spec.serviceMonitorSelector 要求特定标签时，
	//可以通过该字段添加， 例如 release: prometheus
	// +OPTIONAL
	Labels map[string]string `json:"labels,omitempty"`
}

type VLLMServiceStartupProbeSpec struct {
	// Enabled表示是否启用startupProbe。 只有enabled=true时，operator才会给vllm容器添加startupProbe
	// 不填写、false或者startupProbe为空对象时，都不会启动startupProbe
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Path表示startupProbe访问的HTTP路径
	// enabled=true且未填写时，Controller默认使用/health
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern:="^/.*"
	Path string `json:"path,omitempty"`

	// InitialDelaySeconds 表示容器启动后，延迟多少秒才开始执行startupProbe
	// enabled=true且未填写时，Controller默认使用30秒
	// +optional
	// +kubebuilder:validation:Minimum=0
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`

	// PeriodSeconds 表示每隔多少秒执行一次探测
	// enabled=true且未填写时，controller默认使用10秒
	// +optional
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	// TimeoutSeconds表示每次探测最多等待多少秒。
	// enabled=true 且未填写时，Controller默认使用5秒
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// FailureThreshold 表示连续失败多少次后认为启动失败
	// enabled=true 且未填写时，Controller默认使用60次
	// +optional
	// +kubebuilder:validation:Minimum=1
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
}

type VLLMServiceLivenessProbeSpec struct {
	// Enabled 表示是否启用livenessProbe。
	// 只有enabled=true时，operator才会给vllm容器添加livenessProbe
	// 不填写，flase或者livenessProbe为空对象时，都不会启用livenessProbe
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Path 表示livenessProbe访问的HTTP路径
	// enabled=true且未填写时，controller默认使用/health
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern:="^/.*"
	Path string `json:"path,omitempty"`

	// InitialDelaySeconds 表示容器启动后，延迟多少秒才开始执行livenessProbe
	//  enabled=true 且未填写时，Controller默认使用30秒。
	// 如果同时启用了startupProbe, livenessProbe会等startupProbe成功后才真正生效
	// +optional
	// +kubebuilder:validation:Minimum=0
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`

	// PeriodSeconds 表示每隔多少秒执行一次探测
	// enabled=true 且未填写时，Controller默认使用30秒
	// +optional
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	// TimeoutSeconds 表示每次探测最多等待多少秒
	// enabled=true 且未填写时，Controller默认使用5秒
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// FailuerThreshold 表示连续失败多少次后认为容器不健康
	// enabled=true且未填写时，Controller默认使用3次
	// livenessProbe 失败达到该阈值后，kubelet会重启容器
	// +optional
	// +kubebuilder:validation:Minimum=1
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
}

// VLLMServiceSpec defines the desired state of VLLMService
type VLLMServiceSpec struct {

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ModelPath string `json:"modelPath"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ModelName string `json:"modelName"`

	// +optional
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// +optional
	RuntimeClassName string `json:"runtimeClassName,omitempty"`

	// +optional
	SchedulerName string `json:"schedulerName,omitempty"`

	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	// +kubebuilder:default:=8000
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	//GatewayRef 表示当前VLLMService要挂载到哪个Gateway上。 不填写gatewayRef时，operator只创建Deployment和service，不创建HTTPRoute
	// +optional
	GatewayRef *VLLMServiceGatewayRef `json:"gatewayRef,omitempty"`

	// Monitoring 表示当前VLLMService的Prometheus指标抓取配置
	// 只有monitoring.enabled=true时，operator才创建ServiceMonitor,
	// +optional
	Monitoring *VLLMServiceMonitoringSpec `json:"monitoring,omitempty"`

	// EngineArgs表示vllm引擎启动参数。不填写时，operator会使用一组适合小显存实验环境的默认值
	// +optional
	EngineArgs *VLLMServiceEngineArgsSpec `json:"engineArgs,omitempty"`

	// startupProbe表示VLLM容器的启动探针配置
	// 只有startupProbe。enabled=true时，operator才会给容器添加startupProbe
	// +optional
	StartupProbe *VLLMServiceStartupProbeSpec `json:"startupProbe,omitempty"`

	// +optional
	LivenessProbe *VLLMServiceLivenessProbeSpec `json:"livenessProbe,omitempty"`

	// +kubebuilder:validation:Required
	Resources corev1.ResourceRequirements `json:"resources"`

	// +kubebuilder:validation:Required
	Storage VLLMServiceStorageSpec `json:"storage"`
}

// VLLMServiceStatus defines the observed state of VLLMService.
type VLLMServiceStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the VLLMService resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//

	//ObservedGeneration 表示当前status是根据哪一版 VLLMService spec计算出来的
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions表示VLLMService各个组成部分的当前状态
	// 每一种 Condition Type在列表中只能存在一条
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	Phase string `json:"phase,omitempty"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// ServiceMonitorName 表示Operator为当前VLLMService创建的serviceMonitor名称
	// +optional
	ServiceMonitorName string `json:"serviceMonitorName,omitempty"`

	// GatewayRefName 表示当前VLLMService引用的Gateway名称。 注意： 这不是operator创建的Gateway，而是引用
	// +optional
	GatewayRefName string `json:"gatewayRefName,omitempty"`

	// GatewayRefNamespace 表示当前VLLMService引用的Gateway所在命名空间
	// +optional
	GatewayRefNamespace string `json:"gatewayRefNamespace,omitempty"`

	// HTTPRouteName 表示operator为当前VLLMService创建的HTTPRoute名称
	// +optionoal
	HTTPRouteName string `json:"httpRouteName,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// VLLMService is the Schema for the vllmservices API
type VLLMService struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of VLLMService
	// +required
	Spec VLLMServiceSpec `json:"spec"`

	// status defines the observed state of VLLMService
	// +optional
	Status VLLMServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VLLMServiceList contains a list of VLLMService
type VLLMServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VLLMService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VLLMService{}, &VLLMServiceList{})
}
