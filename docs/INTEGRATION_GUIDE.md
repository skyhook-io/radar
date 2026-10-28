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
- [ ] If the integration adds actions, list each HTTP route and MCP tool action in
  `deploy/helm/radar/files/integration-write-baseline.yaml` under a row with a
  grant, existing, or withhold decision for every resource and verb it writes,
  including indirect targets. Tests fail on an unlisted action route or MCP tool
  action, not on a missing resource or verb.
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

### Workspace and resource hosts

`web/src/integrations/resourceHosts.tsx` registers host renderer wrappers and
resource-specific summary, log and destination callbacks. Keep each adapter beside
its integration. Match exact group + resource identity; use the loaded object's
`apiVersion` for detail facts. Renderer overrides are composed once, with duplicate
ownership rejected. Generic detail pages and drawers call these seams rather than
importing integration screens.

`workspaceRoutes.ts` owns route metadata (title, navigation placement, namespace
view scope and context-switch query policy); `workspaceScreens.tsx` mounts the
integration screen. Capacity remains cluster-wide. Shared workspace notices, empty
states and table styles live in `web/src/components/workspace/`; certainty glyphs
live in k8s-ui's `components/facts/certainty.tsx`.

A host can supply k8s-ui `ResourcesView.renderKindView` to replace one kind's table
while retaining the sidebar and drawer. Return `null` or `false` for the ordinary table. A
replacement disables row/search/bulk shortcuts and column-preference loading/saving;
sidebar kind navigation stays available. The caller owns fetching and capability decisions.

### Issues

- [ ] Inspect generic detection in `internal/issues/source_conditions.go`, nearby
  integration-specific `source_*.go` and `pkg/conditions/` before adding a detector.
  Generic Issues primarily inspect false Ready-family conditions; nested,
  negative-polarity or phase-only status may need custom handling.
- [ ] Check detector ownership and fallback suppression: avoid duplicate failures
  or the generic pass reintroducing warnings for intentional states. Don't suppress
  a whole API group when only some kinds are covered.

### Finalizer controller recognition

Stuck-deletion detection covers already-watched, synchronized CRDs. Adding an
owner enriches those findings; it needs no per-kind detector or renderer.

- [ ] Add documented keys and controller names, labels and image paths in
  [`pkg/finalizers/catalog.go`](../pkg/finalizers/catalog.go). Disambiguate shared
  keys by API group/kind, and shared images or identities by role/image checks.
  Name guesses for uncataloged finalizers remain candidates, never owners.
- [ ] Review cleanup semantics before setting `AllowConditionalCleanup` (false
  by default). Leave it disabled for unreviewed cleanup, off-cluster controllers
  or infrastructure cleanup; record those limits on the entry.
- [ ] Add identity fixtures in
  [`pkg/finalizers/catalog_test.go`](../pkg/finalizers/catalog_test.go) and
  guidance/eligibility cases in
  [`internal/issues/finalizers_test.go`](../internal/issues/finalizers_test.go),
  including unrelated workloads and incomplete visibility.
- [ ] Keep reads and policy in `internal/issues/finalizers.go`, pod health in
  `pkg/health`. Catalog additions should not change the shared API/UI machinery.
