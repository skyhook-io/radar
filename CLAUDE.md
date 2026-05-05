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

```
radar/
├── cmd/
│   ├── explorer/              # CLI entry point (main.go)
│   └── desktop/               # Desktop app entry point (Wails v2)
├── internal/
│   ├── app/                   # Application lifecycle management
│   ├── audit/                 # Radar-specific audit runner (cache → pkg/audit bridge)
│   ├── config/                # Configuration management
│   ├── errorlog/              # Error logging utilities
│   ├── helm/                  # Helm client integration
│   │   ├── client.go          # Helm SDK wrapper
│   │   ├── handlers.go        # HTTP handlers for Helm operations
│   │   └── types.go           # Helm release types
│   ├── images/                # Container image analysis
│   │   ├── auth.go            # Registry authentication (pull secrets, ECR, GCR, ACR)
│   │   ├── handlers.go        # HTTP handlers for image inspection
│   │   ├── inspector.go       # Image filesystem extraction and caching
│   │   └── types.go           # Image metadata and filesystem types
│   ├── k8s/
│   │   ├── cache.go           # Singleton wrapper over pkg/k8score + Radar-specific extensions
│   │   ├── capabilities.go    # Cluster capability detection
│   │   ├── client.go          # K8s client initialization
│   │   ├── cluster_detection.go # GKE/EKS/AKS platform detection
│   │   ├── connection_state.go  # Connection state tracking
│   │   ├── context_manager.go   # Multi-context kubeconfig switching
│   │   ├── discovery.go       # API resource discovery for CRDs
│   │   ├── dynamic_cache.go   # CRD/dynamic resource support
│   │   ├── ephemeral.go       # Ephemeral/debug containers
│   │   ├── history.go         # Change history tracking
│   │   ├── fetch.go           # Resource fetching for AI/MCP consumers
│   │   ├── metrics.go         # Pod/node metrics collection
│   │   ├── metrics_history.go # Metrics history tracking
│   │   ├── problems.go        # Problem detection
│   │   ├── subsystems.go      # Cache subsystem management
│   │   ├── topology_adapter.go # Topology adaptation layer
│   │   ├── update.go          # Resource update/delete operations
│   │   └── workload.go        # Workload operations (restart, scale, rollback)
│   ├── mcp/                   # MCP (Model Context Protocol) server
│   │   ├── server.go          # MCP HTTP handler setup
│   │   ├── tools.go           # MCP tool definitions (15 tools)
│   │   ├── tools_helm.go      # Helm-specific MCP tools
│   │   ├── tools_gitops.go    # GitOps-specific MCP tools
│   │   ├── tools_workloads.go # Workload-specific MCP tools
│   │   └── resources.go       # MCP resource definitions (3 resources)
│   ├── opencost/              # OpenCost integration (cost analysis)
│   │   ├── handlers.go        # HTTP handlers for cost endpoints
│   │   └── types.go           # Cost data types
│   ├── prometheus/            # Prometheus client integration
│   │   ├── client.go          # Prometheus API client
│   │   ├── discovery.go       # Auto-discovery of Prometheus/VictoriaMetrics
│   │   ├── handlers.go        # HTTP handlers for Prometheus endpoints
│   │   └── queries.go         # PromQL query helpers
│   ├── server/
│   │   ├── server.go          # chi router, main REST endpoints
│   │   ├── sse.go             # Server-Sent Events broadcaster
│   │   ├── certificate.go     # TLS certificate parsing and expiry
│   │   ├── copy.go            # Copy operations
│   │   ├── desktop_open_url.go # Desktop URL handling
│   │   ├── desktop_update.go  # Desktop app auto-update handlers
│   │   ├── diagnostics.go     # Diagnostics endpoints
│   │   ├── exec.go            # WebSocket pod terminal exec
│   │   ├── logs.go            # Pod logs streaming
│   │   ├── workload_logs.go   # Workload-level log aggregation
│   │   ├── portforward.go     # Port forwarding sessions
│   │   ├── resource_counts.go # Resource counting
│   │   ├── dashboard.go       # Dashboard summary endpoint
│   │   ├── argo_handlers.go   # ArgoCD sync/refresh/terminate/suspend/resume/rollback/selective-sync handlers
│   │   ├── flux_handlers.go   # FluxCD reconcile/suspend/resume/sync-with-source handlers
│   │   ├── gitops_handlers.go # /api/gitops/tree + /api/gitops/insights handlers, insightsResolver wiring
│   │   ├── gitops_types.go    # Shared GitOps request/response types
│   │   ├── ai_handlers.go     # AI resource preview endpoints
│   │   └── traffic_handlers.go # Service mesh traffic flow handlers
│   ├── settings/              # Application settings management
│   ├── static/                # Embedded frontend files
│   ├── traffic/               # Service mesh traffic analysis
│   ├── updater/               # Binary self-update logic
│   └── version/               # Version information
├── pkg/
│   ├── ai/
│   │   └── context/           # AI context minification for LLM-friendly output
│   ├── audit/                 # Shared cluster audit check engine (reusable by skyhook-connector)
│   ├── gitops/                # GitOps operations abstraction
│   │   ├── insights/          # Per-app diagnosis pipeline: issues + drift diff + recent events + plan + history
│   │   └── tree/              # GitOps resource tree builder for ArgoCD/FluxCD detail graphs
│   ├── k8score/               # Shared K8s caching layer (informers, listers, transforms)
│   ├── portforward/           # Port forwarding logic
│   ├── timeline/              # Timeline event storage (memory/SQLite)
│   └── topology/
│       ├── builder.go         # Topology graph construction
│       ├── certificates.go    # Certificate relationship detection
│       ├── memo.go            # 5s-TTL Memoizer wrapping deterministic Topology builds (used by GitOps handlers)
│       ├── pod_grouping.go    # Pod grouping/collapsing logic
│       ├── relationships.go   # Resource relationship detection
│       └── types.go           # Node, edge, topology definitions
├── packages/
│   └── k8s-ui/                # Shared UI package (@skyhook-io/k8s-ui)
│       └── src/
│           ├── components/
│           │   ├── audit/      # AuditCard, AuditAlerts, AuditFindingsTable (shared)
│           │   ├── resources/  # ResourcesView, resource-utils, renderers
│           │   ├── shared/     # ResourceRendererDispatch, ResourceActionsBar, EditableYamlView
│           │   ├── gitops/     # ArgoCD/FluxCD shared status badges, action buttons, tree graph (GitOpsTreeGraph), insights views (GitOpsStatusStrip, GitOpsIssuesBand, GitOpsFailureCard, GitOpsChangesView with inline drift+events expand, GitOpsActivityInsightView)
│           │   ├── workload/   # WorkloadView
│           │   ├── timeline/   # Timeline shared components
│           │   ├── logs/       # Log viewer core
│           │   └── ui/         # Shared UI primitives (Toast, CodeViewer, etc.)
│           ├── hooks/          # useKeyboardShortcuts, useRefreshAnimation
│           ├── types/          # Shared TypeScript types
│           └── utils/          # Pure utilities (api-resources, format, icons, etc.)
├── web/                       # React frontend (embedded at build)
│   ├── src/
│   │   ├── api/               # API client + SSE hooks
│   │   ├── components/
│   │   │   ├── dock/          # Bottom dock with terminal/logs tabs
│   │   │   ├── gitops/        # GitOps workspace: table+tile views, filters, app detail (Topology/Changes/Activity tabs), SyncOptionsDialog, RollbackDialog
│   │   │   ├── helm/          # Helm release management UI
│   │   │   ├── home/          # Home/dashboard view
│   │   │   ├── logs/          # Logs viewer component
│   │   │   ├── portforward/   # Port forward manager
│   │   │   ├── resource/      # Single resource detail page
│   │   │   ├── resource-drawer/ # Resource drawer overlay
│   │   │   ├── resources/     # Resource list panels (thin wrappers over @skyhook-io/k8s-ui)
│   │   │   ├── audit/          # Cluster audit detail view
│   │   │   ├── cost/           # Cost tracking and visualization
│   │   │   ├── settings/      # Settings dialog
│   │   │   ├── shared/        # Shared components (namespace picker, YAML editor)
│   │   │   ├── timeline/      # Timeline view (activity & changes)
│   │   │   ├── topology/      # Graph visualization
│   │   │   ├── traffic/       # Traffic flow visualization
│   │   │   ├── workload/      # Workload detail view
│   │   │   └── ui/            # Base shadcn/ui components
│   │   ├── context/           # React contexts (connection, theme, context-switch)
│   │   ├── contexts/          # React contexts (capabilities)
│   │   ├── hooks/             # Custom React hooks
│   │   ├── types.ts           # TypeScript type definitions
│   │   └── utils/             # Topology and utility functions
│   └── package.json
├── deploy/                    # Docker, Helm, Krew configs
├── docs/                      # User documentation (configuration, in-cluster guide)
├── scripts/                   # Release scripts
├── .github/                   # CI workflows, issue/PR templates, dependabot
└── Makefile
```

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

