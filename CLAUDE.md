# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

Radar is a modern Kubernetes visibility tool — local-first, no account required, no cloud dependency, fast. It provides topology visualization, event timeline, service traffic maps, resource browsing, Helm management, and cluster audit (best-practices scanning). Runs as a kubectl plugin (`kubectl-radar`) or standalone binary and opens a web UI in the browser. Open source, free forever. Built by Skyhook.

## Code comments

- Default to writing no comments. Only add one when the WHY is non-obvious — a hidden constraint, a subtle invariant, a workaround for a specific bug, or behavior that would surprise a reader.
- Don't explain WHAT the code does — well-named identifiers already do that.
- **Don't reference tickets, PRs, bug numbers, or diff history** in code comments (e.g. "fixes SKY-123", "Bugbot caught this on PR #584", "used to read X, now…"). Those belong in the PR description and rot as the codebase evolves. The WHY of the change should stand on its own.
- This applies to comments written by any tool (Cursor, Bugbot, Copilot) as well as humans — strip ticket/PR references before merging.

## Reference Docs — MUST READ before making changes

Not everything is in this file. The following files contain critical details that are **not duplicated here**. You MUST read them when working in the relevant area — do not guess or rely on memory.

| When you are... | Read this file FIRST |
|-----------------|---------------------|
| Adding or modifying **HTTP endpoints** | `internal/server/server.go` — all routes are defined here |
| Adding or modifying **CLI flags** | `cmd/explorer/main.go` — flag definitions and defaults |
| Adding a **new CRD integration** (renderer, topology, discovery) | [docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md) — full checklist with collision gotchas |
| Working on **resource renderers** | `packages/k8s-ui/src/components/resources/renderers/` — all existing renderers live here |
| Understanding **cluster connection behavior** | [docs/configuration.md](docs/configuration.md) — kubeconfig precedence, multi-context, in-cluster |
| Working on **MCP tools or AI context** | [docs/mcp.md](docs/mcp.md) + `internal/mcp/tools.go` — tool definitions and design rationale |
| Writing or modifying **frontend UI / styling** | [DESIGN.md](DESIGN.md) — theme tokens, do's/don'ts, component patterns |
| Touching anything library consumers import | `web/package.json` + `web/src/index.ts` — `web/` IS the `@skyhook-io/radar-app` npm package. Public surface: `RadarApp`, runtime-config setters (`setApiBase` etc.), `NavCustomization`. Breaking it breaks all downstream consumers. |
| Adding or changing **api/fetch call sites** | `web/src/api/config.ts` — all fetches go through `getApiBase()`, `apiUrl()`, `getWsUrl()`, `getAuthHeaders()`, `getCredentialsMode()`. New fetch sites must use these helpers so library consumers (Radar Hub) can override per-cluster. |
| Embedding Radar inside another app | `web/src/RadarApp.tsx` + `web/src/context/NavCustomization.tsx` — `apiBase`, `basename`, `router`, `navSlots` props. Changes to this API surface are breaking. |

## Library distribution

In addition to the standalone binary, Radar's frontend is published as **`@skyhook-io/radar-app`** (source-only npm package, same model as `@skyhook-io/k8s-ui`). The `web/` directory IS the package: `web/package.json` carries the npm metadata, `web/src/index.ts` is the library entry, and Radar's own binary entry (`web/src/main.tsx`) consumes the same source.

Publish with tag `radar-app-v<semver>` — see `.github/workflows/publish-radar-app.yml`.

Consumers get:
- `<RadarApp apiBase basename router navSlots queryClient />` — the whole app as one component
- Runtime config setters for cross-cutting behavior (`setApiBase`, `setBasename`, `setAuthHeadersProvider`, `setCredentialsMode`) for non-React code paths
- `NavCustomization` type for nav slot injection

Known consumers: Radar Hub (`skyhook-dev/radar-hub-web`).

