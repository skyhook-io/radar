# Plan — per-resource health for Argo CD apps when Argo no longer persists it

**Status:** signed off by Nadav (2026-09-13) after two Codex cross-review
cycles. Uncovered by external PR #1624.

**Copy rule (from sign-off):** user-facing text stays plain. No field names,
tier names or internals in banners, markers or tooltips; an average engineer
who has never read Argo's upgrade notes must understand it on first read.
Technical detail goes behind a "Why?" link to docs.

## 1. The problem, in one paragraph

Radar's Argo CD insights take per-resource health from exactly one place: the
`health.status` field inside `Application.status.resources[]`. Argo CD 3.0
(May 2025) changed the controller default `controller.resource.health.persist`
from `true` to `false`, so on any Argo 3.x install running defaults that field
is empty for **every** managed resource, of every kind, and the CR carries
`status.resourceHealthSource: appTree` instead. Argo still evaluates health
(built-in Lua, custom checks) and still rolls it up into the persisted
app-level `status.health.status`; it just stops writing the per-resource
results into the CR. Radar therefore shows the app as **Degraded** with
nothing underneath it: no per-resource Issue, no cause, `Unknown` health on
tree nodes for non-topology kinds, no Degraded/Missing categories in Changes.
The user in #1624 hit exactly this and, reasonably, blamed the CRD.

## 2. Evidence

**Upstream.**
- Argo CD 2.14 → 3.0 upgrade notes, "Health status in the Application CR":
  "The health status of each object used to be persisted under `/status` in
  the Application CR by default … Now, the health status is stored
  externally." Revert: `controller.resource.health.persist: "true"` in
  `argocd-cmd-params-cm`. https://argo-cd.readthedocs.io/en/stable/operator-manual/upgrading/2.14-3.0/
- `status.resourceHealthSource` is `""` (`ResourceHealthLocationInline`) when
  persisted, `"appTree"` when not (`pkg/apis/application/v1alpha1/types.go`).
  The flag also exists on 2.x, so this is a *mode*, not a version.
- argoproj/argo-cd#10312 (the change), #21964 and #24832 (fallout: `Missing`
  never reported in the CR; `Status.Resources[].Health` empty on
  `GET ...?refresh=true`, but populated on a plain GET because argocd-server
  infers it from its tree cache — `inferResourcesStatusHealth`).
- Argo ships built-in health checks for all external-secrets.io kinds
  (`resource_customizations/external-secrets.io/ClusterSecretStore/health.lua`
  is a plain `Ready` condition check). So the #1624 ClusterSecretStore *was*
  evaluated Degraded by Argo; the result simply wasn't persisted.
- Argo's roll-up only counts resources that have a health check; kinds without
  one (Namespace, most uncurated CRDs) contribute nothing
  (`controller/health.go`). `appTree` says where results are stored, not
  which kinds have them.

**In Radar.** Every consumer of per-resource health reads the CR field and
nothing else:
- `pkg/gitops/tree/inventory.go:39` — `parseArgoManagedResources` reads
  `m["health"]["status"]`; empty → `""`.
- `pkg/gitops/tree/graph.go:20` — `nodeFromTopology` fills an empty health
  from Radar's topology status (for topology kinds, in every mode).
  `graph.go:45` — a managed resource not in Radar's topology becomes a
  `syntheticNode` → `healthToTopology("")` → `unknown`.
- `pkg/gitops/tree/graph.go:294` — `Summary.Degraded` counts `Health ==
  Degraded|Missing`; feeds the "N managed resources are degraded" fallback at
  `pkg/gitops/insights/insights.go:656`.
- `pkg/gitops/insights/insights.go:969` — `argoResourceChanges` reads the same
  CR field a second time, independently; drives `categorizeArgoChange`
  (`:1293`) and the per-resource Degraded/Missing Issue at `:620`, which is
  the only path that bridges to the issues engine's concrete cause
  (`resourceProblemCause`).
