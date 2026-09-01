// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package taint

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

const (
	key      = "hero.coreweave.com/taint"
	heroCQ   = "hero-cq"
	ownerRef = "team-a/hero-1"
	nodeName = "n1"
)

var (
	owner = types.NamespacedName{Namespace: "team-a", Name: "hero-1"}
	now   = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	ctx   = context.Background()
)

func newClient(t *testing.T, interceptors *interceptor.Funcs, nodes ...*corev1.Node) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, n := range nodes {
		b = b.WithObjects(n)
	}
	if interceptors != nil {
		b = b.WithInterceptorFuncs(*interceptors)
	}
	return b.Build()
}

func getNode(t *testing.T, c client.Client) *corev1.Node {
	t.Helper()
	n := &corev1.Node{}
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnsureTaintRejectsOverlongCQ(t *testing.T) {
	c := newClient(t, nil, testingnode.MakeNode(nodeName).Ready().Obj())
	longCQ := strings.Repeat("q", 64)
	if err := EnsureTaint(ctx, c, nodeName, key, owner, longCQ, now); err == nil {
		t.Fatal("accepted a CQ name that cannot fit a taint value")
	}
}

func TestEnsureTaintIdempotent(t *testing.T) {
	node := testingnode.MakeNode(nodeName).Ready().Obj()
	c := newClient(t, nil, node)

	if err := EnsureTaint(ctx, c, nodeName, key, owner, heroCQ, now); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTaint(ctx, c, nodeName, key, owner, heroCQ, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got := getNode(t, c)
	count := 0
	for _, taint := range got.Spec.Taints {
		if taint.Key == key {
			count++
			if taint.Value != heroCQ || taint.Effect != corev1.TaintEffectNoSchedule {
				t.Errorf("taint = %+v (value must be the ClusterQueue)", taint)
			}
		}
	}
	if count != 1 {
		t.Errorf("taint count = %d, want 1 (idempotent)", count)
	}
	if got.Annotations[OwnerAnnotation] != "team-a/hero-1" {
		t.Errorf("owner annotation = %q", got.Annotations[OwnerAnnotation])
	}
	// started-at written once, not refreshed by the second Ensure.
	if got.Annotations[StartedAtAnnotation] != now.UTC().Format(time.RFC3339) {
		t.Errorf("started-at = %q, want first-write time", got.Annotations[StartedAtAnnotation])
	}
}

func TestEnsureTaintRefusesForeignOwner(t *testing.T) {
	node := testingnode.MakeNode("n1").Ready().
		Taints(corev1.Taint{Key: key, Value: "team-b_other-hero", Effect: corev1.TaintEffectNoSchedule}).
		Obj()
	c := newClient(t, nil, node)

	if err := EnsureTaint(ctx, c, nodeName, key, owner, heroCQ, now); err == nil {
		t.Fatal("EnsureTaint overwrote a foreign drain taint")
	}
	got := getNode(t, c)
	if got.Spec.Taints[0].Value != "team-b_other-hero" {
		t.Errorf("foreign taint modified: %+v", got.Spec.Taints)
	}
}

func TestEnsureTaintPreservesUnrelatedTaints(t *testing.T) {
	node := testingnode.MakeNode("n1").Ready().
		Taints(corev1.Taint{Key: "example.com/maintenance", Value: "x", Effect: corev1.TaintEffectNoSchedule}).
		Obj()
	c := newClient(t, nil, node)

	if err := EnsureTaint(ctx, c, nodeName, key, owner, heroCQ, now); err != nil {
		t.Fatal(err)
	}
	got := getNode(t, c)
	if len(got.Spec.Taints) != 2 {
		t.Errorf("taints = %+v, want maintenance + drain", got.Spec.Taints)
	}
}

func TestEnsureTaintRetriesOnConflict(t *testing.T) {
	node := testingnode.MakeNode(nodeName).Ready().Obj()
	conflicts := 2
	funcs := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if conflicts > 0 {
				conflicts--
				return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, obj.GetName(), nil)
			}
			return c.Update(ctx, obj, opts...)
		},
	}
	c := newClient(t, &funcs, node)

	if err := EnsureTaint(ctx, c, nodeName, key, owner, heroCQ, now); err != nil {
		t.Fatalf("EnsureTaint did not survive conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("conflicts remaining = %d, retry path not exercised", conflicts)
	}
	got := getNode(t, c)
	if len(got.Spec.Taints) != 1 {
		t.Errorf("taint missing after retries: %+v", got.Spec.Taints)
	}
}

