// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package snapshot

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"

	"github.com/coreweave/kueue-hero-workload-controller/pkg/config"
)

const (
	levelBlock = "cloud.provider.com/topology-block"
	levelRack  = "cloud.provider.com/topology-rack"
	gpu        = corev1.ResourceName("nvidia.com/gpu")
)

func testCfg() *config.Config {
	cfg := config.Default()
	cfg.NonBlockingPodLabels = map[string]string{"app": "hpc-verification"}
	return &cfg
}

// gpuNode: Ready 8-GPU node in the given block/rack.
func gpuNode(name, block, rack string) *testingnode.NodeWrapper {
	return testingnode.MakeNode(name).
		Label(levelBlock, block).
		Label(levelRack, rack).
		Label(corev1.LabelHostname, name).
		StatusAllocatable(corev1.ResourceList{gpu: resource.MustParse("8")}).
		Ready()
}

// kueuePod: running GPU pod owned by a kueue workload.
func kueuePod(name, node, workloadName, gpus string) *corev1.Pod {
	return testingpod.MakePod(name, "tenant-a").
		NodeName(node).
		Request(gpu, gpus).
		Annotation(kueue.WorkloadAnnotation, workloadName).
		StatusPhase(corev1.PodRunning).
		Obj()
}

func buildInput(cfg *config.Config, level string, nodes []corev1.Node, pods []corev1.Pod, heroes ...kueue.Workload) Input {
	return Input{Level: level, Nodes: nodes, Pods: pods, OtherHeroes: heroes, Cfg: cfg}
}

func TestBuildDomainsAndCapacity(t *testing.T) {
	cfg := testCfg()
	nodes := []corev1.Node{
		*gpuNode("n1", "b1", "r1").Obj(),
		*gpuNode("n2", "b1", "r1").Obj(),
		*gpuNode("n3", "b1", "r2").Obj(),
		*gpuNode("n4", "b2", "r3").Obj(),
		*gpuNode("n5", "b2", "r3").NotReady().Obj(),      // unusable: not ready
		*gpuNode("n6", "b2", "r4").Unschedulable().Obj(), // unusable: cordoned
		*gpuNode("n7", "b2", "r4").Taints(corev1.Taint{ // unusable: foreign taint
			Key: "example.com/maintenance", Effect: corev1.TaintEffectNoSchedule,
		}).Obj(),
		*testingnode.MakeNode("cpu-only").Label(levelBlock, "b1").Ready().Obj(), // no GPUs
		*testingnode.MakeNode("unlabeled").Ready().Obj(),                        // not in topology
	}

	s := Build(buildInput(cfg, levelBlock, nodes, nil))

	if len(s.Domains) != 2 {
		t.Fatalf("domains = %v, want b1 and b2", keys(s))
	}
	// b1: n1+n2+n3 usable = 24 GPUs (cpu-only node adds 0).
	if got := s.Domains["b1"].AllocatableGPU.Value(); got != 24 {
		t.Errorf("b1 allocatable = %d, want 24", got)
	}
	if got := len(s.Domains["b1"].Nodes); got != 4 {
		t.Errorf("b1 node count = %d, want 4 (incl. cpu-only)", got)
	}
	// b2: only n4 usable = 8 GPUs; n5/n6/n7 still listed for tainting.
	if got := s.Domains["b2"].AllocatableGPU.Value(); got != 8 {
		t.Errorf("b2 allocatable = %d, want 8", got)
	}
	if got := len(s.Domains["b2"].Nodes); got != 4 {
		t.Errorf("b2 node count = %d, want 4", got)
	}

	// Same nodes at rack level: finer domains.
	s = Build(buildInput(cfg, levelRack, nodes, nil))
	if len(s.Domains) != 4 {
		t.Fatalf("rack domains = %v, want r1..r4", keys(s))
	}
	if got := s.Domains["r1"].AllocatableGPU.Value(); got != 16 {
		t.Errorf("r1 allocatable = %d, want 16", got)
	}
}

