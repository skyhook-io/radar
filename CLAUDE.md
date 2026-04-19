# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

Radar is a modern Kubernetes visibility tool — local-first, no account required, no cloud dependency, fast. It provides topology visualization, event timeline, service traffic maps, resource browsing, Helm management, and cluster audit (best-practices scanning). Runs as a kubectl plugin (`kubectl-radar`) or standalone binary and opens a web UI in the browser. Open source, free forever. Built by Skyhook.

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
│   │   ├── argo_handlers.go   # ArgoCD sync/refresh/suspend handlers
│   │   ├── flux_handlers.go   # FluxCD reconcile/suspend handlers
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
│   ├── k8score/               # Shared K8s caching layer (informers, listers, transforms)
│   ├── portforward/           # Port forwarding logic
│   ├── timeline/              # Timeline event storage (memory/SQLite)
│   └── topology/
│       ├── builder.go         # Topology graph construction
│       ├── certificates.go    # Certificate relationship detection
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
│           │   ├── gitops/     # ArgoCD/FluxCD panels
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
│   │   │   ├── gitops/        # ArgoCD/FluxCD management panels
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
- GitOps: `/api/argo/applications/...`, `/api/flux/{kind}/...`
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
- Constructs directed graph from K8s resources
- Owner reference traversal for parent-child relationships
- Selector-based matching for Service→Pod, Deployment→ReplicaSet
- Two view modes:
  - `traffic`: Network flow (Ingress/Gateway → HTTPRoute → Service → Pod, also IstioGateway → VirtualService → Service, also IngressRoute → TraefikService → Service)
  - `resources`: Full hierarchy (Deployment → ReplicaSet → Pod)
