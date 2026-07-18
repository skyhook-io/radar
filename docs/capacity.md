# Capacity (Karpenter)

A read-only diagnosis surface for Karpenter-managed fleets. It answers the questions operators otherwise stitch together from `kubectl get nodeclaims`, controller logs, and scheduler events:

- Why is my pod pending, and which NodePool could take it?
- Why aren't my nodes joining?
- Am I about to hit a configured limit?
- What is disruption / consolidation / a spot wave doing to my fleet right now?

Capacity is diagnosis only — it never mutates NodePools, NodeClaims, or workloads.

## When it appears

The Capacity entry (nav rail, next to Cost; command palette `g p`) renders only when Karpenter NodePools are discovered in the cluster **and** the current identity can list them. Users without NodePool access never see the nav item, the palette entry, or any bridge into the view — every `/api/capacity/*` route enforces the same gate server-side. Both `karpenter.sh/v1` and `v1beta1` are supported, including provider NodeClasses (EC2NodeClass, AKSNodeClass, …) via API discovery.

## The four screens

Capacity is a hub-and-spoke under `/capacity`:

### Overview

The recurring posture check.

- **KPI tiles** — NodePools (readiness), Nodes (+N outside Karpenter pools), NodeClaims with a lifecycle rollup ("8 ready · 1 launched · 1 failed · +1 orphaned"), and Pending pods with a jump into Demand.
- **Karpenter scheduling capacity bar** — Karpenter-pooled nodes only (static capacity is deliberately outside its ledger); per resource (CPU, memory, plus any extended resource with allocatable or pending demand): scheduled requests fill the allocatable track proportionally; **in-flight claim capacity extends beyond the allocatable edge** (to scale — it is capacity that will exist once claims register, never capacity the scheduler can use today); **pending demand is a count chip after a `//` discontinuity** — demand can exceed the whole fleet, and no bounded bar can draw 10× overflow proportionally without lying. A resource the fleet doesn't have renders as "0 requested of 0 allocatable · N pending" — the GPU-pool-missing story in one line.
- **Operational signals** — prioritized findings (limit pressure, registration health, blocked disruption, pending-demand states), each linking to its diagnosis.
- **NodePool inventory** — every pool with readiness, mode, NodeClass, member counts, scheduled requests, usage, and limit pressure.

### NodePool detail

