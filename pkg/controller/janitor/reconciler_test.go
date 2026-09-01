// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package janitorctrl

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/metrics"
	"github.com/coreweave/kueue-hero-workload-controller/pkg/taint"
	"github.com/coreweave/kueue-hero-workload-controller/pkg/victims"
	"github.com/coreweave/kueue-hero-workload-controller/test/utils/fakekueue"
)

const (
	janitorCQ        = "hero-cq"
	maintenanceTaint = "example.com/maintenance"
)

var (
	ns      *corev1.Namespace
	specIdx int
)

var _ = BeforeEach(func() {
	nowOffset.Store(0)
	specIdx++
	ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("jan-%d", specIdx)}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
})

var _ = AfterEach(func() {
	Expect(k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(ctx, &corev1.Node{})).To(Succeed())
	Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
})

func createNode(name string) {
	node := testingnode.MakeNode(name).Ready().Obj()
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
}

// drainedNodes taints the named nodes for the hero, as the drain
// controller would.
func drainedNodes(hero types.NamespacedName, nodes ...string) {
	for _, n := range nodes {
		createNode(n)
		Expect(taint.EnsureTaint(ctx, k8sClient, n, testCfg.TaintKey, hero, janitorCQ, testNow)).To(Succeed())
	}
}

// suspendedVictim creates a workload marked as suspended by the hero's
// drain (active=false + marker), eviction not yet completed by kueue.
func suspendedVictim(name string, hero types.NamespacedName) *kueue.Workload {
	wl := utiltesting.MakeWorkload(name, ns.Name).
		Active(false).
		Annotation(victims.DeactivatedForAnnotation, victims.OwnerRef(hero)).
		PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
		Obj()
	Expect(k8sClient.Create(ctx, wl)).To(Succeed())
	return wl
}

// createRunningPod creates a pod annotated as the workload's and marks it
// Running (status is a subresource; phase set at create is dropped).
func createRunningPod(name, nodeName, workloadName string) {
	pod := testingpod.MakePod(name, ns.Name).
		NodeName(nodeName).
		Annotation(kueue.WorkloadAnnotation, workloadName).
		Obj()
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	Eventually(func(g Gomega) {
		created := &corev1.Pod{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: name}, created)).To(Succeed())
		created.Status.Phase = corev1.PodRunning
		g.Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())
	}).Should(Succeed())
}

