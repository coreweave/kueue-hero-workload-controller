// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

// Package taint owns the drain taint on nodes: applying it with an
// ownership marker naming the hero it serves, removing it only when the
// owner matches, and rebuilding drain state from the taints alone. The
// taint IS the controller's persistent state — no CRD, no database — so a
// crash can never orphan a drain invisibly.
package taint

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// OwnerAnnotation records the full <namespace>/<name> of the hero a
	// node is drained for. It is the ONLY ownership record: the taint
	// value is spent on admission control instead (see EnsureTaint), so a
	// drain-key taint without this annotation was not written by this
	// controller and is never blocked on or cleaned up.
	OwnerAnnotation = "hero.coreweave.com/drain-owner"
	// StartedAtAnnotation records when the drain began (RFC3339). Written
	// with the taint in the same update: NoSchedule taints have no
	// timeAdded field, and in-memory clocks die with the process.
	StartedAtAnnotation = "hero.coreweave.com/drain-started-at"

	maxTaintValueLen = 63
)

// Owner resolves which hero a node's drain taint belongs to, from the
// OwnerAnnotation alone (the taint value carries the ClusterQueue for
// admission control, not identity). Returns false when the node has no
// taint under key or no parseable owner annotation — such a taint was not
// written by this controller.
func Owner(node *corev1.Node, key string) (types.NamespacedName, bool) {
	found := false
	for i := range node.Spec.Taints {
		// Only the exact shape this controller writes counts: our key
		// with NoSchedule. Same key under another effect (possible —
		// key+effect is the uniqueness pair) was not written by us.
		if node.Spec.Taints[i].Key == key && node.Spec.Taints[i].Effect == corev1.TaintEffectNoSchedule {
			found = true
			break
		}
	}
	if !found {
		return types.NamespacedName{}, false
	}
	ann, ok := node.Annotations[OwnerAnnotation]
	if !ok {
		return types.NamespacedName{}, false
	}
	ns, name, cut := strings.Cut(ann, "/")
	if !cut || ns == "" || name == "" {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: ns, Name: name}, true
}

// EnsureTaint idempotently puts the drain taint on the node and records
// ownership + start time. The taint VALUE is the hero's ClusterQueue name:
// hero pod templates tolerate the key with operator Equal on their own CQ,
// so a drained domain admits only heroes from the drain owner's CQ —
// smaller heroes from other CQs cannot slip into partially-freed capacity.
// Identity (which hero) lives in the OwnerAnnotation.
//
// Read-modify-write with conflict retry — deliberately NOT server-side
// apply: node.spec.taints has no map list semantics, so SSA would take
// ownership of the whole list and fight the kubelet and other controllers.
//
// Returns an error without touching the node if it already carries the key
// for a DIFFERENT owner: overlapping drains are serialized upstream, so
// that means bookkeeping is broken somewhere and clobbering would leak the
// other drain's state.
func EnsureTaint(ctx context.Context, c client.Client, nodeName, key string, owner types.NamespacedName, clusterQueue string, now time.Time) error {
	if len(clusterQueue) > maxTaintValueLen {
		return fmt.Errorf("ClusterQueue name %q exceeds the %d-character taint value limit; hero CQ names must fit a taint value", clusterQueue, maxTaintValueLen)
	}
	ownerRef := owner.Namespace + "/" + owner.Name
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &corev1.Node{}
		if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return err
		}

		for i := range node.Spec.Taints {
			if node.Spec.Taints[i].Key != key {
				continue
			}
			if node.Annotations[OwnerAnnotation] == ownerRef {
				return nil // already ours; idempotent
			}
			return fmt.Errorf("node %s already tainted %s=%s (foreign owner)", nodeName, key, node.Spec.Taints[i].Value)
		}

		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key:    key,
			Value:  clusterQueue,
			Effect: corev1.TaintEffectNoSchedule,
		})
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		node.Annotations[OwnerAnnotation] = ownerRef
		if _, ok := node.Annotations[StartedAtAnnotation]; !ok {
			node.Annotations[StartedAtAnnotation] = now.UTC().Format(time.RFC3339)
		}
		return c.Update(ctx, node)
	})
}

// RemoveOwnedTaint removes the drain taint from the node if and only if the
// node's owner annotation names the given hero, and clears the drain
// annotations. Taints whose owner annotation names someone else (or is
// absent), and every taint under other keys, are left untouched. Returns
// whether a taint was removed.
func RemoveOwnedTaint(ctx context.Context, c client.Client, nodeName, key string, owner types.NamespacedName) (bool, error) {
	ownerRef := owner.Namespace + "/" + owner.Name
	removed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		removed = false
		node := &corev1.Node{}
		if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return nil // node gone = taint gone
			}
			return err
		}
		if node.Annotations[OwnerAnnotation] != ownerRef {
			return nil // not this hero's drain; leave everything alone
		}

		kept := node.Spec.Taints[:0]
		for i := range node.Spec.Taints {
			t := node.Spec.Taints[i]
			if t.Key == key && t.Effect == corev1.TaintEffectNoSchedule {
				removed = true
				continue
			}
			kept = append(kept, t)
		}
		if !removed {
			return nil
		}
		node.Spec.Taints = kept
		delete(node.Annotations, OwnerAnnotation)
		delete(node.Annotations, StartedAtAnnotation)
		delete(node.Annotations, NudgeAnnotation)
		return c.Update(ctx, node)
	})
	return removed, err
}

// Drain is one hero's in-flight drain, rebuilt purely from node state.
type Drain struct {
	Owner types.NamespacedName
	// Nodes carrying the owner's taint.
	Nodes []string
	// StartedAt is the earliest parseable StartedAtAnnotation across the
	// drain's nodes; zero when none survived (the janitor treats that as
	// "clock unknown, restart it").
	StartedAt time.Time
}

// FindDrains rebuilds every in-flight drain from the node list — the
// controller's crash-safe state recovery. Only taints that resolve to a
// hero owner count: a taint under our key with an unrecognizable value was
// not written by this controller, so it neither blocks new drains nor gets
// cleaned up — it is left for the operator, and the snapshot already
// treats its node as unusable.
func FindDrains(nodes []corev1.Node, key string) map[types.NamespacedName]*Drain {
	drains := map[types.NamespacedName]*Drain{}
	for i := range nodes {
		node := &nodes[i]
		owner, ok := Owner(node, key)
		if !ok {
			continue // no taint under our key, or not attributable to a hero
		}
		d, ok := drains[owner]
		if !ok {
			d = &Drain{Owner: owner}
			drains[owner] = d
		}
		d.Nodes = append(d.Nodes, node.Name)
		if raw, ok := node.Annotations[StartedAtAnnotation]; ok {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				if d.StartedAt.IsZero() || t.Before(d.StartedAt) {
					d.StartedAt = t
				}
			}
		}
	}
	return drains
}
