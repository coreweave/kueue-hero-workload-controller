//go:build e2e
// +build e2e

// SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
// SPDX-License-Identifier: Apache-2.0
// SPDX-PackageName: kueue-hero-workload-controller

package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// Cluster picture for the lifecycle spec (one rack = 18 nodes x 4 GPU = 72):
//
//	block b1              block b2              block b3 (created mid-spec)
//	 r1      r2            r3      r4            r5
//	 40/72   40/72         40/72   40/72         0/72
//
// Four 40-GPU victims occupy one rack each (40+40 > 72, so no rack can hold
// two). The hero asks for two 64-GPU slices at rack level PLUS a block-level
// required topology (the documented layering: slices whole per rack, whole
// workload in one block); every rack has only 32 GPU free, so REAL kueue
// marks it stuck. b3 exists so an evicted victim has somewhere to land, but
// is useless to the hero: 128 GPUs exceed b3's single 72-GPU rack.
var _ = Describe("hero drain lifecycle", Ordered, func() {
	const (
		heroCQ   = "lifecycle-hero-cq"
		victimCQ = "lifecycle-victim-cq"
	)

	BeforeAll(func() {
		createRack("lc-b1", "lc-r1")
		createRack("lc-b1", "lc-r2")
		createRack("lc-b2", "lc-r3")
		createRack("lc-b2", "lc-r4")
		createClusterQueue(heroCQ, true, "360")
		createClusterQueue(victimCQ, false, "360")
	})

	AfterAll(func() {
		deleteAllJobs()
		deleteClusterQueues(heroCQ, victimCQ)
		deleteBlocks("lc-b1", "lc-b2", "lc-b3")
	})

	It("drains one block for a stuck hero and tracks every victim to its end state", func() {
		By("filling every lc-b1/lc-b2 rack with one 40-GPU victim (10 pods x 4 GPU)")
		for i := 1; i <= 4; i++ {
			createJob(jobSpec{
				name: fmt.Sprintf("victim-%d", i), queue: victimCQ, priorityClass: victimClass,
				pods: 10, gpusPerPod: 4, requiredTopology: labelRack,
			})
		}
		for i := 1; i <= 4; i++ {
			waitAdmitted(fmt.Sprintf("victim-%d", i))
		}
		victimRacks := rackOfEachVictim()
		Expect(victimRacks).To(HaveLen(4), "each victim should sit in exactly one rack")

		By("adding empty block lc-b3 (single rack) — room for evictees, useless to the hero")
		createRack("lc-b3", "lc-r5")

		By("submitting the hero: 128 pods x 1 GPU, rack slices of size 64, whole workload in one block")
		createJob(jobSpec{
			name: "hero", queue: heroCQ, priorityClass: heroClass,
			pods: 128, gpusPerPod: 1,
			requiredTopology: labelBlock,
			sliceTopology:    labelRack, sliceSize: 64,
			tolerateDrain: true,
		})

		By("waiting for REAL kueue to mark the hero stuck (the detection signal under test)")
		heroWL := workloadForJob("hero")
		Eventually(func(g Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, heroWL, wl)).To(Succeed())
			c := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
		}).Should(Succeed())

		By("the controller taints exactly one block's two racks (36 nodes)")
		var drainedBlock string
		Eventually(func(g Gomega) {
			byBlock := taintedNodesByBlock()
			g.Expect(byBlock).To(HaveLen(1), "all taints must stay within one block")
			for block, nodes := range byBlock {
				g.Expect(nodes).To(HaveLen(2*nodesPerRack), "both racks of the block")
				drainedBlock = block
			}
		}, 4*time.Minute).Should(Succeed())
		Expect(drainedBlock).To(BeElementOf("lc-b1", "lc-b2"), "lc-b3 cannot host two rack slices")

		By("identifying the two victims being evicted (their racks are in the drained block)")
		var evicted []string
		for victim, rack := range victimRacks {
			if blockOfRack(rack) == drainedBlock {
				evicted = append(evicted, victim)
			}
		}
		Expect(evicted).To(HaveLen(2))

		By("each evicted victim is suspended (event) and later reactivated (event)")
		for _, victim := range evicted {
			wlKey := workloadForJob(victim)
			Eventually(eventsFor(wlKey.Name)).Should(ContainElement("SuspendedForHeroDrain"))
			Eventually(eventsFor(wlKey.Name)).Should(ContainElement("ReactivatedAfterHeroDrain"))
		}

		By("the hero is admitted and all 128 pods reach Running in the drained block")
		Eventually(func(g Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, heroWL, wl)).To(Succeed())
			g.Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)).To(BeTrue())
		}, 5*time.Minute).Should(Succeed())
		// The block-level required topology pins every slice into ONE block,
		// and only the drained block has 128 GPUs free. (Without that
		// layering kueue is free to scatter slices across blocks — an
		// earlier revision of this spec observed exactly that; see the
		// submission guide's placement note.)
		Eventually(func(g Gomega) {
			running, blocks := runningPodsOfJob("hero")
			g.Expect(running).To(Equal(128))
			g.Expect(blocks).To(ConsistOf(drainedBlock))
		}, 5*time.Minute).Should(Succeed())

		By("the janitor removes every taint once the hero is fully running")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, 4*time.Minute).Should(Succeed())

		By("evicted victims reach their end states: one readmitted on lc-b3, one requeued")
		// The hero fills both drained racks (8 GPU left in each); only
		// lc-b3's rack still fits 40 GPU. One evicted victim readmits there,
		// the other must wait as a normal, active, pending workload.
		Eventually(func(g Gomega) {
			readmitted, requeued := 0, 0
			for _, victim := range evicted {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, workloadForJob(victim), wl)).To(Succeed())
				g.Expect(wl.Spec.Active == nil || *wl.Spec.Active).To(BeTrue(),
					"no victim may stay deactivated")
				g.Expect(wl.Annotations).NotTo(HaveKey("hero.coreweave.com/deactivated-for"),
					"no victim may keep the drain marker")
				if meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted) {
					readmitted++
					running, blocks := runningPodsOfJob(victim)
					g.Expect(running).To(Equal(10))
					g.Expect(blocks).To(ConsistOf("lc-b3"), "only lc-b3 still fits 40 GPU")
				} else {
					requeued++
				}
			}
			g.Expect(readmitted).To(Equal(1))
			g.Expect(requeued).To(Equal(1))
		}, 5*time.Minute).Should(Succeed())

		By("the untouched block's victims were never disturbed")
		for victim, rack := range victimRacks {
			if blockOfRack(rack) == drainedBlock {
				continue
			}
			Expect(eventsFor(workloadForJob(victim).Name)()).NotTo(
				ContainElement("SuspendedForHeroDrain"))
		}
	})
})

