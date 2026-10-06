# CRD Integration Guide

Radar already discovers custom resources and provides generic details, status,
MCP access and, for CRDs that declare them, Kubernetes printer columns. An
integration adds useful interpretation and relationships—not just another kind
in a list. This guide also applies to extension APIs such as OpenShift Routes;
don't assume every discoverable resource has a CRD object.

## 1. Start here

- [ ] Define the operator question, resource kinds and an independently useful
  first PR. Prefer existing views over a new dashboard. Split larger work by useful
  outcome (e.g. drawer/status, then relationships), not backend versus frontend.
- [ ] Search for existing support and helpers. KEDA is a resource-presentation
  example; Knative demonstrates collisions; CloudNativePG and Velero show
  controller-specific status and Issues. Borrow the pattern, not the entire scope.
- [ ] Agree on supported API versions and real-controller testing. Ideally you
  operate the tool; otherwise identify a testing partner before starting.
- [ ] Identify resources by **API group + kind/plural**, using discovery for served
  versions and namespace scope. Even an apparently unique kind may collide with
  an integration Radar doesn't curate. Examples: `Route`, `Cluster`, `Backup`,
  `Subscription` and `BGPPeer`.
- [ ] For supported-resource startup watching and fallback discovery, update
  `supportedCRDFallbacks` in [dynamic_cache.go](../internal/k8s/dynamic_cache.go).
  Entries carry group, versions, plural, kind and scope; `WarmupCommonCRDs` consumes
  this registry. Don't add a separate name-only warmup list. Fallback registration
  probes access; an entry is not proof that the API is installed or readable.
- [ ] Check the [Helm ClusterRole](../deploy/helm/radar/templates/clusterrole.yaml) and
  [values](../deploy/helm/radar/values.yaml) for the new API group. Follow existing
  per-group read-access toggles; don't rely on an opt-in wildcard or request write
  permissions for a read-only integration. The chart coverage test checks groups,
  not every resource/verb or rendered toggle combination.
- [ ] Make an explicit Radar Cloud caller-permission decision in
  `deploy/helm/radar/files/integration-read-baseline.yaml`: grant, existing, or
  withhold with a reason. Check exact scope and credential-bearing fields; a
  collection grant or renderer is not permission approval. See
  [the default read policy](cloud-rbac-baseline.md). Coverage tests compare the
  policy with backend catalogs and curated frontend identities.
- [ ] Keep namespace filtering, per-user authorization and context-switch behavior
  intact. Missing, not-yet-watched and forbidden are not interchangeable. If adding
  a typed `ResourcePermissions` field, follow `capabilities_alignment_test.go` in
  `internal/k8s/` and the frontend `OPTIONAL_RESOURCE_KINDS`; ordinary dynamic
  resources do not each need a new permission field.

## 2. Choose relevant surfaces

**These sections are optional:** use only those needed for your contribution.
Check existing generic behavior before adding custom handling. Status fixes must
cover the surfaces they affect, but every integration need not customize all four.

Paths below are relative to the repository root. Shared presentation lives in
`packages/k8s-ui`; host data fetching lives in `web`. Read [DESIGN.md](../DESIGN.md)
for UI work and follow the existing wrapper pattern rather than fetching inside
shared renderers.

### Resource views

- [ ] Check API-group labels in `packages/k8s-ui/src/utils/api-resources.ts`.
- [ ] For tables/status, start in `packages/k8s-ui/src/components/resources/`:
  `ResourcesView.tsx`, `generic-status.ts` and `resource-utils-*.ts`. Add curated
  behavior only when it improves the generic view.
- [ ] For rich table cells, reuse/add `renderers/{integration}-cells.tsx` in that
  directory and wire the cells through `CellContent` in `ResourcesView.tsx`.
- [ ] For a drawer, add/reuse a renderer in that directory's `renderers/`, export
  it from `index.ts`, and wire `KNOWN_KINDS`, render lines and `getResourceStatus()` in
  `packages/k8s-ui/src/components/shared/ResourceRendererDispatch.tsx`.
  Reuse sections, properties, conditions, links and problem banners.
