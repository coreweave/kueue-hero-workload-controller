//go:build e2e
// +build e2e

// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package e2e runs the controller against a REAL kueue in a kind cluster.
// Everything upstream of this suite used a simulated kueue; here the real
// scheduler and the real kueue produce the conditions, messages, and
// evictions the controller must react to — including the exact "no fit"
// signal that stuck-detection matches (message text on 0.16.x, the
// TopologyPlacementFailed reason on 0.19+).
//
// Cluster shape (see kind-config.yaml): the kind cluster has ONLY a control
// plane. Every GPU node is a kwok-managed fake — kwok maintains their
// heartbeats and walks pods on them through Running without a kubelet — so
// the suite can model realistic topologies (18-node racks, 4 GPU per node)
// with no hardware.
//
// The controller itself is installed from the Helm chart with the locally
// built image, so the chart is exercised too.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"

	"github.com/coreweave/kueue-hero-workload-controller/test/utils"
)

const (
	// Topology label keys on the fake nodes, mirrored in the Topology object.
	labelBlock = "cloud.provider.com/topology-block"
	labelRack  = "cloud.provider.com/topology-rack"
	// nodeGroupLabel selects the fake GPU nodes into the ResourceFlavor.
	nodeGroupLabel = "node-group"
	nodeGroupValue = "fake-gpu"

	gpuResource  = corev1.ResourceName("nvidia.com/gpu")
	gpusPerNode  = 4
	nodesPerRack = 18 // one rack = 18 nodes x 4 GPU = 72 GPU

	// Suite-wide kueue fixtures (shared by every spec). ClusterQueues and
	// LocalQueues are NOT here: each spec creates and deletes its own via
	// createClusterQueue/createLocalQueue, so specs can model any CQ
	// arrangement (single hero CQ, two hero CQs, ...).
	topologyName  = "e2e-topology"
	flavorName    = "e2e-gpu-flavor"
	heroClass     = "hero-critical"
	victimClass   = "victim-low"
	testNamespace = "hero-e2e"

	// Controller install. Chart values set drainTimeout=3m: short enough
	// that the timeout spec need not wait the 30m default, long enough
	// that a healthy big drain (72 nodes, 256 pods; ~105s observed)
	// converges well inside it.
	controllerNamespace = "hero-system"
	helmRelease         = "hero-controller"
	taintKey            = "hero.coreweave.com/taint"
)

var (
	// kueueVersion accepts "0.16.9" or "v0.16.9" (the Makefile shares the
	// v-prefixed variable with CRD vendoring).
	kueueVersion = strings.TrimPrefix(envOr("KUEUE_VERSION", "v0.16.9"), "v")
	// kubeContext pins every kubectl/helm call and the API client to THIS
	// suite's kind cluster. Never rely on the ambient current-context: the
	// user switches contexts freely, and an ambient-context suite once
	// installed everything into an unrelated cluster.
	kubeContext = "kind-" + envOr("KIND_CLUSTER", "kueue-hero-workload-controller-test-e2e")
	kwokVersion = envOr("KWOK_VERSION", "v0.8.0")
	// e2eImage is built and kind-loaded by `make test-e2e` before the suite runs.
	e2eImage = envOr("E2E_IMG", "kueue-hero-workload-controller:e2e")

	k8sClient client.Client
	ctx       context.Context
	scheme    = runtime.NewScheme()
)