// Two heroes on two ClusterQueues, capacity for both (one block each), but
// drains must run STRICTLY one at a time: hero-b's drain may not start until
// hero-a's taints are gone — and those fall only when hero-a is fully
// Running, not at admission.
var _ = Describe("serial drains for competing heroes", Ordered, func() {
	const (
		heroCQA  = "serial-hero-a-cq"
		heroCQB  = "serial-hero-b-cq"
		victimCQ = "serial-victim-cq"
	)

	BeforeAll(func() {
		createRack("sr-b1", "sr-r1")
		createRack("sr-b1", "sr-r2")
		createRack("sr-b2", "sr-r3")
		createRack("sr-b2", "sr-r4")
		createClusterQueue(heroCQA, true, "360")
		createClusterQueue(heroCQB, true, "360")
		createClusterQueue(victimCQ, false, "360")
	})

	AfterAll(func() {
		deleteAllJobs()
		deleteClusterQueues(heroCQA, heroCQB, victimCQ)
		deleteBlocks("sr-b1", "sr-b2")
	})

	It("runs the second hero's drain only after the first hero is running and untainted", func() {
		By("filling every sr-b1/sr-b2 rack with one 40-GPU victim")
		for i := 1; i <= 4; i++ {
			createJob(jobSpec{
				name: fmt.Sprintf("serial-victim-%d", i), queue: victimCQ, priorityClass: victimClass,
				pods: 10, gpusPerPod: 4, requiredTopology: labelRack,
			})
		}
		for i := 1; i <= 4; i++ {
			waitAdmitted(fmt.Sprintf("serial-victim-%d", i))
		}

		By("submitting hero-a, then hero-b on a different ClusterQueue")
		createJob(jobSpec{
			name: "hero-a", queue: heroCQA, priorityClass: heroClass,
			pods: 128, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
		})
		// Distinct creation timestamps make the drain order deterministic
		// (equal priority falls back to queue-order time, then name).
		time.Sleep(2 * time.Second)
		createJob(jobSpec{
			name: "hero-b", queue: heroCQB, priorityClass: heroClass,
			pods: 128, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
		})
		heroA, heroB := workloadForJob("hero-a"), workloadForJob("hero-b")
		ownerA := testNamespace + "/" + heroA.Name
		ownerB := testNamespace + "/" + heroB.Name

		By("hero-a's drain taints one block while hero-b queues")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByOwner()[ownerA]).To(HaveLen(2 * nodesPerRack))
		}, 4*time.Minute).Should(Succeed())
		Eventually(eventsFor(heroB.Name)).Should(ContainElement("DrainQueued"))
		blockA := oneTaintedBlock()

		By("watching the handoff: at no moment may both heroes hold taints")
		// A plain Consistently("B empty") races the legitimate handoff —
		// hero-a's whole drain can finish in ~30s. Watch continuously until
		// hero-b's turn starts and fail HARD on any true overlap.
		handoff := time.Now().Add(8 * time.Minute)
		for time.Now().Before(handoff) {
			owners := taintedNodesByOwner()
			Expect(len(owners[ownerA]) > 0 && len(owners[ownerB]) > 0).To(BeFalse(),
				"serialization violated: both heroes hold taints (A=%d, B=%d)",
				len(owners[ownerA]), len(owners[ownerB]))
			if len(owners[ownerB]) > 0 {
				break
			}
			time.Sleep(time.Second)
		}

		By("hero-a is fully Running in its block (the untaint precondition)")
		Eventually(func(g Gomega) {
			running, blocks := runningPodsOfJob("hero-a")
			g.Expect(running).To(Equal(128))
			g.Expect(blocks).To(ConsistOf(blockA))
		}, 5*time.Minute).Should(Succeed())
		Expect(taintedNodesByOwner()[ownerA]).To(BeEmpty(),
			"hero-b's turn started, so hero-a's taints must already be gone")

		By("hero-b's drain covers the other block and completes")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByOwner()[ownerB]).To(HaveLen(2 * nodesPerRack))
		}, 4*time.Minute).Should(Succeed())
		Eventually(func(g Gomega) {
			running, blocks := runningPodsOfJob("hero-b")
			g.Expect(running).To(Equal(128))
			g.Expect(blocks).NotTo(ContainElement(blockA))
			g.Expect(blocks).To(HaveLen(1))
		}, 5*time.Minute).Should(Succeed())

		By("no taints remain and no victim stays suspended")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, 4*time.Minute).Should(Succeed())
		Eventually(func(g Gomega) {
			for i := 1; i <= 4; i++ {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, workloadForJob(fmt.Sprintf("serial-victim-%d", i)), wl)).To(Succeed())
				g.Expect(wl.Spec.Active == nil || *wl.Spec.Active).To(BeTrue())
				g.Expect(wl.Annotations).NotTo(HaveKey("hero.coreweave.com/deactivated-for"))
			}
		}, 4*time.Minute).Should(Succeed())
	})
})

