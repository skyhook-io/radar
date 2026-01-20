# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

Skyhook Explorer is an open-source Kubernetes cluster visualization tool that provides real-time topology views and event monitoring. It runs as a kubectl plugin or standalone binary and opens a web UI in the browser.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         User's Machine                          │
│                                                                 │
│   ┌─────────────────┐                   ┌───────────────────┐  │
│   │    Browser      │◄─── HTTP/SSE ────►│  Explorer Binary  │  │
│   │  (React + UI)   │                   │  (Go + Embedded)  │  │
│   └─────────────────┘                   └───────────────────┘  │
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
skyhook-explorer/
├── cmd/explorer/           # CLI entry point
├── internal/
│   ├── k8s/               # K8s client, caching (SharedInformers)
│   ├── server/            # chi HTTP server, REST API, SSE
│   └── topology/          # Graph construction logic
├── web/                   # React frontend (embedded at build)
│   ├── src/
│   │   ├── api/           # API client + SSE hooks
│   │   ├── components/    # React components
│   │   └── utils/         # Topology utilities
│   └── package.json
└── deploy/                # Docker, Helm, Krew configs
```

## Development Commands

### Backend (Go)
```bash
# Build
go build -o explorer ./cmd/explorer

# Run locally (without embedding frontend)
go run ./cmd/explorer --dev

# Run tests
go test ./...

# Build with embedded frontend
make build
```

### Frontend (React)
```bash
cd web

# Install dependencies
npm install

# Development server (with hot reload)
npm run dev

# Build for production
npm run build

# Type check
npm run tsc
```

### Full Build
```bash
# Build everything (frontend + embedded binary)
make build

# Run the complete application
./explorer
```

## Key Patterns

### K8s Caching
- Uses SharedInformers for watch-based caching
- Provides 50-100x latency improvement over direct API calls
- Memory-efficient with field stripping (managed fields, annotations)
- Change notifications via channel for real-time updates

### Server-Sent Events (SSE)
- Real-time updates pushed to browser via `/api/events/stream`
- Topology changes, K8s events, heartbeats
- Auto-reconnects on connection drop

### Topology Builder
- Constructs graph from K8s resources
- Supports two view modes: 'traffic' (network flow) and 'resources' (full hierarchy)
- Node types: Ingress, Service, Deployment, ReplicaSet, Pod, ConfigMap, Secret, HPA

## API Endpoints

```
GET  /api/health                    # Health check
GET  /api/topology                  # Full topology graph
GET  /api/topology?namespace=X      # Namespace-filtered
GET  /api/resources/:kind           # List resources by kind
GET  /api/resources/:kind/:ns/:name # Single resource detail
GET  /api/events                    # Recent K8s events
GET  /api/events/stream             # SSE stream for real-time
GET  /api/cluster-info              # Platform detection (GKE, EKS, etc.)
```

## CLI Flags

```
--kubeconfig    Path to kubeconfig (default: ~/.kube/config)
--namespace     Initial namespace filter (default: all)
--port          Server port (default: 8080)
--no-browser    Don't auto-open browser
--dev           Development mode (serve frontend from web/dist)
```

## Tech Stack

### Backend
- Go 1.22+
- client-go (K8s client)
- chi (HTTP router)
- go:embed (frontend embedding)

### Frontend
- React 18 + TypeScript
- Vite (build tool)
- Xyflow/ReactFlow + ELK.js (graph visualization)
- Tailwind CSS + shadcn/ui (styling)
- TanStack Query v5 (server state)