- `pkg/gitops/insights/insights.go:372` — `enrichChangeHealthFromTree`
  already backfills an empty `Change.Health` from the tree node's health
  (which for topology kinds is Radar's own status, in every mode). So the
  Changes table already shows Radar-derived health with no provenance; the
  per-resource Issue path (`:611`, which re-reads the CR) does not benefit.
  There are already two authorities, silently.
- Graph node colour comes from `topologyStatus`, i.e. Radar's own status
  (`packages/k8s-ui/src/components/gitops/tree/GitOpsTreeGraph.tsx:546`), not
  from Argo's health, even when Argo's health is present. Pre-existing; means
  an Argo-Degraded node can render green today.
- Tree and insights are separate endpoints with separate handlers
  (`internal/server/gitops_handlers.go:133` `handleGitOpsTree`, `:170`
  insights); the RBAC filter recomputes the summary with a second summarizer
  (`:606` `summarizeGitOpsTree`) that differs from `pkg/gitops/tree`'s
  (`graph.go:284`, which excludes the root).
- `grep -rn resourceHealthSource pkg internal web packages` → nothing.
- Our fixture never exercises this: `scripts/gitops-demo.sh:32` pins
  `ARGOCD_VERSION=v2.13.2`.

**What still works on Argo 3:** fleet view (app-level `status.health`),
operation failures, drift detectors, Argo conditions, lifecycle, tree nodes
for topology kinds. The loss is specifically *per-resource* health and
everything derived from it.

## 3. What we are doing right now (PR #1624)

Merged with small maintainer edits: (a) render the already-computed `cause`
on the headline issue row and as a tooltip on unhealthy tree nodes; (b) a
last-resort lead when the app is Degraded, nothing else explained it, and the
app deploys to this cluster: the managed resource with the loudest recent
Warning event, shown as a warning-tier "PossibleCause" row that leaves the
degraded-resources summary visible. The `degradedResourcesExplained` gate
(critical per-resource Issue or failed operation; not info/drift rows) lands
there too and this branch reuses it.

This is a stopgap. It names *one* resource in the Issues band. It does not
restore per-resource health on tree nodes, in Changes categories, in
`Summary.Degraded`, or for the second and third degraded resource.

## 4. Proposed solution — high level

**Principle: show Argo's health when we can get it; otherwise show Radar's
diagnostics, labelled as Radar's, and never manufacture a health verdict.**

| Tier | Source | When | What it yields |
|---|---|---|---|
| A | `status.resources[].health` in the CR | `resourceHealthSource == ""` | Argo's verdict per resource (today's behaviour, unchanged) |
| B | argocd-server, plain `GET /api/v1/applications/{name}` (no `refresh`) | `appTree` mode, Argo API integration connected, app destination in-cluster | Argo's verdict per resource, same field shape as the CR |
| C | Radar diagnostics: the issues engine's existing classification per managed resource | `appTree` mode, Tier B did not answer for this app, app destination in-cluster | **Problems only** (`Degraded` with the engine's reason, message and severity). Never `Healthy`, never `Progressing`; unclassified resources stay "no health" |

A successful Tier B answer is authoritative for the whole app, including
"Argo has no check for this kind": Tier C does not run on top of it. Mixed
sources within one app are therefore limited to Tier A/B + the pre-existing
topology fill (below), never Argo + engine.

Tier C deliberately reuses `internal/issues` rather than a new condition
reader: the engine already applies transient-reason, suspension and
`observedGeneration` filters (`internal/issues/source_conditions.go:490`)
and owner-collapses pod problems onto their workload, so a crashlooping
Deployment and a `Ready=False` ClusterSecretStore both come out as one
classified problem on the managed ref. A naive `Ready=False → Degraded` read
would call a cert-manager Certificate mid-issuance Degraded where Argo says
Progressing; the engine's filters are the difference.