### Backend (Go)
```bash
# Run in dev mode (serves frontend from web/dist instead of embedded — no embed step needed)
go run ./cmd/explorer --dev

# Run tests
go test ./...

# Hot reload with Air (port 9280)
make watch-backend
```

### Frontend (React)
```bash
cd web

# Install dependencies
npm install

# Development server with hot reload (port 9273)
npm run dev

# Build for production (outputs to web/dist)
npm run build

# Type check
npm run tsc
```

### Full Build
```bash
make build          # Build everything (frontend + embed + binary)
make restart        # Build + restart server
make restart-fe     # Frontend-only rebuild + restart (no Go recompile)
make frontend       # Build frontend only (to web/dist)
make embed          # Copy web/dist → internal/static/dist
make backend        # Build Go binary only (uses embedded assets)
make watch-frontend # Vite dev server (port 9273)
make watch-backend  # Air hot reload (port 9280)
make test           # Run all tests
make tsc            # Type check frontend
make kill           # Kill running radar on port 9280
make clean          # Remove build artifacts
```

### Visual Testing
```bash
./scripts/visual-test-start.sh          # Build + launch on random port (9300-9399)
./scripts/visual-test-start.sh --skip-build  # Relaunch without rebuilding
source .playwright-mcp/visual-test-state.env # Load $RADAR_URL, $SCREENSHOT_DIR, etc.
./scripts/visual-test-stop.sh           # Kill process, open screenshot folder
```
Use `/visual-test` command for the full workflow (cluster check, Playwright MCP, screenshots, report). Screenshots go under `.playwright-mcp/visual-test/`.

