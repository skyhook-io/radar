# CRD Integration Guide

Radar already discovers custom resources and provides generic details, status,
MCP access and, for CRDs that declare them, Kubernetes printer columns. An
integration adds useful interpretation and relationships—not just another kind
in a list. This guide also applies to extension APIs such as OpenShift Routes;
don't assume every discoverable resource has a CRD object.

## Start with a useful slice

State the operator question you want to answer and the resource kinds involved.
Prefer existing tables, drawers, topology and Issues over a new dashboard. Not
every integration needs custom behavior on every surface below.

Search for existing support and helpers before adding code. KEDA is a useful
resource-presentation example; Knative demonstrates kind collisions; CloudNativePG
and Velero show controller-specific status and Issues. Borrow the relevant
pattern, not an entire integration's scope.

Ideally you already operate the tool and can test against a real controller. If
you need a testing partner, say so before starting. Agree on the supported API
versions and an independently useful first PR; presentation can ship before
richer relationships, but status fixes must cover the surfaces they affect.

## Identity and discovery

- Identify resources by **API group + kind/plural**, using discovery for served
  versions and namespace scope. Even an apparently unique kind may collide with
  an integration Radar doesn't curate. Examples: `Route`, `Cluster`, `Backup`,
  `Subscription` and `BGPPeer`.
- For supported-resource startup watching and fallback discovery, update
  `supportedCRDFallbacks` in [dynamic_cache.go](../internal/k8s/dynamic_cache.go).
  Entries carry group, versions, plural, kind and scope; `WarmupCommonCRDs` consumes
  this registry. Don't add a separate name-only warmup list. Fallback registration
  probes access; an entry is not proof that the API is installed or readable.
- Check the [Helm ClusterRole](../deploy/helm/radar/templates/clusterrole.yaml) and
  [values](../deploy/helm/radar/values.yaml) for the new API group. Follow existing
  per-group read-access toggles; don't rely on an opt-in wildcard or request write
  permissions for a read-only integration. The chart coverage test checks groups,
  not every resource/verb or rendered toggle combination.
- Keep namespace filtering, per-user authorization and context-switch behavior
  intact. Missing, not-yet-watched and forbidden are not interchangeable. If adding
  a typed `ResourcePermissions` field, follow `capabilities_alignment_test.go` in
  `internal/k8s/` and the frontend `OPTIONAL_RESOURCE_KINDS`; ordinary dynamic
  resources do not each need a new permission field.

## Choose the surfaces that add value

Paths below are relative to the repository root. Shared presentation lives in
`packages/k8s-ui`; host data fetching lives in `web`. Read [DESIGN.md](../DESIGN.md)
for UI work and follow the existing wrapper pattern rather than fetching inside
shared renderers.

| Surface | Where to start | What to consider |
| --- | --- | --- |
| Resource grouping | `packages/k8s-ui/src/utils/api-resources.ts` | Human-readable API-group label; reuse existing mappings. |
| Tables and status | `packages/k8s-ui/src/components/resources/ResourcesView.tsx`, `generic-status.ts`, `resource-utils-*.ts` | Add curated columns/status only when they improve the generic view. |
| Detail drawer | `packages/k8s-ui/src/components/resources/renderers/`, its `index.ts`, and `components/shared/ResourceRendererDispatch.tsx` under `packages/k8s-ui/src/` | Register known kinds, renderer and status dispatch. Reuse sections, properties, conditions, links and problem banners. |
| Topology / Related Resources | `pkg/topology/builder.go`, `relationships.go`, `pseudokinds.go`, `types.go` | Add evidence-backed relationships; see below before introducing node kinds. |
| Issues | `internal/issues/source_conditions.go`, integration-specific `source_*.go`, `pkg/conditions/` | Inspect generic detection before adding a detector or suppressing it. |
| MCP / AI context | `pkg/ai/context/summary_crd.go`, `detail.go`, `redact.go` | Check what existing resource tools return; enrich summaries rather than adding a tool per integration. |

### Resource presentation

**Claim only your exact API group.** Use an existing exact-group helper such as
`isApiGroup` (currently in `resource-utils-cnpg.ts`), not substring `includes`.
Check renderer dispatch, status, actions and table cells: an unrelated CRD with
the same plural must retain its generic behavior, not receive your renderer or
core-only actions. See `ResourceRendererDispatch.test.tsx` for collision fixtures.

