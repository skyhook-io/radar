# Capacity (Karpenter)

A read-only diagnosis surface for Karpenter-managed fleets. It answers the questions operators otherwise stitch together from `kubectl get nodeclaims`, controller logs, and scheduler events:

- Why is my pod pending, and which NodePool could take it?
- Why aren't my nodes joining?
- Am I about to hit a configured limit?
- What is disruption / consolidation / a spot wave doing to my fleet right now?

Capacity is diagnosis only — it never mutates NodePools, NodeClaims, or workloads.

## When it appears

**Capacity requires cluster-level node visibility; Karpenter access adds the Karpenter screens.** That is the whole rule — one page gate, two Overview shapes, never a partial Karpenter rendering.

- **Node visibility is the page gate.** The whole surface is built on the node fleet, so every `/api/capacity/*` route first checks that the current identity can list Nodes cluster-wide. A caller who cannot gets a 403 ("Capacity requires cluster-level node visibility (list nodes)"), before the Karpenter check ever runs.
- **Karpenter access adds the Karpenter screens.** When NodePools are discovered **and** listable, the Overview shows the full Karpenter posture and the NodePool detail, Demand, and Activity screens open. When NodePools exist but the caller is denied them, the Overview **alone** softens to the cluster-only shape — state `denied` (the wire meaning stays "the Karpenter integration"), NodePools coverage denied, and the same nodes/pods/ConfigMap surface a Karpenter-less cluster shows — with an honest "Karpenter view unavailable" notice. Demand, Activity, and the NodePool routes keep failing closed with 403.

Both `karpenter.sh/v1` and `v1beta1` are supported, including provider NodeClasses (EC2NodeClass, AKSNodeClass, …) via API discovery.

## The four screens

Capacity is a hub-and-spoke under `/capacity`:

### Overview

The recurring posture check.

- **KPI tiles** — NodePools (readiness), Nodes (+N outside Karpenter pools), NodeClaims with a lifecycle rollup ("8 ready · 1 launched · 1 failed · +1 orphaned"), and Pending pods with a jump into Demand.
- **Cluster scheduling capacity bar** — scheduled requests vs node allocatable across **all observed nodes** (`summary.clusterScheduling`); the Karpenter-scoped variant (`summary.scheduling`, Karpenter-pooled nodes only) renders only when no cluster-wide ledger could be built. Per resource (CPU, memory, plus any extended resource with allocatable or pending demand): scheduled requests fill the allocatable track proportionally; **in-flight claim capacity extends beyond the allocatable edge** (to scale — it is capacity that will exist once claims register, never capacity the scheduler can use today) and stays Karpenter-only, labeled with its manager; **pending demand is a count chip after a `//` discontinuity** — demand can exceed the whole fleet, and no bounded bar can draw 10× overflow proportionally without lying. A resource the fleet doesn't have renders as "0 requested of 0 allocatable · N pending" — the GPU-pool-missing story in one line.
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

A **type rollup strip** summarizes the whole filtered window ("Provision · 28 · 3 failed"), so a provisioning storm reads as a shape, not a page of rows. The rollup comes with the first page and stays stable while the type pills narrow the list (mirroring the Demand state pills); when timeline coverage is partial or bounded, every count renders as a `≥` lower bound. Evidence tables lead with when/source/raw/references; the normalized reason code, relationship, and confidence columns sit behind a per-episode "Show provenance" toggle.

## Entry points

Operators rarely start at the nav. Capacity meets them where they are:

- **Home** — a Karpenter posture card when the integration is detected.
- **Issues** — capacity-relevant scheduling issues get a "View in Capacity" link that lands on Demand **filtered to the affected workload**; NodePool issues land on the pool's detail page.
- **Pod drawer** — unscheduled Pending pods show "Evaluate against Karpenter NodePools", landing on Demand **filtered to that pod's workload** (`?pod=`, resolved server-side).
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

Two measured facts about pending demand are surfaced without changing any group's state:

- **Negative-priority requests.** Requests from pods with `spec.priority < 0` are reported separately as `negativePriorityRequests`. These pods are potential preemption victims, so this is a measured priority fact, not an overprovisioning claim — whether they are actually preempted depends on scheduler policy, placement, and disruption constraints.
- **Scheduler nominations.** A pod holding a node nomination (`status.nominatedNodeName` — the scheduler is preempting to make room for it) is annotated per demand group. Nomination is best-effort and can go stale, so it never changes the group's state or a pool's eligibility.

Per-pod request math delegates to `k8s.io/component-helpers/resource` (v0.36) — the same helper the kube-scheduler uses — with in-place-resize (status-based), pod-level resources, and DRA resource-claim accounting enabled; native sidecars (restartable init containers) are counted as that helper does by default.

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
- A claim that records a failure signal — a failing lifecycle-stage transition or a launch-failure event — terminalizes its provision episode as **failed** the moment that signal lands, so a later deletion never softens it; real timeout/ICE storms read as failed because Karpenter records the failing stage before it deletes the claim. A claim deleted before Ready with **no** recorded failure signal terminalizes as **ended** ("cause not recorded") — the deletion itself carries no cause, so Radar refuses to assert failure for manual deletions, cascading NodePool deletions, or any other cause it never observed.
- `DisruptionBlocked` and `Unconsolidatable` classify as disruption being **blocked** — the opposite of disruption happening — via an exact event-reason table.
- The durable trace after Karpenter cleans up timed-out claims is the NodePool's `NodeRegistrationHealthy=False` condition, surfaced as its own issue.