**GitOps demo cluster** (`scripts/gitops-demo.sh` + `make gitops-demo`): bootstraps a `kind` cluster pre-loaded with Argo CD + Flux + a curated set of fixtures (healthy + suspended + manual-sync + ApplicationSet → 3 children + Flux Kustomization with dependsOn chain + HelmRelease) for visual-testing GitOps UI changes against realistic state. Coverage matrix in `scripts/gitops-demo/README.md`. When evaluating GitOps UI changes, run `make gitops-demo` and `kubectl config use-context kind-radar-gitops-demo` before `./scripts/visual-test-start.sh` — otherwise you're testing against whatever cluster is in the current context (often a customer/EKS cluster lacking the variety needed). `make gitops-demo-drift` induces a live OutOfSync state on guestbook for testing drift rendering.

**Before calling a feature done — consider visual-test.** When you wrap up work that touches what the user actually sees (layout, copy, motion, color, theming, loading / empty / error states, modals, navigation, new pages, anything that changed how a screen reads), it's worth pausing to ask whether `/visual-test` would add value. `make tsc` + `make test` verify code correctness; `/visual-test` complements them by checking *feature* correctness — that the screen renders, behaves under interaction, and doesn't obviously regress neighboring surfaces. Not every UI change warrants one; this is a nudge to consider, not a hard rule.

- **Run it yourself** when the change is self-contained and you can predict what the test should assert ("the new audit-finding card should show a severity badge and an expand affordance; clicking expands"). Cheap, catches obvious breakage.
- **Ask the user** when the change is broad (touches many views), the right validation set is non-obvious, or you'd be picking which screens to capture — that judgment is theirs.
- **Skip** for pure refactors with no visual diff, backend-only Go changes that don't surface in the UI, type-only changes, or doc edits.

If a UI change feels worth checking, mention it when you wrap up — even just flagging "want me to run /visual-test on this?" is fine.