- [ ] **Claim only your exact API group** in renderer dispatch, status, actions
  and table cells. Use an exact-group helper such as `isApiGroup` (currently in
  `resource-utils-cnpg.ts`), not substring `includes`. Unrelated CRDs sharing a
  plural must retain generic behavior and never receive core-only actions.
  Guard the existing core renderer, status and actions too—not just the new
  renderer—so both core and custom resources retain the correct behavior.
  See `ResourceRendererDispatch.test.tsx` for collision fixtures.
- [ ] For custom columns, check `GROUP_QUALIFIED_COLUMN_KEYS`,
  `CURATED_COLUMN_GROUPS`, `getColumnsForKind` and `normalizeKindToPlural` in
  `ResourcesView.tsx`. `hasCuratedColumns` selects curated **or** printer columns,
  never both; don't inadvertently remove useful vendor fields. Reuse printer-column
  evaluation rather than writing another JSONPath parser.

### Issues

- [ ] Inspect generic detection in `internal/issues/source_conditions.go`, nearby
  integration-specific `source_*.go` and `pkg/conditions/` before adding a detector.
  Generic Issues primarily inspect false Ready-family conditions; nested,
  negative-polarity or phase-only status may need custom handling.
- [ ] Check detector ownership and fallback suppression: avoid duplicate failures
  or the generic pass reintroducing warnings for intentional states. Don't suppress
  a whole API group when only some kinds are covered.

### MCP / AI context

- [ ] Check existing resource-tool output in `pkg/ai/context/summary_crd.go`,
  `detail.go` and `redact.go`. Preserve reasons and useful references, not raw
  configuration dumps; enrich summaries before considering a new tool.
- [ ] Verify redaction at the output paths you touch. Non-Secret resources can
  contain TLS keys, connector credentials or cloud-init; neither a new summarizer
  nor the generic fallback is automatically safe.
- [ ] If a new tool is justified, follow [MCP docs](mcp.md) and the catalog/test
  requirements in [the repository instructions](../CLAUDE.md).

### Topology and relationships

- [ ] Start in `pkg/topology/`: `builder.go`, `relationships.go`, `pseudokinds.go`
  and `types.go`. Use actual references, documented labels/selectors or controller
  links—not matching names, historical references treated as live usage, or
  configuration treated as observed traffic. Preserve group/namespace identity
  and handle missing/unreadable related objects honestly.
- [ ] Choose semantic edges: `EdgeManages` (ownership), `EdgeExposes` (exposure),
  `EdgeConfigures` (configuration), `EdgeUses` (scaling/usage), `EdgeProtects`
  (protection). These affect Related Resources grouping, not just appearance.
- [ ] Reuse lists between node/edge construction and handle cache errors. Check
  the generic CRD pass and `kindsHandledOutsideGenericCRDPass` for duplicates and
  collisions. Keep pseudo-kind `KindForGVK`, node IDs and `buildNodeID` /
  `normalizeKind` in `relationships.go` consistent; use unique node-ID prefixes
  for colliding kinds and test navigation in both directions.
- [ ] For cluster-scoped kinds, check `ClusterScopedKinds` in
  `pkg/topology/cluster_scoped_kinds.go` and verify REST/MCP authorization for the
  exact group/resource, including pseudo-kinds shared by multiple APIs.
- [ ] For new node kinds, check frontend wiring as applicable:

  - `packages/k8s-ui/src/types/core.ts` (`CoreNodeKind`, `displayKind`) and
    `web/src/App.tsx` (visibility defaults).
  - `packages/k8s-ui/src/utils/`: `resource-icons.ts`, `badge-colors.ts`,
    `resource-hierarchy.ts` (application grouping only where meaningful).
  - `packages/k8s-ui/src/components/topology/`: `TopologyFilterSidebar.tsx`,
    `K8sResourceNode.tsx`, `layout.ts`, `topology.css`.

## 3. Workspace integrations

A workspace is a set of screens for several related kinds (CloudNativePG at
`/cnpg`, Karpenter at `/capacity`). Build one only when the operator question
spans objects: fleet health, a chain such as Backup → ObjectStore → Cluster, or
actions that need facts from more than one object. With fewer kinds, or no
cross-object question, a renderer plus Issues plus the detail slots is enough.
The batch kinds, for example, use only the detail seams.