// Two GIANT heroes on two ClusterQueues, but the cluster can hold only one:
// each needs four rack slices (256 GPU) in ONE block, and only gt-b4 —
// created here with 4 racks — is big enough. The loser must simply wait,
// pending, with no taints anywhere.
var _ = Describe("giant heroes competing for the only big-enough block", Ordered, func() {
	const (
		heroCQ1  = "giant-hero-1-cq"
		heroCQ2  = "giant-hero-2-cq"
		victimCQ = "giant-victim-cq"
	)

	BeforeAll(func() {
		// gt-b4: this spec's only block — 4 racks (288 GPU), the sole
		// domain that can ever hold a 4-slice giant.
		createRack("gt-b4", "gt-r1")
		createRack("gt-b4", "gt-r2")
		createRack("gt-b4", "gt-r3")
		createRack("gt-b4", "gt-r4")
		createClusterQueue(heroCQ1, true, "360")
		createClusterQueue(heroCQ2, true, "360")
		createClusterQueue(victimCQ, false, "360")
	})

	AfterAll(func() {
		deleteAllJobs()
		deleteClusterQueues(heroCQ1, heroCQ2, victimCQ)
		deleteBlocks("gt-b4")
	})

	It("admits one giant hero and leaves the second pending untainted", func() {
		By("filling every gt-b4 rack with one 40-GPU victim (pinned to gt-b4)")
		for i := 1; i <= 4; i++ {
			createJob(jobSpec{
				name: fmt.Sprintf("giant-victim-%d", i), queue: victimCQ, priorityClass: victimClass,
				pods: 10, gpusPerPod: 4, requiredTopology: labelRack,
				nodeSelector: map[string]string{labelBlock: "gt-b4"},
			})
		}
		for i := 1; i <= 4; i++ {
			waitAdmitted(fmt.Sprintf("giant-victim-%d", i))
		}

		By("submitting two giant heroes (256 pods, four 64-pod rack slices, one block)")
		createJob(jobSpec{
			name: "giant-1", queue: heroCQ1, priorityClass: heroClass,
			pods: 256, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
		})
		time.Sleep(2 * time.Second)
		createJob(jobSpec{
			name: "giant-2", queue: heroCQ2, priorityClass: heroClass,
			pods: 256, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
		})
		hero1, hero2 := workloadForJob("giant-1"), workloadForJob("giant-2")
		owner1 := testNamespace + "/" + hero1.Name
		owner2 := testNamespace + "/" + hero2.Name

		By("hero-1 drains all four gt-b4 racks in one drain (72 nodes)")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByOwner()[owner1]).To(HaveLen(4 * nodesPerRack))
		}, 4*time.Minute).Should(Succeed())
		Expect(oneTaintedBlock()).To(Equal("gt-b4"))

		By("hero-1 fully Running on gt-b4, then untainted")
		Eventually(func(g Gomega) {
			running, blocks := runningPodsOfJob("giant-1")
			g.Expect(running).To(Equal(256))
			g.Expect(blocks).To(ConsistOf("gt-b4"))
		}, 6*time.Minute).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, 4*time.Minute).Should(Succeed())

		By("hero-2 stays pending: the only big-enough block hosts hero-1")
		Eventually(eventsFor(hero2.Name)).Should(ContainElement("AllFeasibleDomainsHeroOccupied"))
		Consistently(func(g Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, hero2, wl)).To(Succeed())
			g.Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)).To(BeFalse())
			g.Expect(taintedNodesByOwner()[owner2]).To(BeEmpty())
		}, "30s", "5s").Should(Succeed())
	})
})

