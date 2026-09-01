// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package metrics registers the controller's Prometheus metrics on
// controller-runtime's global registry (served by the manager's metrics
// endpoint). Drain-level counters count DRAINS, not nodes: a drain spans
// many tainted nodes but starts once and completes once.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Outcome label values for DrainsCompleted. A multi-node drain's outcome is
// the reason its LAST taint was removed (per-node teardown reasons can
// differ, e.g. placed_elsewhere on one node and placed on another).
const (
	OutcomeAbandoned       = "abandoned"        // hero deleted
	OutcomeDeactivated     = "deactivated"      // hero spec.active=false
	OutcomeFinished        = "finished"         // hero Finished condition
	OutcomePlaced          = "placed"           // hero fully Running in the drained domain
	OutcomePlacedElsewhere = "placed_elsewhere" // hero admitted outside the drained domain
	OutcomeTimedOut        = "timed_out"        // drain never converged within DrainTimeout
	OutcomeAborted         = "aborted"          // drain controller unwound mid-drain (demotion, quota shrink, infeasible replan)
)

// Selection outcome label values.
const (
	SelectionPlanned             = "planned"
	SelectionNoFeasibleDomains   = "no_feasible_domains"
	SelectionAllFeasibleOccupied = "all_feasible_hero_occupied"
)

var (
	// DrainsStarted counts drains started (first taint applied for a hero).
	DrainsStarted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hero_drains_started_total",
		Help: "Number of hero drains started (domains tainted for a stuck hero).",
	})

	// DrainsCompleted counts drains fully torn down, by outcome.
	DrainsCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hero_drains_completed_total",
		Help: "Number of hero drains completed (all taints removed), by outcome.",
	}, []string{"outcome"})

	// DrainsTimedOut counts drains torn down for exceeding DrainTimeout.
	// There is deliberately NO retry backoff after a timeout; a rising rate
	// here means something structural (victims not vacating, capacity math
	// wrong) and is the operator's investigation signal.
	DrainsTimedOut = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hero_drains_timed_out_total",
		Help: "Number of hero drains torn down because they did not converge within the drain timeout.",
	})

	// DrainsInFlight is the number of hero drains currently holding taints.
	// With cluster-wide serialization this is 0 or 1 in steady state.
	DrainsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hero_drains_in_flight",
		Help: "Number of hero drains currently in flight (holding node taints).",
	})

	// SelectionOutcomes counts domain-selection results for stuck heroes.
	SelectionOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hero_selection_outcomes_total",
		Help: "Number of domain-selection attempts for stuck heroes, by outcome.",
	}, []string{"outcome"})

	// VictimsSuspended counts victim workloads suspended for a drain.
	VictimsSuspended = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hero_victims_suspended_total",
		Help: "Number of victim workloads suspended (spec.active=false) to make room for a hero.",
	})

	// VictimsReactivated counts victim workloads handed back.
	VictimsReactivated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hero_victims_reactivated_total",
		Help: "Number of victim workloads reactivated after a hero drain.",
	})
)

func init() {
	metrics.Registry.MustRegister(
		DrainsStarted,
		DrainsCompleted,
		DrainsTimedOut,
		DrainsInFlight,
		SelectionOutcomes,
		VictimsSuspended,
		VictimsReactivated,
	)
}
