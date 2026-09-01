// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package selection

import (
	"math"
	"testing"
	"time"
)

func TestCostGoldenCases(t *testing.T) {
	c := cfg()
	h := heroSpec(128, 1)

	cases := []struct {
		name string
		v    VictimWorkload
		want float64
	}{
		{
			// Design-doc style worked example: same-CQ, half hero
			// priority, small, brand new.
			name: "same-CQ medium priority young",
			v:    VictimWorkload{Priority: 500, ClusterQueue: heroCQ, PodCount: 2, RunningFor: 0},
			want: 1 * (0.7*0.5 + 0.2*(2.0/18.0) + 0.1*0),
		},
		{
			// Cross-CQ low priority at exactly the half-life.
			name: "cross-CQ low priority at half-life",
			v:    VictimWorkload{Priority: 200, ClusterQueue: otherCQ, PodCount: 2, RunningFor: 6 * time.Hour},
			want: 5 * (0.7*0.2 + 0.2*(2.0/18.0) + 0.1*0.5),
		},
		{
			name: "zero everything",
			v:    VictimWorkload{ClusterQueue: heroCQ},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Cost(tc.v, h, c)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Cost = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCostMonotonicity(t *testing.T) {
	c := cfg()
	h := heroSpec(128, 1)
	base := VictimWorkload{Priority: 300, ClusterQueue: heroCQ, PodCount: 4, RunningFor: time.Hour}

	higherPrio := base
	higherPrio.Priority = 600
	bigger := base
	bigger.PodCount = 40
	older := base
	older.RunningFor = 20 * time.Hour
	crossCQ := base
	crossCQ.ClusterQueue = otherCQ

	baseCost := Cost(base, h, c)
	for name, v := range map[string]VictimWorkload{
		"higher priority": higherPrio,
		"bigger":          bigger,
		"older":           older,
		"cross-CQ":        crossCQ,
	} {
		if got := Cost(v, h, c); got <= baseCost {
			t.Errorf("%s cost %v not > base %v", name, got, baseCost)
		}
	}

	// m_rel dominance: the worst same-CQ victim stays cheaper than a
	// mild cross-CQ victim under default weights.
	worstSameCQ := VictimWorkload{Priority: 999, ClusterQueue: heroCQ, PodCount: 1000, RunningFor: 100 * time.Hour}
	mildCrossCQ := VictimWorkload{Priority: 300, ClusterQueue: otherCQ, PodCount: 4, RunningFor: time.Hour}
	if Cost(worstSameCQ, h, c) >= Cost(mildCrossCQ, h, c) {
		t.Error("cross-CQ multiplier did not dominate")
	}
}