In `ResourcesView.tsx`, check `GROUP_QUALIFIED_COLUMN_KEYS`,
`CURATED_COLUMN_GROUPS`, `getColumnsForKind` and plural normalization together.
`hasCuratedColumns` controls the choice between curated and printer columns:
**they are not merged**. Adding curated columns can remove useful vendor fields.
Use existing printer-column evaluation; don't implement another JSONPath parser.

### Status, Issues and MCP

Derive meaning from the controller's documented API, not the spelling of a phase.
Distinguish desired state from observations, unknown from false, and intentional
pause/stop or reconciliation from failure. Consider observed generation and
timestamps where the controller provides them; absence is not proof of health.

UI status, Go Issues detection and MCP summaries have separate implementations;
generic behavior is not identical across them. Test the same important states
across the surfaces you change. Generic Issues primarily inspect false
Ready-family conditions: nested conditions, negative-polarity conditions and
phase-only APIs may need integration-specific handling. Check detector ownership
and fallback suppression so a failure isn't duplicated—or an intentional state
reintroduced by the generic pass. Don't suppress a whole API group when only
some kinds are covered.

Preserve reasons and useful references in summaries, not raw configuration dumps.
Credentials can appear inside non-Secret resources (TLS keys, connector config,
cloud-init). Verify redaction at the output paths you touch; don't assume a new
curated summarizer or generic fallback is automatically safe. See [MCP docs](mcp.md).
A new MCP tool is usually unnecessary; if justified, follow the catalog and test
requirements in [the repository instructions](../CLAUDE.md).

### Topology and relationships

Use actual references, documented labels/selectors or controller-reported links.
Do not infer ownership from matching names, turn historical references into live
usage, or confuse configuration with observed traffic. Preserve group/namespace
identity and handle unreadable or missing related objects honestly.

Edge types affect Related Resources grouping: `EdgeManages` for ownership,
`EdgeExposes` for exposure, `EdgeConfigures` for configuration, `EdgeUses` for
scaling/usage relationships and `EdgeProtects` for protection. Follow a comparable
existing relationship rather than choosing an edge for its visual appearance.

Reuse lists between node and edge construction; handle cache errors. Check the
generic CRD pass and `kindsHandledOutsideGenericCRDPass` for duplicates and foreign
kind collisions. For pseudo-kinds, keep `KindForGVK`, node IDs and relationship
normalization consistent; test navigation in both directions.

When adding a topology node kind, check the frontend wiring as applicable:

- `packages/k8s-ui/src/types/core.ts` (`CoreNodeKind`, `displayKind`) and
  `web/src/App.tsx` (visibility defaults).
- `packages/k8s-ui/src/utils/`: `resource-icons.ts`, `badge-colors.ts`,
  `resource-hierarchy.ts` (application grouping only where meaningful).
- `packages/k8s-ui/src/components/topology/`: `TopologyFilterSidebar.tsx`,
  `K8sResourceNode.tsx`, `layout.ts`, `topology.css`.

## Verify and hand off

- Add fixtures for supported API shapes and meaningful states: healthy, failing,
  intentionally inactive, stale/missing status, collisions and unavailable related
  resources. Test only relevant combinations, not a speculative matrix.
- Run `make tsc`, `make test` and `make build` for integration code changes. The
  full build includes frontend embedding. Existing guards include
  `internal/k8s/{dynamic_cache_fallback,chart_rbac_coverage,capabilities_alignment}_test.go`
  and resource `curated-column-ownership.test.ts` / `ResourceRendererDispatch.test.tsx`.
- Validate changed views against a real controller, including restricted access
  where relevant. Record versions, screenshots and what was actually exercised;
  distinguish real status from synthetic fixtures. Don't induce destructive
  failures in a shared or production cluster for a test.
- Update [integrations.md](integrations.md) with the surfaces actually supported
  and [README.md](../README.md) as appropriate. No need to change `CLAUDE.md` unless
  introducing an architectural pattern or invariant.

Split larger contributions by useful outcome, not mechanically by backend versus
frontend. Each PR should work, include its tests and state remaining scope. A
drawer/status slice followed by relationship enrichment is often a good split;
don't require every CRD in an ecosystem before the first slice can land.

**Maintainer docs publishing:** `radar-docs` splits `integrations.md` at `##`
headings. A new heading needs matching `INTEGRATION_META` and `docs.json` navigation
in that internal repository before sync. On rename, retain the published slug;
unmatched headings fail sync and the orphan sweep removes old generated pages.
External contributors only need to flag the new/renamed section for maintainers.
