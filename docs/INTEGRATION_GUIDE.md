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

## 3. Before submitting

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