// kubectl builds a kubectl command pinned to this suite's cluster context.
func kubectl(args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--context", kubeContext}, args...)...)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting e2e suite: kueue %s, kwok %s, image %s\n",
		kueueVersion, kwokVersion, e2eImage)
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	ctx = context.Background()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))

	By("installing kwok " + kwokVersion)
	kwokBase := "https://github.com/kubernetes-sigs/kwok/releases/download/" + kwokVersion
	_, err := utils.Run(kubectl("apply", "-f", kwokBase+"/kwok.yaml"))
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(kubectl("apply", "-f", kwokBase+"/stage-fast.yaml"))
	Expect(err).NotTo(HaveOccurred())
	// stage-fast walks pods straight to Succeeded; this suite needs pods
	// that RUN and stay running (victims must occupy their racks until
	// evicted, heroes until "fully running"). Keep node-initialize,
	// pod-ready and pod-delete; drop the auto-completion stage.
	_, err = utils.Run(kubectl("delete", "stage", "pod-complete", "--ignore-not-found"))
	Expect(err).NotTo(HaveOccurred())

	By("installing kueue v" + kueueVersion)
	kueueManifests := fmt.Sprintf(
		"https://github.com/kubernetes-sigs/kueue/releases/download/v%s/manifests.yaml", kueueVersion)
	_, err = utils.Run(kubectl("apply", "--server-side", "-f", kueueManifests))
	Expect(err).NotTo(HaveOccurred())
	// Fake nodes are untainted, so the scheduler will happily place REAL
	// control-plane software on phantom hardware where nothing executes.
	// Pin kueue, kwok and (below, via chart values) our controller to the
	// one real node.
	for _, pin := range [][]string{
		{"kueue-system", "deployment/kueue-controller-manager"},
		{"kube-system", "deployment/kwok-controller"},
	} {
		_, err = utils.Run(kubectl("-n", pin[0], "patch", pin[1],
			"--type", "strategic", "-p",
			`{"spec":{"template":{"spec":{"nodeSelector":{"node-role.kubernetes.io/control-plane":""}}}}}`))
		Expect(err).NotTo(HaveOccurred())
	}
	_, err = utils.Run(kubectl("-n", "kueue-system", "wait",
		"deployment/kueue-controller-manager", "--for=condition=Available", "--timeout=5m"))
	Expect(err).NotTo(HaveOccurred())

	By("building the API client for context " + kubeContext)
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	// NO nodes here: ginkgo randomizes top-level spec order, so every spec
	// creates its own uniquely-named blocks (createRack) and deletes them
	// in AfterAll (deleteBlocks) — leftover capacity from one spec must
	// never satisfy another spec's hero.
	//
	// A kind cluster left behind by a previous FAILED run may still carry
	// fake nodes and jobs; sweep them so reuse is safe.
	By("sweeping fake nodes left by any previous run")
	sweepFakeNodes()

	By("creating suite-wide kueue fixtures (topology, flavor, priority classes)")
	createKueueFixtures()

	By("installing the controller Helm chart with the local image")
	projectDir, err := utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("helm", "--kube-context", kubeContext,
		"upgrade", "--install", helmRelease,
		projectDir+"/charts/kueue-hero-workload-controller",
		"--namespace", controllerNamespace, "--create-namespace",
		"--set", "image.repository="+imageRepo(e2eImage),
		"--set", "image.tag="+imageTag(e2eImage),
		"--set", "image.pullPolicy=Never",
		"--set", "config.drainTimeout=3m",
		// Fast re-nudges: the suite's whole drains are seconds-scale, so a
		// lost first nudge must not cost minutes (production default 2m).
		"--set", "config.nudgeInterval=15s",
		"--set-json", `nodeSelector={"node-role.kubernetes.io/control-plane":""}`,
		"--wait", "--timeout", "3m"))
	Expect(err).NotTo(HaveOccurred())
	// The image tag never changes (":e2e", pullPolicy Never), so on a
	// REUSED cluster the helm manifest is byte-identical and no rollout
	// happens — the pod keeps running the previous run's binary even
	// though `kind load` replaced the image. Force the roll.
	_, err = utils.Run(kubectl("-n", controllerNamespace, "rollout", "restart",
		"deployment/"+helmRelease+"-kueue-hero-workload-controller"))
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(kubectl("-n", controllerNamespace, "rollout", "status",
		"deployment/"+helmRelease+"-kueue-hero-workload-controller", "--timeout=3m"))
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	// On failure, surface the controller logs first — after the sweep below
	// (and the Makefile's cluster deletion on success) they are gone.
	if CurrentSpecReport().Failed() {
		out, err := utils.Run(kubectl("-n", controllerNamespace,
			"logs", "-l", "app.kubernetes.io/name=kueue-hero-workload-controller", "--tail=400"))
		if err == nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "controller logs:\n%s\n", out)
		}
	}
	// Leave the cluster clean regardless of outcome: a failed run's cluster
	// is kept for inspection, and the next run may reuse it.
	deleteAllJobs()
	sweepFakeNodes()
})

func imageRepo(img string) string {
	for i := len(img) - 1; i >= 0; i-- {
		if img[i] == ':' {
			return img[:i]
		}
		if img[i] == '/' {
			break
		}
	}
	return img
}

func imageTag(img string) string {
	for i := len(img) - 1; i >= 0; i-- {
		if img[i] == ':' {
			return img[i+1:]
		}
		if img[i] == '/' {
			break
		}
	}
	return "latest"
}

// createRack creates one rack: 18 kwok-managed fake nodes of 4 GPU each.
// The apiserver wipes status on create, so allocatable (including the fake
// GPUs) and the Ready condition are written with a follow-up status update;
// kwok then maintains the heartbeat because of the kwok.x-k8s.io/node
// annotation.
func createRack(block, rack string) {
	GinkgoHelper()
	for i := 1; i <= nodesPerRack; i++ {
		name := fmt.Sprintf("%s-%s-n%02d", block, rack, i)
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					corev1.LabelHostname: name,
					labelBlock:           block,
					labelRack:            rack,
					nodeGroupLabel:       nodeGroupValue,
				},
				Annotations: map[string]string{
					"kwok.x-k8s.io/node": "fake",
				},
			},
		}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, node))).To(Succeed())

		res := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("32"),
			corev1.ResourceMemory: resource.MustParse("256Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
			gpuResource:           resource.MustParse(fmt.Sprintf("%d", gpusPerNode)),
		}
		// kwok starts heartbeating the node the moment it exists, so the
		// status write races its patches: retry on conflict.
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
				return err
			}
			node.Status.Capacity = res
			node.Status.Allocatable = res
			node.Status.Conditions = []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				Reason:             "KwokReady",
				LastTransitionTime: metav1.Now(),
				LastHeartbeatTime:  metav1.Now(),
			}}
			return k8sClient.Status().Update(ctx, node)
		})).To(Succeed())
	}
}

