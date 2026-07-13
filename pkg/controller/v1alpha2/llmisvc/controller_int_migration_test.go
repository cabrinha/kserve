/*
Copyright 2025 The KServe Authors.

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

package llmisvc_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	igwapiv1alpha2 "github.com/kserve/kserve/pkg/apis/gie/v1alpha2pool"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
	. "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("InferencePool v1-only", func() {
	It("should create only the v1 InferencePool and point HTTPRoute at it", func(ctx SpecContext) {
		svcName := "test-llm-v1-only"
		testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithManagedScheduler(),
		)

		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() {
			testNs.DeleteAndWait(ctx, llmSvc)
		}()

		expectedPoolName := svcName + "-inference-pool"

		// v1 pool is created
		Eventually(func(g Gomega, ctx context.Context) error {
			v1Pool := &igwapi.InferencePool{}
			return envTest.Client.Get(ctx, client.ObjectKey{Name: expectedPoolName, Namespace: testNs.Name}, v1Pool)
		}).WithContext(ctx).Should(Succeed(), "v1 InferencePool should be created")

		v1Pool := &igwapi.InferencePool{}
		Expect(envTest.Client.Get(ctx, client.ObjectKey{Name: expectedPoolName, Namespace: testNs.Name}, v1Pool)).To(Succeed())
		Expect(v1Pool).To(BeOwnedBy(llmSvc))

		// v1alpha2 pool is not created (or is deleted if present)
		Consistently(func(g Gomega, ctx context.Context) {
			v1alpha2Pool := &igwapiv1alpha2.InferencePool{}
			err := envTest.Client.Get(ctx, client.ObjectKey{Name: expectedPoolName, Namespace: testNs.Name}, v1alpha2Pool)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "v1alpha2 InferencePool should not exist")
		}).WithContext(ctx).Should(Succeed())

		// HTTPRoute uses v1 group and migration annotation
		Eventually(func(g Gomega, ctx context.Context) error {
			routes, errList := managedRoutes(ctx, llmSvc)
			g.Expect(errList).ToNot(HaveOccurred())
			g.Expect(routes).To(HaveLen(1))

			route := &routes[0]
			g.Expect(route.Spec.Rules).ToNot(BeEmpty())
			g.Expect(route.Spec.Rules[0].BackendRefs).ToNot(BeEmpty())

			backendRef := route.Spec.Rules[0].BackendRefs[0]
			g.Expect(backendRef.Group).ToNot(BeNil())
			g.Expect(string(*backendRef.Group)).To(Equal(constants.InferencePoolV1APIGroupName))
			g.Expect(route.Annotations).To(HaveKeyWithValue(llmisvc.AnnotationInferencePoolMigrated, "v1"))
			return nil
		}).WithContext(ctx).Should(Succeed())
	})

	It("should delete a pre-existing owned v1alpha2 InferencePool", func(ctx SpecContext) {
		svcName := "test-llm-cleanup-alpha2"
		testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithManagedScheduler(),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() {
			testNs.DeleteAndWait(ctx, llmSvc)
		}()

		expectedPoolName := svcName + "-inference-pool"

		// Wait for the service to reconcile once
		Eventually(func(g Gomega, ctx context.Context) error {
			v1Pool := &igwapi.InferencePool{}
			return envTest.Client.Get(ctx, client.ObjectKey{Name: expectedPoolName, Namespace: testNs.Name}, v1Pool)
		}).WithContext(ctx).Should(Succeed())

		// Inject a legacy owned v1alpha2 pool as if left over from dual-write
		fresh := &v1alpha2.LLMInferenceService{}
		Expect(envTest.Client.Get(ctx, client.ObjectKeyFromObject(llmSvc), fresh)).To(Succeed())
		legacy := &igwapiv1alpha2.InferencePool{}
		legacy.Name = expectedPoolName
		legacy.Namespace = testNs.Name
		legacy.Labels = llmisvc.SchedulerLabels(llmSvc)
		legacy.Spec.Selector = map[igwapiv1alpha2.LabelKey]igwapiv1alpha2.LabelValue{"app": "test"}
		legacy.Spec.TargetPortNumber = 8000
		Expect(controllerutil.SetControllerReference(fresh, legacy, envTest.Client.Scheme())).To(Succeed())
		Expect(envTest.Client.Create(ctx, legacy)).To(Succeed())


		// Trigger reconcile
		fresh.Annotations = map[string]string{"trigger-reconcile": "true"}
		Expect(envTest.Client.Update(ctx, fresh)).To(Succeed())

		// Controller should remove it
		Eventually(func(g Gomega, ctx context.Context) {
			v1alpha2Pool := &igwapiv1alpha2.InferencePool{}
			err := envTest.Client.Get(ctx, client.ObjectKey{Name: expectedPoolName, Namespace: testNs.Name}, v1alpha2Pool)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})
})