// A hero that fits on its own: real kueue admits it into the empty block
// with no help, so the controller must stay completely inert — the drain
// trigger is hero-ness AND kueue's no-fit signal, never hero-ness alone.
var _ = Describe("no drain when the hero fits without help", Ordered, func() {
	const (
		heroCQ   = "nofit-hero-cq"
		victimCQ = "nofit-victim-cq"
	)

	BeforeAll(func() {
		createRack("nf-b1", "nf-r1")
		createRack("nf-b1", "nf-r2")
		createRack("nf-b2", "nf-r3")
		createRack("nf-b2", "nf-r4")
		createClusterQueue(heroCQ, true, "360")
		createClusterQueue(victimCQ, false, "360")
	})

	AfterAll(func() {
		deleteAllJobs()
		deleteClusterQueues(heroCQ, victimCQ)
		deleteBlocks("nf-b1", "nf-b2")
	})

	It("admits the hero via kueue alone and never touches a taint or victim", func() {
		By("filling only nf-b2 with victims — nf-b1 stays free for the hero")
		for i := 1; i <= 2; i++ {
			createJob(jobSpec{
				name: fmt.Sprintf("nofit-victim-%d", i), queue: victimCQ, priorityClass: victimClass,
				pods: 10, gpusPerPod: 4, requiredTopology: labelRack,
				nodeSelector: map[string]string{labelBlock: "nf-b2"},
			})
		}
		for i := 1; i <= 2; i++ {
			waitAdmitted(fmt.Sprintf("nofit-victim-%d", i))
		}

		By("submitting a hero identical to the stuck ones — but nf-b1 fits it")
		createJob(jobSpec{
			name: "content-hero", queue: heroCQ, priorityClass: heroClass,
			pods: 128, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
		})
		hero := workloadForJob("content-hero")

		By("kueue admits it and all pods run in nf-b1, no drain needed")
		Eventually(func(g Gomega) {
			running, blocks := runningPodsOfJob("content-hero")
			g.Expect(running).To(Equal(128))
			g.Expect(blocks).To(ConsistOf("nf-b1"))
		}, 4*time.Minute).Should(Succeed())

		By("the controller stayed inert: no taints, ever")
		Consistently(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, "30s", "3s").Should(Succeed())

		By("no victim was suspended or marked")
		for i := 1; i <= 2; i++ {
			wl := &kueue.Workload{}
			Expect(k8sClient.Get(ctx, workloadForJob(fmt.Sprintf("nofit-victim-%d", i)), wl)).To(Succeed())
			Expect(wl.Spec.Active == nil || *wl.Spec.Active).To(BeTrue())
			Expect(wl.Annotations).NotTo(HaveKey("hero.coreweave.com/deactivated-for"))
		}

		By("the hero's event trail is pure kueue — no drain events")
		Expect(eventsFor(hero.Name)()).NotTo(ContainElements(
			"DomainsTainted", "VictimsSuspended", "DrainQueued", "DrainTimedOut", "DrainAborted"))
	})
})

