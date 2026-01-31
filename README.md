# Radar

**Modern Kubernetes visibility.**
<br>Local-first. No account. No cloud dependency. Fast.

Topology, event timeline, and service traffic — plus resource browsing, Helm management, and GitOps support for FluxCD and ArgoCD.

[![CI](https://github.com/skyhook-io/radar/actions/workflows/ci.yml/badge.svg)](https://github.com/skyhook-io/radar/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/skyhook-io/radar?logo=github)](https://github.com/skyhook-io/radar/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/skyhook-io/radar)](https://goreportcard.com/report/github.com/skyhook-io/radar)
[![Downloads](https://img.shields.io/github/downloads/skyhook-io/radar/total?logo=github)](https://github.com/skyhook-io/radar/releases)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev/)

Visualize your cluster topology, browse resources, stream logs, exec into pods, manage Helm releases, monitor GitOps workflows (FluxCD & ArgoCD), and forward ports — all from a single binary with zero cluster-side installation.

<p align="center">
  <img src="docs/screenshot.png" alt="Radar Screenshot" width="800">
</p>

## Why Radar?

- **Zero install on your cluster** — runs on your laptop, talks to the K8s API directly
- **Single binary** — no dependencies, no agents, no CRDs
- **Real-time** — watches your cluster via informers, pushes updates to the browser via SSE
- **Works everywhere** — GKE, EKS, AKS, minikube, kind, k3s, or any conformant cluster
- **In-cluster option** — deploy with Helm for shared team access with RBAC-scoped permissions

---

## Installation

### Quick Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/skyhook-io/radar/main/install.sh | bash
```

### Homebrew (macOS/Linux)

```bash
brew install skyhook-io/tap/radar
```

### Krew (kubectl plugin manager)

```bash
kubectl krew install radar
```

### Direct Download

Download the latest release for your platform from [GitHub Releases](https://github.com/skyhook-io/radar/releases):

| Platform | Architecture | Download |
|----------|--------------|----------|
| macOS | Apple Silicon (M1/M2/M3) | `radar_*_darwin_arm64.tar.gz` |
| macOS | Intel | `radar_*_darwin_amd64.tar.gz` |
| Linux | x86_64 | `radar_*_linux_amd64.tar.gz` |
| Linux | ARM64 | `radar_*_linux_arm64.tar.gz` |
| Windows | x86_64 | `radar_*_windows_amd64.zip` |

### In-Cluster Deployment

Deploy Radar to your Kubernetes cluster for shared team access:

```bash
helm repo add skyhook https://skyhook-io.github.io/helm-charts
helm install radar skyhook/radar -n radar --create-namespace
```

See the [In-Cluster Deployment Guide](docs/in-cluster.md) for ingress, authentication, and RBAC configuration.

---

## Usage

```bash
# Opens browser automatically
kubectl radar

# Filter to a specific namespace
kubectl radar --namespace production

# Custom port
kubectl radar --port 8080

# Use a specific kubeconfig
kubectl radar --kubeconfig /path/to/kubeconfig

# Persist timeline events across restarts
kubectl radar --timeline-storage sqlite
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--kubeconfig` | `~/.kube/config` | Path to kubeconfig file |
| `--namespace` | (all) | Initial namespace filter |
| `--port` | `9280` | Server port |
| `--no-browser` | `false` | Don't auto-open browser |
| `--timeline-storage` | `memory` | Timeline storage backend: `memory` or `sqlite` |
| `--timeline-db` | `~/.radar/timeline.db` | Path to SQLite database (when using sqlite storage) |
| `--history-limit` | `10000` | Maximum events to retain in timeline |
| `--debug-events` | `false` | Enable verbose event debugging (logs all event drops) |
| `--version` | | Show version and exit |

---

## Views

### Topology

Interactive graph showing how your Kubernetes resources are connected in real-time.

<p align="center">
  <img src="docs/screenshots/topology-view.png" alt="Topology View" width="800">
  <br><em>Topology View — Visualize resource relationships</em>
</p>

- Two modes: **Resources** (full hierarchy) and **Traffic** (network flow path)
- Group by namespace, app label, or view ungrouped
- Filter by resource kind — click any node for full details
- Auto-layout powered by ELK.js, live updates via SSE

### Resources

Table-based resource browser with smart columns per resource kind.

<p align="center">
  <img src="docs/screenshots/resources-view.png" alt="Resources View" width="800">
  <br><em>Resources View — Browse and filter all cluster resources</em>
</p>

- Browse all resource types including CRDs
- Search by name, filter by status or problems (CrashLoopBackOff, ImagePullBackOff, etc.)
- Click any resource for YAML manifest, related resources, logs, and events

### Timeline

Unified timeline of Kubernetes events and resource changes.

<p align="center">
  <img src="docs/screenshots/timeline-view.png" alt="Timeline View" width="800">
  <br><em>Timeline View — Track cluster activity in real-time</em>
</p>

- Filter by event type (all or warnings only)
- Resource change diffs showing what changed (replicas, images, etc.)
- Real-time updates as new events occur

### Helm

Manage Helm releases deployed in your cluster.

<p align="center">
  <img src="docs/screenshots/helm-view.png" alt="Helm View" width="800">
  <br><em>Helm View — Manage your Helm deployments</em>
</p>

- View all releases across namespaces with status, chart version, and app version
- Inspect values, compare revisions, view release history
- Upgrade, rollback, or uninstall releases directly from the UI

### GitOps

Monitor and manage FluxCD and ArgoCD resources with unified status views and actions.

- **FluxCD**: GitRepository, OCIRepository, HelmRepository, Kustomization, HelmRelease, Alert
- **ArgoCD**: Application, ApplicationSet, AppProject
- Real-time sync status, health indicators, and reconciliation countdowns
- Trigger reconciliation, suspend/resume resources, and view managed resource inventory
- Problem detection with clear alerts for degraded or out-of-sync resources
- **Note**: Topology connections between GitOps resources and managed workloads only appear when both are in the same cluster. FluxCD typically deploys to its own cluster. ArgoCD often manages remote clusters — connect Radar to the target cluster to see workloads, or to the ArgoCD cluster to see Application status.

### Traffic

Visualize live network traffic between services using Hubble or Caretta.

<p align="center">
  <img src="docs/screenshots/traffic-view.png" alt="Traffic View" width="800">
  <br><em>Traffic View — See how services communicate in real-time</em>
</p>

- Auto-detects Hubble (Cilium) or Caretta as traffic data sources
- Animated flow graph showing requests per second between services
- Filter by namespace, protocol, or status code
- Setup wizard to install a traffic source if none is detected

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
| **GitOps (FluxCD)** | GitRepository, OCIRepository, HelmRepository, Kustomization, HelmRelease, Alert |
| **GitOps (ArgoCD)** | Application, ApplicationSet, AppProject |
| **Argo Rollouts** | Rollout |
| **Argo Workflows** | Workflow, WorkflowTemplate |
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

See the **[Development Guide](DEVELOPMENT.md)** for building from source, architecture details, API reference, and contributing.

Quick start:
```bash
git clone https://github.com/skyhook-io/radar.git
cd radar
make deps

# Terminal 1: Frontend with hot reload (port 9273)
make watch-frontend

# Terminal 2: Backend with hot reload (port 9280)
make watch-backend
```

---

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on the development workflow, pull request process, and coding standards.

---

## License

Apache 2.0 — see [LICENSE](LICENSE)

---

<p align="center">
  <strong>Open source. Free forever.</strong>
  <br>
  <sub>Built by <a href="https://skyhook.io">Skyhook</a></sub>
</p>
