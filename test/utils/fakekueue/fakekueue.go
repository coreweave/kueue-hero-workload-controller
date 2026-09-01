// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package fakekueue simulates the slices of kueue behavior the drain
// controller depends on, for envtest suites where no real kueue runs.
// Tests invoke the steps explicitly between assertions — deterministic,
// no background goroutines.
package fakekueue

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// StuckMessage is the kueue 0.16.9-shaped condition message for a TAS
// no-fit at the given level.
func StuckMessage(level string, pods int) string {
	return fmt.Sprintf(
		"couldn't assign flavors to pod set main: topology %q doesn't allow to fit any of %d pod(s)",
		level, pods)
}

// MarkStuck stamps the workload with the 0.16.9 pending condition for a
// TAS no-fit (reason literally "Pending", cause only in the message).
func MarkStuck(ctx context.Context, c client.Client, wl *kueue.Workload, level string, pods int) error {
	patch := client.MergeFrom(wl.DeepCopy())
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:    kueue.WorkloadQuotaReserved,
		Status:  metav1.ConditionFalse,
		Reason:  "Pending",
		Message: StuckMessage(level, pods),
	})
	return c.Status().Patch(ctx, wl, patch)
}

// EvictDeactivated performs kueue's reaction to spec.active=false for
// every deactivated, still-admitted workload: Evicted=True/Deactivated,
// admission cleared, QuotaReserved=False, and the workload's pods deleted
// (standing in for the job framework suspending the job).
func EvictDeactivated(ctx context.Context, c client.Client) (int, error) {
	list := &kueue.WorkloadList{}
	if err := c.List(ctx, list); err != nil {
		return 0, err
	}
	evicted := 0
	for i := range list.Items {
		wl := &list.Items[i]
		if wl.Spec.Active == nil || *wl.Spec.Active {
			continue
		}
		if meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
			continue
		}

		patch := client.MergeFrom(wl.DeepCopy())
		meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadEvicted,
			Status:  metav1.ConditionTrue,
			Reason:  kueue.WorkloadDeactivated,
			Message: "The workload is deactivated",
		})
		meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionFalse,
			Reason:  "Pending",
			Message: "The workload is deactivated",
		})
		wl.Status.Admission = nil
		if err := c.Status().Patch(ctx, wl, patch); err != nil {
			return evicted, err
		}

		if err := deletePodsOf(ctx, c, wl); err != nil {
			return evicted, err
		}
		evicted++
	}
	return evicted, nil
}

func deletePodsOf(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(wl.Namespace)); err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Annotations[kueue.WorkloadAnnotation] != wl.Name {
			continue
		}
		zero := int64(0)
		if err := c.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// Admit stamps an admission with a topology assignment placing all pods
// on the given hostnames (levels = [kubernetes.io/hostname]).
func Admit(ctx context.Context, c client.Client, wl *kueue.Workload, cq string, podsPerHost map[string]int32) error {
	patch := client.MergeFrom(wl.DeepCopy())

	slices := make([]kueue.TopologyAssignmentSlice, 0, len(podsPerHost))
	for host, count := range podsPerHost {
		h := host
		n := count
		slices = append(slices, kueue.TopologyAssignmentSlice{
			DomainCount: 1,
			ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
				{Universal: &h},
			},
			PodCounts: kueue.TopologyAssignmentSlicePodCounts{Universal: &n},
		})
	}
	var total int32
	for _, n := range podsPerHost {
		total += n
	}
	wl.Status.Admission = &kueue.Admission{
		ClusterQueue: kueue.ClusterQueueReference(cq),
		PodSetAssignments: []kueue.PodSetAssignment{{
			Name:  "main",
			Count: &total,
			TopologyAssignment: &kueue.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Slices: slices,
			},
		}},
	}
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:   kueue.WorkloadQuotaReserved,
		Status: metav1.ConditionTrue,
		Reason: "QuotaReserved",
	})
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:   kueue.WorkloadAdmitted,
		Status: metav1.ConditionTrue,
		Reason: "Admitted",
	})
	return c.Status().Patch(ctx, wl, patch)
}

// Finish marks the workload Finished (success).
func Finish(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	patch := client.MergeFrom(wl.DeepCopy())
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:   kueue.WorkloadFinished,
		Status: metav1.ConditionTrue,
		Reason: "Succeeded",
	})
	return c.Status().Patch(ctx, wl, patch)
}

// Reactivated stamps the requeued state kueue sets after active flips
// back to true.
func Reactivated(ctx context.Context, c client.Client, wl *kueue.Workload, now time.Time) error {
	patch := client.MergeFrom(wl.DeepCopy())
	meta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:               kueue.WorkloadRequeued,
		Status:             metav1.ConditionTrue,
		Reason:             "Reactivated",
		LastTransitionTime: metav1.NewTime(now),
	})
	return c.Status().Patch(ctx, wl, patch)
}