### Development Ports
- **9280**: Backend API server (Go)
- **9273**: Vite dev server (proxies /api to 9280)

## API Endpoints & CLI Flags

**You MUST read `internal/server/server.go` before adding or modifying any endpoint** — it is the single source of truth for all routes. CLI flags live in `cmd/explorer/main.go`. Key URL patterns:
- REST resources: `/api/resources/{kind}`, `/api/resources/{kind}/{ns}/{name}`, `/api/resources/apply` (POST)
- SSE streaming: `/api/events/stream`, `/api/traffic/flows/stream`
- WebSocket: `/api/pods/{ns}/{name}/exec`
- MCP: `/mcp` (Streamable HTTP — POST for JSON-RPC, GET for SSE)
- Helm: `/api/helm/releases/...`
- Workloads: `/api/workloads/{kind}/{ns}/{name}/...` (logs, restart, scale, rollback)
- GitOps controller actions: `/api/argo/applications/...` (sync, refresh, terminate, suspend, resume, rollback, selective-sync), `/api/flux/{kind}/...` (reconcile, suspend, resume, sync-with-source)
- GitOps detail data: `/api/gitops/tree/{kind}/{ns}/{name}` (resource tree + ownership edges), `/api/gitops/insights/{kind}/{ns}/{name}` (curated diagnosis: summary + issues + drift + events + plan + history + capabilities)
- Nodes: `/api/nodes/{name}/...` (cordon, uncordon, drain, debug)
- Audit: `/api/audit`, `/api/audit/resource/{kind}/{ns}/{name}`, `/api/settings/audit` (GET/PUT)
- CAPI: `/api/capi/clusters/{ns}/{name}/kubeconfig` (GET), `/api/capi/clusters/{ns}/{name}/connect` (POST)

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
- **Probe-based RBAC gating** (`internal/k8s/capabilities.go`): at startup, Radar runs a real list call against each typed kind (using the SA / kubeconfig identity) to decide if it goes cluster-wide, namespace-scoped, or off. List probes are authoritative because they ARE the operation the informer will perform — SSAR is one indirection too many and can disagree with reality on clusters using webhook authorizers (e.g. GKE IAM)
- **In-app namespace switcher = per-user view filter**: the header's `NamespaceSwitcher` POSTs to `/api/cluster/namespace`, which the server stores as a per-user preference in `Server.nsPreferences` (key: `username\x00contextName`). It does NOT mutate the shared cache. The pick is intersected with the user's RBAC-allowed namespaces on every read in `parseNamespacesForUser` (REST) and `filterNamespacesForUser` (MCP). For the no-auth/local case, the pick persists across restarts via `settings.ActiveNamespaces` and is loaded lazily on first request. On context switch, all users' picks are dropped — they reference the previous cluster's namespaces
- **Per-user RBAC filtering** (auth enabled): namespaced reads filter via `parseNamespacesForUser` → `getUserNamespaces` → `auth.DiscoverNamespaces` (SubjectAccessReview-based, "list pods" / "list deployments" sentinel). Cluster-scoped reads gated per-kind via `Server.canRead` / MCP `canReadClusterScopedKind` — both run a SAR for the exact (group, resource, verb) and cache on `UserPermissions.canI`. Cluster-wide pod visibility does NOT imply cluster-scoped reads; this is the load-bearing security distinction. Static cluster-only kinds map via `k8s.ClusterOnlyKindGVR`; dynamic CRDs use discovery's `GetResourceWithGroup`. MCP write tools / exec / logs impersonate via `DynamicClientFromContext`, so the apiserver enforces full RBAC there directly
- Supports: Pods, Services, Deployments, DaemonSets, StatefulSets, ReplicaSets, Ingresses, IngressClasses, ConfigMaps, Secrets, Events, Jobs, CronJobs, HorizontalPodAutoscalers, PersistentVolumeClaims, PersistentVolumes, StorageClasses, PodDisruptionBudgets, ServiceAccounts, Nodes, Namespaces

### Server-Sent Events (SSE)
- Central `SSEBroadcaster` manages connected clients
- Per-client namespace filters and view mode tracking
- Cached topology for relationship lookups
- Heartbeat mechanism for connection health
- Event types: topology changes, K8s events, resource updates

