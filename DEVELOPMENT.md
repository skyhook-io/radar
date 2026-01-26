# Development Guide

Guide for developers contributing to Skyhook Explorer or building custom versions.

## Development Setup

### Prerequisites

- **Go 1.22+**
- **Node.js 20+**
- **npm**
- **kubectl** with cluster access

### Quick Start

```bash
git clone https://github.com/skyhook-io/explorer.git
cd explorer

# Install dependencies
make deps

# Start development (two terminals)

# Terminal 1: Frontend with hot reload (port 9273)
make watch-frontend

# Terminal 2: Backend with hot reload (port 9280)
make watch-backend
```

Open http://localhost:9273 - the frontend proxies API calls to the backend.

### Make Commands

```bash
make build            # Build everything (frontend + binary)
make frontend         # Build frontend only
make backend          # Build backend only
make test             # Run Go tests
make lint             # Run linter
make tsc              # TypeScript type check
make clean            # Clean build artifacts
```

## Project Structure

```
explorer/
├── cmd/explorer/           # CLI entry point
├── internal/
│   ├── k8s/               # Kubernetes client, informers, caching
│   ├── server/            # HTTP server, REST API, SSE, WebSocket
│   ├── topology/          # Graph construction
│   ├── helm/              # Helm client
│   └── static/            # Embedded frontend (built)
├── web/                    # React frontend
│   ├── src/
│   │   ├── api/           # API client, React Query hooks
│   │   ├── components/    # React components
│   │   ├── hooks/         # Custom hooks
│   │   └── types.ts       # TypeScript types
│   └── package.json
├── deploy/                 # Helm chart, Docker, Krew
└── docs/                   # User documentation
```

## Building

### Development Builds

```bash
# CLI binary (frontend embedded)
make build
./explorer --help
```

### Docker

```bash
# Build image
make docker

# Test locally
docker run -v ~/.kube:/root/.kube -p 9280:9280 ghcr.io/skyhook-io/explorer:latest
```

## Releasing

### Quick Release

```bash
# Interactive release (prompts for version and targets)
make release

# Or release specific components:
make release-binaries     # CLI via goreleaser → GitHub + Homebrew
make release-docker       # Docker image → GHCR
```

### Release Targets

| Target | Command | Output |
|--------|---------|--------|
| CLI binaries | `make release-binaries` | GitHub Releases + Homebrew tap |
| Docker | `make release-docker` | `ghcr.io/skyhook-io/explorer:VERSION` |
| All | `make release` | Interactive, choose targets |

### Release Script Options

```bash
# Non-interactive release (uses latest tag)
./scripts/release.sh --binaries    # CLI only
./scripts/release.sh --docker      # Docker only
./scripts/release.sh --all         # Everything
```

### Prerequisites for Releasing

| Target | Requirements |
|--------|--------------|
| CLI binaries | `goreleaser`, `GITHUB_TOKEN` or `gh auth login` |
| Docker | Docker running, GHCR auth (`docker login ghcr.io`) |

### Release Checklist

1. **Update version** in `cmd/explorer/main.go` (optional - goreleaser uses git tags)
2. **Ensure tests pass**: `make test`
3. **Run release**: `make release`
4. **Verify**:
   - GitHub release: https://github.com/skyhook-io/explorer/releases
   - Homebrew: `brew update && brew info skyhook-explorer`
   - Docker: `docker pull ghcr.io/skyhook-io/explorer:VERSION`
5. **Update Helm chart** `appVersion` in `deploy/helm/skyhook-explorer/Chart.yaml`

### Distribution Channels

| Channel | Updated By | Notes |
|---------|------------|-------|
| GitHub Releases | `make release-binaries` | Automatic via goreleaser |
| Homebrew | `make release-binaries` | Auto-publishes to `skyhook-io/homebrew-skyhook-cli` |
| Docker (GHCR) | `make release-docker` | Manual trigger |
| Helm chart | Manual | Update `Chart.yaml` after release |

## Architecture

### Backend

```
┌─────────────────────────────────────────────────────────────────┐
│                         Go Backend                              │
│                                                                 │
│   ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐   │
│   │   chi       │    │  Informers  │    │  SSE            │   │
│   │   Router    │───►│  (cached)   │───►│  Broadcaster    │   │
│   └─────────────┘    └─────────────┘    └─────────────────┘   │
│         │                   │                    │             │
│         ▼                   ▼                    ▼             │
│   REST API            K8s Watches         Real-time push      │
│   WebSocket (exec)    Resource cache      to browser          │
└─────────────────────────────────────────────────────────────────┘
```

**Key patterns:**
- **SharedInformers** - Watch-based caching, 50-100x faster than direct API calls
- **SSE Broadcaster** - Central hub for real-time updates to all connected browsers
- **Topology Builder** - Constructs graph from cached resources on demand

### Frontend

```
┌─────────────────────────────────────────────────────────────────┐
│                      React Frontend                             │
│                                                                 │
│   ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐   │
│   │  React      │    │  TanStack   │    │  @xyflow/react  │   │
│   │  Router     │───►│  Query      │───►│  + ELK.js       │   │
│   └─────────────┘    └─────────────┘    └─────────────────┘   │
│                             │                    │             │
│                             ▼                    ▼             │
│                      API + SSE hooks      Graph visualization  │
└─────────────────────────────────────────────────────────────────┘
```

**Key patterns:**
- **useEventSource** - SSE connection with automatic reconnection
- **React Query** - Server state management, caching, refetching
- **Context providers** - Namespace filter, context switching, dock state

## Adding Features

### New API Endpoint

1. Add handler in `internal/server/server.go`:
   ```go
   r.Get("/api/new-endpoint", s.handleNewEndpoint)
   ```

2. Implement handler:
   ```go
   func (s *Server) handleNewEndpoint(w http.ResponseWriter, r *http.Request) {
       // ...
   }
   ```

### New Resource Type

1. Add to informer setup in `internal/k8s/cache.go`
2. Add to topology builder in `internal/topology/builder.go`
3. Add TypeScript type in `web/src/types.ts`

### New UI Component

1. Create component in `web/src/components/`
2. Add route if needed in `web/src/App.tsx`
3. Add API hooks if needed in `web/src/api/`

## Testing

```bash
# Go tests
make test

# Type check
make tsc

# Manual testing
make watch-backend   # Terminal 1
make watch-frontend  # Terminal 2
```

## Code Style

- Go: `gofmt`, `golint`
- TypeScript: Prettier (run `npm run format:write` in `web/`)
- Commits: Conventional commits preferred (`feat:`, `fix:`, `docs:`)