func TestRemoveOwnedTaint(t *testing.T) {
	cases := []struct {
		name        string
		node        *corev1.Node
		owner       types.NamespacedName
		wantRemoved bool
		wantTaints  int
	}{
		{
			name: "removes own taint and annotations",
			node: testingnode.MakeNode(nodeName).Ready().
				Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
				Annotation(OwnerAnnotation, ownerRef).
				Annotation(StartedAtAnnotation, now.Format(time.RFC3339)).
				Obj(),
			owner:       owner,
			wantRemoved: true,
			wantTaints:  0,
		},
		{
			name: "leaves foreign owner under same key",
			node: testingnode.MakeNode(nodeName).Ready().
				Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
				Annotation(OwnerAnnotation, "team-b/other").
				Obj(),
			owner:       owner,
			wantRemoved: false,
			wantTaints:  1,
		},
		{
			name: "leaves unrelated keys",
			node: testingnode.MakeNode(nodeName).Ready().
				Taints(
					corev1.Taint{Key: "example.com/maintenance", Value: "x", Effect: corev1.TaintEffectNoSchedule},
					corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule},
				).Annotation(OwnerAnnotation, ownerRef).Obj(),
			owner:       owner,
			wantRemoved: true,
			wantTaints:  1,
		},
		{
			name:        "no taint at all",
			node:        testingnode.MakeNode(nodeName).Ready().Obj(),
			owner:       owner,
			wantRemoved: false,
			wantTaints:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, nil, tc.node)
			removed, err := RemoveOwnedTaint(ctx, c, "n1", key, tc.owner)
			if err != nil {
				t.Fatal(err)
			}
			if removed != tc.wantRemoved {
				t.Errorf("removed = %v, want %v", removed, tc.wantRemoved)
			}
			got := getNode(t, c)
			if len(got.Spec.Taints) != tc.wantTaints {
				t.Errorf("taints = %+v, want %d", got.Spec.Taints, tc.wantTaints)
			}
			if tc.wantRemoved {
				if _, ok := got.Annotations[OwnerAnnotation]; ok {
					t.Error("owner annotation not cleared")
				}
				if _, ok := got.Annotations[StartedAtAnnotation]; ok {
					t.Error("started-at annotation not cleared")
				}
			}
		})
	}
}

func TestRemoveOwnedTaintNodeGone(t *testing.T) {
	c := newClient(t, nil)
	removed, err := RemoveOwnedTaint(ctx, c, "ghost", key, owner)
	if err != nil || removed {
		t.Errorf("node gone: removed=%v err=%v, want false nil", removed, err)
	}
}

func TestOwner(t *testing.T) {
	cases := []struct {
		name   string
		node   *corev1.Node
		want   types.NamespacedName
		wantOK bool
	}{
		{
			name: "annotation names the owner",
			node: testingnode.MakeNode(nodeName).
				Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
				Annotation(OwnerAnnotation, ownerRef).Obj(),
			want:   owner,
			wantOK: true,
		},
		{
			name: "taint without annotation is not ours",
			node: testingnode.MakeNode(nodeName).
				Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).Obj(),
			wantOK: false,
		},
		{
			name: "malformed annotation is not ours",
			node: testingnode.MakeNode(nodeName).
				Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
				Annotation(OwnerAnnotation, "garbage").Obj(),
			wantOK: false,
		},
		{
			name: "annotation without taint is not a drain",
			node: testingnode.MakeNode(nodeName).
				Annotation(OwnerAnnotation, ownerRef).Obj(),
			wantOK: false,
		},
		{
			name:   "no taint",
			node:   testingnode.MakeNode(nodeName).Obj(),
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Owner(tc.node, key)
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Errorf("Owner = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFindDrains(t *testing.T) {
	other := types.NamespacedName{Namespace: "team-b", Name: "hero-2"}
	early := now.Add(-time.Hour)

	nodes := []corev1.Node{
		*testingnode.MakeNode(nodeName).
			Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
			Annotation(OwnerAnnotation, ownerRef).
			Annotation(StartedAtAnnotation, now.Format(time.RFC3339)).Obj(),
		*testingnode.MakeNode("n2").
			Taints(corev1.Taint{Key: key, Value: heroCQ, Effect: corev1.TaintEffectNoSchedule}).
			Annotation(OwnerAnnotation, ownerRef).
			Annotation(StartedAtAnnotation, early.Format(time.RFC3339)).Obj(),
		*testingnode.MakeNode("n3").
			Taints(corev1.Taint{Key: key, Value: "other-cq", Effect: corev1.TaintEffectNoSchedule}).
			Annotation(OwnerAnnotation, "team-b/hero-2").Obj(),
		*testingnode.MakeNode("n4").
			Taints(corev1.Taint{Key: key, Value: "garbage", Effect: corev1.TaintEffectNoSchedule}).Obj(),
		*testingnode.MakeNode("n5").Obj(), // untainted
	}

	drains := FindDrains(nodes, key)
	if len(drains) != 2 {
		t.Fatalf("drains = %d, want 2 (unattributable taints are not ours)", len(drains))
	}
	if d := drains[owner]; d == nil || len(d.Nodes) != 2 {
		t.Errorf("owner drain = %+v", d)
	} else if !d.StartedAt.Equal(early) {
		t.Errorf("StartedAt = %v, want earliest %v", d.StartedAt, early)
	}
	if d := drains[other]; d == nil || len(d.Nodes) != 1 {
		t.Errorf("other drain = %+v", d)
	} else if !d.StartedAt.IsZero() {
		t.Errorf("other StartedAt = %v, want zero (no annotation)", d.StartedAt)
	}
	if _, ok := drains[types.NamespacedName{}]; ok {
		t.Error("unattributable taint must not appear as a drain")
	}
}
