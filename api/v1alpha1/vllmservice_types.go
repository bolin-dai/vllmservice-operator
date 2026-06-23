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
	ReadOnly bool `json:"readOnly,omitempty"`

	// +optional
	SubPath string `json:"subPath,omitempty"`
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

	// +optional
	Phase string `json:"phase,omitempty"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

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