// A drain that can never converge: the hero's pods carry a nodeSelector no
// node satisfies. The controller's capacity math (GPUs and taints only)
// believes draining helps and starts one. Kueue even ADMITS the hero (its
// admission does not evaluate the pod nodeSelector) — but the pods pend
// forever, so the hero never becomes fully running. The janitor must give
// up at the drain timeout (3m in this suite, and it covers the whole
// journey through pods-Running), warn the hero, remove every taint, and
// hand the victims back.
var _ = Describe("drain timeout", Ordered, func() {
	const (
		heroCQ   = "timeout-hero-cq"
		victimCQ = "timeout-victim-cq"
	)

	BeforeAll(func() {
		createRack("to-b1", "to-r1")
		createRack("to-b1", "to-r2")
		createClusterQueue(heroCQ, true, "360")
		createClusterQueue(victimCQ, false, "360")
	})

	AfterAll(func() {
		deleteAllJobs()
		deleteClusterQueues(heroCQ, victimCQ)
		deleteBlocks("to-b1")
	})

	It("tears the drain down after the timeout and restores the victims", func() {
		By("filling both to-b1 racks with one 40-GPU victim each")
		for i := 1; i <= 2; i++ {
			createJob(jobSpec{
				name: fmt.Sprintf("timeout-victim-%d", i), queue: victimCQ, priorityClass: victimClass,
				pods: 10, gpusPerPod: 4, requiredTopology: labelRack,
			})
		}
		for i := 1; i <= 2; i++ {
			waitAdmitted(fmt.Sprintf("timeout-victim-%d", i))
		}

		By("submitting a hero whose pods no node can satisfy (impossible nodeSelector)")
		createJob(jobSpec{
			name: "timeout-hero", queue: heroCQ, priorityClass: heroClass,
			pods: 128, gpusPerPod: 1,
			requiredTopology: labelBlock, sliceTopology: labelRack, sliceSize: 64,
			tolerateDrain: true,
			nodeSelector:  map[string]string{"e2e/never": "true"},
		})
		hero := workloadForJob("timeout-hero")

		By("the drain starts anyway: taints appear on to-b1")
		// No exact node count: kueue ADMITS this hero despite the impossible
		// nodeSelector (its admission does not evaluate it — the pods then
		// pend forever), and the janitor immediately releases the few nodes
		// outside the assignment. The invariant is the sequence, not a
		// momentary count.
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByBlock()["to-b1"]).NotTo(BeEmpty())
		}, 4*time.Minute).Should(Succeed())

		By("the janitor times the drain out: warning on the hero")
		Eventually(eventsFor(hero.Name), 5*time.Minute).Should(ContainElement("DrainTimedOut"))

		By("stopping the eviction loop the documented way: deactivate the hero")
		// There is deliberately NO cooling-off after a timeout: the hero is
		// immediately eligible again, so a hero whose drains can never
		// succeed re-drains every drainTimeout forever (the eviction-loop
		// signature docs/submitting-hero-workloads.md alerts on). Asserting
		// "taints gone" while the loop runs races the next drain; the
		// documented operator intervention — deactivating the hero — is
		// what makes teardown final (the janitor's abandoned path).
		Eventually(func(g Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, hero, wl)).To(Succeed())
			active := false
			wl.Spec.Active = &active
			g.Expect(k8sClient.Update(ctx, wl)).To(Succeed())
		}).Should(Succeed())

		By("teardown is now final: taints gone and staying gone")
		Eventually(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, 2*time.Minute).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(taintedNodesByBlock()).To(BeEmpty())
		}, "15s", "3s").Should(Succeed())

		By("every victim is handed back and the hero stays pending")
		Eventually(func(g Gomega) {
			for i := 1; i <= 2; i++ {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, workloadForJob(fmt.Sprintf("timeout-victim-%d", i)), wl)).To(Succeed())
				g.Expect(wl.Spec.Active == nil || *wl.Spec.Active).To(BeTrue())
				g.Expect(wl.Annotations).NotTo(HaveKey("hero.coreweave.com/deactivated-for"))
			}
		}, 2*time.Minute).Should(Succeed())
		wl := &kueue.Workload{}
		Expect(k8sClient.Get(ctx, hero, wl)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)).To(BeFalse())
	})
})

