# Ownership: owners, dependents and GitOps membership

Radar answers "what owns this object?", "what does it own?" and "which GitOps manager records it?" from the informer
caches it already holds, with no API calls. This page is the map: the model, the layers, and where each rule lives.
The doc comments at the linked code are the specification.

## The model

Three kinds of evidence, kept apart:

| Evidence | Where it comes from | What it proves |
|---|---|---|
| **Owner references** | `metadata.ownerReferences` on the dependent | What Kubernetes' garbage collector links. A reference names its owner's UID; whether it resolves to the owner is a rule's call (`ResolveOwner`). |
| **Recorded membership** | A GitOps manager's status: an Argo CD Application's `status.resources`, a Flux Kustomization's or HelmRelease's `status.inventory` | That the manager recorded the object: for Argo CD, the objects its last comparison covered (desired objects not yet created, live ones awaiting pruning, objects another Application also tracks); for Flux, its last apply. Not that it applied the object or alone manages it. |
| **Claims** | Labels and annotations on the object (Argo CD's tracking annotation or label, Flux's `*.toolkit.fluxcd.io/name` labels, Helm's release annotations) | What the object says manages it: a candidate, not a verdict. Argo CD decides membership by its configured tracking method and installation ID, which Radar may not be able to read. Parsed by `pkg/gitops/claims.go`, every candidate kept. |

Every answer in this package says what it couldn't read. An empty answer means "none" only when it is complete.

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

`View.Manager(ref)` (`pkg/ownership/manager.go`) weighs the two GitOps kinds of evidence for one object and names no
winner they don't support. It gathers, for the object, the local managers whose lists name it (**recorders**) and the
claims its metadata makes, each checked against its manager (recorded, omitted by a list that was read, unreadable,
remote, missing, not a recording kind as for a native Helm release, unresolved for a claim with no namespace,
withheld from the caller, or unchecked when Radar couldn't establish what the list says). When that evidence doesn't settle the question it walks the controller chain, Pod →
ReplicaSet → Deployment, to the nearest ancestor whose evidence does, and reports it as **via**. A claim is **weak** —
it doesn't stop the walk — when it was copied from another object, or is metadata an object a controller owns commonly
inherits: an Argo CD instance label from its template, or a Helm release's annotations, which a Deployment copies onto
its ReplicaSets. When no object on the chain settles it and the object makes no claim that isn't copied, the
nearest ancestor that makes one answers, as **via**, with the state **claimed**; claims farther up stay in
**between**. A walk stops at an owner it
can't follow and says so. A manager the request read before its recorder search found it, unavailable then or
another incarnation, leaves the answer incomplete rather than confirming membership.

The state, first match wins: **shared** (several lists name it), **disagree** (a list names it and a claim names
another manager), **agree** (one list names it and every claim names that manager), **listed** (one list, no claim),
**claimed** (claims only), **unknown** (no evidence, and Radar couldn't look everywhere) and **none** (no evidence in
what Radar collects and the caller may read — never "unmanaged"). A native Helm release's annotations count only when
no GitOps evidence exists, since Flux's helm-controller writes them too. With Argo CD's annotation+label tracking, an
instance label cut to 63 characters names the tracking ID's Application, not another one. The answer reaches the wire as
`relationships.ownership.manager`; `relationships.managedBy` keeps its own meaning, the topmost owner the graph links.

On the wire the answer carries the state and the evidence that answered: each object's recorders and claims. Which
manager lists Radar couldn't read or search stays internal: `unknown` says that left no answer, `none` that nothing was
found within what Radar collects and the caller may read. A positive state doesn't say every list was read, and doesn't
establish exclusive management. On a full relationship response, `ownership.managers` separately says what the
resource's own recorder search missed; it is not coverage of an answer that came through a controller ancestor.

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
