// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package taint

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NudgeAnnotation exists to close a hole between how this controller frees
// capacity and how kueue notices capacity.
//
// The drain frees a domain by evicting victims and letting their pods
// terminate — and at kueue 0.16.9 (the customer's version) that is
// invisible: kueue retries a workload it has marked inadmissible only on
// kueue events, and victim pods finishing termination is not one. Worse,
// kueue memorizes each REJECTED workload shape (a scheduling hash of
// priority, pod spec, counts and topologyRequest) in the ClusterQueue's
// noFitSchedulingHashes set, and re-adds of a memorized shape are parked
// again WITHOUT a scheduling attempt (kueue pkg/cache/queue/
// cluster_queue.go, the noFitSchedulingHashes gate in PushOrUpdate) — so
// merely updating the hero Workload cannot wake it. Left alone, the hero
// sits Pending beside a fully drained, taint-protected, EMPTY domain until
// the drain times out. (The timeout teardown then works by accident: the
// untaint is a node change, see below.)
//
// The one eraser for that rejection memory is a NODE change: kueue's TAS
// node watcher treats any change to a TAS-flavor node's taints, labels,
// allocatable, conditions — or ANNOTATIONS, unfiltered — as "capacity may
// have changed", wipes the NoFit memory, and moves every parked workload
// back into the scheduling heap for a genuine retry (kueue
// pkg/controller/tas/resource_flavor.go, extractNodeAnnotationsChange →
// NotifyRetryInadmissible → queueInadmissibleWorkloads). Rewriting this
// annotation on one drained node is the lightest change that takes that
// path: annotations carry no scheduling meaning, so nothing about real
// placement is disturbed.
//
// The value is the RFC3339 time of the last bump. It changing every bump
// is what guarantees a real update event (rewriting an identical value is
// a no-op the watch never sees), and it doubles as the rate limiter — no
// in-memory state, so a controller restart loses nothing. The drain
// controller bumps it while [own drain in flight + drained nodes empty of
// victims + hero without quota reservation]: the first bump immediately,
// repeats paced by config NudgeInterval (default 2m — deliberately slow,
// since every bump re-evaluates all pending workloads; the repeats only
// insure against kueue's node-event batching racing the first bump).
// RemoveOwnedTaint strips it with the other drain annotations, so it
// never outlives its drain.
const NudgeAnnotation = "hero.coreweave.com/scheduling-nudge"

// NudgeKueue bumps the nudge annotation on the node unless the last bump
// is younger than minInterval. Returns whether it patched.
func NudgeKueue(ctx context.Context, c client.Client, nodeName string, now time.Time, minInterval time.Duration) (bool, error) {
	node := &corev1.Node{}
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return false, err
	}
	if last, err := time.Parse(time.RFC3339, node.Annotations[NudgeAnnotation]); err == nil &&
		now.Sub(last) < minInterval {
		return false, nil
	}
	patch := client.MergeFrom(node.DeepCopy())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[NudgeAnnotation] = now.UTC().Format(time.RFC3339)
	if err := c.Patch(ctx, node, patch); err != nil {
		return false, err
	}
	return true, nil
}
