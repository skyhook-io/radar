# Ownership: owners, dependents and managers

Radar answers "what owns this object?", "what does it own?" and "what manages it?" from the informer caches it already
holds, with no API calls. This page is the map: the model, which field answers which question, the layers, and where
each rule lives. The doc comments at the linked code are the specification.

## The model

Three kinds of evidence, kept apart:

| Evidence | Where it comes from | What it proves |
|---|---|---|
| **Owner references** | `metadata.ownerReferences` on the dependent | What Kubernetes' garbage collector links. A reference names its owner's UID; whether it resolves to the owner is a rule's call (`ResolveOwner`). |
| **Recorded membership** | A GitOps manager's status: an Argo CD Application's `status.resources`, a Flux Kustomization's or HelmRelease's `status.inventory` | That the manager recorded the object: for Argo CD, the objects its last comparison covered (desired objects not yet created, live ones awaiting pruning, objects another Application also tracks); for Flux, its last apply. Not that it applied the object or alone manages it. |
| **Claims** | Labels and annotations on the object (Argo CD's tracking annotation or label, Flux's `*.toolkit.fluxcd.io/name` labels, Helm's release annotations) | What the object says manages it: a candidate, not a verdict. Argo CD decides membership by its configured tracking method and installation ID, which Radar may not be able to read. Parsed by `pkg/gitops/claims.go`, every candidate kept. |

Every answer in this package says what it couldn't read. An empty answer means "none" only when it is complete.

## Which field answers what

| Question | Field | Evidence |
|---|---|---|
| What owns it / what does it own | `relationships.owner`, `owners`, `children` (REST resource GET); `resourceContext.owner` (AI context) | Owner references |
| Which managers record it / what a manager records | `relationships.managers`, `manages` (Related Resources → Managed By / Manages) | Recorded membership, plus graph links for other managing controllers (a cert-manager Certificate) |
| What manages it, weighed | `relationships.ownership.manager`; `resourceContext.manager` (compact) | Recorded membership and claims, through the controller chain ([below](#what-manages-an-object)) |
| Legacy manager pointer | `relationships.managedBy` | The manager the object's metadata names (Argo CD, Flux, Helm) if any, else the topmost object its manages edges reach — not a controller-owner answer; read by workload selection and summary grouping |
| Top controller, or other managers, for agents | `resourceContext.managedBy` | The highest readable controller from `View.ControllerChain`, plus observed non-GitOps managing controllers from separately classified spec-derived graph relations. `resourceContext.manager` carries GitOps/Helm evidence. An incomplete chain is noted in `omitted` |
| What a list row or search hit belongs to, for triage | `summaryContext.managedBy` (REST and MCP lists and search) | Controller-first next hop: the highest readable controller, else an observed spec-declared managing controller, else an eligible `View.Manager` lead. `source` identifies native-controller versus GitOps/Helm evidence; custom native kinds include `group`. `omitted` notes incomplete controller walks. A Trivy report retains its scanned-workload semantics |
| Where a request change belongs | rightsizing `managedBy: {tool, signal}` | Controller owner, GitOps/Helm metadata, `addonmanager` mode ([mcp.md](mcp.md)) |

The four `managedBy` fields answer different questions; read `manager` for "which GitOps manager".

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
  (`ReadableDependents` for typed kinds, `ReadableIndexed` for dynamic ones).
- **`pkg/ownership/cachesource`: the cache source.** One traversal (`dynamicIndexed`) reads either index for custom
  resources. It reads a resource watched at several versions from one version, pins the namespaces a version is watched
  in at the session's first look, and turns every unread kind or namespace into a gap.

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
controller-owned object inherits (an instance label from its template, Helm annotations from its Deployment). If nothing
settles it and the object makes no claim of its own, the nearest ancestor claim answers as **via**, **claimed**; other
claims on the way stay in **between**. A walk that can't follow an owner says so (**stop**), and a manager the request
read differently earlier (still loading, or another incarnation) leaves the answer incomplete.

On the wire (`relationships.ownership.manager`) the answer carries the state and the evidence that answered. Which
lists Radar couldn't read stays internal: `unknown` reports it left no answer. A positive state doesn't mean every list
was read or that management is exclusive. `ownership.managers` separately covers the object's own recorder search.

Every answer is computed as the caller. Recorded membership comes only from managers the caller may read: an unreadable
manager contributes no evidence. A name already in the object's own metadata stays as a claim, checked `withheld` when
the caller can't read that manager. `narrowed` means the answer is scoped to the caller and may be incomplete; a
manager they can't read may still revert a change. REST decides readability by namespace (Radar's [trust model](authentication.md#read-granularity-namespace-level));
MCP checks each manager kind exactly, so the two can differ for a user whose namespace access is wider than their
GitOps permissions.

## The top controller

The agent pointers read `View.ControllerChain` as the caller; they do not walk the topology or reconcile a graph pointer
with the chain. A Pod can therefore point through its StatefulSet to the operator's custom resource, even when that
resource is not drawn. This is the highest controller Radar read, not proof that a particular field should be edited there.

List/search summaries prefer this controller to a GitOps or Helm manager, including copied GitOps labels. Without a
readable controller ancestor, an observed spec-declared managing controller (such as a Certificate for its Secret) takes
precedence over a GitOps lead. Those residual graph sources use the same classification as Related Resources: exclude
owner references, controller shortcuts, unreadable objects, GitOps managers and native Helm. An unsettled chain cannot
establish that a graph source is not an ownership shortcut, so it supplies no residual manager. The graph is loaded and
indexed only when this residual lookup is needed.

With no controller relation, `View.Manager` supplies a summary lead for `agree`, `listed` or `shared` with a lead. A
`claimed` lead needs a matching active claim checked `recorded`, `omitted` or `unreadable`, or a native Helm
`not-recording` claim with release namespace and name. Copied or weak inherited claims and missing, unresolved, withheld or unchecked claims remain in
full detail instead of becoming summary pointers. A lead is where to inspect next, never a verdict of exclusive management.

Both detail and summary `omitted` use `managedBy: rbac_denied` or `unavailable` when a controller walk stops short,
including when no ancestor was readable. An independent spec/GitOps lead is not labelled as a partial controller root.
Lists share one `k8s.AgentOwnership` session and view across rows. Claim checks share a manager's membership state in
that view; member enumeration still reads current status. UI ownership retains namespace-level access; REST AI context
and summaries additionally require exact reference `get` permission before deriving their answers, as MCP does.

## Coverage

- A **gap** is a kind (and namespace) the read couldn't cover, with a `pkg/readreason` code. A whole-kind state is one
  gap however many namespaces a search names.
- `DependentCandidatesRead.Complete()`: no gaps, among the kinds the garbage collector acts on.
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
