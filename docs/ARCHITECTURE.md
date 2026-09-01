# Architecture: how the drain and janitor controllers interact

The controller ships as one process in one Deployment, running two
independent reconcile loops. The **drain controller** starts drains. The
**janitor** ends them.

The two loops never talk to each other directly and keep no shared memory.
Everything one needs to know from the other is written onto the cluster
objects themselves: node taints, node annotations, and markers on
workloads. If either loop crashes, restarts, or falls behind, the other
keeps working from what it reads in the cluster.

The drain controller finds a hero workload that kueue cannot place, picks
the least disruptive group of nodes, taints them, and suspends the
workloads running there so kueue moves them elsewhere. The janitor watches
every tainted node and removes the taint as soon as the hero no longer
needs it: the hero is running, or it finished, or it was deleted, or the
drain ran out of time.

## Split of responsibilities

```
             DRAIN CONTROLLER                        JANITOR
             (starts drains)                         (ends drains)
                   │                                     │
  watches: Workloads (heroes, victims),   watches: Nodes carrying the drain
           Nodes                                   taint, hero pods, hero
                   │                               Workload updates/deletes
                   ▼                                     ▼
     stuck hero → select domains →        checks the HERO's state; first
     taint nodes → suspend victims →       match wins: deleted or deactivated
     reactivate evicted victims →          / finished / placed elsewhere /
     nudge kueue once the domain           fully running here / none of these
     is empty                              by the deadline (timeout)
                   │                                     │
                   └────────── shared state ─────────────┘
                          taints + node annotations
                          victim + hero markers
```

Why two loops instead of one? Starting a drain involves judgment calls.
The controller must pick which domain to drain, rank the victims by cost,
and make sure only one drain runs at a time. Decisions like these must not
race each other, so one loop makes them one at a time
(`MaxConcurrentReconciles: 1`).

Ending a drain is different. It must always work, no matter what happened
in between. So the janitor keeps it simple: look at one tainted node,
decide if its hero still needs it, clean up if not. It needs no memory of
how the drain began.

## The shared-state contract

The markers ARE the interface between the two controllers:

| marker | on | written by | removed by | meaning |
|---|---|---|---|---|
| taint `hero.coreweave.com/taint=<CQ>` | node | drain | janitor (or drain, on abort) | drain in flight; value = hero's ClusterQueue, so only that queue's heroes tolerate the freed capacity |
| `hero.coreweave.com/drain-owner` | node | drain | janitor / drain-abort | which hero this node is drained for (the only ownership record) |
| `hero.coreweave.com/drain-started-at` | node | drain | janitor / drain-abort | the drain deadline's clock |
| `hero.coreweave.com/scheduling-nudge` | node | drain | janitor / drain-abort (with the taint) | timestamp of the last kueue wake-up bump (see `taint.NudgeAnnotation`) |
| `hero.coreweave.com/deactivated-for` | victim Workload | drain | whoever reactivates (drain normally, janitor's sweep at teardown) | this victim's suspension belongs to that hero's drain |

A taint under our key with no parseable owner annotation was not written
by this controller. It never blocks new drains and is never cleaned up;
the capacity snapshot just treats its node as unusable.

## A drain's life

```
DRAIN CONTROLLER                                   JANITOR
────────────────                                   ───────
hero stuck (kueue no-fit condition)
  └─ serialization gate: no other hero's
     taints anywhere, front of the line
  └─ select domains → taint every node
     (+ owner/started-at annotations)
  └─ suspend victims (active=false,
     deactivated-for marker)
  └─ kueue evicts → reactivate victims
     (they requeue outside the taint)
  └─ domain empty, hero still pending:
     bump scheduling-nudge on one node ──▶ kueue retries → admits hero
                                              │
                                              ▼
                                           hero pods all Running?
                                              └─ teardown, node by node:
                                                 count drain done (last node),
                                                 remove taint + annotations,
                                                 sweep any still-suspended
                                                 victims back to active,
                                                 nudge-channel ping ──┐
  next queued hero starts  ◀──────────────────────────────────────────┘
```

The timeout path is the same teardown reached differently. If the hero is
not fully Running by `drain-started-at + drainTimeout` (never admitted, or
admitted but its pods never all started), the janitor tears down exactly as
above, plus a `DrainTimedOut` warning on the hero. There is deliberately no
retry backoff; repeated timeouts surface through events and the
`hero_drains_timed_out_total` metric.

Victim restoration is intentionally owned twice: the drain controller
reactivates each victim as kueue finishes evicting it (the normal path),
and the janitor's teardown sweep unconditionally reactivates anything still
marked `deactivated-for` that drain. The sweep is the safety net: it closes
the gap where kueue never completed an eviction, e.g. kueue down mid-drain.

## Coordination rules

- **One drain cluster-wide.** The gate reads node taints, not memory: a
  hero proceeds only if no node carries another hero's drain taint AND it
  is first in the deterministic `NextHero` order that every reconcile
  computes identically.
- **Janitor → drain nudge channel.** The only in-process link, and it is
  best-effort: after each teardown the janitor pings a channel that
  re-enqueues every stuck hero, so the next drain starts promptly instead
  of on the 30s poll. A dropped ping costs latency, never correctness.
- **A deactivated hero belongs to the janitor.** `spec.active=false` is the
  operator stop switch: the drain controller stands down entirely (no
  draining, no re-tainting, not even a place in line) while the janitor's
  abandoned trigger tears the drain down. Both acting at once is how you
  get a taint/untaint fight.
- **Idempotence over bookkeeping.** Neither controller records what it did;
  both re-derive intent from the markers on every pass. Restart at any
  point mid-drain and the next reconcile continues where the cluster state
  says things stand.

## Where the rest lives

- [PLAN.md](PLAN.md): milestones, settled design decisions, kueue version
  compatibility, the selection/cost math.
- [submitting-hero-workloads.md](submitting-hero-workloads.md): the
  operator/customer-facing contract and troubleshooting table.
- Code doc comments carry the per-marker rationale in depth, e.g.
  `taint.NudgeAnnotation` (why kueue needs waking at all) and the janitor's
  teardown comment (why completion is counted before the last untaint).
