// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package drain

import (
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/taint"
)

var base = time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)

func stuckHero(name string, priority int32, created time.Time) kueue.Workload {
	return *utiltesting.MakeWorkload(name, "ns").
		Priority(priority).
		Creation(created).
		Obj()
}

func evictedHero(name string, priority int32, created, evictedAt time.Time) kueue.Workload {
	wl := stuckHero(name, priority, created)
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
		LastTransitionTime: metav1.NewTime(evictedAt),
	})
	return wl
}

func TestNextHero(t *testing.T) {
	cases := []struct {
		name  string
		stuck []kueue.Workload
		want  string
	}{
		{
			name: "highest priority wins",
			stuck: []kueue.Workload{
				stuckHero("low", 100, base),
				stuckHero("high", 900, base.Add(time.Hour)), // newer but higher
			},
			want: "high",
		},
		{
			name: "priority tie: oldest wins",
			stuck: []kueue.Workload{
				stuckHero("young", 500, base.Add(time.Hour)),
				stuckHero("old", 500, base),
			},
			want: "old",
		},
		{
			name: "full tie: lexicographic name",
			stuck: []kueue.Workload{
				stuckHero("bbb", 500, base),
				stuckHero("aaa", 500, base),
			},
			want: "aaa",
		},
		{
			name:  "single candidate",
			stuck: []kueue.Workload{stuckHero("only", 1, base)},
			want:  "only",
		},
		{
			// Kueue parity: a hero evicted by a PodsReady timeout
			// re-queues by its EVICTION time, so an older-created but
			// recently-evicted hero ranks behind a younger fresh one.
			name: "pods-ready-evicted hero uses eviction time",
			stuck: []kueue.Workload{
				evictedHero("old-but-evicted", 500, base, base.Add(2*time.Hour)),
				stuckHero("young-fresh", 500, base.Add(time.Hour)),
			},
			want: "young-fresh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextHero(tc.stuck)
			if got == nil || got.Name != tc.want {
				t.Errorf("NextHero = %v, want %s", got, tc.want)
			}
			// Order independence: reversed input, same winner.
			rev := make([]kueue.Workload, 0, len(tc.stuck))
			for _, wl := range slices.Backward(tc.stuck) {
				rev = append(rev, wl)
			}
			if got := NextHero(rev); got == nil || got.Name != tc.want {
				t.Errorf("NextHero(reversed) = %v, want %s", got, tc.want)
			}
		})
	}

	if NextHero(nil) != nil {
		t.Error("NextHero(nil) should be nil")
	}
}

func TestBlockingDrain(t *testing.T) {
	self := types.NamespacedName{Namespace: "ns", Name: "me"}
	other := types.NamespacedName{Namespace: "ns", Name: "other"}

	cases := []struct {
		name   string
		drains map[types.NamespacedName]*taint.Drain
		want   *types.NamespacedName // nil = not blocked
	}{
		{
			name:   "no drains",
			drains: map[types.NamespacedName]*taint.Drain{},
			want:   nil,
		},
		{
			name: "own drain does not block (crash resume)",
			drains: map[types.NamespacedName]*taint.Drain{
				self: {Owner: self, Nodes: []string{"n1"}},
			},
			want: nil,
		},
		{
			name: "foreign drain blocks",
			drains: map[types.NamespacedName]*taint.Drain{
				other: {Owner: other, Nodes: []string{"n1"}},
			},
			want: &other,
		},
		{
			name: "own plus foreign still blocks",
			drains: map[types.NamespacedName]*taint.Drain{
				self:  {Owner: self, Nodes: []string{"n1"}},
				other: {Owner: other, Nodes: []string{"n2"}},
			},
			want: &other,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BlockingDrain(tc.drains, self)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("BlockingDrain = %+v, want nil", got)
			case tc.want != nil && got == nil:
				t.Errorf("BlockingDrain = nil, want owner %v", *tc.want)
			case tc.want != nil && got.Owner != *tc.want:
				t.Errorf("BlockingDrain owner = %v, want %v", got.Owner, *tc.want)
			}
		})
	}
}