### WebSocket Pod Exec
- Full terminal emulation via xterm.js in browser
- Container and shell selection support
- Terminal resize handling with size queue
- TTY, stdin, stdout, stderr support

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

### Timeline
- In-memory or SQLite storage for event tracking (`--timeline-storage`)
- Records: resource kind, name, namespace, change type, timestamp, owner info, health state
- Configurable limit (default: 10000 events)
- Supports grouping by owner, app label, or namespace

### Resource Relationships
- Computed at query time for resource detail views
- Tracks: parent (owner), children (owned), deployment (grandparent shortcut for Pods owned by ReplicaSets), config (ConfigMaps/Secrets), network (Services/Ingresses/Gateways/Routes), scalers (HPA/VPA/KEDA), policies (PDB), storage (PVC→PV→StorageClass)
- Used for topology edges and change propagation

### AI Context Minification
- Converts K8s resources into token-efficient representations for LLM consumption
- Three verbosity levels:
  - `Summary`: Typed struct with key fields per resource kind (used by MCP `list_resources`)
  - `Detail`: Full spec/status with metadata noise stripped (used by MCP `get_resource`)
  - `Compact`: Aggressive pruning for token-constrained contexts (probes, volumes, security contexts removed)
- Secret safety: never exposes `.data`/`.stringData`, redacts env values with known secret patterns (API keys, tokens, passwords, base64 blocks)
- Event deduplication: groups by (reason, normalized message), replaces pod hashes/UUIDs/IPs with placeholders
- Log filtering: prioritizes error/warning patterns, falls back to last 20 lines, redacts secrets

### MCP Server
- Stateless HTTP handler mounted at `/mcp` (JSON-RPC over HTTP)
- 17 tools organized into read and write categories:
  - **Read tools** (8): `get_dashboard` (with problem-correlated changes), `list_resources`, `get_resource` (with optional `include`: events, relationships, metrics, logs), `get_topology` (with `format`: graph or summary), `get_events` (with optional `kind`/`name` resource filter), `get_pod_logs`, `list_namespaces`, `get_changes` (timeline of resource mutations)
  - **Read tools — Audit** (1): `get_cluster_audit` (best-practice findings with remediation, filter by namespace/category/severity)
  - **Read tools — Helm** (2): `list_helm_releases`, `get_helm_release` (with optional values/history/diff)
  - **Read tools — Logs** (1): `get_workload_logs` (aggregated, AI-filtered logs across all pods)
  - **Write tools** (5): `apply_resource` (create or update from YAML, supports multi-doc and dry-run), `manage_workload` (restart/scale/rollback), `manage_cronjob` (trigger/suspend/resume), `manage_gitops` (ArgoCD sync/suspend/resume, FluxCD reconcile/suspend/resume), `manage_node` (cordon/uncordon/drain)
- 3 resources: `cluster://health`, `cluster://topology`, `cluster://events`
- Tool annotations: read-only tools use `readOnlyHint`, write tools use `destructiveHint: false`
- Respects cluster RBAC
- Enabled by default, disable with `--no-mcp`

### Error Handling (Backend)
All HTTP handlers use the simple `writeError` pattern:
```go
s.writeError(w, http.StatusXXX, "error message")
// Returns: {"error": "error message"}
```

**HTTP Status Code Conventions:**
- `400 Bad Request`: Invalid input (missing params, invalid YAML, unknown resource kind)
- `403 Forbidden`: RBAC insufficient permissions (lister is nil or K8s API returns forbidden)
- `404 Not Found`: Resource doesn't exist
- `409 Conflict`: Operation already in progress (e.g., sync running)
- `503 Service Unavailable`: Client/cache not initialized, or not connected to cluster
- `500 Internal Server Error`: Unexpected errors (always log before returning)

**`requireConnected` Guard:**
Most handlers that access cluster data call `s.requireConnected(w)` at the top, which returns 503 if the cluster connection isn't established yet. Use this pattern for any new handler that needs cache data.

**Multi-Namespace Query Parameters:**
Endpoints that accept namespace filters support both `?namespace=X` (single, backward compat) and `?namespaces=X,Y` (comma-separated, preferred). Use the `parseNamespaces()` helper to handle both.

