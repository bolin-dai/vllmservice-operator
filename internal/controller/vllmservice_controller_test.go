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

	aiinfrav1alpha1 "github.com/bolin-dai/vllmservice-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("VLLMService Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind VLLMService")

			resource := &aiinfrav1alpha1.VLLMService{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				return
			}

			Expect(errors.IsNotFound(err)).To(BeTrue())

			resource = &aiinfrav1alpha1.VLLMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: aiinfrav1alpha1.VLLMServiceSpec{
					Image:     "docker.m.daocloud.io/vllm/vllm-openai:latest",
					ModelPath: "/data/models/Qwen2.5-1.5B-Instruct",
					ModelName: "qwen2.5-1.5b-instruct",
					Resources: corev1.ResourceRequirements{},
					Storage: aiinfrav1alpha1.VLLMServiceStorageSpec{
						PVCName:   "test-pvc",
						MountPath: "/data/models",
					},
				},
			}

			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &aiinfrav1alpha1.VLLMService{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return
			}

			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance VLLMService")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")

			controllerReconciler := &VLLMServiceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the Deployment was created")

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deployment)).To(Succeed())

			Expect(deployment.Name).To(Equal(resourceName))
			Expect(deployment.Namespace).To(Equal("default"))
			Expect(deployment.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("docker.m.daocloud.io/vllm/vllm-openai:latest"))
		})
	})
})