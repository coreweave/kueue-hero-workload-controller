// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package drainctrl

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/taint"
)

// A node update that matters but gets dropped here fails silently: no
// error, no retry, the hero simply never wakes up. So every field the
// drain pipeline reads from a node needs a spec below — if the snapshot
// starts reading a new one and nobody teaches the predicate about it,
// one of these should be what says so.
var _ = Describe("nodeAffectsPlacement", func() {
	var pred predicate.Predicate

	// base is a plain healthy GPU node, the shape every spec mutates.
	base := func() *testingnode.NodeWrapper {
		return testingnode.MakeNode("gpu-1").
			Label("cloud.provider.com/topology-block", "block-a").
			Annotation(taint.StartedAtAnnotation, "2026-08-12T12:00:00Z").
			StatusAllocatable(corev1.ResourceList{
				testCfg.GPUResourceName: resource.MustParse("8"),
			}).
			Ready()
	}

	// update runs the predicate over base() -> mutate(base()).
	update := func(mutate func(*testingnode.NodeWrapper) *testingnode.NodeWrapper) bool {
		return pred.Update(event.UpdateEvent{
			ObjectOld: base().Obj(),
			ObjectNew: mutate(base()).Obj(),
		})
	}

	BeforeEach(func() {
		pred = (&Reconciler{Cfg: &testCfg}).nodeAffectsPlacement()
	})

	It("drops a Ready heartbeat rewrite", func() {
		// Kubelet does this every few seconds on every node in the
		// cluster; each one used to fan out into a list of every stuck
		// workload. This is the spec the whole predicate exists for.
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.ConditionHeartbeat(corev1.NodeReady,
				metav1.NewTime(testNow.Add(time.Minute)))
		})).To(BeFalse())
	})

	It("drops an identical update", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n
		})).To(BeFalse())
	})

	It("passes a taint change", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Taints(corev1.Taint{
				Key:    "example.com/maintenance",
				Effect: corev1.TaintEffectNoSchedule,
			})
		})).To(BeTrue())
	})

	It("passes a flip to unschedulable", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Unschedulable()
		})).To(BeTrue())
	})

	It("passes a topology label change", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Label("cloud.provider.com/topology-block", "block-b")
		})).To(BeTrue())
	})

	It("passes a drain annotation change", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Annotation(taint.StartedAtAnnotation, "2026-08-12T13:00:00Z")
		})).To(BeTrue())
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Annotation(taint.NudgeAnnotation, "2026-08-12T13:00:00Z")
		})).To(BeTrue())
	})

	It("drops an annotation the drain pipeline never reads", func() {
		// Deliberate: annotations are not compared wholesale the way
		// labels are, because unrelated controllers write to them.
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.Annotation("example.com/unrelated", "x")
		})).To(BeFalse())
	})

	It("passes a GPU allocatable change", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.StatusAllocatable(corev1.ResourceList{
				testCfg.GPUResourceName: resource.MustParse("4"),
			})
		})).To(BeTrue())
	})

	It("passes a Ready flip", func() {
		Expect(update(func(n *testingnode.NodeWrapper) *testingnode.NodeWrapper {
			return n.NotReady()
		})).To(BeTrue())
	})

	It("passes create and delete events", func() {
		// Only updates are filtered: a node appearing or disappearing
		// always moves cluster capacity.
		Expect(pred.Create(event.CreateEvent{Object: base().Obj()})).To(BeTrue())
		Expect(pred.Delete(event.DeleteEvent{Object: base().Obj()})).To(BeTrue())
	})

	It("passes an update carrying objects of an unexpected type", func() {
		Expect(pred.Update(event.UpdateEvent{
			ObjectOld: &corev1.Pod{},
			ObjectNew: &corev1.Pod{},
		})).To(BeTrue())
	})
})
