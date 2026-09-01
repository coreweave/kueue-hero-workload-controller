// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package drain serializes drains: one runs at a time per cluster, and
// when several heroes are stuck the same one is always chosen next. No
// queue data structure exists anywhere — each reconcile recomputes both
// answers from live state, so restarts cannot lose or duplicate a slot.
package drain

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/hero"
	"github.com/coreweave/kueue-hero-workload-controller/pkg/taint"
)

// queueOrdering mirrors the kueue scheduler's FIFO key exactly by calling
// kueue's own implementation (pkg/workload GetQueueOrderTimestamp with the
// EvictionTimestamp default from apis/config defaults.go): a workload
// evicted by a PodsReady timeout or an admission check re-queues by its
// eviction time, everything else by creation time. Exact parity matters —
// NextHero must pick the same hero kueue will admit first, or the drained
// domain can go to the wrong hero.
var queueOrdering = workload.Ordering{PodsReadyRequeuingTimestamp: kueueconfig.EvictionTimestamp}

// NextHero picks which stuck hero drains next: highest priority first,
// then oldest (creation time), then name for full determinism. Every
// reconcile of every stuck hero runs this over the same candidate list and
// only proceeds if it IS the winner — a queue without a queue.
func NextHero(stuck []kueue.Workload) *kueue.Workload {
	var best *kueue.Workload
	for i := range stuck {
		if best == nil || heroBefore(&stuck[i], best) {
			best = &stuck[i]
		}
	}
	return best
}

func heroBefore(a, b *kueue.Workload) bool {
	if pa, pb := hero.Priority(a), hero.Priority(b); pa != pb {
		return pa > pb
	}
	ta, tb := queueOrdering.GetQueueOrderTimestamp(a), queueOrdering.GetQueueOrderTimestamp(b)
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	if a.Namespace != b.Namespace {
		return strings.Compare(a.Namespace, b.Namespace) < 0
	}
	return strings.Compare(a.Name, b.Name) < 0
}

// BlockingDrain returns a drain that stops the given hero from starting
// its own: any in-flight drain owned by a DIFFERENT hero, including
// unresolvable taints (zero owner) — those must be garbage-collected by
// the janitor before new drains begin. Nil when the hero itself owns the
// only drain (resuming after a crash) or no drain exists.
func BlockingDrain(drains map[types.NamespacedName]*taint.Drain, self types.NamespacedName) *taint.Drain {
	// Deterministic scan order so logs/events are stable.
	var blocking *taint.Drain
	for owner, d := range drains {
		if owner == self {
			continue
		}
		if blocking == nil || ownerBefore(owner, blocking.Owner) {
			blocking = d
		}
	}
	return blocking
}

func ownerBefore(a, b types.NamespacedName) bool {
	if a.Namespace != b.Namespace {
		return strings.Compare(a.Namespace, b.Namespace) < 0
	}
	return strings.Compare(a.Name, b.Name) < 0
}
