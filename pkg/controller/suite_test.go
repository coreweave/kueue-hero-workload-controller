// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package controller

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	scheme    = runtime.NewScheme()

	ctx    context.Context
	cancel context.CancelFunc
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueuev1beta2.AddToScheme(scheme))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "test", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// The suite must prove the wiring before any controller exists: kueue CRDs
// install cleanly and a manager using the shared scheme starts and stops.
var _ = Describe("envtest bootstrap", func() {
	It("serves the kueue v1beta2 Workload API", func() {
		wl := &kueuev1beta2.Workload{}
		wl.Name = "bootstrap-probe"
		wl.Namespace = "default"
		wl.Spec.QueueName = "default-queue"
		wl.Spec.PodSets = []kueuev1beta2.PodSet{{
			Name:  "main",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "main",
						Image: "registry.k8s.io/pause:3.9",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					}},
				},
			},
		}}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())

		fetched := &kueuev1beta2.Workload{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), fetched)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fetched)).To(Succeed())
	})

	It("starts and stops a manager with the shared scheme", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:         scheme,
			Metrics:        metricsserver.Options{BindAddress: "0"},
			LeaderElection: false,
		})
		Expect(err).NotTo(HaveOccurred())

		mgrCtx, mgrCancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(mgrCtx) }()

		Eventually(mgr.GetCache().WaitForCacheSync).WithArguments(mgrCtx).Should(BeTrue())
		mgrCancel()
		Eventually(done).Should(Receive(BeNil()))
	})
})