// nudgeNode patches a label so the janitor re-evaluates the node now rather
// than at its armed requeue.
func nudgeNode(name string) {
	GinkgoHelper()
	node := &corev1.Node{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
	patch := client.MergeFrom(node.DeepCopy())
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	node.Labels["test/nudge"] = fmt.Sprintf("%d", time.Now().UnixNano())
	Expect(k8sClient.Patch(ctx, node, patch)).To(Succeed())
}

// eventsFor returns a poller over event reasons recorded for the workload.
func eventsFor(wl types.NamespacedName) func() []string {
	return func() []string {
		events := &corev1.EventList{}
		if err := k8sClient.List(ctx, events, client.InNamespace(wl.Namespace)); err != nil {
			return nil
		}
		var reasons []string
		for i := range events.Items {
			if events.Items[i].InvolvedObject.Name == wl.Name {
				reasons = append(reasons, events.Items[i].Reason)
			}
		}
		return reasons
	}
}

func taintedNodeNames() []string {
	nodes := &corev1.NodeList{}
	Expect(k8sClient.List(ctx, nodes)).To(Succeed())
	var out []string
	for i := range nodes.Items {
		for _, t := range nodes.Items[i].Spec.Taints {
			if t.Key == testCfg.TaintKey {
				out = append(out, nodes.Items[i].Name)
			}
		}
	}
	return out
}

var _ = Describe("janitor phase A: abandoned drains", func() {
	It("tears down the drain of a hero that no longer exists", func() {
		hero := types.NamespacedName{Namespace: ns.Name, Name: "ghost-hero"}
		// No workload is ever created for this hero — simulates deletion
		// while the controller was down (crash recovery path). The victim
		// exists BEFORE the taints, as in reality: victims are only ever
		// marked while a drain's taints are up.
		victim := suspendedVictim("ghost-victim", hero)
		drainedNodes(hero, "gone-n1", "gone-n2")

		By("removing the taints")
		Eventually(taintedNodeNames).Should(BeEmpty())

		By("sweeping the victim unconditionally (eviction never completed)")
		Eventually(func() bool {
			v := &kueue.Workload{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: victim.Namespace, Name: victim.Name}, v)).To(Succeed())
			_, marked := v.Annotations[victims.DeactivatedForAnnotation]
			return v.Spec.Active != nil && *v.Spec.Active && !marked
		}).Should(BeTrue())
	})

	It("tears down the drain of a deactivated hero", func() {
		heroWL := utiltesting.MakeWorkload("parked-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "parked-n1")

		By("keeping the taint while the hero is active")
		Consistently(taintedNodeNames, "2s").Should(ConsistOf("parked-n1"))

		By("deactivating the hero")
		patch := client.MergeFrom(heroWL.DeepCopy())
		active := false
		heroWL.Spec.Active = &active
		Expect(k8sClient.Patch(ctx, heroWL, patch)).To(Succeed())

		Eventually(taintedNodeNames).Should(BeEmpty())
	})

	It("leaves unattributable and foreign taints alone", func() {
		// Drain-key taint without an owner annotation: not ours.
		node := testingnode.MakeNode("mystery-n1").Ready().
			Taints(corev1.Taint{Key: testCfg.TaintKey, Value: "who-knows", Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		// Unrelated taint key entirely.
		other := testingnode.MakeNode("maintenance-n1").Ready().
			Taints(corev1.Taint{Key: maintenanceTaint, Value: "x", Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		Expect(k8sClient.Create(ctx, other)).To(Succeed())

		Consistently(func() []string {
			nodes := &corev1.NodeList{}
			Expect(k8sClient.List(ctx, nodes)).To(Succeed())
			var out []string
			for i := range nodes.Items {
				for _, t := range nodes.Items[i].Spec.Taints {
					// envtest's apiserver adds node.kubernetes.io taints
					// to fresh nodes; only ours and the fixture's matter.
					if t.Key == testCfg.TaintKey || t.Key == maintenanceTaint {
						out = append(out, nodes.Items[i].Name+"/"+t.Key)
					}
				}
			}
			return out
		}, "3s").Should(ConsistOf(
			"mystery-n1/"+testCfg.TaintKey,
			"maintenance-n1/example.com/maintenance",
		))
	})

	It("removes the taint once every hero pod is Running (placed)", func() {
		heroWL := utiltesting.MakeWorkload("placed-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 2).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "placed-n1", "placed-n2")

		By("admitting the hero into the drained nodes")
		Expect(fakekueue.Admit(ctx, k8sClient, heroWL, janitorCQ,
			map[string]int32{"placed-n1": 1, "placed-n2": 1})).To(Succeed())

		By("keeping taints while only one of two pods is Running")
		createRunningPod("placed-pod-0", "placed-n1", heroWL.Name)
		Consistently(taintedNodeNames, "3s").Should(ConsistOf("placed-n1", "placed-n2"))

		By("removing taints when the second pod runs")
		createRunningPod("placed-pod-1", "placed-n2", heroWL.Name)
		Eventually(taintedNodeNames).Should(BeEmpty())
	})

	It("times out a hero that is admitted here but never fully running", func() {
		// Admission is not the finish line: the drain deadline bounds the
		// whole journey through pods-Running. An admitted hero whose pods
		// never all start (bad image, unschedulable pods) must not pin the
		// drained domain forever.
		heroWL := utiltesting.MakeWorkload("limbo-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 2).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "limbo-n1", "limbo-n2")
		Expect(fakekueue.Admit(ctx, k8sClient, heroWL, janitorCQ,
			map[string]int32{"limbo-n1": 1, "limbo-n2": 1})).To(Succeed())

		By("keeping the taints while the deadline has not passed")
		createRunningPod("limbo-pod-0", "limbo-n1", heroWL.Name) // 1 of 2, forever
		Consistently(taintedNodeNames, "2s").Should(ConsistOf("limbo-n1", "limbo-n2"))

		By("jumping past the drain timeout")
		nowOffset.Store(int64(testCfg.DrainTimeout.Duration + time.Minute))
		nudgeNode("limbo-n1")
		nudgeNode("limbo-n2")

		By("tearing down with the timeout warning despite the admission")
		Eventually(taintedNodeNames).Should(BeEmpty())
		Eventually(eventsFor(hero)).Should(ContainElement(EventDrainTimedOut))
	})

	It("removes the taint immediately when the hero is admitted elsewhere", func() {
		heroWL := utiltesting.MakeWorkload("wanderer-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "drained-n1")
		createNode("elsewhere-n1")

		By("admitting the hero onto a node outside the drain")
		Expect(fakekueue.Admit(ctx, k8sClient, heroWL, janitorCQ,
			map[string]int32{"elsewhere-n1": 1})).To(Succeed())

		By("untainting at admission, without waiting for Running pods")
		Eventually(taintedNodeNames).Should(BeEmpty())
	})

	It("ignores our key under a different effect", func() {
		node := testingnode.MakeNode("prefer-n1").Ready().
			Taints(corev1.Taint{Key: testCfg.TaintKey, Value: "hero-cq", Effect: corev1.TaintEffectPreferNoSchedule}).
			Annotation(taint.OwnerAnnotation, ns.Name+"/nobody").
			Obj()
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		Consistently(func() int {
			n := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prefer-n1"}, n)).To(Succeed())
			count := 0
			for _, t := range n.Spec.Taints {
				if t.Key == testCfg.TaintKey {
					count++
				}
			}
			return count
		}, "3s").Should(Equal(1), "PreferNoSchedule taint under our key must not be touched")
	})

	It("cleans our taint on a node that also carries foreign taints", func() {
		hero := types.NamespacedName{Namespace: ns.Name, Name: "ghost-mixed"}
		node := testingnode.MakeNode("mixed-n1").Ready().
			Taints(corev1.Taint{Key: maintenanceTaint, Value: "x", Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		Expect(taint.EnsureTaint(ctx, k8sClient, "mixed-n1", testCfg.TaintKey, hero, janitorCQ, testNow)).To(Succeed())

		By("removing only our taint; the maintenance taint survives")
		Eventually(func() []string {
			n := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mixed-n1"}, n)).To(Succeed())
			var keys []string
			for _, t := range n.Spec.Taints {
				if t.Key == testCfg.TaintKey || t.Key == maintenanceTaint {
					keys = append(keys, t.Key)
				}
			}
			return keys
		}).Should(ConsistOf(maintenanceTaint))
	})

	It("tears down when the hero finishes", func() {
		heroWL := utiltesting.MakeWorkload("done-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "done-n1")

		Consistently(taintedNodeNames, "2s").Should(ConsistOf("done-n1"))

		Expect(fakekueue.Finish(ctx, k8sClient, heroWL)).To(Succeed())
		Eventually(taintedNodeNames).Should(BeEmpty())
	})

	It("tears down and stamps the hero when the drain times out", func() {
		heroWL := utiltesting.MakeWorkload("slow-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		timedOutBefore := testutil.ToFloat64(metrics.DrainsTimedOut)
		completedBefore := testutil.ToFloat64(metrics.DrainsCompleted.WithLabelValues(metrics.OutcomeTimedOut))
		drainedNodes(hero, "slow-n1")

		By("keeping the taint before the deadline")
		Consistently(taintedNodeNames, "2s").Should(ConsistOf("slow-n1"))

		By("jumping past the drain timeout")
		nowOffset.Store(int64(testCfg.DrainTimeout.Duration + time.Minute))
		// Nudge the node so the janitor re-evaluates now rather than at
		// its armed requeue.
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "slow-n1"}, node)).To(Succeed())
		patch := client.MergeFrom(node.DeepCopy())
		node.Labels = map[string]string{"test/nudge": "1"}
		Expect(k8sClient.Patch(ctx, node, patch)).To(Succeed())

		By("tearing down")
		Eventually(taintedNodeNames).Should(BeEmpty())
		_ = hero // no backoff stamp by design; timeouts surface via events/metrics

		By("counting the timed-out drain exactly once")
		Eventually(func() float64 { return testutil.ToFloat64(metrics.DrainsTimedOut) }).
			Should(Equal(timedOutBefore + 1))
		Expect(testutil.ToFloat64(metrics.DrainsCompleted.WithLabelValues(metrics.OutcomeTimedOut))).
			To(Equal(completedBefore + 1))
	})

	It("restarts a lost drain clock instead of timing out", func() {
		heroWL := utiltesting.MakeWorkload("clockless-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		hero := types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}
		drainedNodes(hero, "clockless-n1")

		By("stripping the started-at annotation")
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "clockless-n1"}, node)).To(Succeed())
		patch := client.MergeFrom(node.DeepCopy())
		delete(node.Annotations, taint.StartedAtAnnotation)
		Expect(k8sClient.Patch(ctx, node, patch)).To(Succeed())

		By("rewriting the clock rather than tearing down")
		Eventually(func() string {
			n := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "clockless-n1"}, n)).To(Succeed())
			return n.Annotations[taint.StartedAtAnnotation]
		}).ShouldNot(BeEmpty())
		Consistently(taintedNodeNames, "2s").Should(ConsistOf("clockless-n1"))
	})

	It("keeps a live hero's drain untouched", func() {
		heroWL := utiltesting.MakeWorkload("live-hero", ns.Name).
			PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
			Obj()
		Expect(k8sClient.Create(ctx, heroWL)).To(Succeed())
		drainedNodes(types.NamespacedName{Namespace: ns.Name, Name: heroWL.Name}, "live-n1", "live-n2")

		Consistently(taintedNodeNames, "3s").Should(ConsistOf("live-n1", "live-n2"))
	})
})
