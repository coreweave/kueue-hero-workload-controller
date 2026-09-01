# Alerting on failed drains

A drain "fails" when the controller taints a domain and evicts victims,
but the hero still is not fully running by the deadline (`drainTimeout`,
default 30m). The janitor then tears everything down: taints removed,
victims reactivated, and a `DrainTimedOut` warning event stamped on the
hero.

> **Note:** the clock starts at the first taint and covers everything
> after it: victim eviction, kueue admitting the hero, and every hero pod
> reaching Running. Slow pod startup counts against the deadline too, so
> size `drainTimeout` with image pulls and init time in mind.

There is deliberately no cooling-off period after that. The hero
immediately becomes eligible for a fresh drain. A hero whose drains can
never succeed will therefore drain again every `drainTimeout`, evicting
whoever occupies the domain it picks. Each attempt picks fresh. Often the
same domain wins again, since it just got emptier, but nothing guarantees
it. This repeats until someone intervenes; the controller will not detect
the loop for you. The signal is observability.

## The alert

```
hero_drains_timed_out_total
```

Counter, one increment per timed-out drain.

| level | rule | reading |
|---|---|---|
| Warning | `increase(hero_drains_timed_out_total[1h]) > 0` | a drain gave up; look at it when convenient |
| Critical | two or more within a few hours | the eviction-loop signature: retries recur roughly every `drainTimeout` |

The metric carries no per-hero label, because workload names are unbounded
cardinality. Identify the hero through its events:

```bash
kubectl --context <ctx> get events -A --field-selector reason=DrainTimedOut
```

## Why there is no automatic backoff

The loop works like this: the drain times out, the hero is still stuck,
and the ordering (priority, then age) ranks it first again, so it drains
again. Each attempt picks its domain fresh. Often the same domain wins
again, since it just got emptier, but nothing guarantees it.

A backoff would need the controller to remember which hero failed and on
which domains, and feed that history into scheduling: skip this hero for a
while, avoid that domain, or both. That is real scheduling state, with its
own expiry, crash-recovery, and fairness questions, in a controller that
is meant to be a simple, stateless stopgap. Instead every attempt is
identical, and loop detection is left to observability, where an operator
sees the pattern and can act with full context.

## How the failure is bounded

A drain that can never succeed is costly but strictly bounded:

- **In time.** Every attempt lasts at most `drainTimeout`, and the whole
  journey counts against it (taint, evict, admit, pods Running). Nothing
  waits forever.
- **In space.** One drain runs cluster-wide at a time, and it taints only
  the selected domain set. A looping hero keeps re-selecting the same
  cheapest domain, so the repeated disruption stays concentrated there
  instead of sweeping across the cluster. But it also blocks other queued
  heroes from draining, which is part of why the critical alert matters.
- **In damage per cycle.** Victims are not parked for the drain's
  duration. Each one is suspended just long enough for kueue to evict it,
  then reactivated so kueue requeues it outside the tainted domain, usually
  within seconds. Teardown finishes any toggle that never completed. The
  cost of a cycle is one eviction-and-requeue of the domain's occupants,
  not a prolonged outage.
- **Against crashes.** All drain state lives on cluster objects, so a
  controller crash mid-drain cannot strand taints or victims. The janitor
  garbage-collects structurally from node state alone.

Each cycle is bounded, but nothing stops the next one: a doomed hero
retries every `drainTimeout` (default 30m) indefinitely. The alert exists
so an operator knows when to step in.

## What makes a drain time out

- **A victim that will not vacate.** Its workload is suspended, but the
  pods take forever to actually terminate: a long grace period with a slow
  preStop hook, a process ignoring SIGTERM, a stuck volume unmount, a node
  whose kubelet stopped responding. The freed capacity never materializes,
  so kueue can never place the hero.
- **The hero is admitted, but its pods never start.** Kueue's admission
  does not validate everything the real scheduler enforces: a nodeSelector
  or affinity no node satisfies, an image that ImagePullBackOffs forever,
  runtime-class or device-plugin problems. The deadline covers this
  stretch too, so it ends in the same teardown rather than pinning the
  domain forever.
- **Immovable non-kueue pods, mis-modeled.** The controller counts
  non-kueue pods as permanently blocking capacity unless they carry a
  `nonBlockingPodLabels` label declaring them transient. If a pod is
  labeled transient but actually is not, the controller drains a domain
  believing that pod's GPUs will become usable; they never do.
- **Capacity shifts under the drain.** A node in the drained domain goes
  NotReady or gets cordoned after tainting, so the domain no longer holds
  the hero even when empty. When the controller can prove the hero no
  longer fits, it aborts immediately with a `DrainAborted` event instead of
  waiting; the timeout catches the cases it cannot prove.
- **Something refills the freed space.** Pods that tolerate the drain
  taint, or a webhook injecting such tolerations, can slip into the
  drained domain between eviction and hero admission.

Reading the pattern: one-off timeouts are usually the last two causes,
transient and self-healing (the retry after teardown converges). Repeated
timeouts on the same hero mean something structural from the first three.
Investigate rather than wait.

## Supporting signals

- `hero_drains_in_flight` pinned at 1 for close to `drainTimeout` means a
  drain is struggling right now.
- `hero_victims_suspended_total` minus `hero_victims_reactivated_total`
  should return to zero after every drain. A persistent gap means stranded
  victims, which the janitor's teardown sweep should make impossible; a
  gap is a bug signal.
- `hero_drains_completed_total{outcome}` breaks completions down by how
  they ended (`placed`, `timed_out`, `aborted`, ...).

## Stopping an eviction loop

Deactivate the hero while investigating:

```bash
kubectl --context <ctx> patch workload <hero-workload> --type merge -p '{"spec":{"active":false}}'
```

The janitor tears its drain down (taints removed, victims restored) and no
new drain starts until the workload is reactivated, or the hero workload
is fixed or deleted.