// ── helpers ─────────────────────────────────────────────────────────────

type jobSpec struct {
	name          string
	queue         string
	priorityClass string
	pods          int32
	gpusPerPod    int64
	// requiredTopology sets kueue.x-k8s.io/podset-required-topology (victims).
	requiredTopology string
	// sliceTopology + sliceSize set the slice pair (heroes).
	sliceTopology string
	sliceSize     int32
	tolerateDrain bool
	// nodeSelector pins pods to labeled nodes (kueue TAS honors it).
	nodeSelector map[string]string
}

func createJob(spec jobSpec) {
	GinkgoHelper()
	annotations := map[string]string{}
	if spec.requiredTopology != "" {
		annotations["kueue.x-k8s.io/podset-required-topology"] = spec.requiredTopology
	}
	if spec.sliceTopology != "" {
		annotations["kueue.x-k8s.io/podset-slice-required-topology"] = spec.sliceTopology
		annotations["kueue.x-k8s.io/podset-slice-size"] = fmt.Sprintf("%d", spec.sliceSize)
	}
	var tolerations []corev1.Toleration
	if spec.tolerateDrain {
		// The taint's value is the hero's ClusterQueue name; per-spec
		// LocalQueues are named after their CQ, so queue == CQ == value.
		tolerations = append(tolerations, corev1.Toleration{
			Key: taintKey, Operator: corev1.TolerationOpEqual,
			Value: spec.queue, Effect: corev1.TaintEffectNoSchedule,
		})
	}
	gpu := resource.MustParse(fmt.Sprintf("%d", spec.gpusPerPod))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: spec.name, Namespace: testNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name":     spec.queue,
				"kueue.x-k8s.io/priority-class": spec.priorityClass,
				"e2e-job":                       spec.name,
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptrTo(spec.pods), Completions: ptrTo(spec.pods),
			Suspend:      ptrTo(true), // kueue unsuspends on admission
			BackoffLimit: ptrTo(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: annotations,
					Labels:      map[string]string{"e2e-job": spec.name},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					NodeSelector:  spec.nodeSelector,
					Tolerations:   tolerations,
					Containers: []corev1.Container{{
						Name: "main", Image: "registry.k8s.io/pause:3.10",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{gpuResource: gpu},
							Limits:   corev1.ResourceList{gpuResource: gpu},
						},
					}},
				},
			},
		},
	}
	// Kueue's webhooks admit Jobs; retry rides out transient webhook 500s.
	Eventually(func() error {
		return client.IgnoreAlreadyExists(k8sClient.Create(ctx, job))
	}, time.Minute).Should(Succeed())
}

