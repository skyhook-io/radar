# Multi-Context Support Plan (Issue #199)

## Context

Issue #199: Users want to view multiple Kubernetes clusters simultaneously in different browser tabs without running separate Radar instances on different ports. Currently, switching context is a global operation — changing context in one tab affects all tabs.

The recent `pkg/k8score` extraction made the core caching engine multi-instance-ready (plain struct, not singleton), but the `internal/k8s/` wrapper layer and all server handlers still use process-global singletons. This plan designs the full end-to-end architecture for multi-context support.

## Architecture Overview

### Current State: Everything is a Singleton

**12 backend singletons** accessed via `Get*()` functions:

| Singleton | Package | Global Var | Init Pattern |
|-----------|---------|-----------|--------------|
| K8s clients (4 vars) | `internal/k8s/client.go` | `k8sClient`, `k8sConfig`, `discoveryClient`, `dynamicClient` | `sync.Once` + `clientMu` RWMutex |
| Resource cache | `internal/k8s/cache.go` | `resourceCache` | `sync.Once` wrapping `pkg/k8score.ResourceCache` |
| Dynamic cache | `internal/k8s/dynamic_cache.go` | `dynamicResourceCache` | `sync.Once` wrapping `pkg/k8score.DynamicResourceCache` |
| Discovery | `internal/k8s/discovery.go` | `resourceDiscovery` | `sync.Once` |
| Metrics history | `internal/k8s/metrics_history.go` | singleton | `sync.Once` |
| Connection state | `internal/k8s/connection_state.go` | `connectionStatus` | global |
| Capabilities | `internal/k8s/capabilities.go` | `cachedCapabilities` | global + TTL |
| Helm | `internal/helm/client.go` | `globalClient` | `sync.Once` |
| Timeline | `internal/timeline/manager.go` | `globalStore` | `sync.Once` |
| Traffic | `internal/traffic/manager.go` | `manager` | `sync.Once` |
| Prometheus | `internal/prometheus/client.go` | `globalClient` | global |

**Server access pattern**: 109 `k8s.Get*()` calls across 14 handler files + 23 in MCP. All handlers call singletons directly.

**Frontend**: `API_BASE = '/api'` hardcoded. 70 query hooks with 86 query keys — none context-aware. Single SSE connection. Context switch nukes entire React Query cache.

### Target State: Per-Context Sessions

A `ClusterSession` struct holds all per-context state. A `SessionManager` maintains `map[string]*ClusterSession` with lazy init and LRU eviction. Chi middleware extracts context from `X-Radar-Context` header, injects session into `context.Context`. Handlers access session instead of globals.

```
Browser Tab A (context: gke-prod)          Browser Tab B (context: eks-staging)
    │ X-Radar-Context: gke-prod               │ X-Radar-Context: eks-staging
    ▼                                          ▼
┌─────────────────────────────────────────────────┐
│              Chi Middleware                       │
│  Extract header → SessionManager.GetOrCreate()   │
│  Inject ClusterSession into context.Context      │
└──────────────┬───────────────────────┬───────────┘
               ▼                       ▼
    ┌──────────────────┐    ┌──────────────────┐
    │  ClusterSession   │    │  ClusterSession   │
    │  "gke-prod"       │    │  "eks-staging"    │
    │  ├─ ResourceCache │    │  ├─ ResourceCache │
    │  ├─ DynamicCache  │    │  ├─ DynamicCache  │
    │  ├─ HelmClient    │    │  ├─ HelmClient    │
    │  ├─ Broadcaster   │    │  ├─ Broadcaster   │
    │  ├─ Timeline      │    │  ├─ Timeline      │
    │  └─ ...           │    │  └─ ...           │
    └──────────────────┘    └──────────────────┘
```

---

## Design Decisions