- [ ] Verify a real stuck deletion, including intentional uninstall if enabling
  removal. Update the [MCP contract](mcp.md#read-tools) when supported behavior
  changes; distinguish controller evidence from synthetic fixtures.

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
  - Declare the context-switch policy in workspace metadata. CNPG pins `ctx` on
    details to avoid opening a same-named object in another cluster; Capacity
    follows the active cluster.
- [ ] **One aggregate endpoint.**
  - List dynamic kinds with `readWorkspaceKind` in `internal/server/kind_access.go`.
    Its `Coverage()` returns `internal/integration.KindCoverage`:
    `full|partial|denied|notInstalled|syncing|uncached|error`, with denied and
    uncached namespaces named only when the caller supplied the namespace list.
  - Assess a typed kind's scope with the cache's `AssessKindRead`
    (`pkg/k8score/read_scope.go`), which MCP, Capacity and Vitals use too. It splits
    the namespaces asked for into read, loading, failed and not collected, so a
    namespace whose own informer has synced is readable while another still
    loads. `Complete` means every requested namespace is readable now, and
    `TargetReadiness()` is the state of the requested namespaces alone.
    CNPG's caller adapter is `internal/server/cnpg_read_scope.go`. Namespace
    selection utilities live in `internal/readscope`; scope alone does not
    establish initial readiness.
  - After a per-namespace check, read through something that serves exactly
    those namespaces. The listers of ConfigMaps, Secrets, Events, PVCs,
    PersistentVolumes, StorageClasses, PodDisruptionBudgets and
    NetworkPolicies are nil until every namespace of the kind has loaded, so
    read them with `ReadableObjects`/`ReadableObject`. Every other kind's
    lister exists while the kind loads and serves a loaded namespace's objects
    from that namespace's own store: a lookup inside a namespace the
    assessment reports read is complete, a list across namespaces is not.
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
  - Gate new contracts while retaining already-shipped reads whose producer
    contract is unchanged. Hide unsupported new destinations or fall back to
    standard detail views.
  - Once a flag has shipped, its responses only grow. A new route or action,
    a newly required parameter, or a newly bound fact on an existing action
    needs a new flag: an older Radar answers those with 404 or 400, not with
    "needs a newer Radar".
  - A record Radar writes onto a cluster object (CNPG's restore-validation
    note) carries a format version, and readers skip a version they do not
    know: Radars of other versions read the same object.
  - An action error `code` tells the client the write was refused before it
    took effect. Never put a code on a failure that may follow a write; use
    `outcome_unknown`, or `partial` with what completed.
- [ ] **Guided multi-resource setup.** Keep integration-specific target models and workflow state in the app, and reuse the shared strict-create/YAML review. Put preflight and reviewed conditional writes in the integration service, with thin HTTP adapters and a new feature flag when the endpoint/verb is new. Each independent write needs its own review and observed follow-through; saving declarations is not end-to-end success. Do not grow a universal provisioning registry around one integration. A future second concrete setup flow should extract only the seam it actually shares.
- [ ] **Domain rules.** Put interpretation reused by checks, findings and actions in a pure integration package (CNPG uses `pkg/cnpg`), independent of HTTP and caller permissions. Keep matching frontend derivations together; share fixture cases for rules represented in both languages. Put request-independent orchestration in an integration service (`internal/cnpg` is the current example), with caller-scoped observations/clients supplied by server adapters. Share typed service reads between endpoints and reports rather than invoking handlers through a response recorder. Keep HTTP requests, routing and process-wide client resolution outside the service.
- [ ] **Findings.** The Issues engine owns findings from cached Kubernetes objects. Cross-resource findings retain their inventory operations in `Issue.RequiredReads`; hosts supply `CanReadEvidence` for both composition and cached related-issue projections. A live proxy or Prometheus measurement stays in the workspace with `source: 'measurement'` and `measuredBy`; identity limitations such as CNPG's `unverifiedMatch` stay in its domain model. Map domain severity to a non-healthy `HealthLevel` when presenting a `WorkspaceProblem`. Title reasons the Issues page already titles with `issueReasonTitle`. Carry semantic reasons/states through presentation; never branch on generated IDs or display text.
- [ ] **Screens.**
  - k8s-ui `components/facts`: `Fact`, `FactGrid`, `FactRow`, `FactValue`,
    `FactSource`, `CertaintyGlyph` and `ManagedByText`. These are for any
    surface that shows observed values, single-kind renderers included.
  - k8s-ui `components/problems`: `WorkspaceProblem`, and
    `WorkspaceProblemCallout`/`ProblemList`/`ProblemMeta` with the workspace's
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
    - `useReviewedActionMutation` (`web/src/api/reviewedActionMutation.ts`) keeps
      in-flight and uncertain results through dialog remounts and connection
      resets; pass its retained error to the dialog with `actionDialogResult`.
      Writes that invalidate each other's review share a target key. The host
      still owns follow-through and notification.
    - `ActionConfirmDialog` and the GitOps write guard (`useGitOpsWriteGuard`).
      Multi-step setup can supply `onBack`/`backLabel` without giving Cancel a
      second meaning. For YAML creation, `CreateResourceDialog.onBack(yaml)`
      returns the edited draft to the parent; the parent retains unrepresented
      fields and owns any explicit replacement. `onCreated(result, submittedYaml)`
      reports the actual write so follow-through never assumes the earlier form
      still describes edited YAML. Keep domain forms and step state in the
      integration host; these presentation contracts contain no integration logic.
  - An accepted POST is not a completed action: follow the outcome in status.
- [ ] **Docs and fixtures.** Add a `docs/<x>.md` listing which source each value
  comes from and how it reads when unknown, a `scripts/<x>-demo.sh` with its
  README, and a CLAUDE.md row.

### Future extension points

Use the [resource and workspace hosts](#workspace-and-resource-hosts) for
integration contributions. A kind-list replacement uses one `table / wait / view`
decision for both fetching and rendering, keyed by exact API group and plural.
Keep data hooks in mounted integration components and gate new contracts with
capabilities.

Two host seams need another concrete consumer before choosing a common shape:

| Seam | Location | Constraint |
|---|---|---|
| Category-workspace counts and coverage | `ResourcesView.tsx`'s `useCNPGSidebarWorkspace` and `sidebarCategoryWorkspaces` | Reuse the sidebar props and preserve partial/denied coverage; mount data hooks in integration-owned components. |
| Event-driven workspace invalidation | `App.tsx`'s CNPG pending flag and batched invalidation | Contribute query keys to the existing batches and connection lifetime. |

Operation tracking, fixed-path proxy reads, integration-specific log streaming,
report bundles and operator diagnosis remain integration-owned. Read CNPG's
implementations in `web/src/components/cnpg/` and `internal/cnpg/` when a second
integration needs the same behavior. Reuse `internal/podlogs` for bounded log
collection and replay; source selection and browser stream framing retain their
own owners.

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

Workspace presentation is shared in k8s-ui: `FactValue`/`FactSource` retain unknown values and their provenance; `WorkspaceProblemCallout` presents host-classified findings; `ActionConfirmDialog` presents the writes and acknowledgement a host asks the user to review. Hosts own fetching, authorization and action-result classification. These components do not establish that evidence is complete or that a write is safe to retry.
