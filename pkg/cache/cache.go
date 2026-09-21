// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package cache configures the manager's informer caches.
//
// The controller watches the three highest-cardinality types in a GPU
// cluster — every Pod, every Node, every Kueue Workload — and the default
// cache would keep each object whole. Most of each object is fields no
// reconciler reads: per pod the managed fields, the status and the init
// containers; per node the managed fields and the image list; per workload
// the pod templates. The cached footprint therefore grows with the cluster
// for no benefit. Worse, a cache List DeepCopies every object it returns,
// so the per-object size also sets the allocation cost of every List the
// controller makes.
//
// The transforms below run once as an object enters the cache and drop
// everything outside the field inventory each type's readers actually
// touch. Adding a field read anywhere in the controller means adding it
// here too — a dropped field reads as its zero value, it does not fail
// loudly.
package cache

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/config"
)

// Options returns the manager's cache options. Kueue's ClusterQueues,
// LocalQueues, ResourceFlavors and Topologies are left whole: they are
// few, small, and read field by field.
func Options(cfg *config.Config) ctrlcache.Options {
	return ctrlcache.Options{
		DefaultTransform: ctrlcache.TransformStripManagedFields(),
		ByObject: map[client.Object]ctrlcache.ByObject{
			&corev1.Pod{}: {
				Transform: TransformPod(cfg),
				Field: fields.AndSelectors(
					fields.OneTermNotEqualSelector("status.phase", string(corev1.PodSucceeded)),
					fields.OneTermNotEqualSelector("status.phase", string(corev1.PodFailed)),
				),
			},
			&corev1.Node{}:    {Transform: TransformNode},
			&kueue.Workload{}: {Transform: TransformWorkload},
		},
	}
}

// TransformPod keeps a pod's identity, its placement, its phase, the kueue
// workload annotation that makes it a victim, the labels that can make it
// non-blocking, and its GPU requests. That is the whole of what
// pkg/snapshot and both reconcilers read from a Pod.
//
// Containers requesting no GPU are dropped outright: podGPURequest sums
// over containers, so a container contributing zero is indistinguishable
// from an absent one.
func TransformPod(cfg *config.Config) toolscache.TransformFunc {
	gpu := cfg.GPUResourceName
	return func(obj any) (any, error) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil // tombstone or foreign type; not ours to strip
		}
		pod.ManagedFields = nil
		pod.OwnerReferences = nil
		pod.Finalizers = nil
		pod.Annotations = keepKeys(pod.Annotations, kueue.WorkloadAnnotation)
		pod.Status = corev1.PodStatus{Phase: pod.Status.Phase}

		containers := pod.Spec.Containers[:0]
		for i := range pod.Spec.Containers {
			req, hasReq := pod.Spec.Containers[i].Resources.Requests[gpu]
			lim, hasLim := pod.Spec.Containers[i].Resources.Limits[gpu]
			if !hasReq && !hasLim {
				continue
			}
			ctr := corev1.Container{}
			if hasReq {
				ctr.Resources.Requests = corev1.ResourceList{gpu: req}
			}
			if hasLim {
				ctr.Resources.Limits = corev1.ResourceList{gpu: lim}
			}
			containers = append(containers, ctr)
		}
		pod.Spec = corev1.PodSpec{NodeName: pod.Spec.NodeName, Containers: containers}
		return pod, nil
	}
}

// TransformNode strips node STATUS down to the allocatable capacity and
// the Ready condition. The image list and the full condition list are
// among the largest parts of a node object and neither is read here.
//
// Metadata and spec are deliberately kept VERBATIM, including
// managedFields: pkg/taint applies and removes the drain taint with
// Get+Update, so anything dropped from a cached node's metadata or spec
// would be dropped from the node itself on the next taint write. Status is
// safe to strip because it is a subresource — a Node update never persists
// it.
func TransformNode(obj any) (any, error) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	var conditions []corev1.NodeCondition
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			conditions = append(conditions, node.Status.Conditions[i])
		}
	}
	node.Status = corev1.NodeStatus{
		Allocatable: node.Status.Allocatable,
		Conditions:  conditions,
	}
	return node, nil
}

// TransformWorkload drops the pod templates kueue carries on every
// Workload — typically the largest part of the object — down to the two
// things read from them: the GPU requests that size the hero's demand, and
// the tolerations hero identification checks against the drain taint.
// Everything the cost formula and the reconcilers need lives in spec
// counts, spec priority, the conditions and the admission.
//
// Safe to strip because every Workload write in this controller is a merge
// patch computed against the object it just read: a patch carries only the
// fields it changes, so absent fields are never sent.
func TransformWorkload(obj any) (any, error) {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return obj, nil
	}
	wl.ManagedFields = nil
	wl.OwnerReferences = nil
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		ps.Template = corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers:  stripToResources(ps.Template.Spec.Containers),
			Tolerations: ps.Template.Spec.Tolerations,
		}}
	}
	return wl, nil
}

// stripToResources reduces containers to their resource requirements.
// Unlike the Pod transform this keeps every container: the GPU resource
// name is a config value, and a podset's per-pod request is summed the
// same way regardless, so there is nothing to gain from guessing here.
func stripToResources(containers []corev1.Container) []corev1.Container {
	out := containers[:0]
	for i := range containers {
		out = append(out, corev1.Container{Resources: containers[i].Resources})
	}
	return out
}

// keepKeys returns the subset of m under keys, or nil when none are
// present (an empty map costs an allocation per object).
func keepKeys(m map[string]string, keys ...string) map[string]string {
	var out map[string]string
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(keys))
		}
		out[k] = v
	}
	return out
}
