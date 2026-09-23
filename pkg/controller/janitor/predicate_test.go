// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package janitorctrl

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// A pod event that matters but gets dropped here fails silently: the
// janitor never learns the hero's pods started and holds the drain taint
// until the timeout. The two predicates are ANDed at the watch, so they
// are tested the way they are wired.
var _ = Describe("pod predicates", func() {
	var pred predicate.Predicate

	// heroPod is a kueue-managed pod, the shape the janitor counts.
	heroPod := func(phase corev1.PodPhase, node string) *corev1.Pod {
		return testingpod.MakePod("hero-0", "default").
			Annotation(kueue.WorkloadAnnotation, "hero").
			StatusPhase(phase).
			NodeName(node).
			Obj()
	}

	BeforeEach(func() {
		pred = predicate.And(kueueManagedPod(), podMoved())
	})

	It("passes a hero pod going Pending -> Running", func() {
		Expect(pred.Update(event.UpdateEvent{
			ObjectOld: heroPod(corev1.PodPending, "gpu-1"),
			ObjectNew: heroPod(corev1.PodRunning, "gpu-1"),
		})).To(BeTrue())
	})

	It("passes a hero pod landing on a node", func() {
		Expect(pred.Update(event.UpdateEvent{
			ObjectOld: heroPod(corev1.PodPending, ""),
			ObjectNew: heroPod(corev1.PodPending, "gpu-1"),
		})).To(BeTrue())
	})

	It("drops an update that only rewrites container statuses", func() {
		// Image pulls, restart counts and readiness churn constantly
		// while the phase stays put; none of it changes a teardown.
		oldPod := heroPod(corev1.PodRunning, "gpu-1")
		newPod := heroPod(corev1.PodRunning, "gpu-1")
		newPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "c", Ready: true, RestartCount: 1,
		}}
		Expect(pred.Update(event.UpdateEvent{ObjectOld: oldPod, ObjectNew: newPod})).To(BeFalse())
	})

	It("drops every event for a pod kueue does not manage", func() {
		plain := heroPod(corev1.PodRunning, "gpu-1")
		delete(plain.Annotations, kueue.WorkloadAnnotation)
		moved := heroPod(corev1.PodPending, "gpu-1")
		delete(moved.Annotations, kueue.WorkloadAnnotation)

		Expect(pred.Create(event.CreateEvent{Object: plain})).To(BeFalse())
		Expect(pred.Update(event.UpdateEvent{ObjectOld: moved, ObjectNew: plain})).To(BeFalse())
		Expect(pred.Delete(event.DeleteEvent{Object: plain})).To(BeFalse())
		Expect(pred.Generic(event.GenericEvent{Object: plain})).To(BeFalse())
	})

	It("passes the delete of an annotated pod", func() {
		// Only updates are filtered: a hero pod disappearing changes
		// the running count the janitor waits on.
		Expect(pred.Delete(event.DeleteEvent{
			Object: heroPod(corev1.PodRunning, "gpu-1"),
		})).To(BeTrue())
		Expect(pred.Create(event.CreateEvent{
			Object: heroPod(corev1.PodPending, ""),
		})).To(BeTrue())
	})

	It("passes an update carrying objects of an unexpected type", func() {
		node := &corev1.Node{}
		node.Annotations = map[string]string{kueue.WorkloadAnnotation: "hero"}
		Expect(pred.Update(event.UpdateEvent{ObjectOld: node, ObjectNew: node})).To(BeTrue())
	})
})