**Logging Convention:**
Always log 500 errors with context before returning:
```go
log.Printf("[module] Failed to <action> %s/%s: %v", namespace, name, err)
s.writeError(w, http.StatusInternalServerError, err.Error())
```

**K8s Error Detection:**
Use `apierrors.IsNotFound(err)` for proper K8s error type checking:
```go
if apierrors.IsNotFound(err) {
    s.writeError(w, http.StatusNotFound, err.Error())
    return
}
```

### Error Handling (Frontend)
The frontend uses React Query mutations with meta for toast messages:
```typescript
useMutation({
  mutationFn: async (...) => { ... },
  meta: {
    errorMessage: 'Failed to update resource',  // Shown in toast
    successMessage: 'Resource updated',
  },
})
```

Error responses are parsed as `{"error": "message"}` and displayed in toasts.

### Shared UI Package (@skyhook-io/k8s-ui)
- Located at `packages/k8s-ui/` — shared presentation components decoupled from data fetching
- Components in the package are pure: data fetching hooks live in `web/`, injected via props/callbacks
- `web/src/components/resources/ResourcesView.tsx` is a thin wrapper that instantiates hooks and passes data to the package's `ResourcesView`
- Linked via npm workspaces; Vite aliases `@skyhook-io/k8s-ui` to `../packages/k8s-ui/src` (source-level, no build step)
- Key exports: `ResourcesView`, `ResourceRendererDispatch`, `ResourceActionsBar`, `EditableYamlView`, all renderers, resource-utils, `categorizeResources`, `getKindLabel`, `getKindPlural`
- **Badge colors**: `packages/k8s-ui/src/components/ui/Badge.tsx` is the source of truth for badge color definitions (static strings for Tailwind scanning — never use template literals for class names). `packages/k8s-ui/src/utils/badge-colors.ts` re-exports these and provides derived constants (`SEVERITY_BADGE`, `KIND_BADGE_COLORS`, `HEALTH_BADGE_COLORS`, `HELM_STATUS_COLORS`, etc.). For status badges in tables, use CSS classes `.status-healthy`, `.status-degraded`, `.status-alert`, `.status-unhealthy`, `.status-neutral`, `.status-unknown` defined in `packages/k8s-ui/src/theme/components.css`.
- **Status vocabulary** (one source, three layers): `HealthLevel` type in `resource-utils.ts` (`healthy | degraded | alert | unhealthy | neutral | unknown`) → `.status-*` CSS classes (`theme/components.css`) → typed helpers in `components/ui/status-tone.tsx` (`StatusDot` for tiny indicator dots, `mapHealthToTone` to normalize API strings). All three carry the same six tones; no parallel vocabulary exists. For pill-shaped status badges, use the canonical pattern directly: `<span className={`badge ${healthColors[tone]}`}>...</span>` (used in 56+ sites across OSS). The `alert` (orange) tier is the intermediate between `degraded` (amber) and `unhealthy` (red) — used for severity gradients (Problems pages, Audit findings, Cert expiry) where the data carries a 3-step urgency that must be visually distinguishable. Use `mapHealthToTone(severityOrHealthString)` to normalize raw API values onto a tone.
- **Centralized CSS classes** (all in `@layer components` in `packages/k8s-ui/src/theme/components.css` — Tailwind utilities can override them):
  - `.badge` / `.badge-sm` — badge structure (padding, radius, border-width)
  - `.btn-brand` / `.btn-brand-muted` / `.btn-brand-toggle` — brand buttons (reference `--color-brand` CSS variables)
  - `.card-inner` / `.card-inner-lg` — nested containers in drawers/renderers
  - `.selection` / `.selection-strong` / `.selection-text` / `.selection-ring` — selected rows/items (reference `--selection-*` CSS variables)
  - `.dialog` — modal/dialog containers

### Frontend Styling Rules
**Use theme tokens — never hardcode colors.** See [DESIGN.md](DESIGN.md) for the full reference. Quick rules:
- Backgrounds: `bg-theme-base/surface/elevated/hover` — not `bg-white`, `bg-gray-*`, `bg-slate-*`
- Text: `text-theme-text-primary/secondary/tertiary` — not `text-gray-*`
- Borders: `border-theme-border` — not `border-gray-*`
- Buttons: `.btn-brand` — not hand-rolled `bg-blue-*`
- Badges: `<Badge severity="...">` or `<Badge kind="...">` — never hand-write color strings
- Shadows: `shadow-theme-sm/md/lg` — not raw Tailwind shadows