- Node types: Internet, Ingress, Gateway, GatewayClass, HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, Service, Deployment, DaemonSet, StatefulSet, ReplicaSet, Pod, Job, CronJob, ConfigMap, Secret, HorizontalPodAutoscaler, PersistentVolumeClaim, PersistentVolume, StorageClass, PodDisruptionBudget, VerticalPodAutoscaler, Rollout (Argo), Node, Namespace, NodePool, NodeClaim, NodeClass (Karpenter), ScaledObject, ScaledJob (KEDA), NetworkPolicy, CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy, CAPICluster (Cluster API), MachineDeployment, MachineSet, Machine, MachinePool, KubeadmControlPlane, ClusterClass, MachineHealthCheck
- Edge type semantics (these drive UI grouping in Related Resources): `EdgeManages` (owner), `EdgeUses` (autoscalers like HPA/VPA/KEDA → Scalers group), `EdgeProtects` (PDB/NetworkPolicy/CiliumNetworkPolicy → Policies group), `EdgeConfigures` (ConfigMap/Secret/DestinationRule), `EdgeExposes` (Service/Ingress/Gateway/VirtualService). Choose the right edge type — don't reuse one just because the code pattern is similar.
- Istio service mesh nodes: VirtualService, DestinationRule, IstioGateway (note: uses "istiogateway" node ID prefix to disambiguate from Gateway API's "gateway")
  - VirtualService → Service edges (EdgeExposes, via spec.http/tcp/tls route destinations, parses short/FQDN Istio host format)
  - Istio Gateway → VirtualService edges (EdgeExposes, via spec.gateways[] references)
  - DestinationRule → Service edges (EdgeConfigures, via spec.host)
  - Uses `GetGVRWithGroup("Gateway", "networking.istio.io")` to disambiguate Istio Gateway from Gateway API Gateway
  - Frontend detects Istio vs Gateway API Gateways via `data.apiVersion?.includes('networking.istio.io')`
- Knative nodes: KnativeService, KnativeConfiguration, KnativeRevision, KnativeRoute (Serving); Broker, Trigger, PingSource, ApiServerSource, ContainerSource, SinkBinding (Eventing/Sources)
  - Uses "knativeservice/" node ID prefix to disambiguate from core K8s Service; similarly "knativeingress/", "knativecertificate/"
  - Uses `GetGVRWithGroup("Service", "serving.knative.dev")` for collision-prone kinds (Service, Ingress, Certificate, Configuration, Route, Broker, Channel)
  - Serving edges: Route → Revision (EdgeExposes, via spec.traffic[].revisionName), Configuration/Revision owner-ref edges
  - Eventing edges: Trigger → Broker (EdgeExposes), Trigger → subscriber (EdgeExposes), Sources → sink (EdgeExposes)
  - Frontend detects Knative vs core kinds via `data.apiVersion?.includes('serving.knative.dev')` etc.
- Traefik nodes: IngressRoute, IngressRouteTCP, IngressRouteUDP, Middleware, MiddlewareTCP, TraefikService, ServersTransport, ServersTransportTCP, TLSOption, TLSStore
  - Node IDs use lowercase singular: `ingressroute/{ns}/{name}`, `middleware/{ns}/{name}`, etc.
  - IngressRoute → Service edges (EdgeExposes, via spec.routes[].services[])
  - IngressRoute → Middleware edges (EdgeConfigures, via spec.routes[].middlewares[])
  - IngressRoute → TraefikService edges (EdgeExposes, when service kind is "TraefikService")
  - TraefikService → Service edges (EdgeExposes, via spec.weighted/mirroring/highestRandomWeight services)
  - TraefikService → TraefikService edges (EdgeExposes, for recursive references)
  - Middleware chain → Middleware edges (EdgeConfigures, via spec.chain.middlewares[])
  - ServersTransport/ServersTransportTCP → Secret edges (EdgeConfigures, via spec.rootCAsSecrets[] and spec.certificatesSecrets[])
  - TLSOption → Secret edges (EdgeConfigures, via spec.clientAuth.secretNames[])
  - TLSStore → Secret edges (EdgeConfigures, via spec.defaultCertificate.secretName)
  - IngressRoute → ServersTransport edges (EdgeConfigures, via service serversTransport field)
  - IngressRoute → TLSOption/TLSStore edges (EdgeConfigures, via spec.tls.options/store)
  - Traffic view uses two-phase processing for TraefikService (Phase 1: nodes + ID map, Phase 2: edges) to handle forward references
  - Kubernetes informers strip kind/apiVersion from cached objects — use stored prefix from `def.prefix` for ServersTransport lookups, not `GetKind()`
- Cluster API (CAPI) nodes: CAPICluster, MachineDeployment, MachineSet, Machine, MachinePool, KubeadmControlPlane, ClusterClass, MachineHealthCheck
  - Uses "capicluster/" node ID prefix to disambiguate from CNPG's Cluster (postgresql.cnpg.io)
  - Uses `GetGVRWithGroup("Cluster", "cluster.x-k8s.io")` for collision-prone kinds (Cluster, Machine, MachineSet)
  - Cluster → KCP edges (EdgeManages, via ownerRef on KCP)
  - Cluster → MachineDeployment/MachinePool edges (EdgeManages, via ownerRef)
  - MachineDeployment → MachineSet → Machine chain (EdgeManages, via ownerRef)
  - KubeadmControlPlane → Machine edges (EdgeManages, via ownerRef)
  - Machine → Node edges (EdgeManages, via status.nodeRef.name — semantic, not owner-ref)
  - ClusterClass → Cluster edges (EdgeConfigures, via spec.topology.class match)
  - MachineHealthCheck → Cluster edges (EdgeProtects, via ownerRef)
  - v1beta1/v1beta2 dual support: status extractors check status.v1beta2.conditions then fall back to status.conditions
  - Frontend detects CAPI vs CNPG clusters via `data?.apiVersion?.includes('cluster.x-k8s.io')`
  - Topology-controlled badge: resources with label `topology.cluster.x-k8s.io/owned` show a warning banner
- GitOps nodes: Application (ArgoCD), Kustomization, HelmRelease, GitRepository (FluxCD)
  - Connected to managed resources via status.resources (ArgoCD) or status.inventory (FluxCD Kustomization)
  - HelmRelease connects to resources via FluxCD labels (`helm.toolkit.fluxcd.io/name`) or standard Helm label (`app.kubernetes.io/instance`). Matches Deployment, Service, StatefulSet, DaemonSet, Job, CronJob, Rollout.
  - **Single-cluster limitation**: Radar only shows connections when GitOps controller and managed resources are in the same cluster. ArgoCD commonly deploys to remote clusters (hub-spoke model), so Application→resource edges won't appear when connected to the ArgoCD cluster. FluxCD typically deploys to its own cluster, so connections usually work.

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
- **Badge colors**: `packages/k8s-ui/src/components/ui/Badge.tsx` is the source of truth for badge color definitions (static strings for Tailwind scanning — never use template literals for class names). `packages/k8s-ui/src/utils/badge-colors.ts` re-exports these and provides derived constants (`SEVERITY_BADGE`, `KIND_BADGE_COLORS`, `HEALTH_BADGE_COLORS`, `HELM_STATUS_COLORS`, etc.). For status badges in tables, use CSS classes `.status-healthy`, `.status-degraded`, `.status-unhealthy`, `.status-neutral`, `.status-unknown` defined in `packages/k8s-ui/src/theme/components.css`.
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