// workloadForJob resolves the kueue Workload created for a Job (named
// job-<name>-<suffix>, owner-referenced to it).
func workloadForJob(jobName string) types.NamespacedName {
	GinkgoHelper()
	var key types.NamespacedName
	Eventually(func(g Gomega) {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(testNamespace))).To(Succeed())
		for i := range list.Items {
			for _, ref := range list.Items[i].OwnerReferences {
				if ref.Kind == "Job" && ref.Name == jobName {
					key = types.NamespacedName{Namespace: testNamespace, Name: list.Items[i].Name}
					return
				}
			}
		}
		g.Expect(false).To(BeTrue(), "no Workload found for job "+jobName)
	}).Should(Succeed())
	return key
}

func waitAdmitted(jobName string) {
	GinkgoHelper()
	key := workloadForJob(jobName)
	Eventually(func(g Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, key, wl)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)).To(BeTrue())
	}, 4*time.Minute).Should(Succeed())
	Eventually(func(g Gomega) {
		running, _ := runningPodsOfJob(jobName)
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, key, wl)).To(Succeed())
		g.Expect(int32(running)).To(Equal(wl.Spec.PodSets[0].Count))
	}, 4*time.Minute).Should(Succeed())
}

// rackOfEachVictim maps victim job name -> the rack its pods run in.
func rackOfEachVictim() map[string]string {
	GinkgoHelper()
	racks := map[string]string{}
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("victim-%d", i)
		_, _, rack := podPlacement(name)
		Expect(rack).NotTo(BeEmpty())
		racks[name] = rack
	}
	return racks
}

// podPlacement returns running-pod count and the distinct blocks/one rack
// the job's pods occupy.
func podPlacement(jobName string) (running int, blocks []string, rack string) {
	pods := &corev1.PodList{}
	Expect(k8sClient.List(ctx, pods, client.InNamespace(testNamespace),
		client.MatchingLabels{"e2e-job": jobName})).To(Succeed())
	seenBlocks := map[string]bool{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName == "" {
			continue
		}
		running++
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, node)).To(Succeed())
		seenBlocks[node.Labels[labelBlock]] = true
		rack = node.Labels[labelRack]
	}
	for b := range seenBlocks {
		blocks = append(blocks, b)
	}
	return running, blocks, rack
}

func runningPodsOfJob(jobName string) (int, []string) {
	running, blocks, _ := podPlacement(jobName)
	return running, blocks
}