func createKueueFixtures() {
	GinkgoHelper()

	topology := &kueue.Topology{
		ObjectMeta: metav1.ObjectMeta{Name: topologyName},
		Spec: kueue.TopologySpec{Levels: []kueue.TopologyLevel{
			{NodeLabel: labelBlock},
			{NodeLabel: labelRack},
			{NodeLabel: corev1.LabelHostname},
		}},
	}
	// Kueue's webhooks come up slightly after its Deployment reports
	// Available (endpoint programming lags pod readiness), and EACH kueue
	// kind has its own webhook — retrying only the first create is not
	// enough (a Topology create can pass while the ResourceFlavor webhook
	// still refuses connections). Every kueue fixture create retries.
	eventuallyCreate(topology)

	flavor := &kueue.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{Name: flavorName},
		Spec: kueue.ResourceFlavorSpec{
			NodeLabels:   map[string]string{nodeGroupLabel: nodeGroupValue},
			TopologyName: ptrTo(kueue.TopologyReference(topologyName)),
		},
	}
	eventuallyCreate(flavor)

	for _, pc := range []struct {
		name  string
		value int32
	}{{heroClass, 1000}, {victimClass, 100}} {
		wpc := &kueue.WorkloadPriorityClass{
			ObjectMeta: metav1.ObjectMeta{Name: pc.name},
			Value:      pc.value,
		}
		eventuallyCreate(wpc)
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())
	deleteAllJobs() // leftovers from a previous failed run
}

// eventuallyCreate retries a fixture create until kueue's webhook for that
// kind answers. Each kueue kind has its own webhook; the Deployment
// reporting Available does not mean every webhook endpoint is reachable yet.
func eventuallyCreate(obj client.Object) {
	GinkgoHelper()
	Eventually(func() error {
		fresh, ok := obj.DeepCopyObject().(client.Object)
		Expect(ok).To(BeTrue())
		return client.IgnoreAlreadyExists(k8sClient.Create(ctx, fresh))
	}, 2*time.Minute).Should(Succeed())
}

// sweepFakeNodes deletes every kwok-managed fake node this suite ever makes
// (all carry the node-group label) and waits until they are gone.
func sweepFakeNodes() {
	GinkgoHelper()
	Expect(k8sClient.DeleteAllOf(ctx, &corev1.Node{},
		client.MatchingLabels{nodeGroupLabel: nodeGroupValue})).To(Succeed())
	Eventually(func(g Gomega) {
		nodes := &corev1.NodeList{}
		g.Expect(k8sClient.List(ctx, nodes, client.MatchingLabels{nodeGroupLabel: nodeGroupValue})).To(Succeed())
		g.Expect(nodes.Items).To(BeEmpty())
	}).Should(Succeed())
}

// createClusterQueue creates a per-spec ClusterQueue (heroEnabled adds the
// hero.coreweave.com/enabled label) with a matching LocalQueue of the same
// name in the test namespace. Specs own cleanup via deleteClusterQueues.
func createClusterQueue(name string, heroEnabled bool, quotaGPU string) {
	GinkgoHelper()
	cq := &kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kueue.ClusterQueueSpec{
			// v1beta2 default (nil) matches NO namespaces — "Workload
			// namespace doesn't match ClusterQueue selector". Empty selector
			// = every namespace.
			NamespaceSelector: &metav1.LabelSelector{},
			ResourceGroups: []kueue.ResourceGroup{{
				CoveredResources: []corev1.ResourceName{gpuResource},
				Flavors: []kueue.FlavorQuotas{{
					Name: kueue.ResourceFlavorReference(flavorName),
					Resources: []kueue.ResourceQuota{{
						Name:         gpuResource,
						NominalQuota: resource.MustParse(quotaGPU),
					}},
				}},
			}},
		},
	}
	if heroEnabled {
		cq.Labels = map[string]string{"hero.coreweave.com/enabled": "true"}
	}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, cq))).To(Succeed())
	lq := &kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       kueue.LocalQueueSpec{ClusterQueue: kueue.ClusterQueueReference(name)},
	}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, lq))).To(Succeed())
}

// deleteClusterQueues removes the spec's queues; jobs must be gone first
// (kueue holds CQ deletion on remaining workloads via finalizer).
func deleteClusterQueues(names ...string) {
	GinkgoHelper()
	for _, name := range names {
		lq := &kueue.LocalQueue{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, lq))).To(Succeed())
		cq := &kueue.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cq))).To(Succeed())
	}
	Eventually(func(g Gomega) {
		for _, name := range names {
			err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, &kueue.ClusterQueue{})
			g.Expect(err).To(HaveOccurred(), "ClusterQueue %s should be gone", name)
		}
	}).Should(Succeed())
}

func ptrTo[T any](v T) *T { return &v }

// Silence unused-import churn while specs are added incrementally.
var _ = batchv1.Job{}
