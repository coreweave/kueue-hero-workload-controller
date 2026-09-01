<h1 style="display: flex; align-items: center; gap: 12px;">
  <span>kueue-hero-workload-controller</span>
</h1>

<div align="center">

[![Status](https://img.shields.io/badge/status-active-success.svg)]()
[![Slack](https://img.shields.io/badge/Slack-4A154B?logo=slack&logoColor=fff)](#)
[![Confluence](https://img.shields.io/badge/Confluence-172B4D?logo=confluence&logoColor=fff)](#)

</div>

## 📝 Table of Contents

- [About](#about)
- [Getting Started (Quick Start)](#getting-started)
- [Documentation](#documentation)


## 🧐 About <a name="about"></a>

A stopgap controller that lets "hero" workloads on Kueue with Topology Aware
Scheduling (TAS) start promptly. Heroes get stuck pending when other
tenants' within-quota workloads are scattered across every topology domain:
no single domain has room for the hero's required placement, and Kueue by
design cannot preempt across ClusterQueues within nominal quota.

When the controller sees a hero stuck on a TAS no-fit, it:

1. picks the topology domain that is cheapest to disrupt,
2. taints that domain's nodes (`NoSchedule`),
3. suspends/unsuspends the victim workloads so Kueue requeues them
   elsewhere,
4. lets the taint-tolerating hero in,
5. removes the taint once the hero is placed (or finished/abandoned/timed
   out).

No CRDs, no external state: the taint (value = the hero's ClusterQueue, so
only that queue's heroes tolerate the drained domain) plus a node
annotation naming the owning hero are the record of a drain in flight, and
any taint whose named hero is gone is garbage-collected — a crash can
never permanently brick a domain.

Tested against Kueue **0.16.9** (current production), **0.18.6**, and
**0.19.2** with the full e2e suite; one binary serves all of them. The
wire format of every field used is additive through kueue's current
development branch (0.20-devel), verified by source diff.

## 🚨 Alerting

Alert on this metric:

```
hero_drains_timed_out_total
```

It counts drains that did not get the hero fully running by `drainTimeout`
(default 30m). There is no cooling-off after a timeout: a hero whose drains
can never succeed will drain again every `drainTimeout`, evicting whoever
occupies the domain it picks. Each attempt picks fresh. Often the same
domain wins again, since it just got emptier, but nothing guarantees it.
The controller will not detect that loop for you.

> **Note:** the `drainTimeout` clock covers the whole journey: victim
> eviction, hero admission, and every hero pod reaching Running. Size it
> with image pulls and init time in mind.

- **Warning:** any increase (`increase(hero_drains_timed_out_total[1h]) > 0`)
- **Critical:** two or more within a few hours (the eviction-loop signature)

To stop a loop while investigating, deactivate the hero; the janitor tears
its drain down:

```bash
kubectl --context <ctx> patch workload <hero-workload> --type merge -p '{"spec":{"active":false}}'
```

Causes, supporting signals, and triage detail: [docs/alerting.md](docs/alerting.md).

## 🚀 Getting Started (Quick Start) <a name="getting-started"></a>

### Prerequisites

- Go (version from [go.mod](go.mod))
- Docker (image builds)
- A cluster with Kueue ≥ 0.16.9 and TAS enabled (envtest covers local runs)

### Build and test

```bash
make build          # compile the manager binary
make test           # unit + envtest suites (downloads envtest binaries)
make test-e2e       # full e2e on a local kind cluster (~20 min)
make lint           # golangci-lint
make kueue-crds     # re-vendor kueue CRDs used by envtest (pinned version)
```

`make test-e2e` needs `kind` and Docker. It spins up a kind cluster with
kwok-faked GPU nodes, real kueue, and the chart, then runs the drain
scenarios end to end.

- On success the cluster is deleted; on failure it is kept for debugging
  (`make cleanup-test-e2e` removes it).
- Other kueue version: `KUEUE_VERSION=v0.20.0 make test-e2e`

### Run against a cluster

```bash
bin/manager --help                          # all flags
bin/manager --config path/to/config.yaml    # or a mounted ConfigMap
```

See [config/manager/controller_config.yaml](config/manager/controller_config.yaml)
for the full set of knobs and their defaults (taint key, drain timeout,
cost weights, dry-run, …).

### Build the image

```bash
make docker-build  IMG=<registry>/kueue-hero-workload-controller:<tag>  # build (local arch)
make docker-push   IMG=<registry>/kueue-hero-workload-controller:<tag>  # push
make docker-buildx IMG=<registry>/kueue-hero-workload-controller:<tag>  # multi-arch build+push
```

- Always set `IMG` — the default is `controller:latest`.
- Multi-stage distroless `Dockerfile`; only Docker required (`buildx` for
  the multi-arch target, platforms via `PLATFORMS=linux/amd64,linux/arm64`).

### Deploy to a cluster (Helm)

The chart lives in this repo at `charts/kueue-hero-workload-controller`
(no chart registry — install from a checkout).

```bash
# 1. Build and push the image (see "Build the image" above)

# 2. Install or upgrade the chart
helm --kube-context <ctx> upgrade --install hero-controller \
  charts/kueue-hero-workload-controller \
  --namespace hero-system --create-namespace \
  --set image.repository=<registry>/kueue-hero-workload-controller \
  --set image.tag=<tag>
```

- All controller knobs live under the chart's `config:` block
  ([values.yaml](charts/kueue-hero-workload-controller/values.yaml)) and are
  rendered into a ConfigMap; config changes roll the Deployment
  automatically.
- First install on a new cluster: set `config.dryRun=true` — the controller
  plans drains and emits `DrainPlannedDryRun` events without tainting or
  evicting anything. Flip to `false` once the plans look right.

## 📚 Documentation <a name="documentation"></a>

- **[Submitting a hero workload](docs/submitting-hero-workloads.md)** — what
  cluster admins must set up (hero ClusterQueue label, WorkloadPriorityClass)
  and the three things every hero Job needs (queue + priority-class labels,
  required-topology annotation, taint toleration), with a complete example
  and troubleshooting table.
- **[Architecture](docs/ARCHITECTURE.md)** — how the drain and janitor
  controllers split the work and coordinate purely through cluster state
  (taints, annotations, markers), with a drain's full lifecycle.
- **[Alerting](docs/alerting.md)** — the failed-drain alert, what causes
  timed-out drains, supporting signals, and how to stop an eviction loop.
- **[Implementation plan](docs/PLAN.md)** — milestone status and the design
  decisions (version compatibility, detection strategy, disruption-cost
  heuristic).

## ✍️ Author <a name="author"></a>
@amy