Every resolved value carries provenance — `{status, reason, message,
severity, source: argo | argoApi | radar}` — and *everything* that presents
health (tree colour, Changes category, per-resource Issue,
`Summary.Degraded`) consumes that one resolved assessment. Radar-sourced
Issues carry `source: radar` explicitly, use the engine's reason
(`CrashLoopBackOff`, `Ready: InvalidProviderConfig`) and the engine's
severity (a warning-tier finding does not become a critical Issue just
because it sits under an Argo app).

The pre-existing topology fill (`nodeFromTopology`, `graph.go:20`, and the
`enrichChangeHealthFromTree` backfill) is kept as behaviour — removing it
would regress Argo 2 users who rely on Radar's Deployment/Pod status for
kinds Argo doesn't assess — but it is folded into the same model with
`source: radar` so it stops masquerading as Argo's verdict.

Non-goals: re-implementing Argo's Lua library; changing Tier-A behaviour;
reading Argo's Redis; remote-destination (hub-spoke) apps beyond "nothing
derived, explain why" — see §7.

## 5. Why this design, and what I considered

- **Why not just document "set `persist: true`"?** We will document it, but
  it's an operator-side revert of an upstream default chosen for controller
  performance. Most users won't, and "Radar needs you to reconfigure Argo" is
  a bad first impression. Escape hatch, not fix.
- **Why plain GET rather than `resource-tree` for Tier B?** Same field shape
  as the CR, so it slots into `parseArgoManagedResources` with no new
  projection; declared resources only (no descendants, images, host info);
  cheaper. `resource-tree` stays the fallback if a supported 3.x version
  turns out not to infer health on GET. Must be verified on a real 3.x before
  building (§7).
- **Why not make Tier B the only fix?** The integration is optional and many
  deployments won't wire an Argo token. Tier C is what the OSS binary gets by
  default, and it's honest about being Radar's read.
- **Why Tier C yields problems only, not health?** Radar cannot know which
  kinds Argo evaluated. Claiming `Healthy` for a kind Argo never checks, or
  `Degraded` for one Argo would call `Progressing`, is exactly the false
  authority we must avoid. "Radar found a problem here" is a claim Radar can
  stand behind; "this resource is Healthy per Argo" is not.
- **Why resolve in the host handler, not in `pkg/gitops/tree`?** The three
  sources live in three places: the CR (pkg), the Argo API client
  (`internal/argocd`), the issues engine (`internal/issues`). The handler
  (`internal/server/gitops_handlers.go:170-200`) already has all three and
  already runs `filterGitOpsTreeForUser`. A host-side resolver keeps `pkg/`
  free of `internal/` imports and gives tree + insights one overlay.
- **Why gate Tier C on `appTree` mode rather than "health is empty"?** On
  inline mode an empty health means "Argo has no check for this kind" — a
  known, intentional absence. On `appTree` it means "Argo may have an answer
  we can't see". Only the second case warrants Radar's diagnostics.

## 6. Detailed steps

### PR 1 — mode detection, resolved-health model, Tier C, provenance, fixture

**Step 1 — Mode detection (Certain).**
`pkg/gitops/tree/inventory.go`: read `status.resourceHealthSource`; expose
`ResourceTree.HealthMode` = `inline | appTree`. Detection is the explicit
field only — do not infer from "any entry has health".

**Step 2 — One resolved-health assessment (Confident).**
Introduce `HealthAssessment{Status, Reason, Message, Severity, Source}` on
tree `Node` (JSON `health`, `healthReason`, `healthMessage`,
`healthSeverity`, `healthSource`), **replacing** the bare `Health` string as
the authority. The builder populates it from Tier A (`source: argo`) or the
topology fill (`source: radar`, topology status only for topology kinds, as
today). Migrate every reader to it: `healthToTopology`/node colouring,
`Summary.Degraded` (`graph.go:294`), `categorizeArgoChange`, the
per-resource Issue at `insights.go:620`, and `enrichChangeHealthFromTree`
(`:372`, which becomes the only way `Change.Health` is set —
`argoResourceChanges` stops reading the CR field itself). `Issue` gains
`Source`. Collapse the two summarizers into `pkg/gitops/tree.Summarize` and
call it once per response, after overlay and after the RBAC filter. Node
colour switching from `topologyStatus` to the assessment also fixes the
pre-existing "Argo says Degraded, node renders green" mismatch.