The pieces below are shared and should be imported, not copied. Read
[DESIGN.md](../DESIGN.md#unknown-partial-and-denied-values) for the rules on
unknown, partial and denied values first: every piece here exists to keep them.

- [ ] **Placement.** Under Resources as a category workspace
  (`ResourcesSidebar` `categoryWorkspaces`, keyed by the category name from
  `api-resources.ts`), or a top-level page when the subject is cluster-wide.
  Say whether the namespace filter applies, and why.
- [ ] **Routes and navigation.** Use `/x`, `/x/<screen>` and
  `/x/<plural>/<ns|_>/<name>?ctx=`.
  - The in-drawer trail is `?drawer=`, encoded by `web/src/utils/drawer-trail.ts`.
  - Back labels come from `currentPageLabel` and subject-filtered Issues links
    from `issuesPathForSubject` (both in `web/src/utils/page-links.ts`).
  - Pin `ctx` on a detail page. After a context switch, say the object is not in
    this context; never open a same-named object from another cluster.
- [ ] **One aggregate endpoint.**
  - List each kind with `readWorkspaceKind` (dynamic kinds) or
    `typedKindScope` (typed kinds such as Pods), both in
    `internal/server/kind_access.go`. Their answer's `Coverage()` is a
    `internal/integration.KindCoverage`: `full|partial|denied|notInstalled|syncing|uncached|error`,
    with denied and uncached namespaces named only when the caller supplied the
    namespace list.
  - Radar's own cache scope comes from `integration.NamespacesWithinCache`
    (`internal/integration/cache_scope.go`).
  - A per-object read with several sources reports a `ReadSource`
    `{state, grant, reason}` per source (`internal/integration/reads.go`), with the missing
    permission as a `Grant` (`internal/auth/grant.go`), never a sentence.
  - Fan out over namespaces with `integration.FanOut` (`internal/integration/fanout.go`), behind a cap.
  - Prometheus series matched to an object by name rather than identity carry a
    `SeriesIsolation` (`internal/prometheus/series_scope.go`).
  - A GitOps or Helm manager comes from `topology.ManagedByFromMeta`, never from
    labels read on the client.
- [ ] **Version skew.** Give the workspace's endpoints one `FeatureCapabilities`
  flag and a `radarFeatures.ts` entry with `flagShippedWithEndpoint: true`.
  - Gate every hook with `useRadarFeature`, including mutations, streams and
    downloads. Never add `retry` to a mutation.
  - When the Radar is too old, hide the sidebar destinations and fall back to
    the standard detail views.
- [ ] **Domain rules.** Put interpretation reused by checks, findings and actions in a pure integration package (CNPG uses `pkg/cnpg`), independent of HTTP and caller permissions. Keep matching frontend derivations together; share fixture cases for rules represented in both languages. Put request-independent orchestration in an integration service (`internal/cnpg` is the current example), with caller-scoped observations/clients supplied by server adapters. Share typed service reads between endpoints and reports rather than invoking handlers through a response recorder. Keep HTTP requests, routing and process-wide client resolution outside the service.
- [ ] **Findings.** The Issues engine owns findings from cached Kubernetes objects. Cross-resource findings retain their inventory operations in `Issue.RequiredReads`; hosts supply `CanReadEvidence` for both composition and cached related-issue projections. A live proxy or Prometheus measurement stays in the workspace as a `WorkspaceProblem` with `source: 'measurement'`, `measuredBy` and, when matched by name only, `unverifiedMatch`. Add no new severity ladder, and title reasons the Issues page already titles with `issueReasonTitle`. Carry semantic reasons/states through presentation; never branch on generated IDs or display text.
- [ ] **Screens.**
  - k8s-ui `components/facts`: `Fact`, `FactGrid`, `FactRow`, `FactValue`,
    `FactSource`, `CertaintyGlyph` and `ManagedByText`. These are for any
    surface that shows observed values, single-kind renderers included.
  - k8s-ui `components/problems`: `WorkspaceProblem`, and
    `ProblemCallout`/`ProblemList`/`ProblemMeta` with the workspace's
    `rootKind`, plus `OpenIssueContext`.
  - Also from k8s-ui: `SectionHeading`, `FoldSection` and `FoldSummary`
    (`ui/FoldSection`), `ui/RefLink`, `toneTextClass`/`worseTone` in
    `ui/status-tone`, and `formatGrant`.
  - App: `web/src/components/workspace`:
    - layout: `ScreenBody`, `ScreenEmptyState`, `Notice`
    - controls: `Segments`, `FilterChips`
    - tables: `SectionTable` and its table classes
    - text: `RefreshFailedNotice` and `GrantText`
  - Buttons are `.btn-brand` and `.btn-secondary`.
- [ ] **Detail page.** Through `WorkloadView`:
  - `renderSummary`: a composed Overview; the resource's renderer moves to
    "Spec & status".
  - `extraTabs`
  - `renderHeaderActions`
  - Keep an integration's composition in its own host adapter (`web/src/components/cnpg/host.tsx`) and register it in `web/src/integrations/resourceHosts.tsx`, so generic views use one interface for routing, summary, header actions and logs. Fetching stays in the app; shared presentation receives facts and callbacks.
- [ ] **Actions.**
  - Shared contracts/guards (`internal/integration/actions.go`); caller adaptation and HTTP decoding (`internal/server/actions.go`):
    - A capabilities endpoint answers each action as an `ActionCapability`
      `{allowed, reason, reasonCode?, permission, grant}`, built with `grantPermission` and
      `integration.CapabilityVerdict`.
    - The POST body is an `ActionRequest` `{reviewedContext, uid, facts, params}`
      read with `decodeActionRequest`.
    - Bind the facts the user reviewed. Refuse with 409 `changed`
      (`integration.ChangedAction`) or `context_changed`, and use `integration.PartialAction` when a
      multi-step write stops part-way.
    - Writes are impersonated, version-bound (`integration.MergePatchAtVersion`) and never
      retried.
  - Client:
    - `web/src/api/actions.ts`: `actionErrorCode`, widened with the integration's
      own codes; `actionOutcomeLocked`, `actionCompleted` and `capabilityReason`.
    - `ActionConfirmDialog` and the GitOps write guard (`useGitOpsWriteGuard`).
  - An accepted POST is not a completed action: follow the outcome in status.
- [ ] **Docs and fixtures.** Add a `docs/<x>.md` listing which source each value
  comes from and how it reads when unknown, a `scripts/<x>-demo.sh` with its
  README, and a CLAUDE.md row.

### Host ownership and future extension points

`web/src/integrations/resourceHosts.tsx` is the app-owned composition root for
integration adapters. Generic resource views consume its typed routing, detail,
log, renderer and kind-list operations. CNPG's adapter remains in
`web/src/components/cnpg/host.tsx`; existing Karpenter, batch/admission, Ray and
other renderer wrappers register in the same root. This interface is private to
the app; it does not change the public radar-app embedding API.

Add an adapter and registration when extending these existing slots. Reuse
`RendererOverrides` and the detail props from k8s-ui. Resources are keyed by
exact API group and plural; an unresolved group cannot select a colliding CRD.
Declare exclusive ownership of summary, destination and logs. Competing
renderer, kind-list and exclusive-slot owners are rejected. Header actions and
extra tabs compose in order, with duplicate tab IDs rejected. Data-bearing
contributions are mounted components whose hooks remain integration-owned.

Workspace metadata lives in `workspaceRoutes.ts`, while `workspaceScreens.tsx`
registers screens separately so they can use `WorkloadView` without a circular
import. Register metadata and a screen for another workspace; retain its parser,
placement, namespace policy and context-switch policy in its own adapter. The
[architecture plan](CNPG_ARCHITECTURE_PLAN.md) records the extraction rationale.

When adding another integration, check these specific places before extending
generic hosts:

| Extension | Current location | Rule to preserve |
|---|---|---|
| Detail summary, header actions, Diagnose, logs and renderer wrappers | `web/src/integrations/resourceHosts.tsx` and integration host adapters | Reuse the existing detail slots and `RendererOverrides`. Mount integration-owned components for their hooks. Match exact group and Kind; keep version/spec-shape dispatch where it already lives. |
| Drawer expansion and navigation trail | `resourceHosts.tsx` destination/drawer contributions | Keep context, tab/query state, history and API-group collisions. |
| A kind list replaced by a workspace view | `resourceHosts.tsx` kind-list contributions, consumed by `ResourcesView.tsx` | One ownership decision must control both fetching and rendering, including capability loading and unsupported Radar fallback. |
| Workspace routes, labels and context-switch policy | `web/src/integrations/workspaceRoutes.ts` and `workspaceScreens.tsx`; integration route helpers | CNPG and Capacity share registration metadata. Their route parsing, placement and namespace scope remain integration-owned. |
| Category-workspace counts and coverage | `ResourcesView.tsx`'s `useCNPGSidebarWorkspace` and `sidebarCategoryWorkspaces` | Generalize the data-provider composition when another category workspace needs it. Reuse the current sidebar props and retain partial/denied coverage. |
| Event-driven workspace invalidation | `App.tsx`'s CNPG pending flag and batched invalidation | Generalize change-to-query-key contributions when another workspace needs them; reuse the existing batches and connection lifetime. |

A resource can have contributions from multiple integrations, such as batch
execution and Kueue admission on a Job. Compose additive slots explicitly; give
summary, renderer, logs, destination and kind-list takeover an explicit owner.
Registration order must not silently pick between conflicting owners. This is
an internal app interface, not a new public radar-app plugin API.

The sidebar-provider and event-invalidation shapes above are deliberately left
open until another matching consumer appears. The listed locations are the
places to revisit, rather than another set of integration-specific branches to
copy into generic views.

These other pieces are not shared yet, because they have one integration
consumer and the second should shape them:
- the operation tracker
- the fixed-path `pods/proxy` reader
- the merged log stream
- the report bundle
- operator diagnosis

Read CloudNativePG's versions (`web/src/components/cnpg/`,
`internal/cnpg/`), and extract the demonstrated common part when a second integration needs it. The merged log engine already lives in `internal/server/workload_logs.go`; reuse its protocol instead of duplicating it.

