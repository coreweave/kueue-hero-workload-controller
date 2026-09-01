// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package hero

import (
	"slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// RequiredTopologyLevels returns the distinct topology levels (node label
// keys) the workload's podsets require for draining, in podset order.
// Per the customer contract, heroes carry the slice pair — a podset
// contributes its level only when BOTH podSetSliceRequiredTopology and
// podSetSliceSize are set (kueue.x-k8s.io/podset-slice-required-topology +
// kueue.x-k8s.io/podset-slice-size, validated together by kueue's
// webhook): each slice must fit one domain at that level, and the workload
// pends forever on no-fit.
//
// Plain podset-required-topology is deliberately NOT a drain trigger.
// Podsets without the slice pair are skipped: empty result = workload is
// not drainable-for. When podsets disagree on the level, drain at the
// coarsest (see CoarsestLevel).
func RequiredTopologyLevels(wl *kueue.Workload) []string {
	var levels []string
	for i := range wl.Spec.PodSets {
		tr := wl.Spec.PodSets[i].TopologyRequest
		if tr == nil {
			continue
		}
		if tr.PodSetSliceRequiredTopology == nil || *tr.PodSetSliceRequiredTopology == "" ||
			tr.PodSetSliceSize == nil || *tr.PodSetSliceSize <= 0 {
			continue
		}
		if !slices.Contains(levels, *tr.PodSetSliceRequiredTopology) {
			levels = append(levels, *tr.PodSetSliceRequiredTopology)
		}
	}
	return levels
}

// CoarsestLevel picks the drain level: the highest of the required levels
// in the Topology hierarchy. topologyLevels is the Topology object's
// spec.levels, ordered highest to lowest per the CRD contract. Coarsest
// wins because an emptied block satisfies any rack requirement inside it,
// never the converse. Returns false if a required level is not in the
// hierarchy (misconfiguration — surface it, don't drain).
func CoarsestLevel(required, topologyLevels []string) (string, bool) {
	best := -1
	for _, r := range required {
		idx := slices.Index(topologyLevels, r)
		if idx < 0 {
			return "", false
		}
		if best == -1 || idx < best {
			best = idx
		}
	}
	if best < 0 {
		return "", false
	}
	return topologyLevels[best], true
}