## How group attribution works (all managers)

Beyond Karpenter, the Overview carries a logical-group inventory across every capacity manager, built without any cloud API:

- **Identity comes only from node labels or CRDs** — `karpenter.sh/nodepool`, `cloud.google.com/gke-nodepool`, `eks.amazonaws.com/nodegroup`, `kubernetes.azure.com/agentpool`. Provider group names are never parsed into identity (they truncate long pool names). Nodes with no identity evidence are an **unattributed** presentation bucket — never a group, and deliberately never called "static". The eksctl name label is a hint only and does not create groups. Each group carries its identity domain on the wire as `platform` ("gke", "eks", "kops", …), kept deliberately distinct from `manager`: a static EKS managed node group has a platform and no manager, and the inventory renders the platform ("EKS managed node group") rather than concluding "none detected" about a row that exists *because* of a platform label.
- **Autoscaler observations** come from the `kube-system/cluster-autoscaler-status` ConfigMap (published by the Cluster Autoscaler and by the GKE/AKS managed autoscalers; structured YAML ≥ 1.30 plus the legacy text format). Per-zone children (MIGs/VMSS) join a logical group by node-name-prefix evidence; children with no joinable nodes — scale-to-zero groups included — stay **orphans** in their own "known to the autoscaler, unattributed" list, with IDs that never change when nodes later appear.
- **Managers** (`karpenter`, `gke_autoscaler`, `cluster_autoscaler`, `aks_autoscaler`) roll up worst-of health; a denied or unreadable source is never rendered as "none detected". The GKE prefix join is validated against live clusters; the AKS and EKS joins are marked unvalidated on the wire (`managerValidated`). The ConfigMap's own timestamp is surfaced as "as of T" — healthy quiet clusters publish hours-old payloads, so staleness is context, not breakage.
- **Scaling facts** are typed prose ("5–11 nodes · target 9", "bounds not published in-cluster", "NodePool not observed" for a Karpenter-labeled node whose NodePool we couldn't read, "no capacity manager detected") — never a bare dash, never a fabricated zero. A Karpenter node whose pool is unreadable (denied) or gone (label remnant) surfaces as `pool_not_observed`, and its manager rollup is `unknown`, never a claim about a spec we never saw.
- The cluster-wide scheduling ledger (`summary.clusterScheduling`) spans **all observed nodes**; `summary.scheduling` stays Karpenter-scoped forever — consumers depend on that meaning.

## Scope

Capacity is deliberately **cluster-wide**. Supply (NodePools, Nodes, NodeClaims) is cluster-scoped and unfilterable, so the header's namespace view filter does not apply here — scoping only the pod-derived numbers would show "my namespace's demand" against "everyone's supply". RBAC and the `--namespaces` deployment flag remain the only scopers, and both are labeled in the coverage badges.

## Endpoints

All read-only. Every route sits behind the node-visibility gate (list Nodes cluster-wide); the Karpenter-specific routes — and the NodePool data on the Overview — additionally require NodePool list access:

- `GET /api/capacity` — overview: KPIs, scheduling aggregates (Karpenter-scoped `scheduling` + all-nodes `clusterScheduling`), signals, pool summaries, the cross-manager `groups` inventory with autoscaler children, `orphanAutoscalerGroups`, and `summary.managers`; the `autoscalerStatus` coverage source reports denied / cache-scope / not-published / parse-error distinctly
- `GET /api/capacity/pools` (+ `/{name}`, `/{name}/members`) — inventory, detail, paginated members
- `GET /api/capacity/demand` — groups with `?state=`, `?pool=`, `?owner=ns/Kind/name`, and `?pod=ns/name` filters (`pod` is resolved server-side to the same top owner the grouping uses, so the drawer's bridge and the group key can never disagree; mutually exclusive with `owner`)
- `GET /api/capacity/activity` — episode timeline with keyset cursors; `?type=` narrows to one episode type, and first-page responses carry an `aggregate` rollup of the whole filtered window that the type filter deliberately does not narrow

## Limitations

- **No scheduling simulation.** Radar evaluates declared contracts; it does not model provider offerings, spot availability, or bin-packing.
- **DRA demand is invisible to the requests ledger.** Classic Dynamic Resource Allocation `ResourceClaims` (GA since Kubernetes 1.34) express accelerator demand outside container requests, so DRA-based accelerator demand never appears in the requests ledger — demand evaluation already degrades such pods to a labeled `unknown` rather than guessing.
- **No trends yet.** All screens show current state plus the bounded activity window; historical capacity trends are planned.
- **Single cluster.** Like the rest of Radar OSS, Capacity describes the connected cluster.