// taintedNodesByOwner groups nodes carrying our drain taint by the
// hero.coreweave.com/drain-owner annotation ("<ns>/<name>").
func taintedNodesByOwner() map[string][]string {
	nodes := &corev1.NodeList{}
	Expect(k8sClient.List(ctx, nodes)).To(Succeed())
	out := map[string][]string{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		for _, t := range node.Spec.Taints {
			if t.Key == taintKey && t.Effect == corev1.TaintEffectNoSchedule {
				owner := node.Annotations["hero.coreweave.com/drain-owner"]
				out[owner] = append(out[owner], node.Name)
			}
		}
	}
	return out
}

// taintedNodesByBlock groups nodes carrying our drain taint by block label.
func taintedNodesByBlock() map[string][]string {
	nodes := &corev1.NodeList{}
	Expect(k8sClient.List(ctx, nodes)).To(Succeed())
	out := map[string][]string{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		for _, t := range node.Spec.Taints {
			if t.Key == taintKey && t.Effect == corev1.TaintEffectNoSchedule {
				out[node.Labels[labelBlock]] = append(out[node.Labels[labelBlock]], node.Name)
			}
		}
	}
	return out
}

// oneTaintedBlock asserts all current taints sit in exactly one block and
// returns it.
func oneTaintedBlock() string {
	GinkgoHelper()
	byBlock := taintedNodesByBlock()
	Expect(byBlock).To(HaveLen(1))
	for block := range byBlock {
		return block
	}
	return ""
}

// deleteBlocks removes every fake node of the given blocks — each spec owns
// its blocks and must not leak capacity into later (randomly ordered) specs.
func deleteBlocks(blocks ...string) {
	GinkgoHelper()
	for _, block := range blocks {
		Expect(k8sClient.DeleteAllOf(ctx, &corev1.Node{},
			client.MatchingLabels{labelBlock: block})).To(Succeed())
	}
	Eventually(func(g Gomega) {
		for _, block := range blocks {
			nodes := &corev1.NodeList{}
			g.Expect(k8sClient.List(ctx, nodes, client.MatchingLabels{labelBlock: block})).To(Succeed())
			g.Expect(nodes.Items).To(BeEmpty())
		}
	}).Should(Succeed())
}

// deleteAllJobs removes every Job in the test namespace and waits them out.
func deleteAllJobs() {
	GinkgoHelper()
	Expect(k8sClient.DeleteAllOf(ctx, &batchv1.Job{},
		client.InNamespace(testNamespace),
		client.PropagationPolicy(metav1.DeletePropagationForeground))).To(Succeed())
	Eventually(func(g Gomega) {
		jobs := &batchv1.JobList{}
		g.Expect(k8sClient.List(ctx, jobs, client.InNamespace(testNamespace))).To(Succeed())
		g.Expect(jobs.Items).To(BeEmpty())
	}).Should(Succeed())
}

func blockOfRack(rack string) string {
	nodes := &corev1.NodeList{}
	Expect(k8sClient.List(ctx, nodes, client.MatchingLabels{labelRack: rack})).To(Succeed())
	Expect(nodes.Items).NotTo(BeEmpty())
	return nodes.Items[0].Labels[labelBlock]
}

// eventsFor returns a poller over event reasons recorded for the named
// object in the test namespace.
func eventsFor(objectName string) func() []string {
	return func() []string {
		events := &corev1.EventList{}
		if err := k8sClient.List(ctx, events, client.InNamespace(testNamespace),
			client.MatchingFields{"involvedObject.name": objectName}); err != nil {
			// Field selector unsupported through this client path: fall back
			// to a full list + filter.
			if err := k8sClient.List(ctx, events, client.InNamespace(testNamespace)); err != nil {
				return nil
			}
		}
		var reasons []string
		for i := range events.Items {
			if events.Items[i].InvolvedObject.Name == objectName {
				reasons = append(reasons, events.Items[i].Reason)
			}
		}
		return reasons
	}
}