**Backwards-compat rule:** adding props is fine; removing or renaming `apiBase` / `basename` / `navSlots` fields is breaking. Bump major version.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         User's Machine                          │
│                                                                 │
│   ┌─────────────────┐                   ┌───────────────────┐  │
│   │    Browser      │◄── HTTP/SSE/WS ──►│  Radar Binary     │  │
│   │  (React + UI)   │                   │  (Go + Embedded)  │  │
│   └─────────────────┘                   └───────┬───────────┘  │
│                                                  │              │
│   ┌─────────────────┐                            │              │
│   │   AI Tools      │◄──── MCP (HTTP) ───────────┤              │
│   │  (Claude, etc.) │                            │              │
│   └─────────────────┘                            │              │
│                                                  │              │
└──────────────────────────────────────────────────│──────────────┘
                                                   │
                                         ┌─────────┴─────────┐
                                         │  kubeconfig       │
                                         │  (~/.kube/config) │
                                         └─────────┬─────────┘
                                                   │
                                         ┌─────────┴─────────┐
                                         │  Kubernetes API   │
                                         │  (direct access)  │
                                         └───────────────────┘
```

## Project Structure

See [docs/STRUCTURE.md](docs/STRUCTURE.md) for the full directory map + tech-stack snapshot. The load-bearing concerns (caching, topology, MCP, error handling, renderers) have their own sections below — the directory tree exists to orient, not to drive behavior.

## Development Commands

### CRITICAL: Frontend Embedding Pipeline

The Go binary serves the frontend via `go:embed` from `internal/static/dist/`, NOT from `web/dist/`. The build pipeline is:

```
web/src → (npm run build) → web/dist → (make embed) → internal/static/dist → (go build) → binary
```

**ALWAYS use `make build` to build the full application.** Running `cd web && npm run build` followed by `go build` will NOT update the served frontend — the embed step (`make embed`) that copies `web/dist/*` to `internal/static/dist/` will be skipped, and the binary will serve stale frontend assets.

```bash
# CORRECT: Full build (frontend + embed + backend)
make build

# CORRECT: Quick rebuild after frontend-only changes
make restart-fe    # frontend + embed + restart server

# CORRECT: Full rebuild + restart
make restart       # frontend + embed + backend + restart server

# WRONG: This skips the embed step!
cd web && npm run build && cd .. && go build -o radar ./cmd/explorer
```

### Build / test / run

`make help` lists every target — read the Makefile when uncertain. The day-to-day set: `make build` (frontend + embed + binary), `make restart` / `make restart-fe`, `make watch-backend` / `make watch-frontend` (Air :9280 + Vite :9273), `make test`, `make tsc`. `go run ./cmd/explorer --dev` serves the frontend from `web/dist` without the embed step.

### Visual Testing
```bash
./scripts/visual-test-start.sh          # Build + launch on random port (9300-9399)
./scripts/visual-test-start.sh --skip-build  # Relaunch without rebuilding
source .playwright-mcp/visual-test-state.env # Load $RADAR_URL, $SCREENSHOT_DIR, etc.
./scripts/visual-test-stop.sh           # Kill process, open screenshot folder
```
Use `/visual-test` command for the full workflow (cluster check, Playwright MCP, screenshots, report). Screenshots go under `.playwright-mcp/visual-test/`.

**GitOps demo cluster** (`scripts/gitops-demo.sh` + `make gitops-demo`): bootstraps a `kind` cluster pre-loaded with Argo CD + Flux + a curated set of fixtures (healthy + suspended + manual-sync + ApplicationSet → 3 children + Flux Kustomization with dependsOn chain + HelmRelease) for visual-testing GitOps UI changes against realistic state. Coverage matrix in `scripts/gitops-demo/README.md`. When evaluating GitOps UI changes, run `make gitops-demo` and `kubectl config use-context kind-radar-gitops-demo` before `./scripts/visual-test-start.sh` — otherwise you're testing against whatever cluster is in the current context (often a customer/EKS cluster lacking the variety needed). `make gitops-demo-drift` induces a live OutOfSync state on guestbook for testing drift rendering.

**Before calling a UI feature done — consider visual-test.** `make tsc` + `make test` check code; `/visual-test` checks the *screen*. Run it yourself for self-contained UI changes where you can predict what should render; ask the user when the change is broad or the right capture set isn't obvious; skip for pure refactors / Go-only / type-only / doc edits.

### Dev ports
**9280** Go backend · **9273** Vite (proxies `/api` → 9280).

## API Endpoints & CLI Flags

**You MUST read `internal/server/server.go` before adding or modifying any endpoint** — it is the single source of truth for all routes. CLI flags live in `cmd/explorer/main.go`. Key URL patterns:
- REST resources: `/api/resources/{kind}`, `/api/resources/{kind}/{ns}/{name}`, `/api/resources/apply` (POST)
- SSE streaming: `/api/events/stream`, `/api/traffic/flows/stream`
- WebSocket: `/api/pods/{ns}/{name}/exec`
- MCP: `/mcp` (Streamable HTTP — POST for JSON-RPC, GET for SSE)
- Helm: `/api/helm/releases/...`
- Workloads: `/api/workloads/{kind}/{ns}/{name}/...` (logs, restart, scale, rollback)
- GitOps controller actions: `/api/argo/applications/...` (sync, refresh, terminate, suspend, resume, rollback, selective-sync), `/api/flux/{kind}/...` (reconcile, suspend, resume, sync-with-source)
- Argo CD API integration: `PUT /api/integrations/argocd` (URL/token, probe-before-persist, token preserved across GET-redaction round-trips); `/api/argo/applications/{ns}/{name}/resource-diff` (Git-rendered desired vs live via argocd-server managed-resources; dual RBAC gate + structural Secret redaction; see docs/gitops.md)
- GitOps detail data: `/api/gitops/tree/{kind}/{ns}/{name}` (resource tree + ownership edges), `/api/gitops/insights/{kind}/{ns}/{name}` (curated diagnosis: summary + issues + drift + events + plan + history + capabilities)
- Nodes: `/api/nodes/{name}/...` (cordon, uncordon, drain, debug)
- Audit: `/api/audit`, `/api/audit/resource/{kind}/{ns}/{name}`, `/api/settings/audit` (GET/PUT)
- CAPI: `/api/capi/clusters/{ns}/{name}/kubeconfig` (GET), `/api/capi/clusters/{ns}/{name}/connect` (POST)
- RBAC reverse-lookup: `/api/rbac/subject/{kind}/{namespace}/{name}` (ServiceAccount) and `/api/rbac/subject/{kind}/{name}` (User/Group) return direct + group-inherited bindings + flattened effective rules. SA subjects also get a `usedByPods` list (Pods whose `spec.serviceAccountName` matches — closes the loop on the SA detail page). `/api/rbac/role/{kind}/{namespace}/{name}` (use `_` for ClusterRole's empty namespace) returns the inverse — bindings that reference the role + their subjects. `/api/rbac/namespace/{namespace}` returns RoleBindings in the namespace + ClusterRoleBindings with at least one SA subject in it + a ServiceAccount count (backs the NamespaceRenderer's RBAC section; group-only ClusterRoleBindings like `system:authenticated` grants are deliberately excluded — they'd appear in every namespace and would be noise). `/api/rbac/whoami?namespace=...` is a pass-through of `SelfSubjectRulesReview` for the current user. Backed by `pkg/rbac/` (pure index + 5s TTL memo); endpoints gate on `list rolebindings` AND `list clusterrolebindings` (403 when either is denied — silent partial views would mislead operators).

## Key Patterns

### K8s Caching
- Core informer logic lives in `pkg/k8score` — a shared package with no internal/ imports, designed for reuse
- `internal/k8s/cache.go` wraps it as a singleton and wires Radar-specific callbacks (timeline recording, noisy filtering, diff computation)
- Uses SharedInformers for watch-based caching of typed resources
- Two-phase sync: critical informers block startup, deferred informers (events, secrets, configmaps, etc.) sync in background
- Dynamic caching for CRDs and custom resource types via API discovery
- Memory-efficient with field stripping (removes managed fields, last-applied annotations)
- Change notifications via channel for real-time SSE updates
- Application-specific behavior injected via `CacheConfig` callbacks: `OnChange`, `OnEventChange`, `OnReceived`, `OnDrop`, `ComputeDiff`, `IsNoisyResource`
- **Per-kind scope decisions** via `CacheConfig.ResourceScopes` — each kind can independently be cluster-wide, namespaced, or disabled based on what the SA can list. `pkg/k8score/cache.go`'s `pickFactory` routes each informer to the matching factory; cluster-only kinds (Nodes, Namespaces, PVs, StorageClasses, IngressClasses) always use the cluster-wide factory regardless of caller intent
- **Probe-based RBAC gating** (`internal/k8s/capabilities.go`): at startup, Radar runs a real list call against each typed kind (using the SA / kubeconfig identity) to decide if it goes cluster-wide, namespace-scoped, or off. List probes are authoritative because they ARE the operation the informer will perform — SSAR is one indirection too many and can disagree with reality on clusters using webhook authorizers (e.g. GKE IAM). When cluster-wide list is denied for a kind, the probe falls back across candidate namespaces — `{contextNs, flagNs}` plus, when listable, the user's accessible-namespaces set — capped to bound fanout on large clusters; see `buildScopeCandidates`
- **In-app namespace switcher = per-user view filter**: the header's `NamespaceSwitcher` POSTs to `/api/cluster/namespace`, which the server stores as a per-user preference in `Server.nsPreferences` (key: `username\x00contextName`). It does NOT mutate the shared cache. The pick is intersected with the user's RBAC-allowed namespaces on every read in `parseNamespacesForUser` (REST) and `filterNamespacesForUser` (MCP). For the no-auth/local case, the pick persists across restarts via `settings.ActiveNamespaces` and is loaded lazily on first request. On context switch, all users' picks are dropped — they reference the previous cluster's namespaces
- **Per-user RBAC filtering** (auth enabled): namespaced reads filter via `parseNamespacesForUser` → `getUserNamespaces` → `auth.DiscoverNamespaces` (SubjectAccessReview-based, "list pods" / "list deployments" sentinel). Cluster-scoped reads gated per-kind via `Server.canRead` / MCP `canReadClusterScopedKind` — both run a SAR for the exact (group, resource, verb) and cache on `UserPermissions.canI`. Cluster-wide pod visibility does NOT imply cluster-scoped reads; this is the load-bearing security distinction. Static cluster-only kinds map via `k8s.ClusterOnlyKindGVR`; dynamic CRDs use discovery's `GetResourceWithGroup`. MCP write tools / exec / logs impersonate via `DynamicClientFromContext`, so the apiserver enforces full RBAC there directly
- Supports: Pods, Services, Deployments, DaemonSets, StatefulSets, ReplicaSets, Ingresses, IngressClasses, EndpointSlices, ConfigMaps, Secrets, Events, Jobs, CronJobs, HorizontalPodAutoscalers, PersistentVolumeClaims, PersistentVolumes, StorageClasses, PodDisruptionBudgets, ServiceAccounts, Nodes, Namespaces

### SSE + WebSocket exec

SSE: `internal/server/sse.go` — central `SSEBroadcaster` with per-client namespace filter + view-mode, heartbeats, topology cache for relationship lookups, emits topology / K8s-event / resource-update frames.

WebSocket pod exec: `internal/server/exec.go` — xterm.js terminal, container/shell selection, resize via size queue, full TTY/stdin/stdout/stderr.

### Topology Builder
- Constructs directed graph from K8s resources via owner references + selector matching
- Two view modes: `traffic` (network flow: Ingress/Gateway → HTTPRoute → Service → Pod) and `resources` (hierarchy: Deployment → ReplicaSet → Pod)
- **Edge type semantics** (drive UI grouping): `EdgeManages` (owner), `EdgeUses` (HPA/VPA/KEDA), `EdgeProtects` (PDB/NetworkPolicy), `EdgeConfigures` (ConfigMap/Secret/DestinationRule), `EdgeExposes` (Service/Ingress/Gateway). Choose the right type — don't reuse.
- **CRD collision pattern**: When a CRD kind collides with core K8s (e.g., Knative Service, CAPI Cluster), use `GetGVRWithGroup("Kind", "group")` and prefix node IDs (`knativeservice/`, `capicluster/`). Frontend disambiguates via `data?.apiVersion?.includes('group.name')`.
- Supported integrations: Core K8s, Gateway API, Istio, Knative, Traefik, Contour, CAPI, Karpenter, KEDA, cert-manager, GitOps (Argo/Flux). See `docs/integrations.md` for full list.
- GitOps nodes: Application (ArgoCD), Kustomization, HelmRelease, GitRepository (FluxCD)
  - `/api/gitops/tree/{kind}/{namespace}/{name}` — resource tree (managed resources + ownership edges)
  - `/api/gitops/insights/{kind}/{namespace}/{name}` — curated diagnosis (summary, issues, drift, events, plan, history, capabilities)
  - **Detail page structure**: 3 top-level tabs (Topology, Changes, Activity). Graph nodes for GitOps CRDs route to nested detail pages; ordinary K8s resources open the standard drawer.
  - **Operations**: Argo: Sync (with options dialog), Refresh, Terminate, Suspend/Resume, Rollback, Selective sync. Flux: Reconcile, Suspend/Resume, Reconcile-with-source. Sentinel errors (`ErrOperationInProgress`, `ErrResourceTerminating`) mapped via `errors.Is` at HTTP layer.
  - **Lifecycle (Terminating)**: `assertNotTerminating` pre-flight on all mutating operations. Frontend suppresses Sync/Health badges, disables action buttons, renders orange `[Terminating]` chip. Lifecycle Issue severity ramps by deletion age (info <5min, warning 5-30min, alert >30min). Cluster Audit `stuckTerminating` check uses same thresholds. Finalizer catalog (`pkg/gitops/insights/finalizers.go`) enriches lifecycle Issues with controller-health attribution.
  - **Nested navigation**: `classifyGitOpsKind` tags nodes with `data.gitopsTool` + `data.gitopsKind`. Portal nodes route to child detail pages; lineage breadcrumb (`?from=kind|ns|name`) enables back navigation.
  - **Severity vocabulary**: `critical` (0, red) → `alert` (1, orange) → `warning` (2, amber) → `info` (3, blue). Adding a new severity requires updating both Go `severityRank` and TS union in `gitops-insights.ts`.
  - **Single-cluster limitation**: Application↔resource edges only render when controller + workloads are in same cluster (ArgoCD hub-spoke deployments won't show connections).
  - **Per-resource drift**: computed from `kubectl.kubernetes.io/last-applied-configuration` annotation. SSA/Helm-installed resources lack this; SSA fallback tracked in [#601](https://github.com/skyhook-io/radar/issues/601).

### Timeline + resource relationships

Timeline (`pkg/timeline/`): in-memory or SQLite (`--timeline-storage`), default 10k-event ring, groupable by owner / app label / namespace. Resource relationships (`pkg/topology/relationships.go`): computed at query time — parent/children/deployment-grandparent/config/network/scalers/policies/storage — used for both detail views and topology edges.

### RBAC Visibility

`pkg/rbac/` is a pure package over typed `rbacv1` listers — no K8s API calls, no internal/ imports. `BuildIndex` produces `BindingsBySubject` + `BindingsByRole` maps; `EffectiveRules(subject)` flattens direct + implicit-group bindings (`system:authenticated`, `system:serviceaccounts`, `system:serviceaccounts:<ns>` — included only for ServiceAccount subjects) with provenance preserved. Flat rule output capped at `MaxFlatRules` (500); response sets `truncated: true`. 5s `rbac.Memoizer` absorbs the SA/Pod-detail fetch burst; `finalizePostContextSwitch` calls `Invalidate()` so a kubeconfig context switch doesn't serve the previous cluster's RBAC for up to 5s. No mutation invalidation today — read-only MVP.

Renderers (`ServiceAccountRenderer`, `RoleRenderer`, `RoleBindingRenderer`, `PodRenderer`, `WorkloadRenderer`, `NamespaceRenderer`) accept optional `rbacData` / `rbacRoleData` / `roleRules` props and render the reverse-lookup sections only when the host wires the fetch. Host wrappers in `web/src/components/resources/renderers/` use `useRBACSubject` / `useRBACRole` / `useRBACNamespace`. Library consumers (Radar Hub) that skip the fetch get the original sections; nothing breaks.

Pod **Permissions** is the differentiator — frames the SA's grant as blast radius. Workload detail ships the same surface framed at the workload level. Detection lives in `packages/k8s-ui/src/utils/rbac-blast-radius.ts` (`detectBlastRadius` + `rulePermissivenessScore` + `RBAC_BLAST_*` verb-set constants), extracted from the two renderers so they can't drift; 12 unit tests pin the triggers. Triggers: verb wildcards, cluster-admin bindings, `escalate`/`bind`/`impersonate`, cluster-wide `create pods`. Resource-only wildcards deliberately do NOT trigger — they fire on every authenticated SA. Theme-aware badge classes for RBAC display live in `packages/k8s-ui/src/utils/rbac-badges.ts` — hand-rolled `bg-*-500/20 text-*-400` strings wash out in light mode.

### AI Context Minification

`pkg/ai/context/` collapses K8s resources for LLM consumption. Three verbosity levels: `Summary` (MCP `list_resources` — typed structs), `Detail` (MCP `get_resource` — full spec/status, metadata noise stripped), `Compact` (aggressive — probes/volumes/security contexts removed). Secret safety is structural: never emits `.data`/`.stringData`, redacts env values matching API-key/token/password/base64 patterns. Events dedup on `(reason, normalized message)` with hash/UUID/IP placeholders; log filtering prioritizes error/warning lines, falls back to last 20.

### MCP Server

Stateless HTTP at `/mcp` (JSON-RPC). Read tools use `readOnlyHint`, write tools use `destructiveHint: true`. Respects cluster RBAC (impersonates via `DynamicClientFromContext` for write/exec/logs). Enabled by default; `--no-mcp` to disable. Tool catalogue + design rationale lives in `internal/mcp/tools.go` + [docs/mcp.md](docs/mcp.md) — don't restate it here. **When adding/removing a tool in `registerTools`, also update the user-facing setup dialog catalog `web/src/components/home/mcpToolCatalog.ts`** — `TestSetupDialogCoversAllTools` fails CI if the two diverge.

### Error Handling (Backend)

Handlers emit `{"error": "..."}` via `s.writeError(w, status, msg)`. Status conventions:
- **400** invalid input (missing params, bad YAML, unknown kind)
- **403** RBAC denied (nil lister or apiserver Forbidden)
- **404** resource doesn't exist — check via `apierrors.IsNotFound(err)`
- **409** operation already in progress (sync running, etc.)
- **503** cache/connection not ready — most cluster-touching handlers call `s.requireConnected(w)` at the top
- **500** unexpected — always `log.Printf("[module] Failed to <action> %s/%s: %v", ns, name, err)` before returning

Namespace filters accept both `?namespace=X` (single) and `?namespaces=X,Y` (preferred). Use `parseNamespaces()` to handle both.

### Error Handling (Frontend)

React Query mutations carry `meta: { errorMessage, successMessage }` — the global toast handler reads those. Server errors arrive as `{"error": "..."}` and surface unchanged. Don't add per-mutation `onError` toasts that would duplicate the meta-driven path.

### Shared UI Package (@skyhook-io/k8s-ui)

`packages/k8s-ui/` is the shared presentation layer — components are pure, data hooks live in `web/` and inject via props/callbacks. `web/src/components/resources/ResourcesView.tsx` is the canonical wrapper pattern. Linked via npm workspaces; Vite source-aliases `@skyhook-io/k8s-ui` → `../packages/k8s-ui/src` (no build step). Key exports: `ResourcesView`, `ResourceRendererDispatch`, `ResourceActionsBar`, `EditableYamlView`, renderers, resource-utils, `categorizeResources`, `getKindLabel`, `getKindPlural`.

**Badges + status tones.** `components/ui/Badge.tsx` owns the canonical color strings (literal class names — Tailwind's scanner can't see template literals). `utils/badge-colors.ts` re-exports + derives `SEVERITY_BADGE`, `KIND_BADGE_COLORS`, `HEALTH_BADGE_COLORS`, `HELM_STATUS_COLORS`. Table status badges use the `.status-*` CSS classes (`theme/components.css`).

The `HealthLevel` vocabulary — `healthy | degraded | alert | unhealthy | neutral | unknown` — flows through three coordinated layers: the type in `resource-utils.ts`, the `.status-*` CSS classes, and `components/ui/status-tone.tsx` (`StatusDot`, `mapHealthToTone`). Same six tones everywhere; no parallel vocabulary. For pill badges: `<span className={`badge ${healthColors[tone]}`}>` (used 56+ places). The `alert` tier (orange) is the intermediate between `degraded` (amber) and `unhealthy` (red) — needed for 3-step severity gradients (Problems, Audit findings, Cert expiry). Normalize raw API strings via `mapHealthToTone`.

Centralized `@layer components` classes in `theme/components.css` (Tailwind utilities can override): `.badge` / `.badge-sm`, `.btn-brand*`, `.card-inner` / `.card-inner-lg`, `.selection*`, `.dialog`.

### Frontend Styling Rules
**Use theme tokens — never hardcode colors.** See [DESIGN.md](DESIGN.md) for the full reference. Quick rules:
- Backgrounds: `bg-theme-base/surface/elevated/hover` — not `bg-white`, `bg-gray-*`, `bg-slate-*`
- Text: `text-theme-text-primary/secondary/tertiary` — not `text-gray-*`
- Borders: `border-theme-border` — not `border-gray-*`
- Buttons: `.btn-brand` — not hand-rolled `bg-blue-*`
- Badges: `<Badge severity="...">` or `<Badge kind="...">` — never hand-write color strings
- Shadows: `shadow-theme-sm/md/lg` — not raw Tailwind shadows

### Resource Renderers

**Adding or modifying a CRD integration? Read [docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md) first** — full checklist with collision gotchas. Renderers live in `packages/k8s-ui/src/components/resources/renderers/` (100+ components, 20+ integrations); register in that folder's `index.ts` plus `shared/ResourceRendererDispatch.tsx` (KNOWN_KINDS, render line, `getResourceStatus()`). Use `AlertBanner` / `ProblemAlerts` / `ConditionsSection` for problem surfaces and `LabelSelectorDisplay` for selectors — never hand-roll. Sections default to `defaultExpanded={true}` unless empty/low-priority.

**Kind collision rule:** When a CRD kind shadows core (Knative Service vs core Service) or two CRDs share a kind (CNPG Cluster vs CAPI Cluster), guard THREE places in `ResourceRendererDispatch.tsx`: the renderer line, `getResourceStatus()`, and action buttons (Port Forward, etc.). Use `data?.apiVersion?.includes('group.name')`. Missing any one produces dual-render bugs.

**Crossplane renderers are spec-shape detected, not kind-enumerated.** Managed Resources / Composites / Claims have unbounded plurals (one CRD per provider service), so dispatch uses `isManagedResource(data)` / `isComposite(data)` / `isClaim(data)` from `resource-utils-crossplane.ts` as fall-throughs. `Provider` / `ProviderConfig` / `Composition` / `CompositionRevision` / `XRD` / `Function` / `Configuration` are kind-dispatched. v1↔v2 path handling lives entirely in the resource-utils accessors (try `spec.crossplane.x` first, fall back to `spec.x`). `CompositeRenderer` accepts a `composedRefStatuses` Map injected by the host wrapper (`web/src/components/resources/CompositeRenderer.tsx`) that fans out React Query lookups for each `resourceRefs` entry — each composed-resource row gets a live status badge that way.

## Tech stack + server config

Tech stack snapshot lives in [docs/STRUCTURE.md](docs/STRUCTURE.md#tech-stack-snapshot). `go.mod` and `web/package.json` are the source of truth. Server middleware (Logger, Recoverer, 60s timeout, CORS for `localhost:*` / `127.0.0.1:*`) and the Vite dev proxy (`/api` → `:9280`, `ws: true`) are configured inline in `internal/server/server.go` and `web/vite.config.ts` — read those when changing them.