Tests: Argo-2 root with inline health + an unhealthy topology hit renders
Argo's value (`source: argo`), not Radar's; Argo-2 root with a kind lacking
inline health and a topology hit yields `source: radar`; a kind with neither
stays "no health".

**Step 3 — Tier C overlay in the host, shared by both endpoints (Confident).**
`internal/server/gitops_handlers.go`: one `resolveGitOpsTree(r, req)` used by
`handleGitOpsTree` (`:133`) and the insights handler (`:170`), so the two
endpoints can't disagree. It builds the tree, then when `HealthMode ==
appTree` **and** `isInClusterDestination(root)`
(`internal/server/argo_diff.go:238`, used at `gitops_handlers.go:206`) **and**
Tier B did not answer, for each declared node with no assessment calls the
existing `insightsResolver.ResourceProblems` (`:847`). A hit →
`{Degraded, <engine reason>, <engine message>, <engine severity>, radar}`;
no hit → leave empty. Then RBAC filter, then `Summarize` once.

`Missing` is **out of scope for PR 1**: the builder's cache miss
(`isEnrichNotFound`, `builder.go:18`) is documented to include hub-spoke
objects absent from the local cluster, and `GetDynamicWithGroup` can return
not-found from an unsynced informer. Deriving absence needs a synced-scope
guarantee we don't have today. Tracked separately (§7).

Tests: `appTree` root with a crashlooping Deployment (engine hit via owner
collapse), a `Ready=False` CRD (engine generic-CRD hit), a `Ready=False` CRD
the engine filters as transient (no derivation), a remote-destination app
(no derivation, warning emitted).

**Step 4 — Provenance in the UI, shipped with Step 3 (Confident).**
- Detail page, `appTree` mode and Tier B didn't answer: one quiet banner
  above the Issues band. Copy: *"Argo CD 3 no longer records health for each
  resource on the application. Rows marked "Found by Radar" are problems
  Radar detected by looking at the resources itself."*
  plus a "Why?" link to the docs paragraph (which is where
  `resourceHealthSource`, `persist: true` and the Argo API integration get
  named). When Tier B answered: no banner.
- Tree node + Changes chip, `healthSource === 'radar'`: a small "Radar"
  marker. Tooltip: *"Found by Radar. Argo CD's own health for this resource
  isn't available."* followed by the engine message. `argo`/`argoApi`: no
  marker.
- Issues: no extra marker needed — the reason vocabulary already differs.
- **Remote-destination apps** (`!isInClusterDestination(root)`): a different
  banner, same slot: *"This application deploys to a different cluster, so
  its resources aren't visible from here."* When Radar is running standalone
  and the cloud funnel button is mounted (cloudConnect capability), append:
  *"Connect both clusters to Radar Cloud to see this app's resources
  together."* where "Radar Cloud" opens the funnel's own dialog (which
  connects this cluster; the destination is a second connect from Cloud). One line, no modal, no repeat nag:
  honour whatever dismissal state the funnel already keeps. Confirmed at
  sign-off: Radar Cloud does correlate a hub Application to its resources on
  the destination cluster, so the copy may say so.

**Step 5 — #1624 fallback gate (Confident).**
Change the events fallback gate from `len(out)==0` to "no resource-scoped
Issue" (informational/operation/condition issues don't count). It stays a
single-pick last resort; its `Message` gets a "likely" qualifier. The tree
summary fallback at `:656` gets the same gate. Not tracking per-resource
coverage — a heuristic that picks one resource cannot cover N.

**Step 6 — Fixture (Certain).**
`scripts/gitops-demo.sh`: add `ARGOCD_VERSION` 3.x as the default and keep
2.13.2 selectable. Add to `argocd-cm` a `resource.customizations.health`
entry for a fixture-defined CRD (`radar.demo/Widget`, `Ready` condition
based) so Argo actually evaluates it, plus one fixture app containing: a
`Widget` with `Ready=False` (Argo Degraded, Radar engine hit), a second
fixture CRD with **no** customization and `Ready=False` (Argo ignores it;
Radar must not present it as Argo's verdict — the banner + `radar` source
make this visible), and a crashlooping Deployment. README documents the
matrix. Verify against the real Argo tree/API results, not just Radar's.

**Step 7 — Docs (Certain).**
`docs/gitops.md:65` "Per-resource health": three sources, what `appTree`
means, the `persist: true` escape hatch. Mirror to radar-docs.

### PR 2 — Tier B via the Argo API (Confident on shape; verify first)

- Spike: on a 3.x cluster, confirm plain `GET /api/v1/applications/{name}`
  returns `status.resources[].health`. If not, use `resource-tree` projected
  to declared refs only.
- `pkg/argoapi`: `Application(ctx, q)` (or `ResourceTree`), bounded body
  like `ManagedResources` (`client.go:177`), request deadline.
- `internal/argocd/manager.go`: cached next to `ManagedResourcesCached`
  (`:393`), single-flight per app, backoff on failure; invalidated with the
  existing config/context resets.
- Identity check: the configured server is an arbitrary URL
  (`internal/argocd/manager.go`); a different Argo could serve a same-named
  app. Accept the response only if `metadata.uid`, namespace/name and
  `spec.destination` match the local CR; otherwise log once and treat Tier B
  as unavailable. (Worth applying the same UID check to the existing
  managed-resources diff path — separate small fix.)
- Handler: in `appTree` mode, connected, in-cluster destination, identity
  verified → overlay `{status, message, argoApi}` on declared nodes and
  **skip Tier C** for that app. Failure/mismatch → Tier C, with the banner
  saying so.
- Per-user filtering unchanged (`filterGitOpsTreeForUser`, `:470`).

### Sequencing

#1624 merges first. PR 1 conflicts with it only at the fallback gate (Step
5). PR 2 is independent of PR 1's UI but depends on its assessment model.

## 7. Risks and open questions

- **Remote-destination (hub-spoke) apps — decided.** Neither Tier B nor
  Tier C is safe for an app whose destination is another cluster: a local
  SAR can't authorize remote data, and Tier C would read a same-named
  *local* object. PR 1 derives nothing for those, says so, and (standalone
  only) hints that Radar Cloud connects additional clusters. Proper
  hub-spoke health stays a multi-cluster (Cloud) feature; not in OSS scope.
- **`Missing` derivation — deferred (sign-off default).** Real loss on Argo 3
  (Argo itself has the same bug in the CR, #21964). Doing it right needs "this
  kind+namespace scope is synced" from the cache plus in-cluster destination.
  Separate ticket, after PR 1.
- **Tier B verification (Certain we must).** #24832 reports plain GET
  populates health on 3.x; confirm on the fixture before writing the client.
- **Engine coverage limits Tier C (Confident, acceptable).** Kinds the issues
  engine doesn't classify get no Radar diagnostic. The banner tells the user
  Argo's per-resource verdicts aren't visible and how to get them (Tier B or
  `persist: true`). That is the honest floor.
- **Residual from cross-review (accepted, not folded).** Codex wanted the
  #1624 events fallback to track per-resource coverage. Rejected: it picks
  one resource by design; once Tier C produces per-resource Issues the
  fallback only matters when nothing was classified, and "no resource-scoped
  Issue" is the right gate for that.
- **Argo 3 as the demo default — yes (sign-off default).** Flips every
  gitops visual test onto the new shape; 2.13 stays selectable via
  `ARGOCD_VERSION`. Tier B keeps the in-cluster-destination gate the diff
  endpoint already uses (sign-off default).
