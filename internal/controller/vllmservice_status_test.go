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
	"errors"
	"strings"
	"testing"

	aiinfrav1alpha1 "github.com/bolin-dai/vllmservice-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestUpdateVLLMServiceStatus(t *testing.T) {
	type conditionExpectation struct {
		status metav1.ConditionStatus
		reason string
	}
	pvcReadErr := errors.New("PVC read denied")
	tests := []struct {
		name              string
		configure         func(*aiinfrav1alpha1.VLLMService, *appsv1.Deployment, *corev1.PersistentVolumeClaim)
		routeMessage      string
		monitoringMessage string
		storageErr        error
		wantAvailable     metav1.ConditionStatus
		wantConditions    map[string]conditionExpectation
		wantDetails       []string
	}{
		{
			name:          "all components ready clears previous error",
			wantAvailable: metav1.ConditionTrue,
		},
		{
			name: "ready replicas with unavailable deployment",
			configure: func(_ *aiinfrav1alpha1.VLLMService, deployment *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				deployment.Status.AvailableReplicas = 0
			},
			wantAvailable: metav1.ConditionFalse,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionDeploymentReady: {metav1.ConditionFalse, "DeploymentProgressing"},
			},
			wantDetails: []string{"readyReplicas=1/1, availableReplicas=0/1"},
		},
		{
			name: "deployment replica failure retains error",
			configure: func(_ *aiinfrav1alpha1.VLLMService, deployment *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				deployment.Status.Conditions = append(deployment.Status.Conditions, appsv1.DeploymentCondition{
					Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Message: "pod quota exceeded",
				})
			},
			wantAvailable: metav1.ConditionFalse,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionDeploymentReady: {metav1.ConditionFalse, "DeploymentReplicaFailure"},
			},
			wantDetails: []string{"pod quota exceeded"},
		},
		{
			name: "ready deployment with lost PVC",
			configure: func(_ *aiinfrav1alpha1.VLLMService, _ *appsv1.Deployment, pvc *corev1.PersistentVolumeClaim) {
				pvc.Status.Phase = corev1.ClaimLost
			},
			wantAvailable: metav1.ConditionFalse,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionStorageReady: {metav1.ConditionFalse, "PVCLost"},
			},
			wantDetails: []string{"PVC default/models 已丢失其绑定的 PersistentVolume"},
		},
		{
			name: "ready replicas with stale deployment status",
			configure: func(_ *aiinfrav1alpha1.VLLMService, deployment *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				deployment.Status.ObservedGeneration = deployment.Generation - 1
			},
			wantAvailable: metav1.ConditionUnknown,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionDeploymentReady: {metav1.ConditionUnknown, "DeploymentStatusStale"},
			},
			wantDetails: []string{"observedGeneration=1, generation=2"},
		},
		{
			name: "ready deployment with invalid gateway and monitoring error",
			configure: func(resource *aiinfrav1alpha1.VLLMService, _ *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				resource.Spec.GatewayRef = &aiinfrav1alpha1.VLLMServiceGatewayRef{Name: "gateway"}
			},
			routeMessage:      "Gateway default/gateway does not exist",
			monitoringMessage: "ServiceMonitor update forbidden",
			wantAvailable:     metav1.ConditionFalse,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionRouteReady:      {metav1.ConditionFalse, "GatewayReferenceInvalid"},
				aiinfrav1alpha1.VLLMServiceConditionMonitoringReady: {metav1.ConditionFalse, "ServiceMonitorReconcileFailed"},
			},
			wantDetails: []string{"Gateway default/gateway does not exist", "ServiceMonitor update forbidden"},
		},
		{
			name: "ready deployment with pending route",
			configure: func(resource *aiinfrav1alpha1.VLLMService, _ *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				resource.Spec.GatewayRef = &aiinfrav1alpha1.VLLMServiceGatewayRef{Name: "gateway"}
			},
			wantAvailable: metav1.ConditionUnknown,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionRouteReady: {metav1.ConditionUnknown, "HTTPRoutePending"},
			},
			wantDetails: []string{"HTTPRoute 尚未创建或尚未获取到状态"},
		},
		{
			name: "ready deployment with pending monitoring",
			configure: func(resource *aiinfrav1alpha1.VLLMService, _ *appsv1.Deployment, _ *corev1.PersistentVolumeClaim) {
				resource.Spec.Monitoring = &aiinfrav1alpha1.VLLMServiceMonitoringSpec{Enabled: true}
			},
			wantAvailable: metav1.ConditionUnknown,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionMonitoringReady: {metav1.ConditionUnknown, "ServiceMonitoringPending"},
			},
			wantDetails: []string{"监控已启用,但ServiceMonitor尚未创建或尚未获取到。"},
		},
		{
			name:          "PVC read error is persisted and returned",
			storageErr:    pvcReadErr,
			wantAvailable: metav1.ConditionUnknown,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionStorageReady: {metav1.ConditionUnknown, "PVCCheckFailed"},
			},
			wantDetails: []string{pvcReadErr.Error()},
		},
		{
			name: "false takes priority while preserving unknown component detail",
			configure: func(resource *aiinfrav1alpha1.VLLMService, _ *appsv1.Deployment, pvc *corev1.PersistentVolumeClaim) {
				resource.Spec.GatewayRef = &aiinfrav1alpha1.VLLMServiceGatewayRef{Name: "gateway"}
				pvc.Status.Phase = corev1.ClaimPending
			},
			wantAvailable: metav1.ConditionFalse,
			wantConditions: map[string]conditionExpectation{
				aiinfrav1alpha1.VLLMServiceConditionStorageReady: {metav1.ConditionFalse, "PVCNotBound"},
				aiinfrav1alpha1.VLLMServiceConditionRouteReady:   {metav1.ConditionUnknown, "HTTPRoutePending"},
			},
			wantDetails: []string{"当前状态为 Pending", "HTTPRoute 尚未创建或尚未获取到状态"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := aiinfrav1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			resource := &aiinfrav1alpha1.VLLMService{
				ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "default", Generation: 3},
				Spec: aiinfrav1alpha1.VLLMServiceSpec{
					Storage: aiinfrav1alpha1.VLLMServiceStorageSpec{PVCName: "models"},
				},
				Status: aiinfrav1alpha1.VLLMServiceStatus{ObservedGeneration: 1, Message: "previous error"},
			}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "default", Generation: 2},
				Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2, ReadyReplicas: 1, AvailableReplicas: 1,
					Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}},
				},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "models", Namespace: "default"},
				Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
			}
			if tt.configure != nil {
				tt.configure(resource, deployment, pvc)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(resource).WithObjects(resource, pvc).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.PersistentVolumeClaim); ok && tt.storageErr != nil {
							return tt.storageErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
			reconciler := &VLLMServiceReconciler{Client: fakeClient, Scheme: scheme}
			service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "model-service"}}
			err := reconciler.updateVLLMServiceStatus(ctx, resource, deployment, service, nil, nil, tt.routeMessage, tt.monitoringMessage)
			if !errors.Is(err, tt.storageErr) {
				t.Fatalf("update error = %v, want %v", err, tt.storageErr)
			}
			updated := &aiinfrav1alpha1.VLLMService{}
			if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(resource), updated); err != nil {
				t.Fatal(err)
			}
			if updated.Status.ObservedGeneration != updated.Generation || updated.Status.ReadyReplicas != deployment.Status.ReadyReplicas {
				t.Fatalf("observedGeneration or readyReplicas changed unexpectedly: %+v", updated.Status)
			}
			if updated.Status.DeploymentName != deployment.Name || updated.Status.ServiceName != service.Name {
				t.Fatalf("resource names changed unexpectedly: %+v", updated.Status)
			}
			available := apimeta.FindStatusCondition(updated.Status.Conditions, aiinfrav1alpha1.VLLMServiceConditionAvailable)
			if available == nil || available.Status != tt.wantAvailable {
				t.Fatalf("Available = %+v, want %s", available, tt.wantAvailable)
			}
			wantReason := map[metav1.ConditionStatus]string{
				metav1.ConditionTrue: "AllComponentsReady", metav1.ConditionFalse: "ComponentsNotReady", metav1.ConditionUnknown: "ComponentStatusUnknown",
			}[tt.wantAvailable]
			if available.Reason != wantReason || !strings.HasPrefix(updated.Status.Message, available.Message) {
				t.Fatalf("message %q does not summarize Available %+v", updated.Status.Message, available)
			}
			if tt.wantAvailable == metav1.ConditionTrue && updated.Status.Message != available.Message {
				t.Fatalf("ready message = %q, want %q", updated.Status.Message, available.Message)
			}
			if strings.Contains(updated.Status.Message, "previous error") {
				t.Fatalf("stale error retained: %q", updated.Status.Message)
			}
			for _, detail := range tt.wantDetails {
				if !strings.Contains(updated.Status.Message, detail) {
					t.Errorf("message %q missing detail %q", updated.Status.Message, detail)
				}
			}
			conditionTypes := []string{
				aiinfrav1alpha1.VLLMServiceConditionDeploymentReady, aiinfrav1alpha1.VLLMServiceConditionStorageReady,
				aiinfrav1alpha1.VLLMServiceConditionRouteReady, aiinfrav1alpha1.VLLMServiceConditionMonitoringReady,
				aiinfrav1alpha1.VLLMServiceConditionAvailable,
			}
			if len(updated.Status.Conditions) != len(conditionTypes) {
				t.Fatalf("conditions = %+v, want all five conditions", updated.Status.Conditions)
			}
			for _, conditionType := range conditionTypes {
				condition := apimeta.FindStatusCondition(updated.Status.Conditions, conditionType)
				if condition == nil || condition.ObservedGeneration != updated.Generation || condition.LastTransitionTime.IsZero() {
					t.Fatalf("missing or incomplete condition %s: %+v", conditionType, condition)
				}
				want := conditionExpectation{status: metav1.ConditionTrue}
				if conditionType == aiinfrav1alpha1.VLLMServiceConditionAvailable {
					want = conditionExpectation{tt.wantAvailable, wantReason}
				} else if expected, ok := tt.wantConditions[conditionType]; ok {
					want = expected
				}
				if condition.Status != want.status || (want.reason != "" && condition.Reason != want.reason) {
					t.Errorf("%s = %+v, want %+v", conditionType, condition, want)
				}
			}
		})
	}
}