### Resource Renderers
- **Adding a new CRD integration? You MUST read [docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md) first** — it has the full step-by-step checklist with all files, patterns, and collision gotchas. Do not skip this.
- Renderers, resource-utils, and table column config live in `packages/k8s-ui/src/components/resources/`
- Sections with data should use `defaultExpanded` (true) — only collapse empty or low-priority sections
- Register in: `packages/k8s-ui/src/components/resources/renderers/index.ts` (export), `packages/k8s-ui/src/components/shared/ResourceRendererDispatch.tsx` (KNOWN_KINDS + render line + `getResourceStatus()`)
- Use `AlertBanner` for problem detection, `ProblemAlerts` for multiple warnings/errors, `ConditionsSection` for K8s conditions
- Use `LabelSelectorDisplay` for rendering K8s label selectors — handles `matchLabels` + `matchExpressions` + flat selectors. Never hand-roll selector badge rendering.
- Long text in alerts/banners needs `break-all` class for CSS word breaking
- **Kind collision rule:** When a CRD kind collides with a core K8s kind (e.g., Knative Service vs core Service) or two CRD kinds collide (e.g., CNPG Cluster `postgresql.cnpg.io` vs CAPI Cluster `cluster.x-k8s.io`), you must guard THREE places in `ResourceRendererDispatch.tsx`: (1) the core renderer line, (2) `getResourceStatus()`, (3) action buttons (Port Forward, etc.). Use `data?.apiVersion?.includes('group.name')` checks. Missing any one causes dual rendering bugs.
- Core K8s renderers: Pod, Service, ConfigMap, Secret, Ingress, PersistentVolume, ReplicaSet, StorageClass, NetworkPolicy, Event, Workload (Deployment/StatefulSet/DaemonSet), Role, ClusterRole, RoleBinding, ClusterRoleBinding, ServiceAccount, IngressClass, PriorityClass, RuntimeClass, Lease, MutatingWebhookConfiguration, ValidatingWebhookConfiguration
- 100+ CRD renderer components across 20+ integrations — see `packages/k8s-ui/src/components/resources/renderers/` for the full list, and **[docs/INTEGRATION_GUIDE.md](docs/INTEGRATION_GUIDE.md)** for the step-by-step checklist when adding new ones

## Tech Stack

### Backend
- Go 1.26+
- client-go (K8s client library)
- chi (HTTP router with middleware)
- gorilla/websocket (WebSocket support for exec)
- helm.sh/helm/v3 (Helm SDK)
- cilium/cilium (Hubble traffic observation)
- google/go-containerregistry (image filesystem inspection)
- modernc.org/sqlite (timeline storage)
- modelcontextprotocol/go-sdk (MCP server implementation)
- wailsapp/wails/v2 (desktop app framework)
- go:embed (frontend embedding)

### Frontend
- React 19 + TypeScript
- Vite (build tool, dev server)
- @xyflow/react + elkjs (graph visualization and layout)
- @xterm/xterm + @xterm/addon-fit (terminal emulation)
- @monaco-editor/react (YAML editing)
- shiki (syntax highlighting)
- @tanstack/react-query v5 (server state management)
- react-router-dom (client-side routing)
- Tailwind CSS v4 + shadcn/ui (styling, uses @tailwindcss/vite plugin)
- clsx + tailwind-merge (class utilities)
- react-markdown + @tailwindcss/typography (markdown rendering)
- Lucide React (icons)
- yaml (YAML parsing)

## Server Configuration

### Middleware Stack
- Logger, Recoverer (panic recovery)
- 60-second request timeout
- CORS enabled for `http://localhost:*` and `http://127.0.0.1:*`

### Vite Dev Proxy
In development, Vite proxies `/api` requests to the backend:
```javascript
proxy: {
  '/api': {
    target: 'http://localhost:9280',
    ws: true  // WebSocket support for exec
  }
}
```
