// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package compat verifies at compile time that the kueue packages this
// controller depends on build against the pinned kueue module version.
package compat

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/tas"
)

var (
	_ = kueue.WorkloadQuotaReserved
	_ = kueue.WorkloadDeactivated
	_ = kueue.PodSetRequiredTopologyAnnotation
	_ = tas.InternalFrom
	_ = tas.DomainID
)