The **capacity ledger** per resource: configured limit, provisioned (`status.resources` — Karpenter's actual provisioning gate; in-flight claims are already inside it), limit headroom, node allocatable, scheduled requests, non-bin-packed unallocated, and metrics-sampled actual usage. Plus tabs for claim lifecycle, fleet composition (capacity type / instance type / zone / architecture), workload attribution, and full configuration (requirements, taints, disruption policy, budgets).

### Demand

Pending pods grouped by **scheduling signature** — canonicalized node selectors, required affinity, tolerations, and per-pod requests — so five hundred pending replicas read as one group, not five hundred rows. Each group carries:

- a **state**: `awaiting_capacity`, `blocked`, `held` (scheduling gates), `waiting_for_scheduler`, or `unknown` — derived conservatively from scheduler verdicts;
- **pool evaluations**: the group checked against every NodePool's declared constraints, with per-predicate evidence (see below);
- the scheduler's own reasons, aggregated.

Filterable by state, by pool ("how does observed demand evaluate against this pool" — the filter narrows only which evaluation perspective is returned; group states and counts are always classified against the whole fleet), and by workload owner — the form Issues deep links use, filtered server-side so an empty result is a true zero, not a paging artifact.

Evaluations cover Karpenter NodePools only: `blocked` means no NodePool can take the demand, not that no node can — on mixed clusters, capacity Karpenter doesn't manage may still satisfy it.

### Activity

A bounded evidence timeline of provisioning, disruption, interruption, termination, and NodePool config-change **episodes**, correlated from resource lifecycle transitions and Karpenter's exact event vocabulary. Every episode carries its evidence with per-item confidence — heuristic matches are labeled `inferred`, never presented as fact.

## Entry points

Operators rarely start at the nav. Capacity meets them where they are:

- **Home** — a Karpenter posture card when the integration is detected.
- **Issues** — capacity-relevant scheduling issues get a "View in Capacity" link that lands on Demand **filtered to the affected workload**; NodePool issues land on the pool's detail page.
- **Pod drawer** — Pending pods show "Evaluate against Karpenter NodePools".
- **NodePool drawer** — "Open in Capacity".

## Reading the numbers

Capacity's core contract is **per-value certainty**. Every quantity carries one of:

| Glyph | Meaning |
|-------|---------|
| `=` | Exact — the source was fully observed |
| `≥` | Lower bound — partial coverage (namespace-scoped pods, sampled metrics) |
| `≤` | Upper bound — a difference computed from a lower-bound input |
| `?` | Unknown — the source was not observed; **absence of data is never zero** |

Hover (or focus) a glyph for the coverage detail behind it. The invariants this enforces:

- **Unavailable ≠ zero.** An RBAC-denied or unobserved source renders "Unavailable" / "Not observed" — never `0`.
- **Partial ≠ exact.** Metrics sampled on 3 of 100 nodes render `≥` with the sampled share.
- **Scheduling capacity ≠ actual usage.** Requests are what the scheduler consumes; usage is an efficiency signal. The ledger keeps them structurally apart, and the bar never acquires health colors — high utilization is a bin-packing goal, not an incident.
- **Declared ≠ actual.** See below.

## How demand evaluation works

Pool evaluations are **declared compatibility**: does the pod's declared scheduling contract intersect the NodePool's declared provisioning contract? Radar checks readiness (pool and NodeClass), permanent taints vs tolerations, selector/requirement feasibility, configured limits, `minValues`, and observed member shapes. The result is `declared_compatible`, `incompatible` (with per-predicate evidence), or `unknown` — and the boundaries are deliberate:

- A required label the pool **never declares** is *incompatible* — Karpenter only applies labels from pool requirements and template labels, so it can never provision a matching node. This is the classic misconfiguration.
- Provider/well-known labels (zone, instance-type, capacity-type, arch) are *unknown* when undeclared — the offering catalogue can supply them — but *declared compatible* when the pool's own `In` requirement bounds the values and the pod's need intersects them.
- A pod whose requests fit **no observed member shape** degrades to *unknown* — shapes are compared as whole vectors, never per-resource maxima, so a pool whose biggest CPU and biggest memory live on different instance types can't fabricate a composite machine.
- Unevaluable constraints (exotic toleration operators, unsupported affinity fields) degrade to *unknown*, never to a false verdict in either direction.

Radar does **not** simulate provider offerings or bin-packing, and the UI never claims a pod *will* schedule — `declared_compatible` means the declarations agree, which is exactly the boundary where Karpenter's own provisioning (and the cloud's actual capacity) takes over.

## How activity classification works

The failure model matches how Karpenter actually fails:

- Failing lifecycle stages stay at `status=Unknown` with a failure reason (`LaunchFailed`, and cloud-provider vocabulary like `VCPULimitExceeded`, `InsufficientInstanceCapacity`); `False` is reserved for hard invariants on `karpenter.sh/v1` — and Radar applies the older `v1beta1` dialect (where `False` was an ordinary unmet stage) when reading v1beta1 claims.
- A claim deleted before ever reaching Ready — the registration-timeout path — terminalizes its provision episode as **failed**, so "nodes not joining" never reads as provisioning still in progress.
- `DisruptionBlocked` and `Unconsolidatable` classify as disruption being **blocked** — the opposite of disruption happening — via an exact event-reason table.
- The durable trace after Karpenter cleans up timed-out claims is the NodePool's `NodeRegistrationHealthy=False` condition, surfaced as its own issue.

## Scope

Capacity is deliberately **cluster-wide**. Supply (NodePools, Nodes, NodeClaims) is cluster-scoped and unfilterable, so the header's namespace view filter does not apply here — scoping only the pod-derived numbers would show "my namespace's demand" against "everyone's supply". RBAC and the `--namespaces` deployment flag remain the only scopers, and both are labeled in the coverage badges.

## Endpoints

All read-only, all behind the NodePool RBAC gate:

- `GET /api/capacity` — overview: KPIs, scheduling aggregate, signals, pool summaries
- `GET /api/capacity/pools` (+ `/{name}`, `/{name}/members`) — inventory, detail, paginated members
- `GET /api/capacity/demand` — groups with `?state=`, `?pool=`, `?owner=ns/Kind/name` filters
- `GET /api/capacity/activity` — episode timeline with keyset cursors

## Limitations

- **No scheduling simulation.** Radar evaluates declared contracts; it does not model provider offerings, spot availability, or bin-packing.
- **No trends yet.** All screens show current state plus the bounded activity window; historical capacity trends are planned.
- **Single cluster.** Like the rest of Radar OSS, Capacity describes the connected cluster.