## 4. Before submitting

- [ ] Verify status against the controller's documented API: desired versus
  observed, unknown versus false, intentional pause/stop versus failure. Consider
  observed generation and timestamps; missing status is not proof of health.
  UI status, Go Issues and MCP summaries are separate implementations: check the
  same important states across the surfaces you change.
- [ ] Add fixtures for supported API shapes and meaningful states: healthy, failing,
  intentionally inactive, stale/missing status, collisions and unavailable related
  resources. Test only relevant combinations, not a speculative matrix.
- [ ] Run `make tsc`, `make test` and `make build` for integration code changes, plus
  `npm test --prefix packages/k8s-ui` and `npm test --prefix web` for frontend tests.
  The full build includes frontend embedding. Existing guards include
  `internal/k8s/{dynamic_cache_fallback,chart_rbac_coverage,capabilities_alignment}_test.go`
  and resource `curated-column-ownership.test.ts` / `ResourceRendererDispatch.test.tsx`.
- [ ] Validate changed views against a real controller, including restricted access
  where relevant. Record versions, screenshots and what was actually exercised;
  distinguish real status from synthetic fixtures. Don't induce destructive
  failures in a shared or production cluster for a test.
- [ ] For the surfaces changed, confirm intended table columns/cells, drawer,
  topology icons and edges. For collisions, verify both core and custom resources'
  renderers, status and available actions; check existing resource types for regressions.
- [ ] Update [integrations.md](integrations.md) with the surfaces actually supported
  and [README.md](../README.md) as appropriate. No need to change `CLAUDE.md` unless
  introducing an architectural pattern or invariant.

- [ ] State remaining scope in the PR. Each slice should work and include its tests;
  you don't need every CRD in an ecosystem before the first slice can land.

**Maintainer docs publishing:** `radar-docs` splits `integrations.md` at `##`
headings. A new heading needs matching `INTEGRATION_META` and `docs.json` navigation
in that internal repository before sync. On rename, retain the published slug;
unmatched headings fail sync and the orphan sweep removes old generated pages.
External contributors only need to flag the new/renamed section for maintainers.
