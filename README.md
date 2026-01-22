# Skyhook Explorer

[![CI](https://github.com/skyhook-io/explorer/actions/workflows/ci.yml/badge.svg)](https://github.com/skyhook-io/explorer/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev/)

**Real-time Kubernetes cluster visualization in your browser.** Explore your cluster's topology, browse resources, track events, and manage Helm releases — all from a single, zero-install UI.

<p align="center">
  <img src="docs/screenshot.png" alt="Skyhook Explorer Screenshot" width="800">
</p>

## Features

### Cluster Visualization
- **Real-time topology graph** - See pods, deployments, services, and ingresses connected
- **Live event stream** - Watch resource changes as they happen
- **Resource details** - Click any node to see full resource information with YAML editor
- **Multiple view modes** - Traffic view (network path) or Resources view (hierarchy)
- **Change history** - Track resource changes over time with optional persistence

### Pod Operations
- **Terminal access** - Open interactive shell sessions into pods via WebSocket
- **Log streaming** - View and stream pod logs in real-time
- **Container selection** - Choose specific containers in multi-container pods

### Port Forwarding
- **Session management** - Start and stop port forwards from the UI
- **Auto-discovery** - Automatically detect available ports on pods and services
- **Multiple sessions** - Run concurrent port forwards to different targets

### Helm Integration
- **Release management** - View all Helm releases across namespaces
- **Revision history** - Compare manifests between revisions
- **Upgrade & rollback** - Upgrade releases or rollback to previous versions
- **Values inspection** - View computed values for any release

### Additional Features
- **Namespace filtering** - Focus on specific namespaces
- **Platform detection** - Automatic detection of GKE, EKS, AKS, minikube, kind
- **CRD support** - Dynamic discovery and display of Custom Resource Definitions
- **Zero cluster modification** - Read-only by default, no agents to install
- **Dark/Light mode** - Easy on the eyes, day or night

---

## Installation

### Quick Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/skyhook-io/explorer/main/install.sh | bash
```

### Using Homebrew (macOS/Linux)

```bash
brew install skyhook-io/tap/explorer
skyhook-explorer

# Also works as kubectl plugin
kubectl explorer
```

### Direct Download

Download the latest release for your platform from [GitHub Releases](https://github.com/skyhook-io/explorer/releases):

| Platform | Architecture | Download |
|----------|--------------|----------|
| macOS | Apple Silicon (M1/M2/M3) | `explorer_*_darwin_arm64.tar.gz` |
| macOS | Intel | `explorer_*_darwin_amd64.tar.gz` |
| Linux | x86_64 | `explorer_*_linux_amd64.tar.gz` |
| Linux | ARM64 | `explorer_*_linux_arm64.tar.gz` |
| Windows | x86_64 | `explorer_*_windows_amd64.zip` |

### Docker

```bash
docker run -v ~/.kube:/root/.kube -p 9280:9280 ghcr.io/skyhook-io/explorer
```

### Build from Source

```bash
git clone https://github.com/skyhook-io/explorer.git
cd explorer
make build
./explorer
```

---

## Usage

```bash
# Basic usage — opens browser automatically
skyhook-explorer

# Specify initial namespace
skyhook-explorer --namespace production

# Custom port
skyhook-explorer --port 8080

# Use specific kubeconfig
skyhook-explorer --kubeconfig /path/to/kubeconfig

# Don't auto-open browser
skyhook-explorer --no-browser

# Enable change history persistence
skyhook-explorer --persist-history --history-limit 5000
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--kubeconfig` | `~/.kube/config` | Path to kubeconfig file |
| `--namespace` | (all) | Initial namespace filter |
| `--port` | `9280` | Server port |
| `--no-browser` | `false` | Don't auto-open browser |
| `--dev` | `false` | Development mode (serve frontend from filesystem) |
| `--persist-history` | `false` | Persist change history to `~/.skyhook-explorer/history.jsonl` |
| `--history-limit` | `1000` | Maximum changes to retain in history |
| `--version` | | Show version and exit |

---

## View Modes

Skyhook Explorer provides four main views to help you understand and manage your cluster:

### 1. Topology View

Interactive graph visualization showing how your Kubernetes resources are connected.

<!-- TODO: Add screenshot -->
<p align="center">
  <img src="docs/screenshots/topology-view.png" alt="Topology View" width="800">
  <br><em>Topology View — Visualize resource relationships</em>
</p>

**Features:**
- Real-time updates via Server-Sent Events (SSE)
- Two sub-modes: **Full** (complete resource hierarchy) and **Traffic** (network flow path)
- Grouping options: by namespace, by app label, or ungrouped
- Filter by resource kind (Pods, Deployments, Services, etc.)
- Click any node to see detailed resource information
- Collapsible groups for cleaner visualization
- Auto-layout powered by ELK.js

---

### 2. Resources View

Comprehensive resource browser with a familiar table interface.

<!-- TODO: Add screenshot -->
<p align="center">
  <img src="docs/screenshots/resources-view.png" alt="Resources View" width="800">
  <br><em>Resources View — Browse and filter all cluster resources</em>
</p>

**Features:**
- Browse all Kubernetes resource types (including CRDs)
- Smart columns per resource kind (e.g., Ready/Status for Pods, Replicas for Deployments)
- Search by name or namespace
- Filter by status, health, or problems (e.g., CrashLoopBackOff, ImagePullBackOff)
- Sort by any column
- Click any resource to open detail drawer with:
  - YAML manifest
  - Related resources
  - Container logs (for Pods)
  - Events

---

### 3. Events View

Timeline of Kubernetes events and resource changes.

<!-- TODO: Add screenshot -->
<p align="center">
  <img src="docs/screenshots/events-view.png" alt="Events View" width="800">
  <br><em>Events View — Track cluster activity in real-time</em>
</p>

**Features:**
- Unified timeline combining K8s Events and resource changes
- Filter by event type (All, Warnings only)
- Click any event to drill down into resource details
- Recent activity summary showing most active resources
- Resource change diffs showing what changed (replicas, images, etc.)
- Real-time updates as new events occur

---

### 4. Helm View

Manage Helm releases deployed in your cluster.

<!-- TODO: Add screenshot -->
<p align="center">
  <img src="docs/screenshots/helm-view.png" alt="Helm View" width="800">
  <br><em>Helm View — Manage your Helm deployments</em>
</p>

**Features:**
- List all Helm releases across namespaces
- View release status, chart version, and app version
- Inspect release values (user-supplied and computed)
- View release history and revisions
- Navigate to resources created by the release
- Filter by namespace

---

## API Reference

The Explorer exposes a REST API for programmatic access:

### Core Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /api/health` | Health check with resource counts |
| `GET /api/cluster-info` | Cluster platform and version info |
| `GET /api/topology` | Current topology graph |
| `GET /api/namespaces` | List of namespaces |
| `GET /api/api-resources` | Available API resources (for CRDs) |

### Resource Operations

| Endpoint | Description |
|----------|-------------|
| `GET /api/resources/{kind}` | List resources by kind |
| `GET /api/resources/{kind}/{ns}/{name}` | Get single resource with relationships |
| `PUT /api/resources/{kind}/{ns}/{name}` | Update resource from YAML |
| `DELETE /api/resources/{kind}/{ns}/{name}` | Delete resource |

### Events & History

| Endpoint | Description |
|----------|-------------|
| `GET /api/events` | Recent Kubernetes events |
| `GET /api/events/stream` | SSE stream for real-time events |
| `GET /api/changes` | Resource change history |

### Pod Operations

| Endpoint | Description |
|----------|-------------|
| `GET /api/pods/{ns}/{name}/logs` | Fetch pod logs |
| `GET /api/pods/{ns}/{name}/logs/stream` | Stream logs via SSE |
| `GET /api/pods/{ns}/{name}/exec` | WebSocket terminal session |

### Port Forwarding

| Endpoint | Description |
|----------|-------------|
| `GET /api/portforwards` | List active sessions |
| `POST /api/portforwards` | Start port forward |
| `DELETE /api/portforwards/{id}` | Stop port forward |

### Helm Management

| Endpoint | Description |
|----------|-------------|
| `GET /api/helm/releases` | List all releases |
| `GET /api/helm/releases/{ns}/{name}` | Release details |
| `POST /api/helm/releases/{ns}/{name}/rollback` | Rollback release |
| `DELETE /api/helm/releases/{ns}/{name}` | Uninstall release |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Your Machine                                │
│                                                                     │
│   ┌─────────────────┐              ┌───────────────────────────┐   │
│   │     Browser     │◄────SSE─────►│    Explorer Binary        │   │
│   │  (React + UI)   │◄───REST─────►│  (Go + Embedded Frontend) │   │
│   │                 │◄──WebSocket──►│                           │   │
│   └─────────────────┘              └───────────────────────────┘   │
│                                              │                      │
└──────────────────────────────────────────────│──────────────────────┘
                                               │
                                      ┌────────┴────────┐
                                      │   Kubernetes    │
                                      │   API Server    │
                                      └─────────────────┘
```

**Key design decisions:**

- **SharedInformers** — Efficient watch-based caching with 50-100x latency improvement over direct API calls
- **Server-Sent Events (SSE)** — Real-time push updates to the browser without polling
- **WebSocket** — Bidirectional communication for pod terminal access
- **Embedded Frontend** — Single binary deployment with `go:embed`
- **Read-Only by Default** — No cluster modifications unless explicitly enabled

---

## Supported Resources

| Category | Resources |
|----------|-----------|
| **Workloads** | Deployments, DaemonSets, StatefulSets, ReplicaSets, Pods, Jobs, CronJobs |
| **Networking** | Services, Ingresses, NetworkPolicies, Endpoints |
| **Configuration** | ConfigMaps, Secrets (names only, values hidden) |
| **Storage** | PersistentVolumeClaims, PersistentVolumes, StorageClasses |
| **Autoscaling** | HorizontalPodAutoscalers |
| **Cluster** | Nodes, Namespaces, ServiceAccounts, Events |
| **CRDs** | Any Custom Resource Definition in your cluster |

---

## Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Escape` | Close panel/modal |
| `?` | Show keyboard shortcuts |
| `/` | Focus search |
| `r` | Refresh topology |
| `f` | Fit view to screen |
| `1` | Traffic view |
| `2` | Resources view |

**Navigation:** Pan (drag), Zoom (scroll), Select (click), Multi-select (Shift+click)

---

## Development

### Prerequisites

- **Go 1.22+** — Backend server
- **Node.js 20+** — Frontend build
- **npm** — Package management
- **kubectl** — Kubernetes CLI (configured with cluster access)

### Project Structure

```
explorer/
├── cmd/explorer/          # CLI entry point
├── internal/
│   ├── k8s/              # Kubernetes client, caching, informers
│   ├── server/           # HTTP server, REST API, SSE
│   ├── topology/         # Graph construction logic
│   ├── helm/             # Helm client integration
│   └── static/           # Embedded frontend (built)
├── web/                   # React frontend source
│   ├── src/
│   │   ├── api/          # API client + React Query hooks
│   │   ├── components/   # React components
│   │   │   ├── topology/   # Graph visualization
│   │   │   ├── resources/  # Resource browser
│   │   │   ├── events/     # Events timeline
│   │   │   └── helm/       # Helm management
│   │   ├── hooks/        # Custom React hooks
│   │   └── types.ts      # TypeScript types
│   └── package.json
├── deploy/               # Docker, Helm chart, Krew manifest
├── Makefile              # Build commands
└── README.md
```

### Quick Start

```bash
# Clone the repo
git clone https://github.com/skyhook-io/explorer.git
cd explorer

# Install dependencies
make deps

# Start development (two terminals)
# Terminal 1: Frontend with hot reload
make watch-frontend

# Terminal 2: Backend with hot reload
make watch-backend
```

Frontend runs on `http://localhost:9273` (Vite dev server)
Backend runs on `http://localhost:9280` (Go server)

The frontend proxies API calls to the backend automatically.

### Make Commands

```bash
# Build
make build            # Build everything (frontend + embedded binary)
make frontend         # Build frontend only
make backend          # Build backend only (requires frontend built)
make install          # Build and install to /usr/local/bin

# Development
make watch-frontend   # Vite dev server with HMR (port 9273)
make watch-backend    # Go with air hot reload (port 9280)
make restart          # Rebuild and restart server
make restart-fe       # Frontend-only rebuild (faster)

# Run
make run              # Run built binary
make run-dev          # Run in dev mode (frontend from filesystem)

# Quality
make test             # Run Go tests
make lint             # Run Go linter
make tsc              # TypeScript type check
make fmt              # Format Go code

# Utilities
make deps             # Install all dependencies
make install-tools    # Install dev tools (air)
make clean            # Clean build artifacts
make kill             # Kill running server on port 9280
make docker           # Build Docker image

# Help
make help             # Show all available commands
```

### Building for Production

```bash
# Full production build
make build

# This produces ./explorer binary with:
# - Frontend embedded via go:embed
# - Optimized and minified assets
# - Single portable binary
```

### Running Tests

```bash
# Go tests
make test

# Type check frontend
make tsc
```

---

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on:

- Code of Conduct
- Development workflow
- Pull request process
- Coding standards

---

## License

Apache 2.0 — see [LICENSE](LICENSE)

---

## Related Projects

- [Skyhook](https://skyhook.io) — The platform that makes Kubernetes simple
- [skyhook-connector](https://github.com/skyhook-dev/skyhook-connector) — In-cluster agent for Skyhook platform
