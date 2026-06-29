# Project structure

Reference layout for the Radar repo. Use this when locating where a concern lives — most of the load-bearing detail is on the per-area sections inside `CLAUDE.md` (k8s caching, topology, MCP, error handling, renderers). This file is the directory map.

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
│   │   ├── hook_evidence.go   # Live Job/Pod/Event/log evidence for failed hooks
│   │   └── types.go           # Helm release types
│   ├── images/                # Container image analysis
│   │   ├── auth.go            # Registry authentication (pull secrets, ECR, GCR, ACR)
│   │   ├── handlers.go        # HTTP handlers for image inspection
│   │   ├── inspector.go       # Image filesystem extraction and caching
│   │   └── types.go           # Image metadata and filesystem types
│   ├── k8s/
│   │   ├── cache.go           # Singleton wrapper over pkg/k8score + Radar-specific extensions
│   │   ├── capabilities.go    # Cluster capability detection (probe-based RBAC gating)
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
│   │   ├── tools.go           # MCP tool definitions
│   │   ├── tools_helm.go      # Helm-specific MCP tools
│   │   ├── tools_gitops.go    # GitOps-specific MCP tools
│   │   ├── tools_workloads.go # Workload-specific MCP tools
│   │   └── resources.go       # MCP resource definitions
│   ├── opencost/              # OpenCost integration (cost analysis)
│   ├── prometheus/            # Prometheus client integration
│   ├── server/
│   │   ├── server.go          # chi router, main REST endpoints (SOURCE OF TRUTH for routes)
│   │   ├── sse.go             # Server-Sent Events broadcaster
│   │   ├── certificate.go     # TLS certificate parsing and expiry
│   │   ├── exec.go            # WebSocket pod terminal exec
│   │   ├── logs.go            # Pod logs streaming
│   │   ├── workload_logs.go   # Workload-level log aggregation
│   │   ├── portforward.go     # Port forwarding sessions
│   │   ├── resource_counts.go # Resource counting
│   │   ├── dashboard.go       # Dashboard summary endpoint
│   │   ├── argo_handlers.go   # ArgoCD sync/refresh/terminate/suspend/resume/rollback/selective-sync
│   │   ├── flux_handlers.go   # FluxCD reconcile/suspend/resume/sync-with-source
│   │   ├── gitops_handlers.go # /api/gitops/tree + /api/gitops/insights handlers
│   │   ├── gitops_types.go    # Shared GitOps request/response types
│   │   ├── ai_handlers.go     # AI resource preview endpoints
│   │   └── traffic_handlers.go # Service mesh traffic flow handlers
│   ├── settings/              # Application settings management
│   ├── static/                # Embedded frontend files
│   ├── traffic/               # Service mesh traffic analysis
│   ├── updater/               # Binary self-update logic
│   └── version/               # Version information
├── pkg/
│   ├── ai/context/            # AI context minification for LLM-friendly output
│   ├── audit/                 # Shared cluster audit check engine (reusable by skyhook-connector)
│   ├── gitops/
│   │   ├── insights/          # Per-app diagnosis pipeline: issues + drift diff + recent events + plan + history
│   │   └── tree/              # GitOps resource tree builder for ArgoCD/FluxCD detail graphs
│   ├── k8score/               # Shared K8s caching layer (informers, listers, transforms)
│   ├── portforward/           # Port forwarding logic
│   ├── timeline/              # Timeline event storage (memory/SQLite)
│   └── topology/
│       ├── builder.go         # Topology graph construction
│       ├── certificates.go    # Certificate relationship detection
│       ├── memo.go            # 5s-TTL Memoizer wrapping deterministic Topology builds
│       ├── pod_grouping.go    # Pod grouping/collapsing logic
│       ├── relationships.go   # Resource relationship detection
│       └── types.go           # Node, edge, topology definitions
├── packages/k8s-ui/           # Shared UI package (@skyhook-io/k8s-ui)
│   └── src/
│       ├── components/
│       │   ├── audit/         # AuditCard, AuditAlerts, AuditFindingsTable
│       │   ├── resources/     # ResourcesView, resource-utils, renderers
│       │   ├── shared/        # ResourceRendererDispatch, ResourceActionsBar, EditableYamlView
│       │   ├── gitops/        # Argo/Flux badges + actions + tree graph + insights views
│       │   ├── workload/      # WorkloadView
│       │   ├── timeline/      # Timeline shared components
│       │   ├── logs/          # Log viewer core
│       │   └── ui/            # Shared UI primitives (Toast, CodeViewer, etc.)
│       ├── hooks/             # useKeyboardShortcuts, useRefreshAnimation
│       ├── types/             # Shared TypeScript types
│       └── utils/             # Pure utilities (api-resources, format, icons, etc.)
├── web/                       # React frontend (embedded at build) — IS @skyhook-io/radar-app
│   ├── src/
│   │   ├── api/               # API client + SSE hooks + getApiBase/apiUrl/getWsUrl helpers
│   │   ├── components/
│   │   │   ├── dock/          # Bottom dock with terminal/logs tabs
│   │   │   ├── gitops/        # GitOps workspace: table+tile, filters, detail (Topology/Changes/Activity)
│   │   │   ├── helm/          # Helm release management UI
│   │   │   ├── home/          # Home/dashboard view
│   │   │   ├── logs/          # Logs viewer component
│   │   │   ├── portforward/   # Port forward manager
│   │   │   ├── resource/      # Single resource detail page
│   │   │   ├── resource-drawer/ # Resource drawer overlay
│   │   │   ├── resources/     # Resource list panels (thin wrappers over @skyhook-io/k8s-ui)
│   │   │   ├── audit/         # Cluster audit detail view
│   │   │   ├── cost/          # Cost tracking and visualization
│   │   │   ├── settings/      # Settings dialog
│   │   │   ├── shared/        # Namespace picker, YAML editor
│   │   │   ├── timeline/      # Timeline view (activity & changes)
│   │   │   ├── topology/      # Graph visualization
│   │   │   ├── traffic/       # Traffic flow visualization
│   │   │   ├── workload/      # Workload detail view
│   │   │   └── ui/            # Base shadcn/ui components
│   │   ├── context/           # React contexts (connection, theme, context-switch)
│   │   ├── contexts/          # React contexts (capabilities)
│   │   ├── hooks/             # Custom React hooks
│   │   └── utils/             # Topology and utility helpers
│   └── package.json
├── deploy/                    # Docker, Helm, Krew configs
├── docs/                      # User + Claude-facing reference docs
├── scripts/                   # Release scripts + gitops-demo + visual-test orchestration
├── .github/                   # CI workflows, issue/PR templates, dependabot
└── Makefile
```

## Tech stack snapshot

**Backend:** Go 1.26+, client-go, chi, gorilla/websocket, helm.sh/helm/v3, cilium/cilium (Hubble), google/go-containerregistry, modernc.org/sqlite, modelcontextprotocol/go-sdk, wailsapp/wails/v2 (desktop), `go:embed` for frontend.

**Frontend:** React 19 + TypeScript, Vite, @xyflow/react + elkjs (graph), @xterm/* (terminal), @monaco-editor/react (YAML), shiki (syntax), @tanstack/react-query v5, react-router-dom, Tailwind CSS v4 + shadcn/ui (`@tailwindcss/vite` plugin), Lucide React (icons), `yaml`.

`go.mod` and `web/package.json` are the source of truth — this snapshot is for orientation only.