### 1. Context routing: `X-Radar-Context` header
- URL prefix (`/api/ctx/{name}/...`) is problematic: context names often contain `:`, `/`, `_` (AWS ARNs, GKE names). URL encoding creates ugly paths.
- Query param (`?context=name`) pollutes URLs and interacts with existing params.
- Header is clean: one middleware extracts it, fallback (no header) = default context for backward compat.
- **Exception**: SSE (`EventSource` API doesn't support custom headers) → pass context as query param `?context=name` on the SSE endpoint only.

### 2. Session struct: `ClusterSession` holding all per-context state
- Single struct vs each subsystem managing its own map → single struct is cleaner, easier lifecycle management.
- Import cycles: external subsystems (helm, timeline, traffic, prometheus) stored as interfaces to avoid `k8s→helm→k8s` cycles. Same callback registration pattern already used by `context_manager.go`.

### 3. SSE: Per-session broadcaster
- Each `ClusterSession` gets its own `SSEBroadcaster` watching its own cache's `Changes()` channel.
- Simpler than multiplexing events from multiple contexts on one connection.
- Tab connects to SSE with `?context=name`, middleware routes to correct session's broadcaster.

### 4. Frontend: Header injection + context-aware query keys
- `fetchJSON` injects `X-Radar-Context` header from React context provider.
- All query keys prepend active context: `[contextName, 'dashboard', ...]`.
- Context switcher becomes tab-local: sets active context in React state, invalidates queries, reconnects SSE.
- No URL path change needed (context is per-tab state, not part of URL).

### 5. Lifecycle: Lazy init + LRU eviction
- Sessions created on first request for a context.
- `--max-contexts` flag (default: 3) limits concurrent sessions.
- LRU eviction when limit reached (stops informers, frees memory).
- ~100-200MB per session for a cluster with 10K resources.

---

## Implementation Phases

### Phase 0: Refactor Singletons to Support Per-Session Instantiation (no behavior change)

**Goal**: Make it possible to create multiple instances of each subsystem without changing any external behavior. All existing `Get*()` functions continue to work.

#### 0.1: Create `ClusterSession` struct
**New file**: `internal/k8s/session.go`

```go
type ClusterSession struct {
    ContextName     string
    ClusterName     string
    Client          *kubernetes.Clientset
    Config          *rest.Config
    DiscoveryClient *discovery.DiscoveryClient
    DynamicClient   dynamic.Interface
    ResourceCache   *ResourceCache
    DynamicCache    *DynamicResourceCache
    Discovery       *ResourceDiscovery
    MetricsHistory  *MetricsHistoryStore
    Connection      ConnectionState
    // External subsystems as interfaces (break import cycles)
    Helm            any // *helm.Client
    Timeline        any // timeline.EventStore
    Traffic         any // *traffic.Manager
    Prometheus      any // *prometheus.Client
    // Lifecycle
    ctx             context.Context
    cancel          context.CancelFunc
    lastAccessed    time.Time
    mu              sync.RWMutex
}

// context.Context key for request-scoped session
func SessionFromContext(ctx context.Context) *ClusterSession
func ContextWithSession(ctx context.Context, s *ClusterSession) context.Context
```

#### 0.2: Create `SessionManager`
**New file**: `internal/k8s/session_manager.go`

```go
type SessionManager struct {
    sessions     map[string]*ClusterSession
    defaultCtx   string
    maxSessions  int
    mu           sync.RWMutex
    // Shared kubeconfig state
    kubeconfigPath  string
    kubeconfigPaths []string
}

func NewSessionManager(maxSessions int) *SessionManager
func (sm *SessionManager) GetOrCreate(contextName string) (*ClusterSession, error)
func (sm *SessionManager) Get(contextName string) *ClusterSession
func (sm *SessionManager) GetDefault() *ClusterSession
func (sm *SessionManager) Remove(contextName string)
func (sm *SessionManager) ActiveSessions() []*ClusterSession
```

#### 0.3: Add factory functions alongside existing singletons
Each subsystem gets a `NewXForSession()` factory. Existing `Init*/Get*/Reset*` continue to delegate to a "default" session internally.

| File | New Function |
|------|-------------|
| `internal/k8s/cache.go` | `NewResourceCacheForSession(session)` |
| `internal/k8s/dynamic_cache.go` | `NewDynamicCacheForSession(session)` |
| `internal/k8s/discovery.go` | `NewDiscoveryForSession(session)` |
| `internal/k8s/metrics_history.go` | `NewMetricsHistoryForSession(session)` |
| `internal/k8s/capabilities.go` | `NewCapabilitiesForSession(session)` |
| `internal/helm/client.go` | `NewClientForSession(kubeconfig)` |
| `internal/timeline/manager.go` | `NewStoreForSession(config)` |
| `internal/traffic/manager.go` | `NewManagerForSession(client, config)` |
| `internal/prometheus/client.go` | `NewClientForSession(client, config)` |

#### 0.4: Refactor `subsystems.go` to operate on a session
`InitAllSubsystems` and `ResetAllSubsystems` currently use globals. Refactor to accept a `*ClusterSession` and populate its fields. The existing global versions become thin wrappers that operate on the default session.

**Verification**: All existing tests pass. `make build` succeeds. No behavior change.

---

### Phase 1: Backend Multi-Session Routing

**Goal**: Route requests to the correct `ClusterSession` based on header. No header = default session (backward compat).

#### 1.1: Chi context middleware
**New file**: `internal/server/context_middleware.go`

```go
func (s *Server) contextMiddleware(next http.Handler) http.Handler {
    // Extract X-Radar-Context header (or ?context= for SSE)
    // Call sessionManager.GetOrCreate(contextName)
    // Inject session into r.Context()
    // Fallback: default session when no header
}
```

Register in `setupRoutes()` on the `/api` route group.

#### 1.2: Add session accessors to Server
```go
func (s *Server) session(r *http.Request) *ClusterSession
func (s *Server) cache(r *http.Request) *ResourceCache
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) *ClusterSession
```

#### 1.3: Migrate handlers from globals to session
Replace all 109 `k8s.Get*()` calls in `internal/server/` with `s.session(r).Field`:

| File | `k8s.Get*` calls | Notes |
|------|-------------------|-------|
| `server.go` | 34 | Largest file, topology + resources |
| `dashboard.go` | 9 | |
| `diagnostics.go` | 9 | |
| `copy.go` | 8 | |
| `workload_logs.go` | 8 | |
| `portforward.go` | 7 | Also need per-session session tracking |
| `traffic_handlers.go` | 7 | Uses `traffic.GetManager()` |
| `logs.go` | 5 | |
| `argo_handlers.go` | 5 | |
| `flux_handlers.go` | 3 | |
| `resource_counts.go` | 3 | |
| `certificate.go` | 2 | |
| `ai_handlers.go` | 2 | |
| `exec.go` | 2 | |
| `sse.go` | 12 | Move to per-session broadcaster |

Also: 23 calls in `internal/mcp/` (tools.go, tools_workloads.go, resources.go, tools_gitops.go).

**Pattern**: `k8s.GetResourceCache()` → `s.session(r).ResourceCache`

#### 1.4: Per-session SSE broadcaster
- Each `ClusterSession` owns an `SSEBroadcaster`
- SSE endpoint extracts session from middleware context (via `?context=` query param since EventSource doesn't support headers)
- `watchResourceChanges` watches session's cache changes channel
- Remove global `context_changed` SSE event — context changes are now per-tab

#### 1.5: Revise `handleSwitchContext`
Old behavior: destructive global switch via `PerformContextSwitch()`.
New behavior: `GetOrCreate(contextName)` — lazy init, no teardown of other sessions. The old endpoint remains for backward compat but only affects the "default" session (for CLI/MCP clients that don't send the header).

#### 1.6: Session lifecycle APIs
- `GET /api/sessions` — list active sessions with last access time, resource counts
- `DELETE /api/sessions/{context}` — explicitly tear down a session
- `GET /api/contexts` — unchanged, lists available kubeconfig contexts

**Verification**: All existing tests pass. Single-tab usage unchanged. Can test with `curl -H "X-Radar-Context: other-ctx"`.

---

### Phase 2: Frontend Multi-Context

**Goal**: Different browser tabs can independently choose which context they're viewing.

#### 2.1: Context-aware API client
**File**: `web/src/api/client.ts`

```typescript
let activeContext: string | null = null
export function setActiveContext(ctx: string | null) { activeContext = ctx }

export async function fetchJSON<T>(path: string): Promise<T> {
    const headers: Record<string, string> = {}
    if (activeContext) headers['X-Radar-Context'] = activeContext
    const response = await fetch(`${API_BASE}${path}`, { headers })
    // ...
}
```

#### 2.2: Context-aware query keys
Update all 86 query key definitions:

```typescript
// Before: queryKey: ['dashboard', namespaces]
// After:  queryKey: [activeContext, 'dashboard', namespaces]
```

#### 2.3: Context-aware SSE
**File**: `web/src/hooks/useEventSource.ts`

SSE URL gets `?context=name` parameter. Remove global `context_changed` handling. When tab switches context: close old EventSource, open new one with new context param, invalidate queries for old context.

#### 2.4: Update ConnectionContext
**File**: `web/src/context/ConnectionContext.tsx`

Connection state becomes per-context. Poll `/api/connection` with `X-Radar-Context` header. Each tab tracks its own connection state independently.

#### 2.5: Update ContextSwitcher
**File**: `web/src/components/ContextSwitcher.tsx`

Change from global destructive switch to tab-local state change:
1. User selects new context from dropdown
2. `setActiveContext(name)` updates tab-local state
3. React Query cache for old context remains (can be revisited)
4. SSE reconnects to new context
5. No `POST /api/contexts/{name}` needed — backend lazy-inits on first request

#### 2.6: Update component imports
46 files import from `api/client`. Most don't need changes since context threading happens inside `fetchJSON` and query keys. Files that directly reference connection state or context switching need updates.

**Verification**: Open two tabs, select different contexts, verify independent data.

---

### Phase 3: Lifecycle Management

#### 3.1: `--max-contexts` CLI flag
Default: 3. Add to `cmd/explorer/main.go`.

#### 3.2: LRU eviction
`SessionManager.GetOrCreate()` checks count. If at limit, evicts least-recently-accessed session (stops informers, frees cache). Touch `lastAccessed` on every request via middleware.

#### 3.3: Session status UI
Frontend shows active sessions indicator. Clicking shows which contexts are loaded, memory usage, last access. Option to explicitly disconnect a context.

#### 3.4: Timeline handling
Shared timeline store with `context` field on events, filtered on query. Keeps single timeline view across contexts while supporting per-context filtering.

---

## Quantified Effort

| Phase | Files Changed | Estimated LOC | Risk |
|-------|--------------|---------------|------|
| Phase 0 | ~12 | ~500 new + ~200 refactored | Low (no behavior change) |
| Phase 1 | ~18 backend | ~800 refactored | Medium (handler migration is mechanical but wide) |
| Phase 2 | ~50 frontend | ~300 refactored | Medium (query key changes are mechanical) |
| Phase 3 | ~5 | ~200 new | Low |

**Total**: ~85 files, ~2000 LOC changed. Each phase is independently shippable.

## Key Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Memory (N × informer sets) | 100-200MB per context | `--max-contexts` default 3, LRU eviction |
| Helm SDK context names | Special chars in context names | Test with AWS ARN and GKE context names |
| Port forward/exec session isolation | Leaked sessions after eviction | Per-session session tracking, cleanup on eviction |
| EventSource no custom headers | Can't use header for SSE | Use `?context=` query param for SSE only |
| MCP context awareness | AI tools need to specify context | MCP HTTP endpoint gets header from HTTP layer |

## Verification

- **Phase 0**: `make build && go test ./... && make tsc` — all pass, no behavior change
- **Phase 1**: `curl -H "X-Radar-Context: ctx-a" /api/health` vs `curl -H "X-Radar-Context: ctx-b" /api/health` return different cluster info
- **Phase 2**: Open two browser tabs, select different contexts, verify independent topology/resources/helm
- **Phase 3**: Open 4 contexts with `--max-contexts=3`, verify LRU eviction of oldest