func TestBuildPodClassification(t *testing.T) {
	cfg := testCfg()
	nodes := []corev1.Node{*gpuNode("n1", "b1", "r1").Obj()}

	dsPod := testingpod.MakePod("ds-exporter", "kube-system").
		NodeName("n1").Request(gpu, "1").StatusPhase(corev1.PodRunning).Obj()
	hpcPod := testingpod.MakePod("hpc-check", "hpc").
		NodeName("n1").Request(gpu, "2").Label("app", "hpc-verification").
		StatusPhase(corev1.PodRunning).Obj()
	cpuPod := testingpod.MakePod("cpu-sidecar", "tenant-a").
		NodeName("n1").Request(corev1.ResourceCPU, "4").
		Annotation(kueue.WorkloadAnnotation, "cpu-wl").
		StatusPhase(corev1.PodRunning).Obj()
	donePod := testingpod.MakePod("finished", "tenant-a").
		NodeName("n1").Request(gpu, "4").
		Annotation(kueue.WorkloadAnnotation, "done-wl").
		StatusPhase(corev1.PodSucceeded).Obj()
	limitsOnly := testingpod.MakePod("limits-only", "tenant-b").
		NodeName("n1").Limit(gpu, "1").StatusPhase(corev1.PodRunning).Obj()
	// Terminating pods occupy no future capacity: neither victims (kueue
	// one) nor non-reclaimable (non-kueue one), however large.
	now := metav1.Now()
	dyingVictim := kueuePod("dying-victim", "n1", "wl-dying", "8")
	dyingVictim.DeletionTimestamp = &now
	dyingForeign := testingpod.MakePod("dying-agent", "kube-system").
		NodeName("n1").Request(gpu, "8").StatusPhase(corev1.PodRunning).Obj()
	dyingForeign.DeletionTimestamp = &now

	pods := []corev1.Pod{
		*kueuePod("victim-1", "n1", "wl-a", "2"),
		*kueuePod("victim-2", "n1", "wl-a", "2"),
		*dsPod, *hpcPod, *cpuPod, *donePod, *limitsOnly,
		*dyingVictim, *dyingForeign,
	}

	s := Build(buildInput(cfg, levelBlock, nodes, pods))
	d := s.Domains["b1"]

	if got := len(d.Victims); got != 2 {
		t.Fatalf("victims = %d, want 2 (only kueue GPU pods)", got)
	}
	if d.Victims[0].Workload.Name != "wl-a" || d.Victims[0].Workload.Namespace != "tenant-a" {
		t.Errorf("victim workload = %v", d.Victims[0].Workload)
	}
	// Non-reclaimable: ds (1) + limits-only non-kueue (1) = 2.
	// hpc pod exempt via non-blocking label; cpu pod ignored (no GPU);
	// succeeded pod ignored.
	if got := d.NonReclaimableGPU.Value(); got != 2 {
		t.Errorf("non-reclaimable = %d, want 2", got)
	}
}

func TestBuildNonReclaimableOnlyOnUsableNodes(t *testing.T) {
	cfg := testCfg()
	nodes := []corev1.Node{
		*gpuNode("good", "b1", "r1").Obj(),
		*gpuNode("bad", "b1", "r1").NotReady().Obj(),
	}
	pods := []corev1.Pod{
		// Non-kueue pod on the unusable node: its node contributes no
		// capacity, so it must not be subtracted either.
		*testingpod.MakePod("op", "infra").NodeName("bad").
			Request(gpu, "8").StatusPhase(corev1.PodRunning).Obj(),
		// Victim on the unusable node still counts as a victim (eviction
		// is domain-wide).
		*kueuePod("victim-bad-node", "bad", "wl-b", "4"),
	}

	s := Build(buildInput(cfg, levelBlock, nodes, pods))
	d := s.Domains["b1"]
	if got := d.AllocatableGPU.Value(); got != 8 {
		t.Errorf("allocatable = %d, want 8 (good node only)", got)
	}
	if got := d.NonReclaimableGPU.Value(); got != 0 {
		t.Errorf("non-reclaimable = %d, want 0 (pod on unusable node)", got)
	}
	if got := len(d.Victims); got != 1 {
		t.Errorf("victims = %d, want 1", got)
	}
}

func TestMarkHeroDomains(t *testing.T) {
	cfg := testCfg()
	nodes := []corev1.Node{
		*gpuNode("n1", "b1", "r1").Obj(),
		*gpuNode("n2", "b2", "r3").Obj(),
		*gpuNode("n3", "b3", "r5").Obj(),
	}

	// Hero assigned with block-level values in the assignment.
	heroBlock := utiltesting.MakeWorkload("hero-1", "ns").
		ReserveQuotaAt(utiltesting.MakeAdmission("cq").
			PodSets(kueue.PodSetAssignment{
				Name: "main",
				TopologyAssignment: utiltas.V1Beta2From(&utiltas.TopologyAssignment{
					Levels:  []string{levelBlock},
					Domains: []utiltas.TopologyDomainAssignment{{Values: []string{"b1"}, Count: 8}},
				}),
			}).Obj(), testNow()).Obj()

	// Hero assigned hostname-only (lowest level is hostname): must map
	// through the node list to find its block.
	heroHost := utiltesting.MakeWorkload("hero-2", "ns").
		ReserveQuotaAt(utiltesting.MakeAdmission("cq").
			PodSets(kueue.PodSetAssignment{
				Name: "main",
				TopologyAssignment: utiltas.V1Beta2From(&utiltas.TopologyAssignment{
					Levels:  []string{corev1.LabelHostname},
					Domains: []utiltas.TopologyDomainAssignment{{Values: []string{"n2"}, Count: 8}},
				}),
			}).Obj(), testNow()).Obj()

	s := Build(buildInput(cfg, levelBlock, nodes, nil, *heroBlock, *heroHost))

	if !s.Domains["b1"].HasOtherHero {
		t.Error("b1 should be hero-occupied (block-level assignment)")
	}
	if !s.Domains["b2"].HasOtherHero {
		t.Error("b2 should be hero-occupied (hostname assignment via n2)")
	}
	if s.Domains["b3"].HasOtherHero {
		t.Error("b3 must not be hero-occupied")
	}
}

func testNow() time.Time {
	return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
}

func keys(s *Snapshot) []string {
	out := make([]string, 0, len(s.Domains))
	for k := range s.Domains {
		out = append(out, k)
	}
	return out
}
