# Skyhook Explorer

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)

Real-time Kubernetes cluster topology visualization in your browser.

<p align="center">
  <img src="docs/screenshot.png" alt="Skyhook Explorer Screenshot" width="800">
</p>

## Features

- **Real-time topology graph** - See your pods, deployments, services, and ingresses connected
- **Live event stream** - Watch resource changes as they happen
- **Resource details** - Click any node to see full resource information
- **Namespace filtering** - Focus on specific namespaces
- **Multiple view modes** - Traffic view (network path) or Resources view (hierarchy)
- **Platform detection** - Automatic detection of GKE, EKS, AKS, minikube, kind
- **Zero cluster modification** - Read-only access, no agents to install

## Installation

### Using kubectl plugin (Krew)

```bash
kubectl krew install explorer
kubectl explorer
```

### Using Homebrew (macOS)

```bash
brew install skyhook-io/tap/explorer
skyhook-explorer
```

### Direct download

Download the latest release from [GitHub Releases](https://github.com/skyhook-io/skyhook-explorer/releases).

### Docker

```bash
docker run -v ~/.kube:/root/.kube -p 8080:8080 ghcr.io/skyhook-io/explorer
```

## Usage

```bash
# Basic usage - opens browser automatically
skyhook-explorer

# Specify namespace
skyhook-explorer --namespace production

# Custom port
skyhook-explorer --port 9090

# Use specific kubeconfig
skyhook-explorer --kubeconfig /path/to/kubeconfig

# Don't auto-open browser
skyhook-explorer --no-browser
```

## API Endpoints

When running, the following API endpoints are available:

| Endpoint | Description |
|----------|-------------|
| `GET /api/health` | Health check |
| `GET /api/cluster-info` | Cluster platform and version info |
| `GET /api/topology` | Current topology graph |
| `GET /api/namespaces` | List of namespaces |
| `GET /api/resources/:kind` | List resources by kind |
| `GET /api/events/stream` | SSE stream for real-time updates |

## Development

### Prerequisites

- Go 1.22+
- Node.js 20+
- npm or pnpm

### Build from source

```bash
# Clone the repository
git clone https://github.com/skyhook-io/skyhook-explorer.git
cd skyhook-explorer

# Build everything
make build

# Run
./explorer
```

### Development mode

```bash
# Start backend
go run ./cmd/explorer --dev --no-browser

# In another terminal, start frontend with hot reload
cd web
npm install
npm run dev
```

## Architecture

```
┌─────────────────┐         ┌───────────────────┐
│    Browser      │◄──SSE──►│  Explorer Binary  │
│  (React + UI)   │         │  (Go + Embedded)  │
└─────────────────┘         └───────────────────┘
                                    │
                           ┌────────┴────────┐
                           │   Kubernetes    │
                           │   API Server    │
                           └─────────────────┘
```

The Explorer uses Kubernetes SharedInformers for efficient, watch-based caching of resources. Changes are pushed to the browser via Server-Sent Events (SSE) for real-time updates.

## Supported Resources

- **Workloads**: Deployments, DaemonSets, StatefulSets, ReplicaSets, Pods
- **Networking**: Services, Ingresses
- **Configuration**: ConfigMaps, Secrets (names only)
- **Autoscaling**: HorizontalPodAutoscalers
- **Batch**: Jobs, CronJobs

## License

Apache 2.0 - see [LICENSE](LICENSE)

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details.

## Related Projects

- [Skyhook](https://skyhook.io) - The platform for Kubernetes made simple
- [skyhook-connector](https://github.com/skyhook-dev/skyhook-connector) - In-cluster agent
