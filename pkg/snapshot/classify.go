// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package snapshot

import (
	corev1 "k8s.io/api/core/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/config"
)

type podClass int

const (
	// podVictim: Kueue-managed — evictable by toggling its Workload's
	// spec.active, so its GPUs are reclaimable and it costs disruption.
	podVictim podClass = iota
	// podNonReclaimable: nothing we can do moves it (DaemonSet, static
	// pod, non-Kueue operator, ...); its GPUs are permanently occupied.
	podNonReclaimable
	// podNonBlocking: non-Kueue but declared transient via
	// NonBlockingPodLabels (e.g. hpc-verification); ignored entirely.
	podNonBlocking
)

// classify buckets a GPU-requesting pod for the capacity math.
//
// The only lever this controller has is suspending Kueue Workloads, so the
// split is: Kueue-managed (carries the kueue.x-k8s.io/workload annotation,
// set when kueue starts the job) = victim; everything else is
// non-reclaimable unless explicitly declared non-blocking. DaemonSet and
// static pods fall out of the non-Kueue branch without special-casing.
func classify(pod *corev1.Pod, cfg *config.Config) podClass {
	if _, ok := pod.Annotations[kueue.WorkloadAnnotation]; ok {
		return podVictim
	}
	for k, v := range cfg.NonBlockingPodLabels {
		if pod.Labels[k] == v {
			return podNonBlocking // ANY-match
		}
	}
	return podNonReclaimable
}
