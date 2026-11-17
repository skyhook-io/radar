# Ownership: owners, dependents and managers

Radar answers "what owns this object?", "what does it own?" and "what manages it?" from the informer caches it already
holds, without fetching relationship objects from the API. Authenticated requests may perform SubjectAccessReviews
to authorize those cached reads. This page is the map: the model, which field answers which question, the layers, and where
each rule lives. The doc comments at the linked code are the specification.

## The model

Three kinds of evidence, kept apart:

| Evidence | Where it comes from | What it proves |
|---|---|---|
| **Owner references** | `metadata.ownerReferences` on the dependent | What Kubernetes' garbage collector links. A reference names its owner's UID; whether it resolves to the owner is a rule's call (`ResolveOwner`). |
| **Recorded membership** | A GitOps manager's status: an Argo CD Application's `status.resources`, a Flux Kustomization's or HelmRelease's `status.inventory` | That the manager recorded the object: for Argo CD, the objects its last comparison covered (desired objects not yet created, live ones awaiting pruning, objects another Application also tracks); for Flux, its last apply. Not that it applied the object or alone manages it. |
| **Claims** | Labels and annotations on the object (Argo CD's tracking annotation or label, Flux's `*.toolkit.fluxcd.io/name` labels, Helm's release annotations) | What the object says manages it: a candidate, not a verdict. Argo CD decides membership by its configured tracking method and installation ID, which Radar may not be able to read. Parsed by `pkg/gitops/claims.go`, every candidate kept. |

Answers report what they couldn't read. Even a complete empty answer establishes absence only within its evidence model
and scope; it never proves that no external controller or writer exists.

## Which field answers what

| Question | Field | Evidence |
|---|---|---|
| What owns it / what does it own | `relationships.owner`, `owners`, `children` (REST resource GET); `resourceContext.owner` (AI context) | Owner references |
| Which managers record it / what a manager records | `relationships.managers`, `manages` (Related Resources → Managed By / Manages) | Recorded membership, plus graph links for other managing controllers (a cert-manager Certificate) |
| What manages it, weighed | `relationships.ownership.manager`; `resourceContext.manager` (compact) | Recorded membership and claims, through the controller chain ([below](#what-manages-an-object)) |
| Legacy manager pointer | `relationships.managedBy` | The manager the object's metadata names, else the topmost object its manages edges reach; still used by workload navigation and older-server manager fallbacks, not a canonical controller-owner answer |
| Controller and other generators, for agents | `resourceContext.managedBy` | Highest readable controller plus observed non-GitOps spec generators; GitOps/Helm evidence stays in `manager`. See [agent navigation](#the-top-controller). |
| Where to inspect a list row or search hit next | `summaryContext.managedBy` | One controller-first lead, with `source` identifying its evidence and `omitted` qualifying incomplete reads. See [selection rules](#the-top-controller). |
| Source hints for request changes | rightsizing `managedBy: {tool, signal}` | Controller owner, GitOps/Helm metadata, `addonmanager` mode ([rightsizing](mcp.md#managedby--source-hints-for-request-changes)) |

The four `managedBy` fields answer different questions; read `manager` for "which GitOps manager". The Applications view's `sourceRef` is each workload's `View.Manager` lead under the summary rules below, read with Radar's own access because its rows are shared by every caller with the same namespaces. Each response then withholds a GitOps source, with the delivery state read from it, from a caller who can't get that Application, Kustomization or HelmRelease. A row whose workloads don't all lead to one source, without two of them leading to different ones, takes the single Argo CD Application or Flux Kustomization whose list names every workload in it, if there is one.

Topology draws a manages edge from a GitOps manager to what its status records: an Application's `status.resources`, a Kustomization's or HelmRelease's inventory. A HelmRelease that records no inventory draws them by the same origin-label rule the resource tree uses.

The [GitOps resource tree](gitops.md#topology-tab) also uses `View.Children`, with a namespace-scoped private view and separate per-kind output checks. It can traverse cached hidden intermediates without emitting their identity or metadata. Display topology supplies presentation, observed spec links, and label-based roots for HelmReleases without inventory (`gitops.FluxHelmLabelledRelease`: both origin labels, no hooks, nothing a controller generated). Owner-reference truth comes from the ownership view. The traversal rules (work budget, destination checks, display Pod groups, hidden parents, which gaps raise a notice) live in `pkg/gitops/tree` (`builder.go`, `ownership.go`) and their tests.

## Layers

- **`pkg/ownership`: the contract.** A `Source` opens a `ReadSession` over the current caches. A session does no I/O
  and no authorization; its coverage describes Radar's collection, not what a caller may see (`ownership.go`).
  - `Object(ref)` → `ObjectRead`: present, absent, excluded (Events) or unavailable with a `Gap` (`read.go`).
  - `DependentCandidates(uid, scope)` → candidates whose owner references name a UID, plus gaps (`dependents.go`).
    Candidates aren't children yet: rules judge them.
  - `Members(manager)` and `RecordingManagers(ref)` → a manager's recorded list, and the managers whose lists name an
    object (`membership.go`). A list is **recorded**, **unreadable** (any object may be in it) or **remote** (its
    destination isn't confirmed to be this cluster, so a match isn't reported as a local recorder). A manager whose
    destination is named and isn't this cluster is remote even when its list can't be read; unreadable, it names
    nothing here and leaves no local answer uncertain. A readable remote list naming an object's coordinates still
    does, since its destination may be this cluster under another name.
  - The `Catalog` is discovery's served API, fixed for the session (`catalog.go`). A kind two resources serve, or one
    served at two scopes, is ambiguous: reads of it are unavailable and searches report it as a gap.
  - `ResolveOwner` judges one owner reference as the collector does: found, gone, replaced, unresolvable, excluded or
    unknown (`resolve.go`).
- **`pkg/k8score`: the indexes.** Every informer except Events' indexes objects by owner UID (`owner_index.go`), and
  every Argo CD Application, Flux Kustomization and HelmRelease informer indexes its recorded members
  (`gitops_members_index.go`). Both are always on. Reads answer only from stores loaded in full
  (`ReadableIndexedObjects` for typed kinds, `ReadableIndexed` for dynamic ones).
- **`pkg/ownership/cachesource`: the cache source.** One traversal (`dynamicIndexed`) reads either index for custom
  resources. It reads a resource watched at several versions from one version, pins the namespaces a version is watched
  in at the session's first look, and turns every unread kind or namespace into a gap. Dependent searches reuse the
  session's unwatched-kind gaps; watched stores are still read each time, so a loading store can become available.

## Collection activation

`relations.Kinds()` declares the bounded source, target, lookup and projection dependencies. Radar removes the kinds
served by typed informers using `k8score.AllKindListers()` and gives the rest to each new dynamic cache's watch reconciler.
It starts before the deferred-informer warmup gate, reacts to discovery refreshes and CRD add/update events, and retries
every 75 seconds (after the 60-second denial memory expires). CRD events only signal the reconciler's goroutine; an
update refreshes discovery only when it changes a required CRD's served versions, and CRDs seen before startup discovery
finishes wait for it, then get the usual small-CRD decision. A context switch, reconnect or namespace rescope creates
and starts a new reconciler; stopping a cache cancels its requests and unregisters its discovery subscription.
Activation is non-blocking and singleflight per resource, so a burst of discovery and CRD events starts each watch once.

Both Calico groups and both Traefik groups are activated because families read both. Open-ended Knative destinations
use named Serving, Broker, Channel, flow and JobSink targets, and NodeClasses use the four supported providers; discovery
alone never expands them into watches of Knative internals or arbitrary providers. Crossplane composites/claims and
arbitrary ownership kinds retain their existing behavior: they are read from watches already active, without eagerly
watching every installed provider/XRD kind. The fallback catalog supplies the versions, plurals and scope for recovery;
`TestRelationshipKindsHaveFallbacks` and the chart-RBAC check keep it aligned with the declaration. The recording builder
test pins every activation absent from the graph's cold listing, including targets otherwise started by common warmup.

## Beyond ownership: other relations

`pkg/relations` answers the relations that aren't ownership or management from the same caches, through the same
session, `Access` and gaps: a `relations.Session` is an ownership session that also reads index lookups (`Indexed`)
and lends cached objects (`Borrow`), which the cache source implements.

A **family** (`family.go`) is one way objects name others: the kinds that declare (each optionally narrowed to the
target kinds it can declare), the kinds a declaration can name, and a function that visits what an object declares
with the path declaring it. The `View` answers every family the same way:

- `References(family, subject)`: each target the subject declares, every path declaring it, and what reading it
  found: present, absent, unread (with its gap) or withheld from the caller.
- `Referrers(family, subject)`: what declares the subject, as the caller may read it, up to 500 with a total. A
  namespaced target is normally declared from its own namespace, so the answer reads that namespace's objects of each
  declaring kind through the namespace index and evaluates what each declares. An index of every declaration cost
  about 1.4 KB per Pod and made a Pod relist about five times as long
  ([measurements](loadtest.md#relationship-cost-baselines)).
- A cluster-scoped target, or any target of a family whose declarations cross namespaces (`CrossNamespace`, as Gateway
  API references do), is answered as `Children` answers a cluster-scoped owner: the namespaces the caller may read
  something in are searched, each declarer is checked where it is, and a gap is kept only if it covers what the caller
  may read (`ownership.GapCoversReadable`).
- A declaration counts only if its kind is one of the family's targets and its source may declare that kind (an HPA
  doesn't scale a DaemonSet).
- A family whose sources are many and in every namespace can find a target's referrers through a store index
  keyed by the target (`ReferrersIndex`) instead of reading every source; what each one declares is still
  evaluated. Only `Reflections` does: an index of a busy kind costs every relist
  ([measurements](loadtest.md#relationship-cost-baselines)).
- A declaration may name alternatives (`Declaration.Or`): the same reference in another API group its controller also
  reads, in the order it tries them, as Traefik 2.10 tries `traefik.io` and then `traefik.containo.us`. `References`
  names the first that exists, and `Referrers` counts a declaration only for the target it resolves to. A candidate
  Radar couldn't read, or the caller may not, stops the resolution there: the answer names it and says so rather than
  falling back to a later one. A target in none of them is named in the first group the cluster serves. A target found
  only after served groups that lack it carries `legacy_group_only`, in both directions: Traefik 2.10 and 2.11 read
  `traefik.containo.us` too, Traefik 3 ignores it, and Radar can't tell which controller runs.
- Both answer nothing for a subject the caller may not read; a permission check that couldn't be made is a gap, not a
  denial; a subject the host read that the cache doesn't hold yet leaves the answer incomplete.

The families so far:

| Family | Sources → targets | File |
|---|---|---|
| `ConfigReferences` | Pods, pod-template workloads (Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob, Argo Rollout), Ingress TLS → ConfigMaps, Secrets, PersistentVolumeClaims, ServiceAccounts. ReplicaSets scaled explicitly to zero declare nothing: they are old revisions | `config_references.go` |
| `Autoscalers` | HorizontalPodAutoscalers and KEDA ScaledObjects → Deployments, StatefulSets, ReplicaSets, Rollouts; VerticalPodAutoscalers → those and DaemonSets | `families.go` |
| `KEDAAuthentication` | ScaledObjects, ScaledJobs → TriggerAuthentications, ClusterTriggerAuthentications | `families.go` |
| `IngressBackends` | Ingresses → their default and rule backend Services | `families.go` |
| `RolloutServices` | Argo Rollouts → the canary, stable, active and preview Services their strategy names | `families.go` |
| `CertificateSecrets` | cert-manager Certificates → the Secret they write | `families.go` |
| `GatewayParents` | Gateway API routes (HTTP, GRPC, TCP, TLS, UDP) → the Gateways their parentRefs attach to, in any namespace | `families.go` |
| `GatewayBackends` | Gateway API routes → the Services their rules send traffic to, in any namespace, whether or not a ReferenceGrant permits it | `families.go` |
| `GatewayClasses` | Gateways → the GatewayClass they name | `families.go` |
| `CertManagerIssuers` | cert-manager Certificates, CertificateRequests, ACME Orders and Challenges → the Issuer or ClusterIssuer their `issuerRef` names (external issuers aren't followed) | `families.go` |
| `FluxSources` | Flux Kustomizations, HelmReleases, HelmCharts → the GitRepository, OCIRepository, Bucket, ExternalArtifact, HelmRepository or HelmChart they build from, in any namespace (an inline HelmRelease also declares the HelmChart the controller recorded) | `families.go` |
| `NodeClasses` | Karpenter NodePools → their provider's NodeClass, or EKS Auto Mode's (`IsTarget`: each provider defines its own kind) | `families.go` |
| `ClusterClasses` | Cluster API Clusters → the ClusterClass their topology names, in any namespace | `families.go` |
| `KnativeEventDestinations` | Knative Triggers, event sources and Channels → where they send events (subscriber, sink, backing channel), in any namespace: core Services and any kind in a Knative group, such as JobSink | `families.go` |
| `KnativeTriggerBrokers` | Knative Triggers → the Broker they subscribe to (`brokerRef` in any namespace, or the older same-namespace `broker`) | `families.go` |
| `KnativeRouteRevisions` | Knative Routes → the Revisions their status sends traffic to | `families.go` |
| `IstioGateways` | Istio VirtualServices → the Istio Gateways they bind to, in any namespace (`mesh` binds sidecars, not a Gateway) | `families.go` |
| `IstioServiceHosts(domain)` | Istio VirtualServices (HTTP, TCP, TLS routes) and DestinationRules → the Services their hosts name, in any namespace. Made per request with the mesh's domain from istiod (`configrefs.IstioServiceDomain`): an FQDN resolves only with it | `families.go` |
| `ArgoWorkflowTemplates` | Argo Workflows and CronWorkflows → the WorkflowTemplates and ClusterWorkflowTemplates their `workflowTemplateRef` and task or step `templateRef`s use | `families.go` |
| `TraefikBackends` | Traefik IngressRoutes and TraefikServices → the Services and TraefikServices they route, balance or mirror to; IngressRouteTCPs and IngressRouteUDPs → their Services; in any namespace | `traefik.go` |
| `TraefikRouteConfig` | Traefik HTTP and TCP routes → their Middlewares (MiddlewareTCP), ServersTransports (ServersTransportTCP) and TLSOption, and chain Middlewares → the Middlewares they run, in any namespace | `traefik.go` |
| `TraefikSecrets` | Traefik transports, TLSOptions, TLSStores and routes → the Secrets and ConfigMaps they read | `traefik.go` |
| `ContourBackends` | Contour HTTPProxies → the Services they send to and the HTTPProxies they include or their TCP proxy delegates to, in any namespace | `contour.go` |
| `ContourSecrets` | Contour HTTPProxies → the Secrets they read for TLS and validation | `contour.go` |
| `PodPlacement` | Pods → the Node they run on (a finished Pod runs nowhere); a Node's Pods are read from every Pod | `status_families.go` |
| `Reflections` | Reflector mirrors (Secrets, ConfigMaps) → the source their `reflects` annotation names, in any namespace; a source's mirrors come from `ReflectsIndex` | `status_families.go` |
| `Storage` | PersistentVolumeClaims → their PersistentVolume and StorageClass; PersistentVolumes → their claim and StorageClass | `status_families.go` |
| `MachineNodes` | Karpenter NodeClaims and Cluster API Machines → the Node their status records | `status_families.go` |
| `AnalysisRuns` | Argo Rollouts → the AnalysisRuns their status points at | `status_families.go` |
| `SealedSecrets` | Bitnami SealedSecrets → the Secret of their own name they produce | `status_families.go` |

A **selector family** (`selectors.go`, `selector_families.go`) is one way objects choose others by labels: the kinds
that select and how each reads its selector, and the kinds that can be chosen and how a selector sees each (a
workload is seen as the pods its template makes; a Rollout with a `workloadRef` as its referenced workload's). What a
match depends on but never lists, such as that template or a Namespace's labels, is read with Radar's identity, as the
relationship graph reads it (`Lookup`). `Selected(family, selector)` lists what a selector chooses and
`SelectedBy(family, object)` what chooses an object, through the same search, access checks and coverage as
`Referrers`. They follow the API's semantics, not the topology view's display rules: an empty NetworkPolicy, Cilium,
PodDisruptionBudget or monitor selector chooses everything in scope, as a default-deny policy does. A match Radar
can't judge, such as a Cilium selector on Namespace labels, leaves the answer incomplete (`filter_eval_failed`)
rather than reading as no match.

| Family | Selectors → chosen |
|---|---|
| `ServiceSelectors` | Services → the Deployments, Rollouts, StatefulSets, DaemonSets and active Jobs whose pod template their selector matches (a Service without a selector, or an ExternalName Service, chooses none) |
| `DisruptionBudgets` | PodDisruptionBudgets → Deployments, StatefulSets, DaemonSets |
| `NetworkPolicies` | NetworkPolicies and CiliumNetworkPolicies → Deployments, StatefulSets, DaemonSets; Cilium policies match as Cilium does (`netpol.CiliumSelects`: every spec, label sources, identity labels) |
| `Monitors` | ServiceMonitors → Services; PodMonitors → workloads; in the namespaces their `namespaceSelector` names |
| `CalicoPolicies`, `CalicoStagedPolicies` | Calico NetworkPolicies and GlobalNetworkPolicies, and their staged kinds that preview protection → Deployments, StatefulSets, DaemonSets, matched as Calico does (`netpol.CompileCalicoPolicyMatcher`), reading Namespace and ServiceAccount labels, with Radar's identity as the graph does (they decide a match but are never listed), only once a policy's other selectors match; a policy both Calico groups serve is listed once |

Extractors live in `pkg/configrefs`, and the topology builder calls the same ones, so the topology view and the
families read each reference's fields the same way. Resolution can still differ: topology lists one Traefik group per
cluster, while a family resolves each reference through its candidates, so on a cluster serving both groups a
Middleware only in `traefik.containo.us` is a family's answer but not drawn.

Families are built here while Related Resources still reads the relationship graph; readers switch to the families in
one change that also removes the graph.

The cache-source conformance tests check `Indexed` and both answers against the brute-force `Scan` of generated
worlds whose objects declare references (`ownershiptest.Object.Declares`), and through histories of changes.

## What manages an object

`View.Manager(ref)` (`pkg/ownership/manager.go`) weighs two kinds of evidence and names no winner they don't support:
**recorders** (local managers whose status lists name the object) and **claims** (what the object's metadata names,
each checked against its manager: recorded, omitted, unreadable, remote, missing, not-recording for native Helm,
unresolved without a namespace, withheld, or unchecked).

The state, first match wins:

| state | evidence |
|---|---|
| **shared** | several lists name it |
| **disagree** | a list names it and a claim names another manager |
| **agree** | one list names it and every claim names that manager |
| **listed** | one list names it, no claim |
| **claimed** | claims only |
| **unknown** | no evidence, and Radar couldn't look everywhere |
| **none** | no evidence in what Radar collects and the caller may read — never "unmanaged" |

Copied claims (an Argo CD tracking ID naming another object) never count. A native Helm release's claim counts only
without other evidence, since Flux's helm-controller writes the same annotations. An instance label Argo CD cut to 63
characters names the tracking ID's Application.

When the object's evidence doesn't settle it, the walk follows the controller chain (Pod → ReplicaSet → Deployment) to
the nearest ancestor whose evidence does: **via**. Weak claims don't stop the walk: copied ones, and metadata a
controller-owned object inherits (an instance label from its template, Helm annotations from its Deployment, Flux origin
labels naming a manager that doesn't exist or whose list was read and leaves it out). If nothing settles it and the
object makes no claim of its own, the nearest ancestor claim answers as **via**, **claimed**; other claims on the way
stay in **between**. A walk that can't follow an owner says so (**stop**), and a manager the request read differently
earlier (still loading, or another incarnation) leaves the answer incomplete.

On the wire (`relationships.ownership.manager`) the answer carries the state and the evidence that answered. Which
lists Radar couldn't read stays internal: `unknown` reports it left no answer. A positive state doesn't mean every list
was read or that management is exclusive. `ownership.managers` separately covers the object's own recorder search.

Every answer is computed as the caller. Recorded membership comes only from managers the caller may read: an unreadable
manager contributes no evidence. A name already in the object's own metadata stays as a claim, checked `withheld` when
the caller can't read that manager. `narrowed` means the answer is scoped to the caller and may be incomplete; a
manager they can't read may still revert a change. UI relationships use Radar's
[namespace-level trust model](authentication.md#read-granularity-namespace-level), with additional checks for protected
and cluster-scoped kinds. REST AI context and MCP require exact-kind `get` access for ownership and manager references,
so their answers can be narrower than the UI's. These checks do not support `resourceNames`-only grants.

## The top controller

Agent navigation reads `View.ControllerChain`, independently of display topology: a Pod can lead through its StatefulSet
to an operator CR that Radar does not draw. Only `controller: true` extends the chain. A sole non-controller reference
(for example, Strimzi PodSet → Kafka) remains available as `owner`.

List/search summaries choose one inspection lead, in order:

1. The highest readable controller ancestor.
2. An observed spec generator, such as a Certificate targeting its Secret, when the controller chain is settled.
   Owner references, display shortcuts, GitOps/Helm managers and GatewayClass are excluded from this step.
3. An eligible `View.Manager` lead: `agree`, `listed` or `shared`; or a matching non-copied, non-weak `claimed` lead
   checked `recorded`, `omitted` or `unreadable`. Native Helm also accepts `not-recording` with release namespace/name.

Detail `managedBy` retains the highest controller and observed spec generators; `manager` carries GitOps/Helm evidence.
Summary `source: native` identifies a controller/spec relation, even for GitOps API kinds. Native references include
`group` unless they match a built-in kind and group. A lead grants no edit authority or exclusivity. Retained Flux/Helm
claims need not indicate current reconciliation; native Helm coordinates do not verify release existence.

`omitted` paths are relative to their containing context and can qualify a present pointer:

| Reason | Meaning for agent navigation |
|---|---|
| `rbac_denied` | The subject or a controller ancestor was unreadable. |
| `cache_cold` | An owner kind is known to be uncollected or unsynced. Ownership starts no watches; `list_resources` for that kind can activate its on-demand cache. |
| `unavailable` | A controller walk or observed spec source could not be resolved, the required graph is missing, or management evidence has no eligible lead. Retry is not guaranteed to help. |
| `budget_exceeded` | The list/search enrichment budget elapsed; other causes can coexist. |

A GitOps fallback may coexist with an incomplete-controller note. Detail can report missing spec evidence after a
summary already found its controller lead. Denied spec sources remain hidden without relationship-specific notes.
The graph has no per-kind completeness contract: an unobserved spec relation may have neither a lead nor an omission.
Neither a missing lead nor `manager.state=none` proves unmanaged.

Certificate-targeted unused Secrets participate in relationship graphs, under existing Secret access checks, but remain
hidden in the normal visual graph. Trivy keeps its scan-time workload projection; label-derived scan identity survives
failed enrichment, qualified when live scan state could not be checked.

Each REST AI list/search or MCP list/search request gets a three-second optional-enrichment budget, including authorization and queue waits.
Health and prepared issue counts survive expiry. Saturation or failed preparation can omit issue counts, including in
Omnibar results; `omitted` distinguishes unknown counts from zero. Omnibar requests `context=status` on REST search:
cached health and authorized issue counts, without ownership, relationship-graph or live scan-target lookups. Unrequested
navigation fields have no omission note. The default `context=summary` and MCP search retain navigation. Lists share one caller-scoped ownership session.
Execution and caching details live in [summarycontext](../internal/summarycontext/summarycontext.go),
[AgentOwnership](../internal/k8s/ownership.go) and [cachesource](../pkg/ownership/cachesource/).

## Coverage

- A **gap** is a kind (and namespace) the read couldn't cover, with a `pkg/readreason` code. A whole-kind state is one
  gap however many namespaces a search names.
- `DependentCandidatesRead.Complete()`: no gaps, among the kinds the garbage collector acts on. Its `Gaps` slice is
  read-only, may be shared across searches, and is never changed by later session reads; clone it before modifying it.
  The cache source prepares each scope when first searched and shares equal coverage arrays, but still reads watched
  stores and their readiness on each search.
- `RecordingManagersRead.Complete()`: no gaps, no unreadable and no remote managers, among the three manager kinds.
  An object with no recorders may still be managed by something Radar doesn't read.
- Answers can be stale, never confidently wrong: a store answers as fresh as its informer, and a manager's list as fresh
  as its last status write.

## Checking it

`pkg/ownership/ownershiptest` holds the safety net every change is checked against:

- a `World` states a cluster and how much of it Radar collected;
- `Scan` answers every question from the World by brute force, sharing no read, scope or coverage code with the cache
  source;
- `Conform`/`Differences` ask both every question, on fixed and generated worlds and through change histories.

`internal/ownershiplive` (build tag `liveparity`) runs the same comparison against a live cluster, with its own status
parser, and rechecks each difference against fresh GETs. To add a family: extend the World and the oracle first, then
the source, then the live check.
