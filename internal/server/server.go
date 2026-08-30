package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	pathpkg "path"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"golang.org/x/sync/singleflight"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/updater"
	"github.com/skyhook-io/radar/internal/upgrade"
	"github.com/skyhook-io/radar/internal/version"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/hpadiag"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/perfstats"
	"github.com/skyhook-io/radar/pkg/rbac"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// Server is the Explorer HTTP server
type Server struct {
	router             *chi.Mux
	broadcaster        *SSEBroadcaster
	vitalsMetrics      vitalsMetricsMemo
	port               int
	listenAddress      string
	basePath           string
	startupLog         bool
	remoteAccessHint   bool
	devMode            bool
	staticFS           fs.FS
	startTime          time.Time
	listener           net.Listener
	updater            *updater.Updater
	mcpHandler         http.Handler
	mcpReadOnlyHandler http.Handler
	diagConfig         *DiagConfig
	effectiveConfig    *config.Config // running config for GET /api/config
	openCostCurrency   *opencost.CurrencyResolver
	currencyManaged    bool
	authConfig         auth.Config
	permCache          *auth.PermissionCache
	oidcHandler        *auth.OIDCHandler
	saveFileFunc       func(defaultFilename string, data []byte) (string, error)
	cloudConnectCfg    CloudConnectConfig
	cloudInstall       *cloudInstallManager
	browserReportMu    sync.Mutex
	browserReports     map[string]map[string]struct{}
	browserReportSlots chan struct{}

	// nsPreferences holds each user's active-namespace pick from the in-app
	// switcher. Key shape: "<username>\x00<contextName>" when auth is enabled,
	// "\x00<contextName>" when auth is disabled. Cleared on context switch
	// (new cluster ⇒ old picks meaningless). The pick is a per-user view
	// filter — intersected with the user's RBAC-allowed namespaces at read
	// time. Picking does NOT narrow the shared informer cache (would corrupt
	// other users' views).
	nsPreferences sync.Map

	// scopeMutationMu serializes a forced (--namespace-scope) namespace change so
	// its persisted pick and the live cache scope move as one commit. Without it,
	// two concurrent rescope requests could persist one namespace while the cache
	// ends on another (PerformNamespaceRescope's own lock only serializes the
	// rebuild, not this handler's persist step).
	scopeMutationMu sync.Mutex

	// rootHintOnce keeps the base-path misconfiguration hint to a single log
	// line no matter how many requests reach the origin root.
	rootHintOnce sync.Once

	// nsPickMu serializes namespace-pick mutations: the POST handler's
	// persist+set pair and the read-path stale-pick prune. Without it, a
	// prune computed from a stale snapshot can land after a user's fresh
	// pick and silently revert it.
	nsPickMu sync.Mutex

	// seededPicks marks (user, context) keys whose picker was already seeded
	// from --namespaces, so the configured list applies once per session and
	// a user's clear back to "All namespaces" is not overridden on later
	// reads. Cleared alongside nsPreferences on context switch.
	seededPicks sync.Map

	// Short-TTL cache for topology builds. The Topology graph is a
	// deterministic projection of the informer cache; rebuilding it walks
	// every resource of every kind. A 5s TTL absorbs the typical bursts
	// (page-load tree+insights, in-flight 2s polling, dashboard widgets)
	// without user-visible staleness — controllers reconcile far slower.
	topoMemo *topology.Memoizer

	// Short-TTL cache for the RBAC reverse-lookup index. A SA detail
	// page fires multiple /api/rbac/* calls in quick succession (subject
	// lookup + role lookup for each linked role); cache absorbs the
	// burst. Index is a pure projection of four cached listers — TTL has
	// no semantic effect.
	rbacMemo *rbac.Memoizer

	capacityIssueMemo *capacityIssueMemo

	workloadRevisionMu    sync.Mutex
	workloadRevisionCache map[string]workloadRevisionTargetCacheEntry

	yamlSchemaMu          sync.Mutex
	yamlSchemaCache       map[string][]byte
	yamlSchemaPathCache   map[string]yamlSchemaPathCacheEntry
	yamlSchemaBundleCache map[string]yamlSchemaBundleCacheEntry
	yamlSchemaCacheBytes  int
	yamlSchemaFetchGroup  singleflight.Group

	// aiDiagnoser drives a local agent CLI for "Diagnose with AI" (nil when no
	// CLI is on PATH — the endpoints then 501). Resolved once at startup.
	aiDiagnoser *ai.Diagnoser
	// aiRuns owns investigations as durable server-side jobs (survive panel close
	// / navigation / refresh). nil exactly when aiDiagnoser is.
	aiRuns *ai.RunManager
}

// Config holds server configuration
type Config struct {
	Port               int
	ListenAddress      string         // 127.0.0.1/localhost for local-only; 0.0.0.0 for shared access
	BasePath           string         // Optional URL path prefix for self-hosted subpath deployments
	StartupLog         bool           // Emit the operator-facing startup block after a successful bind
	RemoteAccessHint   bool           // Explain the explicit shared-listener opt-in (native CLI only)
	DevMode            bool           // Serve frontend from filesystem instead of embedded
	StaticFS           embed.FS       // Embedded frontend files
	StaticRoot         string         // Path within StaticFS
	MCPHandler         http.Handler   // MCP server handler (nil = MCP disabled)
	MCPReadOnlyHandler http.Handler   // read-only MCP handler (read tools only)
	DiagConfig         *DiagConfig    // Sanitized config for diagnostics endpoint
	EffectiveConfig    *config.Config // Running startup config for GET /api/config
	OpenCostCurrency   string         // ISO 4217 code labeling values returned by OpenCost endpoints
	OpenCostManaged    bool           // true when an explicit CLI/Helm flag owns the running value
	AuthConfig         auth.Config    // Authentication configuration
	AIHistoryDB        string         // AI run-history SQLite path ("" = memory-only runs)
	CloudConnect       CloudConnectConfig
}

// New creates a new server instance
func New(cfg Config) *Server {
	cfg.AuthConfig.Defaults()
	basePath, err := NormalizeBasePath(cfg.BasePath)
	if err != nil {
		log.Fatalf("Invalid base path %q: %v", cfg.BasePath, err)
	}
	if cfg.CloudConnect.HubAPIURL == "" {
		cfg.CloudConnect.HubAPIURL = "https://api.radarhq.io"
	}
	if cfg.CloudConnect.HubAppURL == "" {
		cfg.CloudConnect.HubAppURL = "https://app.radarhq.io"
	}
	s := &Server{
		router:                chi.NewRouter(),
		broadcaster:           NewSSEBroadcaster(),
		port:                  cfg.Port,
		listenAddress:         cfg.ListenAddress,
		basePath:              basePath,
		startupLog:            cfg.StartupLog,
		remoteAccessHint:      cfg.RemoteAccessHint,
		devMode:               cfg.DevMode,
		startTime:             time.Now(),
		mcpHandler:            cfg.MCPHandler,
		mcpReadOnlyHandler:    cfg.MCPReadOnlyHandler,
		diagConfig:            cfg.DiagConfig,
		effectiveConfig:       cfg.EffectiveConfig,
		openCostCurrency:      opencost.NewCurrencyResolver(cfg.OpenCostCurrency),
		currencyManaged:       cfg.OpenCostManaged,
		authConfig:            cfg.AuthConfig,
		cloudConnectCfg:       cfg.CloudConnect,
		topoMemo:              topology.NewMemoizer(5 * time.Second),
		rbacMemo:              rbac.NewMemoizer(5 * time.Second),
		capacityIssueMemo:     newCapacityIssueMemo(5 * time.Second),
		yamlSchemaCache:       make(map[string][]byte),
		yamlSchemaPathCache:   make(map[string]yamlSchemaPathCacheEntry),
		yamlSchemaBundleCache: make(map[string]yamlSchemaBundleCacheEntry),
	}
	s.cloudInstall = newCloudInstallManager(cfg.CloudConnect)
	s.cloudInstall.sharedListener = s.sharedListener

	// Resolve a local agent CLI for AI diagnosis (keyless, on the user's own
	// subscription). nil when none is found — the feature stays disabled.
	//
	// Gated to no-auth (local/standalone) Radar: the engine drives the CLI
	// against this server's OWN localhost /mcp with no credentials, which only
	// works when /mcp is unauthenticated. Under proxy/OIDC auth (team / cloud
	// deployments) the MCP requires identity headers the local CLI can't supply,
	// and AI diagnosis is the embedding host's job (e.g. Radar Hub) anyway.
	// Also requires /mcp to be mounted — the agent reaches the cluster only
	// through it, so with --no-mcp the feature can't work.
	if !s.authConfig.Enabled() && s.mcpHandler != nil {
		if d, err := ai.NewDetected(context.Background()); err == nil {
			s.aiDiagnoser = d
			// History store opens only when the engine actually enables, so a
			// disabled feature never creates the DB. Open failure degrades to
			// memory-only runs (the historical behavior), never blocks startup.
			var store ai.RunStore
			historyBroken := false
			if cfg.AIHistoryDB != "" {
				if st, err := ai.OpenRunStore(cfg.AIHistoryDB); err != nil {
					log.Printf("[ai] run history disabled — could not open %s: %v", cfg.AIHistoryDB, err)
					historyBroken = true
				} else {
					store = st
				}
			}
			s.aiRuns = ai.NewRunManager(d, s.ActualPort, s.basePath, k8s.GetContextName, store)
			if historyBroken {
				// Persistence was requested but isn't working — the UI must say
				// history won't survive a restart, not just a log line.
				s.aiRuns.MarkHistoryUnavailable(cfg.AIHistoryDB)
			}
		}
	}

	// Register a single context-switch callback so every PerformContextSwitch
	// path (REST switch, CAPI connect, periodic re-auth, …) gets per-user
	// state cleared automatically. Fires inside step 5 of the swap, strictly
	// before PerformContextSwitch returns. Mirrors the MCP package's pattern
	// for mcpPermCache.
	k8s.OnContextSwitch(func(_ string) {
		s.finalizePostContextSwitch()
		// Alongside the subsystem resets in PerformContextSwitch (prometheus,
		// traffic, helm): the Argo CD connection references the previous
		// cluster's endpoint/port-forward.
		argocd.Reset()
	})
	// Cancel + stale AI investigations BEFORE the client repoints at the new
	// cluster, so an in-flight agent (especially an apply) can't write to it.
	k8s.OnBeforeContextSwitch(func(_ string) {
		if s.aiRuns != nil {
			s.aiRuns.OnContextSwitch()
		}
		// Runtime auth-loss demotion fires ONLY this callback (quiesce in
		// place, no switch follows), and Argo CD's private port-forward lives
		// outside the session manager — without this it survives the
		// demotion's teardown indefinitely. Reset is idempotent, so the
		// second call from OnContextSwitch on a real switch is harmless.
		argocd.Reset()
	})

	// Let the destructive cache operations (context switch, namespace rescope)
	// terminate active sessions at their point of no return, rather than the
	// handlers stopping them up front — a switch/rescope that fails before
	// teardown must leave port-forwards / exec terminals intact.
	k8s.SetSessionStopper(StopAllSessions)

	// Initialize auth components when auth is enabled
	if s.authConfig.Enabled() {
		// Stamp cache entries with the current K8s context so an in-flight
		// request mid-context-switch can't use the previous cluster's
		// AllowedNamespaces / canI results to authorize the new cluster's
		// reads. Without the stamp, the window between PerformContextSwitch
		// step 2 (client swap) and the post-switch invalidation is exploitable.
		s.permCache = auth.NewPermissionCache().WithContextName(k8s.GetContextName)

		if s.authConfig.Mode == "oidc" {
			// Validate required OIDC fields before attempting provider discovery
			if s.authConfig.OIDCIssuer == "" {
				log.Fatalf("[auth] --auth-oidc-issuer is required when auth-mode=oidc")
			}
			if s.authConfig.OIDCClientID == "" {
				log.Fatalf("[auth] --auth-oidc-client-id is required when auth-mode=oidc")
			}
			if s.authConfig.OIDCClientSecret == "" {
				log.Fatalf("[auth] OIDC client secret is required when auth-mode=oidc (set --auth-oidc-client-secret flag or RADAR_OIDC_CLIENT_SECRET env var)")
			}
			if s.authConfig.OIDCRedirectURL == "" {
				log.Fatalf("[auth] --auth-oidc-redirect-url is required when auth-mode=oidc")
			}
			oidcHandler, err := auth.NewOIDCHandler(context.Background(), s.authConfig, basePath)
			if err != nil {
				log.Fatalf("[auth] OIDC initialization failed (issuer=%s): %v — cannot start with auth-mode=oidc", s.authConfig.OIDCIssuer, err)
			}

			// Wire up backchannel logout revocation store
			if s.authConfig.OIDCBackchannelLogout {
				revoker := auth.NewMemoryRevoker()
				oidcHandler.SetRevoker(revoker)
				s.authConfig.Revoker = revoker // middleware uses this for IsRevoked checks
			}

			s.oidcHandler = oidcHandler
		}

	}

	// Set up static file system
	if !cfg.DevMode && cfg.StaticRoot != "" {
		subFS, err := fs.Sub(cfg.StaticFS, cfg.StaticRoot)
		if err == nil {
			s.staticFS = subFS
		}
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	if s.basePath != "" {
		appRouter := chi.NewRouter()
		s.setupAppRoutes(appRouter)
		s.router.Get("/", func(w http.ResponseWriter, r *http.Request) {
			s.hintRootRequestUnderBasePath()
			http.Redirect(w, r, s.basePath+"/"+querySuffix(r), http.StatusFound)
		})
		s.router.Mount(s.basePath, s.basePathHandler(appRouter))
		return
	}
	s.setupAppRoutes(s.router)
}

// basePathHandler adapts the prefixed public URL space to the app router, which
// is written as if it owned the origin's root.
//
// The prefix MUST be stripped from r.URL.Path before any app middleware runs.
// chi's Mount only rewrites the routing context's RoutePath and leaves
// r.URL.Path prefixed, while the auth middleware matches on r.URL.Path and
// treats anything outside /api, /mcp and /debug as public static content — so a
// still-prefixed path reads as public and skips authentication entirely,
// /debug/pprof (which dumps the whole informer cache) included. Translating once
// here keeps that concern at the edge rather than in every path check inside.
func (s *Server) basePathHandler(app http.Handler) http.Handler {
	stripped := http.StripPrefix(s.basePath, app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Canonicalize the bare prefix to a trailing slash so the app always
		// sees a rooted path.
		if r.URL.Path == s.basePath {
			http.Redirect(w, r, s.basePath+"/"+querySuffix(r), http.StatusMovedPermanently)
			return
		}
		stripped.ServeHTTP(w, r)
	})
}

// hintRootRequestUnderBasePath explains, once, the misconfiguration that
// otherwise presents only as an unexplained browser redirect loop: an ingress
// that strips the prefix sitting in front of a Radar that also serves under it.
// Radar sends the browser to {basePath}/, the ingress strips it again, and the
// two bounce until the browser gives up with ERR_TOO_MANY_REDIRECTS.
//
// Phrased as a hint rather than an error because reaching / is legitimate — a
// port-forward straight to the pod lands here, as does an ingress that routes
// the origin root through as well.
func (s *Server) hintRootRequestUnderBasePath() {
	s.rootHintOnce.Do(func() {
		log.Printf("[base-path] serving under %s; a request arrived for / and was redirected to %s/. "+
			"If the browser reports too many redirects, the ingress in front of Radar is stripping the prefix: "+
			"either stop stripping it, or unset --base-path / chart basePath.", s.basePath, s.basePath)
	})
}

func querySuffix(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

func (s *Server) setupAppRoutes(r chi.Router) {

	// Middleware (applied to all routes)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Note: Timeout middleware is applied per-group below to exempt streaming endpoints

	// gzip response compression (content-type aware: JSON yes, SSE/WS no).
	// nil when RADAR_COMPRESS_LEVEL=0. See compress.go.
	if cm := compressMiddleware(); cm != nil {
		r.Use(cm)
	}

	// CORS for development
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Content-Type"},
		// Without an expose entry, cross-origin JS reads these as "" and the
		// timeline client silently falls back to full-ring refetches.
		ExposedHeaders:   []string{"X-Radar-Timeline-Epoch", "X-Radar-Timeline-Max-Seq", "X-Radar-Timeline-Min-Seq"},
		AllowCredentials: true,
	}))

	// Auth middleware (when auth is enabled)
	if s.authConfig.Enabled() {
		r.Use(auth.Authenticate(s.authConfig))
	}

	// Auth routes
	if s.oidcHandler != nil {
		r.Get("/auth/login", s.oidcHandler.HandleLogin)
		r.Get("/auth/callback", s.oidcHandler.HandleCallback)
		r.Get("/auth/logout", s.oidcHandler.HandleLogout)
		if s.authConfig.OIDCBackchannelLogout {
			r.Post("/auth/backchannel-logout", s.oidcHandler.HandleBackchannelLogout)
		}
	} else if s.authConfig.Enabled() {
		// Proxy mode: register a simple logout that clears the session cookie
		r.Get("/auth/logout", s.handleLogout)
	}

	metricsHandler := s.newMetricsHandler()
	r.Get("/metrics", metricsHandler.ServeHTTP)

	// pprof routes for profiling. Not mounted under cloud-mode — they'd be
	// reachable via the Cloud tunnel and leak the in-memory K8s cache (every
	// Secret, ConfigMap, Pod spec) via /debug/pprof/heap. Local/standalone
	// installs keep them for debugging.
	if !cloudMode() {
		r.Route("/debug/pprof", func(r chi.Router) {
			r.Get("/", pprof.Index)
			r.Get("/cmdline", pprof.Cmdline)
			r.Get("/profile", pprof.Profile)
			r.Get("/symbol", pprof.Symbol)
			r.Get("/trace", pprof.Trace)
			r.Get("/allocs", pprof.Handler("allocs").ServeHTTP)
			r.Get("/block", pprof.Handler("block").ServeHTTP)
			r.Get("/goroutine", pprof.Handler("goroutine").ServeHTTP)
			r.Get("/heap", pprof.Handler("heap").ServeHTTP)
			r.Get("/mutex", pprof.Handler("mutex").ServeHTTP)
			r.Get("/threadcreate", pprof.Handler("threadcreate").ServeHTTP)
			r.Get("/goroutineleak", pprof.Handler("goroutineleak").ServeHTTP) // requires GOEXPERIMENT=goroutineleakprofile at build time
		})
	}

	// API routes
	r.Route("/api", func(r chi.Router) {
		// Streaming endpoints (SSE/WebSocket) - no timeout
		r.Get("/events/stream", s.handleSSE)
		r.Get("/pods/{namespace}/{name}/logs/stream", s.handlePodLogsStream)
		r.Get("/pods/{namespace}/{name}/exec", s.handlePodExec)
		r.Get("/local-terminal", s.handleLocalTerminal)
		r.Get("/pods/{namespace}/{name}/files/download", s.handlePodFileDownload)
		r.Get("/workloads/{kind}/{namespace}/{name}/logs/stream", s.handleWorkloadLogsStream)
		// AI investigation event stream via SSE — long-lived; lives outside the
		// 60s timeout group. The run keeps going server-side after disconnect.
		r.Get("/diagnose/runs/{id}/stream", s.handleDiagnoseRunStream)

		// Node drain — outside 60s timeout group (drain may need minutes for PDB backoff)
		r.Post("/nodes/{name}/drain", s.handleDrainNode)

		// Cloud Connect prepare/start — outside the 60s timeout group. prepare
		// downloads and renders the chart and runs the exact-manifest
		// preflight; start probes cluster metadata and creates the Hub
		// request. Either can legitimately outlast 60s on a slow link, and
		// being killed mid-flight is worse than waiting. Both carry their own
		// bound (see cloudInstallHandlerTimeout).
		r.Post("/cloud/install/prepare", s.handleCloudInstallPrepare)
		r.Post("/cloud/install/start", s.handleCloudInstallStart)

		// All other API routes get a 60-second timeout
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(60 * time.Second))

			r.Get("/health", s.handleHealth)
			r.Get("/agents", s.handleListAgents)
			// AI investigations as durable server-side jobs (start/list/turn/stop).
			r.Post("/diagnose/runs", s.handleDiagnoseStart)
			r.Get("/diagnose/runs", s.handleDiagnoseList)
			r.Post("/diagnose/runs/{id}/turns", s.handleDiagnoseTurn)
			r.Post("/diagnose/runs/{id}/stop", s.handleDiagnoseStop)
			r.Post("/diagnose/history/clear", s.handleDiagnoseHistoryClear)
			r.Post("/diagnose/consent", s.handleDiagnoseConsent)
			r.Get("/diagnostics", s.handleDiagnostics)
			r.Get("/auth/me", s.handleAuthMe)
			r.Get("/version-check", s.handleVersionCheck)
			r.Get("/version-check/release", s.handleVersionCheckRelease)
			r.Post("/version-check/browser", s.handleVersionCheckBrowser)
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/vitals", s.handleVitals)
			r.Get("/dashboard/crds", s.handleDashboardCRDs)
			r.Get("/dashboard/helm", s.handleDashboardHelm)
			r.Get("/cluster-info", s.handleClusterInfo)
			r.Get("/capabilities", s.handleCapabilities)
			r.Get("/capacity", s.handleCapacityOverview)
			r.Get("/capacity/pools", s.handleCapacityPools)
			r.Get("/capacity/pools/{name}", s.handleCapacityPool)
			r.Get("/capacity/pools/{name}/members", s.handleCapacityPoolMembers)
			r.Get("/capacity/demand", s.handleCapacityDemand)
			r.Get("/capacity/activity", s.handleCapacityActivity)

			// In-product Cloud Connect driver lane; every handler re-checks
			// the gate (local + no auth + no tunnel). prepare/start are
			// registered above, outside the 60s timeout.
			r.Get("/cloud/install/status", s.handleCloudInstallStatus)
			r.Post("/cloud/install/cancel", s.handleCloudInstallCancel)
			r.Post("/cloud/install/dismiss", s.handleCloudInstallDismiss)
			r.Get("/cloud/connect/self", s.handleCloudConnectSelf)
			r.Get("/topology", s.handleTopology)
			r.Get("/gitops/tree/{kind}/{namespace}/{name}", s.handleGitOpsTree)
			r.Get("/gitops/insights/{kind}/{namespace}/{name}", s.handleGitOpsInsights)
			r.Get("/gitops/managed-resources", s.handleGitOpsManagedResources)

			// RBAC reverse-lookup endpoints. Two shapes for /subject:
			// ServiceAccount carries a namespace (3 segments after kind);
			// User and Group are cluster-wide (2 segments). chi disambiguates
			// by segment count. /role uses "_" as a sentinel for ClusterRole's
			// empty namespace because chi requires a literal segment.
			r.Get("/rbac/subject/{kind}/{namespace}/{name}", s.handleRBACSubject)
			r.Get("/rbac/subject/{kind}/{name}", s.handleRBACSubject)
			r.Get("/rbac/role/{kind}/{namespace}/{name}", s.handleRBACRole)
			r.Get("/rbac/namespace/{namespace}", s.handleRBACNamespace)
			r.Get("/rbac/whoami", s.handleRBACWhoami)
			r.Get("/cnpg/imagecatalogs/{namespace}/{name}/clusters", s.handleCNPGCatalogUsers)
			r.Get("/cnpg/clusterimagecatalogs/{name}/clusters", s.handleCNPGCatalogUsers)
			r.Get("/velero/backupstoragelocations/{namespace}/{name}/backups", s.handleVeleroStoredBackups)
			// POST: creates a DownloadRequest, which is the only supported way to
			// read the messages behind a run's error and warning counts.
			r.Post("/velero/{kind}/{namespace}/{name}/messages", s.handleVeleroRunMessages)

			r.Get("/namespaces", s.handleNamespaces)
			r.Get("/api-resources", s.handleAPIResources)
			r.Get("/resource-counts", s.handleResourceCounts)
			r.Get("/resources/{kind}", s.handleListResources)
			r.Get("/resources/{kind}/{namespace}/{name}", s.handleGetResource)
			r.Post("/resources/preview", s.handlePreviewResources)
			r.Post("/resources/schemas", s.handleResourceSchemas)
			r.Post("/resources/apply", s.handleApplyResource)
			r.Put("/resources/{kind}/{namespace}/{name}", s.handleUpdateResource)
			r.Get("/resources/{kind}/{namespace}/{name}/cascade-preview", s.handleCascadeDeletePreview)
			r.Delete("/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			r.Get("/secrets/certificate-expiry", s.handleSecretCertExpiry)
			r.Get("/certificates", s.handleCertificates)

			// Cluster audit
			r.Get("/audit", s.handleAudit)
			r.Get("/audit/resource/{kind}/{namespace}/{name}", s.handleAuditResource)

			// Policy findings for one resource. Separate from /audit so it can
			// carry its own coverage state: an empty list here means one of four
			// things and the response says which.
			r.Get("/policy/resource/{kind}/{namespace}/{name}", s.handlePolicyResource)
			// The inverse: every resource one policy recorded an outcome for.
			r.Get("/policy/policies/{policy}", s.handlePolicyCoverage)
			r.Get("/policy/policies/{policy}/queued", s.handlePolicyQueued)
			r.Get("/upgrade-readiness", s.handleUpgradeReadiness)

			// Network path trace - path-shaped diagnosis for Service /
			// Ingress / HTTPRoute / GRPCRoute / Gateway. See internal/trace.
			r.Get("/trace/{kind}/{namespace}/{name}", s.handleTrace)
			// Whether the active "test from inside the cluster" (a short-lived,
			// restricted, self-destructing probe Job as the caller's RBAC) can
			// run - gates the UI button and names the cluster + namespace the
			// probe pod would land in.
			r.Get("/trace/{kind}/{namespace}/{name}/probe-in-cluster/capability", s.handleProbeInClusterCapability)
			// Whole-subject in-cluster test: runs every route's live probe and folds
			// them in server-side (canonical merge), returning the finalized trace so
			// the frontend never reimplements a divergent merge.
			r.Post("/trace/{kind}/{namespace}/{name}/in-cluster", s.handleTraceInCluster)

			// Packages — merged "what's installed" view across Helm
			// releases, workload labels, CRD registrations, and GitOps
			// declarations. See pkg/packages for merge semantics.
			r.Get("/packages", s.handleListPackages)

			// Applications — the workload-centric twin of /packages: the
			// cluster's own services grouped by pkg/subject app-overlay,
			// anchored on container image:tag. See applications.go.
			r.Get("/applications", s.handleListApplications)
			r.Get("/applications/history", s.handleApplicationHistory)

			// Free-text resource search (name + namespace + labels +
			// annotations + container images). Used by the hub fan-out
			// for cross-cluster search; safe to call directly per-cluster.
			r.Get("/search", s.handleSearch)

			// Unified cluster-health endpoint — composes problems +
			// audit findings + warning events + generic CRD condition
			// fallback into one normalized list. Used by the hub
			// fan-out for cross-cluster issues.
			r.Get("/issues", s.handleIssues)
			r.Get("/issues/resource/{kind}/{namespace}/{name}", s.handleResourceIssues)
			r.Get("/settings/audit", s.handleGetAuditSettings)
			r.Put("/settings/audit", s.handlePutAuditSettings)
			r.Get("/events", s.handleEvents)
			r.Get("/changes", s.handleChanges)
			r.Get("/changes/{kind}/{namespace}/{name}/children", s.handleChangeChildren)
			// The shared timeline wire contract (NDJSON + terminal record) —
			// the same shape the hub serves; backs the web client's single
			// ring-and-delta timeline path.
			r.Get("/timeline/events", s.handleTimelineEvents)

			// Pod logs (non-streaming)
			r.Get("/pods/{namespace}/{name}/logs", s.handlePodLogs)
			r.Get("/pods/{namespace}/{name}/environment", s.handlePodEnvironment)
			r.Post("/pods/{namespace}/{name}/environment/reveal", s.handleRevealPodEnvironment)

			// Pod debug (ephemeral container)
			r.Post("/pods/{namespace}/{name}/debug", s.handleCreateDebugContainer)

			// Node debug (privileged debug pod)
			r.Post("/nodes/{name}/debug", s.handleNodeDebug)
			r.Delete("/nodes/{name}/debug", s.handleNodeDebugCleanup)

			// Node operations (cordon/uncordon)
			r.Post("/nodes/{name}/cordon", s.handleCordonNode)
			r.Post("/nodes/{name}/uncordon", s.handleUncordonNode)

			// Pod file browser
			r.Get("/pods/{namespace}/{name}/files", s.handlePodFileList)

			// Metrics (from metrics.k8s.io API)
			r.Get("/metrics/pods/{namespace}/{name}", s.handlePodMetrics)
			r.Get("/metrics/nodes/{name}", s.handleNodeMetrics)
			r.Get("/metrics/pods/{namespace}/{name}/history", s.handlePodMetricsHistory)
			r.Get("/metrics/nodes/{name}/history", s.handleNodeMetricsHistory)
			r.Get("/metrics/top/pods", s.handleTopPods)
			r.Get("/metrics/top/nodes", s.handleTopNodes)
			r.Get("/metrics/top/resources", s.handleTopResources)

			// Port forwarding
			r.Get("/portforwards", s.handleListPortForwards)
			r.Post("/portforwards", s.handleStartPortForward)
			r.Delete("/portforwards/{id}", s.handleStopPortForward)
			r.Get("/portforwards/available/{type}/{namespace}/{name}", s.handleGetAvailablePorts)

			// Curl a Service's HTTP endpoint server-side (direct in-cluster dial,
			// no credentials). Works in-cluster/Cloud where port-forward can't.
			r.Post("/curl/service", s.handleCurlService)

			// Active sessions (for context switch confirmation)
			r.Get("/sessions", s.handleGetSessions)

			// CronJob operations
			r.Post("/cronjobs/{namespace}/{name}/trigger", s.handleTriggerCronJob)
			r.Post("/cronjobs/{namespace}/{name}/suspend", s.handleSuspendCronJob)
			r.Post("/cronjobs/{namespace}/{name}/resume", s.handleResumeCronJob)

			// Workload restart, scale, rollback
			r.Post("/workloads/{kind}/{namespace}/{name}/restart", s.handleRestartWorkload)
			r.Post("/workloads/{kind}/{namespace}/{name}/scale", s.handleScaleWorkload)
			r.Get("/workloads/{kind}/{namespace}/{name}/images", s.handleGetWorkloadImages)
			r.Post("/workloads/{kind}/{namespace}/{name}/images", s.handleSetWorkloadImages)
			r.Get("/workloads/{kind}/{namespace}/{name}/revisions", s.handleWorkloadRevisions)
			r.Post("/workloads/{kind}/{namespace}/{name}/rollback", s.handleRollbackWorkload)

			// Workload logs (non-streaming)
			r.Get("/workloads/{kind}/{namespace}/{name}/logs", s.handleWorkloadLogs)
			r.Get("/workloads/{kind}/{namespace}/{name}/runs", s.handleWorkloadRuns)
			r.Get("/workloads/{kind}/{namespace}/{name}/pods", s.handleWorkloadPods)

			// Helm routes
			helmHandlers := helm.NewHandlers(s.resolveHelmNamespaces)
			helmHandlers.RegisterRoutes(r)

			// Image inspection routes
			imageHandlers := images.NewHandlers()
			imageHandlers.RegisterRoutes(r)

			// Prometheus metrics routes. The auth gate is required for endpoints
			// that read K8s spec data via the shared informer cache (rightsizing,
			// PVC usage) — the cache is populated under Radar's SA, so without
			// it any authenticated user could fetch any namespace's spec.
			//
			// Two checks here, both load-bearing:
			//   1. canRead (SAR) — does the user have RBAC for this verb on this
			//      resource? Catches missing-RBAC.
			//   2. getUserNamespaces — is the namespace in the user's discovered
			//      allow-list? Matches handleGetResource semantics on the main
			//      resource API. Without this, a user with cluster-wide SAR for
			//      "get" could read derived data via these endpoints in namespaces
			//      they're otherwise filtered out of (multi-tenant separation).
			prometheuspkg.SetAuthGate(func(req *http.Request, group, resource, namespace, verb string) bool {
				if !s.canRead(req, group, resource, namespace, verb) {
					return false
				}
				if namespace != "" && noNamespaceAccess(s.getUserNamespaces(req, []string{namespace})) {
					return false
				}
				return true
			})
			r.Post("/prometheus/rightsizing/scan", s.handleRightsizingScan)
			prometheuspkg.RegisterRoutes(r)

			// OpenCost routes
			r.Post("/opencost/application", s.handleOpenCostApplication)
			r.Post("/opencost/application/trend", s.handleOpenCostApplicationTrend)
			r.Get("/opencost/workload/{kind}/{namespace}/{name}", s.handleOpenCostWorkload)
			r.Get("/opencost/workload/{kind}/{namespace}/{name}/trend", s.handleOpenCostWorkloadTrend)
			opencost.RegisterRoutes(r, s.resolvedOpenCostCurrency)

			// FluxCD routes
			r.Post("/flux/{kind}/{namespace}/{name}/reconcile", s.handleFluxReconcile)
			r.Post("/flux/{kind}/{namespace}/{name}/sync-with-source", s.handleFluxSyncWithSource)
			r.Post("/flux/{kind}/{namespace}/{name}/suspend", s.handleFluxSuspend)
			r.Post("/flux/{kind}/{namespace}/{name}/resume", s.handleFluxResume)

			// Argo Rollouts progressive-delivery control plane. Rollback and
			// revision history are served by the /workloads routes above.
			r.Get("/rollouts/{namespace}/{name}/capabilities", s.handleRolloutCapabilities)
			r.Post("/rollouts/{namespace}/{name}/{action}", s.handleRolloutOperation)

			// ArgoCD routes
			r.Get("/argo/destinations", s.handleArgoDestinations)
			r.Post("/argo/applications/{namespace}/{name}/sync", s.handleArgoSync)
			r.Post("/argo/applications/{namespace}/{name}/validate-resource", s.handleArgoValidateResource)
			r.Post("/argo/applications/{namespace}/{name}/refresh", s.handleArgoRefresh)
			r.Post("/argo/applications/{namespace}/{name}/rollback", s.handleArgoRollback)
			r.Post("/argo/applications/{namespace}/{name}/terminate", s.handleArgoTerminate)
			r.Post("/argo/applications/{namespace}/{name}/suspend", s.handleArgoSuspend)
			r.Post("/argo/applications/{namespace}/{name}/resume", s.handleArgoResume)
			r.Get("/argo/applications/{namespace}/{name}/resource-diff", s.handleArgoResourceDiff)
			r.Get("/argo/applications/{namespace}/{name}/revision-metadata", s.handleArgoRevisionMetadata)

			// AI resource preview (minified output for MCP/debugging).
			// Mounted as a sub-group so agent-log middleware applies only
			// to /api/ai/* — UI-facing /api/resources/* stays untouched.
			r.Group(func(r chi.Router) {
				r.Use(aiAgentLogMiddleware)
				r.Get("/ai/resources/{kind}", s.handleAIListResources)
				r.Get("/ai/resources/{kind}/{namespace}/{name}", s.handleAIGetResource)
				r.Get("/ai/neighborhood/{kind}/{namespace}/{name}", s.handleAINeighborhood)
			})

			// Debug routes (for event pipeline diagnostics)
			r.Get("/debug/events", s.handleDebugEvents)
			r.Get("/debug/events/diagnose", s.handleDebugEventsDiagnose)
			r.Get("/debug/informers", s.handleDebugInformers)

			// Network policy evaluation
			r.Get("/network-policies/evaluate", s.handleEvaluateNetworkPolicies)

			// Traffic routes (non-streaming)
			r.Get("/traffic/sources", s.handleGetTrafficSources)
			r.Get("/traffic/flows", s.handleGetTrafficFlows)
			r.Get("/traffic/source", s.handleGetActiveTrafficSource)
			r.Post("/traffic/source", s.handleSetTrafficSource)
			r.Post("/traffic/connect", s.handleTrafficConnect)
			r.Get("/traffic/connection", s.handleTrafficConnectionStatus)

			// Context routes
			r.Get("/contexts", s.handleListContexts)
			r.Post("/contexts/{name}", s.handleSwitchContext)

			// Active namespace switcher (k9s :ns equivalent for the
			// namespace-scoped path; informational filter for cluster-wide users)
			r.Get("/cluster/namespace-scope", s.handleGetNamespaceScope)
			r.Post("/cluster/namespace", s.handleSetActiveNamespace)

			// CAPI routes
			r.Get("/capi/clusters/{ns}/{name}/kubeconfig", s.handleCAPIClusterKubeconfig)
			r.Post("/capi/clusters/{ns}/{name}/connect", s.handleCAPIClusterConnect)

			// Connection status routes (for graceful startup)
			r.Get("/connection", s.handleConnectionStatus)
			r.Post("/connection/retry", s.handleConnectionRetry)

			// GitHub star status and action
			r.Get("/github/starred", s.handleGitHubStarStatus)
			r.Post("/github/star", s.handleGitHubStar)
			r.Post("/github/dismiss", s.handleGitHubDismiss)

			// Self-upgrade: Hub calls this over the yamux tunnel to patch this
			// Deployment's image. Cloud-owner-gated; uses the SA client (not user
			// impersonation).
			// Requires MY_POD_NAMESPACE + MY_DEPLOYMENT_NAME env vars (set by
			// the Helm chart when rbac.selfUpgrade=true).
			r.Post("/agent/self-upgrade", s.handleSelfUpgrade)

			// Settings (persisted user preferences)
			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)

			// Config (persisted startup configuration)
			r.Get("/config", s.handleGetConfig)
			r.Put("/config", s.handlePutConfig)
			r.Put("/integrations/prometheus", s.handleApplyPrometheusURL)
			r.Put("/integrations/argocd", s.handleApplyArgoCDConfig)
			r.Get("/integrations/argocd/status", s.handleArgoCDStatus)

			// Desktop routes
			r.Post("/desktop/open-url", s.handleDesktopOpenURL)
			r.Post("/desktop/open-file", s.handleDesktopOpenFile)
			r.Post("/desktop/open-folder", s.handleDesktopOpenFolder)
			r.Post("/desktop/save-file", s.handleDesktopSaveFile)
			r.Post("/desktop/update", s.handleDesktopUpdateStart)
			r.Get("/desktop/update/status", s.handleDesktopUpdateStatus)
			r.Post("/desktop/update/apply", s.handleDesktopUpdateApply)
		})

		// Traffic streaming (no timeout)
		r.Get("/traffic/flows/stream", s.handleTrafficFlowsStream)
	})

	// OAuth/OIDC discovery probes from MCP HTTP clients. Without these
	// explicit 404s, two failure modes appear:
	//   (a) the index.html fallback below answers root-level /.well-known/* with the
	//       React index.html (HTTP 200, text/html);
	//   (b) the /mcp Mount answers /mcp/.well-known/* with 405 because the
	//       MCP handler only accepts POST.
	// Both responses trigger claude-code's MCP transport (per upstream issue
	// anthropics/claude-code#46879) to flip the server status to "needs-auth"
	// — Claude Code probes /.well-known/oauth-{protected-resource,
	// authorization-server} and /.well-known/openid-configuration before the
	// MCP initialize and treats any non-404 as "this server is OAuth-
	// protected." That leaks synthetic mcp__<server>__authenticate /
	// complete_authentication tools into the model's tool catalog, which the
	// agent then invents calls for. Per the MCP spec (RFC 9728 + RFC 8414),
	// servers that do not implement OAuth should return 404 here so the
	// client infers no auth is needed. Registered BEFORE the /mcp Mount so
	// chi's radix tree resolves /mcp/.well-known/* to NotFound instead of
	// letting the MCP handler answer with 405.
	r.Handle("/.well-known/*", http.NotFoundHandler())
	r.Handle("/mcp/.well-known/*", http.NotFoundHandler())
	r.Handle("/mcp-readonly/.well-known/*", http.NotFoundHandler())

	// MCP server (Model Context Protocol for AI tools)
	if s.mcpHandler != nil {
		r.Mount("/mcp", s.mcpHandler)
	}
	if s.mcpReadOnlyHandler != nil {
		r.Mount("/mcp-readonly", s.mcpReadOnlyHandler)
	}

	// OAuth discovery probes from MCP HTTP clients. Without this, the frontend
	// catch-all answers /.well-known/oauth-* with HTML 200, which newer
	// claude-code parses as a broken OAuth flow and aborts MCP registration.
	// Radar's MCP server is unauthenticated when run locally; signal that
	// cleanly with a 404 so clients proceed without an auth handshake.
	r.Get("/.well-known/oauth-protected-resource", http.NotFound)
	r.Get("/.well-known/oauth-authorization-server", http.NotFound)

	// Static files (frontend) - index.html fallback for client-side routes.
	if s.staticFS != nil {
		r.Handle("/*", frontendHandler(http.FS(s.staticFS), s.basePath))
	} else if s.devMode {
		// In dev mode, serve from web/dist
		r.Handle("/*", frontendHandler(http.Dir("web/dist"), s.basePath))
	}
}

// NormalizeBasePath canonicalizes the optional URL prefix used when Radar is
// served behind an ingress path like /radar. Empty and "/" mean root.
func NormalizeBasePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" || p == "/" {
		return "", nil
	}
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("must be a path, not a URL")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Allowlist rather than a blocklist: the value is interpolated into a chi
	// route pattern and into href/src attributes of the served index.html, so
	// anything outside unreserved RFC 3986 path characters is rejected up front
	// instead of relying on each consumer to escape it. Also blocks %-encoding,
	// which would make the configured prefix and the routed path disagree.
	for _, segment := range strings.Split(p, "/") {
		if segment == "" {
			continue
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("must not contain . or .. path segments")
		}
		for _, c := range segment {
			isAllowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~'
			if !isAllowed {
				return "", fmt.Errorf("segment %q contains disallowed character %q — use only letters, digits, '-', '_', '.', '~'", segment, c)
			}
		}
	}
	clean := pathpkg.Clean(p)
	if clean == "/" || clean == "." {
		return "", nil
	}
	return clean, nil
}

// frontendHandler serves static files, falling back to index.html for client-side routing
func frontendHandler(fsys http.FileSystem, basePath string) http.Handler {
	fileServer := http.FileServer(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Paths arrive root-relative even under a base path — basePathHandler
		// strips the prefix at the router edge. basePath is still needed to
		// rewrite the asset URLs inside index.html.
		path := r.URL.Path

		if path == "/" || path == "/index.html" {
			serveFrontendIndex(w, r, fsys, basePath)
			return
		}

		// Try to open the file
		f, err := fsys.Open(path)
		if err != nil {
			// File doesn't exist - serve index.html for client-side routing
			serveFrontendIndex(w, r, fsys, basePath)
			return
		}
		defer f.Close()

		// Check if it's a directory (and not the root)
		stat, err := f.Stat()
		if err != nil || (stat.IsDir() && path != "/") {
			// For directories without index.html, serve root index.html
			serveFrontendIndex(w, r, fsys, basePath)
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}

func serveFrontendIndex(w http.ResponseWriter, r *http.Request, fsys http.FileSystem, basePath string) {
	f, err := fsys.Open("/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "failed to read frontend index", http.StatusInternalServerError)
		return
	}
	body = rewriteFrontendIndex(body, basePath)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", stat.ModTime(), bytes.NewReader(body))
}

// prefixAttrPaths re-roots the href/src URLs of the served index.html under
// basePath, turning both root-absolute ("/x") and Vite's relative ("./x") forms
// into "{basePath}/x" — the latter matters at the root too, where basePath is
// empty and "./x" must still become "/x" so deep client routes resolve assets.
//
// Protocol-relative URLs ("//cdn.example.com/x") are deliberately skipped: they
// address another origin, and prefixing one would silently turn it into a local
// path that 404s. Scheme-qualified URLs never match, since the character after
// the quote isn't a slash.
func prefixAttrPaths(html, basePath string) string {
	for _, attr := range []string{`href="`, `src="`} {
		var out strings.Builder
		rest := html
		for {
			i := strings.Index(rest, attr)
			if i < 0 {
				out.WriteString(rest)
				break
			}
			out.WriteString(rest[:i+len(attr)])
			rest = rest[i+len(attr):]
			switch {
			case strings.HasPrefix(rest, "//"):
				// another origin — leave untouched
			case strings.HasPrefix(rest, "./"):
				out.WriteString(basePath + "/")
				rest = rest[len("./"):]
			case strings.HasPrefix(rest, "/"):
				out.WriteString(basePath + "/")
				rest = rest[len("/"):]
			}
		}
		html = out.String()
	}
	return html
}

func rewriteFrontendIndex(body []byte, basePath string) []byte {
	html := prefixAttrPaths(string(body), basePath)
	if basePath == "" {
		return []byte(html)
	}
	cfg := struct {
		BasePath  string `json:"basePath"`
		ApiBase   string `json:"apiBase"`
		AssetBase string `json:"assetBase"`
	}{
		BasePath:  basePath,
		ApiBase:   basePath + "/api",
		AssetBase: basePath,
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return body
	}

	runtimeScript := `<script>window.__RADAR_RUNTIME_CONFIG__=` + string(cfgJSON) + `;</script>`
	if strings.Contains(html, `<script type="module"`) {
		html = strings.Replace(html, `<script type="module"`, runtimeScript+"\n    "+`<script type="module"`, 1)
	} else {
		html = strings.Replace(html, `</head>`, "    "+runtimeScript+"\n  </head>", 1)
	}
	return []byte(html)
}

// Start starts the server. If port is 0, an OS-assigned port is used.
func (s *Server) Start() error {
	return s.StartWithReady(nil)
}

// StartWithReady starts the server and signals on the ready channel once it
// is accepting connections. If port is 0, an OS-assigned port is used.
func (s *Server) StartWithReady(ready chan<- struct{}) error {
	configuredListenAddress := s.listenAddress
	listenAddress, err := NormalizeListenAddress(configuredListenAddress)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", configuredListenAddress, err)
	}
	s.listenAddress = listenAddress
	bindAddr := socketAddress(listenAddress, s.port)
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		displayAddr := net.JoinHostPort(listenAddress, strconv.Itoa(s.port))
		return fmt.Errorf("listen on %s: %w", displayAddr, err)
	}
	s.listener = ln
	if s.startupLog {
		s.logStartupSummaryBlock()
	} else {
		// Keep the security warnings fail-safe for any direct Server caller that
		// opts out of the full CLI/desktop startup block.
		if shouldWarnUnauthenticatedListener(listenAddress, s.authConfig.Enabled()) && !cloud.Mode() {
			log.Printf("WARNING: Radar's HTTP listener is unauthenticated and reachable on %s", listenAddress)
		}
		if s.authConfig.Mode == "proxy" && !cloud.Mode() {
			log.Printf("WARNING: Proxy auth trusts %s and %s; ensure the ingress strips client-supplied identity headers",
				sanitizeForLog(s.authConfig.UserHeader), sanitizeForLog(s.authConfig.GroupsHeader))
		}
	}
	s.broadcaster.Start()

	if ready != nil {
		close(ready)
	}

	return http.Serve(ln, localTCPHandler(s.router))
}

func shouldWarnUnauthenticatedListener(listenAddress string, authEnabled bool) bool {
	return !authEnabled && !cloud.IsLoopbackHostname(listenAddress)
}

// localTCPHandler is the handler exposed on Radar's ordinary pod/host listener.
// In Cloud mode the full application is served only over the authenticated
// yamux session; this listener exists solely for kubelet health probes. Without
// this split, any pod that could reach the ClusterIP Service could spoof the
// Hub's forwarded identity headers and use Radar as a Kubernetes impersonation
// deputy.
func localTCPHandler(next http.Handler) http.Handler {
	if !cloud.Mode() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ActualPort returns the port the server is listening on.
// Useful when configured with port 0 (OS-assigned).
func (s *Server) ActualPort() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.port
}

// BasePath returns the normalized URL prefix the server mounted under, or ""
// when it serves from the root.
func (s *Server) BasePath() string {
	return s.basePath
}

// ActualAddr returns the address the server is listening on (e.g. "localhost:9280").
func (s *Server) ActualAddr() string {
	return fmt.Sprintf("localhost:%d", s.ActualPort())
}

// SetUpdater attaches a desktop updater to the server, enabling the
// /api/desktop/update/* endpoints. Only used by the desktop app.
func (s *Server) SetUpdater(u *updater.Updater) {
	s.updater = u
}

// SetSaveFileFunc attaches a native save-file callback, enabling the
// /api/desktop/save-file endpoint. The callback should show a native OS save
// dialog, write the data to the chosen path, and return the path.
// Only used by the desktop app.
func (s *Server) SetSaveFileFunc(fn func(defaultFilename string, data []byte) (string, error)) {
	s.saveFileFunc = fn
}

// Handler returns the full application handler for the authenticated Cloud
// tunnel and httptest. In Cloud mode, Start exposes only the health-only wrapper
// on the ordinary TCP listener.
func (s *Server) Handler() http.Handler {
	return s.router
}

// Stop gracefully stops the server and releases the listening port.
func (s *Server) Stop() {
	StopAllLocalTermSessions()
	if s.aiRuns != nil {
		s.aiRuns.Shutdown() // cancel investigations so agent children don't outlive us
	}
	s.broadcaster.Stop()
	if s.listener != nil {
		s.listener.Close()
	}
}

// Handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cache := k8s.GetResourceCache()
	status := "healthy"
	if cache == nil {
		status = "degraded"
	}

	// Timeline store status is informational only and doesn't affect overall status.
	var timelineStats map[string]any
	if store := timeline.GetStore(); store != nil {
		timelineStats = map[string]any{
			"store_present": true,
			"store_errors":  timeline.GetStoreErrorCount(),
			"total_drops":   timeline.GetTotalDropCount(),
		}
	}

	// Get runtime stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	runtimeStats := map[string]any{
		"heapMB":        float64(m.HeapAlloc) / 1024 / 1024,
		"heapObjectsK":  float64(m.HeapObjects) / 1000,
		"goroutines":    runtime.NumGoroutine(),
		"uptimeSeconds": int(time.Since(s.startTime).Seconds()),
	}

	// Get informer counts for diagnostics
	dynamicInformerCount := 0
	if dynCache := k8s.GetDynamicResourceCache(); dynCache != nil {
		dynamicInformerCount = dynCache.GetInformerCount()
	}
	runtimeStats["typedInformers"] = 16 // Fixed count of typed informers in cache.go
	runtimeStats["dynamicInformers"] = dynamicInformerCount

	// Get metrics collection health
	var metricsHealth *k8s.MetricsCollectionHealth
	if store := k8s.GetMetricsHistory(); store != nil {
		h := store.CollectionHealth()
		metricsHealth = &h
	}

	s.writeJSON(w, map[string]any{
		"status":        status,
		"resourceCount": cache.GetResourceCount(),
		"timeline":      timelineStats,
		"runtime":       runtimeStats,
		"metrics":       metricsHealth,
	})
}

func (s *Server) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() == k8s.DeploymentModeInCluster {
		info := version.CheckForUpdateRelease(r.Context())
		s.writeJSON(w, info)
		return
	}
	info := version.CheckForUpdate(r.Context())
	s.writeJSON(w, info)
}

func (s *Server) handleVersionCheckRelease(w http.ResponseWriter, r *http.Request) {
	info := version.CheckForUpdateRelease(r.Context())
	s.writeJSON(w, info)
}

func (s *Server) handleClusterInfo(w http.ResponseWriter, r *http.Request) {
	info, err := k8s.GetClusterInfo(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, info)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	var caps *k8s.Capabilities
	var err error

	// When auth is enabled, check capabilities for the specific user
	if user := auth.UserFromContext(r.Context()); user != nil {
		caps, err = k8s.CheckCapabilitiesForUser(r.Context(), user.Username, user.Groups)
	} else {
		caps, err = k8s.CheckCapabilities(r.Context())
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caps.MCPEnabled = s.mcpHandler != nil
	caps.Deployment = k8s.DeploymentInfo{Mode: deploymentMode()}
	caps.CloudConnect = s.cloudConnectCapability()
	caps.Features = k8s.FeatureCapabilities{
		YAMLReview:     true,
		YAMLSchemas:    true,
		WorkloadImages: true,
	}
	caps.AuthEnabled = s.authConfig.Enabled()
	if user := auth.UserFromContext(r.Context()); user != nil {
		caps.Username = user.Username
	}

	// Namespace-scoped re-check for controls whose permission can differ by
	// namespace. This keeps action visibility aligned with the namespace the
	// user is viewing rather than only the kubeconfig default namespace.
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		var nsCaps *k8s.NamespaceCapabilities
		if user := auth.UserFromContext(r.Context()); user != nil {
			nsCaps, err = k8s.CheckNamespaceCapabilitiesForUser(r.Context(), user.Username, user.Groups, ns)
		} else {
			nsCaps, err = k8s.CheckNamespaceCapabilities(r.Context(), ns)
		}
		if err != nil {
			log.Printf("[capabilities] namespace-scoped check for %q failed: %v", ns, err)
		} else if nsCaps != nil {
			mergeNamespaceCapabilities(caps, nsCaps)
		}
	}

	// Port-forward binds a local TCP listener on the radar host and proxies it to
	// the pod — only reachable when radar runs as a local binary. In-cluster
	// (Radar Cloud, reached over the tunnel) the listener would live on the radar
	// pod, unreachable from the user's browser, so the feature can't work
	// regardless of RBAC. Force it off after the namespace merge so a clean
	// per-namespace RBAC grant can't re-enable a capability the runtime can't
	// honor. Mirrors LocalTerminal's runtime-mode gate.
	if k8s.IsInCluster() {
		caps.PortForward = false
	}

	// Resource permissions come straight from the cached probe result, which
	// populates every field of ResourcePermissions via field pointers in
	// resourceProbeTargets(). Using GetEnabledResources() instead would
	// silently drop fields that have no typed informer (the dynamic-cache
	// CRDs surface via probe-result only). The probe-not-yet-run fallback
	// kicks off a probe so the response isn't blank on startup.
	//
	// Intentionally NOT guarded by s.requireConnected — the frontend polls
	// /api/capabilities to detect disconnect/loading state, so the endpoint
	// must still respond when the cluster isn't connected. A nil
	// caps.Resources (resources field omitted from JSON) is the documented
	// "no probe data yet" signal; the frontend has separate connection
	// state to distinguish loading from RBAC restrictions.
	if result := k8s.GetCachedPermissionResult(); result != nil {
		caps.Resources = result.Perms
		caps.Visibility = k8s.BuildVisibilitySummary(result, r.URL.Query().Get("namespace"))
	} else if k8s.GetResourceCache() != nil {
		if result := k8s.CheckResourcePermissions(r.Context()); result != nil {
			caps.Resources = result.Perms
			caps.Visibility = k8s.BuildVisibilitySummary(result, r.URL.Query().Get("namespace"))
		}
	}

	caps.Karpenter = s.karpenterCapability(r)

	// Report the PolicyReport index state so the frontend can say WHY a policy
	// view is empty. Omitted when Kyverno isn't installed at all — there is
	// nothing for the operator to act on, and a "not installed" note on every
	// non-Kyverno cluster would be noise.
	if prStatus := k8s.GetPolicyReportStatus(); prStatus.Status != k8s.KyvernoStatusNotInstalled {
		caps.PolicyReports = &prStatus
	}

	s.writeJSON(w, caps)
}

func mergeNamespaceCapabilities(caps *k8s.Capabilities, nsCaps *k8s.NamespaceCapabilities) {
	caps.Exec = mergeNamespaceCapability(caps.Exec, nsCaps.Exec, nsCaps.Errors.Exec)
	caps.Logs = mergeNamespaceCapability(caps.Logs, nsCaps.Logs, nsCaps.Errors.Logs)
	caps.PortForward = mergeNamespaceCapability(caps.PortForward, nsCaps.PortForward, nsCaps.Errors.PortForward)
	caps.WorkloadWrites.Deployments = mergeNamespaceCapability(caps.WorkloadWrites.Deployments, nsCaps.WorkloadWrites.Deployments, nsCaps.Errors.WorkloadWrites.Deployments)
	caps.WorkloadWrites.DaemonSets = mergeNamespaceCapability(caps.WorkloadWrites.DaemonSets, nsCaps.WorkloadWrites.DaemonSets, nsCaps.Errors.WorkloadWrites.DaemonSets)
	caps.WorkloadWrites.StatefulSets = mergeNamespaceCapability(caps.WorkloadWrites.StatefulSets, nsCaps.WorkloadWrites.StatefulSets, nsCaps.Errors.WorkloadWrites.StatefulSets)
	caps.WorkloadWrites.Rollouts = mergeNamespaceCapability(caps.WorkloadWrites.Rollouts, nsCaps.WorkloadWrites.Rollouts, nsCaps.Errors.WorkloadWrites.Rollouts)
}

func mergeNamespaceCapability(global, namespaced, checkErrored bool) bool {
	if checkErrored {
		return global || namespaced
	}
	// A clean namespace result is authoritative: global may have come from
	// the effective-namespace fallback and must not bleed into a different
	// namespace. On API errors, keep any existing grant so transient SAR
	// failures do not revoke controls.
	return namespaced
}

// parseNamespacesForUser parses namespace query params and filters by user permissions.
// Returns nil for "all namespaces" (no filter), a populated slice for specific namespaces,
// or an empty non-nil slice when the user has no namespace access.
// Use noNamespaceAccess() to check the no-access case.
//
// If the request omits an explicit namespace filter, falls back to the user's
// in-app namespace pick (from the namespace switcher). The pick is treated as
// a view filter — it's still intersected with the user's RBAC-allowed
// namespaces in getUserNamespaces.
func (s *Server) parseNamespacesForUser(r *http.Request) []string {
	namespaces := parseNamespaces(r.URL.Query())
	pickFallback := false
	pickCtx := ""
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		if target == "" {
			return []string{}
		}
		if namespaces == nil {
			namespaces = []string{target}
		} else if slices.Contains(namespaces, target) {
			namespaces = []string{target}
		} else {
			return []string{}
		}
	}
	if namespaces == nil {
		// No explicit filter — use the user's saved picks if any, pruned of
		// namespaces that were deleted from the cluster since the pick was made.
		// When every pick is stale, fall through with no filter so the user sees
		// the full cluster instead of a silently-empty UI. Read the pick and its
		// context as one snapshot so the empty-fallback clear below commits
		// against the same context, not one switched in mid-request.
		s.loadSavedNamespacePreference(r)
		if ctx, picks := s.getActiveNamespaceForUserInContext(r); len(picks) > 0 {
			picks = s.pruneDeletedNamespacePicks(r, ctx, picks)
			if len(picks) > 0 {
				namespaces = picks
				pickFallback = true
				pickCtx = ctx
			}
		}
	}
	filtered := s.getUserNamespaces(r, namespaces)
	// If picks lost RBAC mid-session, the filter shrinks the set. When the
	// intersection is empty every read returns []; recover by dropping the
	// stale pick entirely and recomputing as if no filter were set, so the
	// user sees their full RBAC ceiling instead of a silently-empty UI.
	// Symmetric with handleGetNamespaceScope's partial-revocation eviction.
	// namespaces holds the pruned picks this fallback filtered on; clear only
	// if it's still the live pick, so a stale read can't wipe a concurrent
	// POST or clear across a context switch.
	if pickFallback && noNamespaceAccess(filtered) {
		s.commitPickMutation(r, pickCtx, namespaces, nil, false)
		filtered = s.getUserNamespaces(r, nil)
	}
	return filtered
}

// resolveHelmNamespaces decides which namespaces a Helm list (releases, upgrade
// checks, dashboard summary) should query for this request. Helm releases are
// always namespaced (stored as Secrets/ConfigMaps in a namespace), so unlike
// cluster-scoped kinds it is always safe to narrow an "all namespaces" request
// to the identity's accessible namespaces — which is what lets a
// namespace-restricted ServiceAccount read Helm without a cluster-wide
// `list secrets`.
//
// Returns:
//   - (nil, true)        cluster-wide: a single AllNamespaces list
//   - (namespaces, true) list each and merge
//   - (nil, false)       no namespace access — caller returns an empty result
func (s *Server) resolveHelmNamespaces(r *http.Request) ([]string, bool) {
	// An explicit ?namespace=/?namespaces= request is honored as-is. Helm reads
	// run as the caller (user impersonation, or the SA when auth is off), so the
	// apiserver authorizes the secrets read directly — routing this through
	// parseNamespacesForUser would intersect it with the pod/deployment-based
	// namespace discovery and wrongly drop a namespace where the caller has
	// secrets but no pod access. A denied namespace surfaces as a 403 from the
	// per-namespace list rather than a silent empty result.
	//
	// Guard on len > 0, not != nil: parseNamespaces returns an empty non-nil
	// slice for a degenerate query like ?namespaces=,, — treat that as "no
	// explicit filter" and fall through, rather than short-circuiting to a
	// zero-namespace (empty) result.
	if explicit := parseNamespaces(r.URL.Query()); len(explicit) > 0 {
		// Under --namespace-scope the informer cache holds only the pinned
		// namespace; keep Helm consistent with the rest of the UI by clamping an
		// explicit filter to the pinned namespace (empty when it's out of scope)
		// instead of reading releases the cache doesn't cover.
		if k8s.ForceNamespaceScope {
			if target := k8s.GetNamespaceScopeTarget(); target != "" && slices.Contains(explicit, target) {
				return []string{target}, true
			}
			return []string{}, true
		}
		return explicit, true
	}

	namespaces := s.parseNamespacesForUser(r)
	return s.resolveHelmNamespacesForScope(r, namespaces)
}

// resolveHelmNamespacesForScope applies Helm's Secret-specific RBAC and
// no-auth fallback behavior to an already resolved workload namespace scope.
// Callers that intentionally ignore the browsing namespace picker (such as the
// cluster upgrade scan) can reuse the same Helm resolution without rebuilding
// it from request query state.
func (s *Server) resolveHelmNamespacesForScope(r *http.Request, namespaces []string) ([]string, bool) {
	return upgrade.ResolveHelmNamespaces(r.Context(), httpUpgradeAuthorizer{s: s, r: r}, namespaces)
}

// allNamespaceNames returns every namespace name from the shared cache lister,
// or nil when the namespace informer isn't available. Used as the candidate
// pool for per-user secrets-SAR filtering — the SAR is the authorization gate,
// so the (cluster-wide) pool only needs to be a superset of what the user can
// read.
func allNamespaceNames() []string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	nsList, err := lister.List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(nsList))
	for _, ns := range nsList {
		names = append(names, ns.Name)
	}
	return names
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// noNamespaceAccess returns true when a namespace filter explicitly grants no access
// (non-nil empty slice from auth filtering). Handlers with custom namespace logic
// should check this and return empty results.
func noNamespaceAccess(namespaces []string) bool {
	return namespaces != nil && len(namespaces) == 0
}

// canRead authorizes a single (verb, group, resource, namespace) tuple for
// the calling user via SubjectAccessReview. Used to gate cluster-scoped
// reads — namespace-list discovery is too narrow a signal to authorize
// arbitrary cluster-scoped kinds (a user can have cluster-wide pod
// visibility without `list nodes`, `list secrets`, etc.).
//
// Returns true when:
//
//   - auth is disabled (no user on context — local kubeconfig case), OR
//   - the apiserver's SAR for this exact tuple says yes
//
// Results are cached on UserPermissions and live only as long as the
// surrounding namespace-discovery cache entry (2-min TTL by default), so
// RBAC changes propagate within the TTL window.
//
// Pass namespace="" for a cluster-scoped check.
func (s *Server) canRead(r *http.Request, group, resource, namespace, verb string) bool {
	allowed, _ := s.canReadDecision(r, group, resource, namespace, verb)
	return allowed
}

func (s *Server) canReadDecision(r *http.Request, group, resource, namespace, verb string) (bool, bool) {
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		return true, true
	}
	if s.permCache.Get(user.Username) == nil {
		// Trigger namespace discovery so SAR cache has a parent UserPermissions
		// entry. parseNamespacesForUser is the canonical path that populates
		// this; if it hasn't run yet, canReadUser falls through to a fresh SAR.
		_ = s.getUserNamespaces(r, []string{})
	}
	return s.canReadUserDecision(r.Context(), user, group, resource, namespace, verb)
}

// canReadUser is the request-free core of canRead: it authorizes a single
// (verb, group, resource, namespace) tuple for an already-resolved user via
// SubjectAccessReview, memoizing on the user's UserPermissions.canI cache.
//
// Split out so the SSE broadcast loop — a background goroutine with no
// *http.Request — can authorize per-client change frames with the same gate
// REST uses. The caller captures the user at subscribe time (where the request
// is available) and passes a long-lived context for SAR cancellation.
//
// Fail-closed: no apiserver / SAR error → deny. Returns true only when auth is
// disabled (nil user) or the SAR allows it.
func (s *Server) canReadUser(ctx context.Context, user *auth.User, group, resource, namespace, verb string) bool {
	allowed, _ := s.canReadUserDecision(ctx, user, group, resource, namespace, verb)
	return allowed
}

func (s *Server) canReadUserDecision(ctx context.Context, user *auth.User, group, resource, namespace, verb string) (bool, bool) {
	if user == nil || s.permCache == nil {
		return true, true
	}
	perms := s.permCache.Get(user.Username)
	if perms != nil {
		if v, ok := perms.CanI(verb, group, resource, namespace); ok {
			return v, true
		}
	}
	allowed, authoritative := s.canReadUserSAR(ctx, user, group, resource, namespace, verb)
	// Cache only a real apiserver verdict. A transient failure (no client, SAR
	// error, timeout) fails closed for this call but must NOT be memoized, or a
	// momentary blip would deny the tuple for the whole cache TTL.
	if authoritative && perms != nil {
		perms.SetCanI(verb, group, resource, namespace, allowed)
	}
	return allowed, authoritative
}

// canReadUserSAR runs a single fresh SubjectAccessReview for (group, resource,
// namespace, verb) against the current apiserver, bypassing the shared
// permission cache entirely. It returns (allowed, authoritative): authoritative
// is false when the apiserver couldn't be consulted (no client, SAR error,
// timeout), in which case allowed is a fail-closed false that callers must not
// cache — the next call retries.
//
// canReadUser wraps this behind the shared cache. The SSE change authorizer
// calls it directly instead: reusing the shared cache there would let a decision
// already up to the cache TTL old be re-cached under the SSE memo's own TTL,
// stacking staleness — and the shared entry's context stamping wouldn't help,
// because the SSE memo, not the shared cache, is what a long-lived stream reads.
// A fresh SAR keeps the SSE staleness bounded to that memo's TTL alone.
func (s *Server) canReadUserSAR(ctx context.Context, user *auth.User, group, resource, namespace, verb string) (allowed bool, authoritative bool) {
	client := k8s.GetClient()
	if client == nil {
		// Fail-closed: no apiserver to ask, refuse rather than quietly
		// serving from the cache.
		log.Printf("[auth] canReadUserSAR: K8s client unavailable, denying %s on %s/%s for %s", k8s.SanitizeForLog(verb), k8s.SanitizeForLog(group), k8s.SanitizeForLog(resource), k8s.SanitizeForLog(user.Username))
		return false, false
	}
	allowed, err := auth.SubjectCanI(ctx, client, user.Username, user.Groups, namespace, group, resource, verb)
	if err != nil {
		// Fail-closed on SAR error — apiserver said something we don't trust.
		log.Printf("[auth] canReadUserSAR failed for %s on %s/%s in ns=%q: %v", k8s.SanitizeForLog(user.Username), k8s.SanitizeForLog(group), k8s.SanitizeForLog(resource), k8s.SanitizeForLog(namespace), err)
		return false, false
	}
	return allowed, true
}

// filterNamespacesByCanRead returns the subset of `namespaces` where the
// calling user passes a per-namespace SAR for (group, resource, verb).
// Fail-closed: SAR errors drop the namespace.
//
// Used to enforce per-kind RBAC inside a namespace when the cache reads as
// the SA and the SA has broader permissions than individual users (the chart
// can grant the SA cluster-wide secrets — any of rbac.secrets / rbac.helm /
// auth.mode != "none" / cloud.enabled triggers it, for Helm release
// visibility). Results memoize through UserPermissions.canI, so repeated
// reads within the cache TTL don't re-SAR.
//
// nil or empty input is returned unchanged; the caller's namespace-access
// gate (parseNamespacesForUser / noNamespaceAccess) is the upstream decision.
func (s *Server) filterNamespacesByCanRead(r *http.Request, group, resource, verb string, namespaces []string) []string {
	if len(namespaces) == 0 {
		return namespaces
	}
	// Bounded-parallel: each canRead miss is a SAR round-trip, so a serial loop
	// over a large candidate set (e.g. a cluster-wide reader's full namespace
	// list in resolveHelmNamespaces) would block the request for N round-trips.
	// canRead memoizes on the mutex-guarded UserPermissions.canI, mirroring the
	// parallel SAR probing in internal/k8s/capabilities.go. Result is sorted so
	// the output is deterministic regardless of goroutine completion order.
	const maxConcurrent = 16
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if s.canRead(r, group, resource, ns, verb) {
				mu.Lock()
				out = append(out, ns)
				mu.Unlock()
			}
		}(ns)
	}
	wg.Wait()
	sort.Strings(out)
	return out
}

// deniedClusterScopedTopoKinds returns the set of cluster-scoped topology
// NodeKinds the calling user cannot list. Walks topology.ClusterScopedKinds
// (centralized table — see pkg/topology/cluster_scoped_kinds.go). Reuses
// canRead's per-user canI cache so subsequent topology calls within the
// TTL don't re-SAR.
//
// NodeClass is intentionally excluded here. One synthesized NodeKind contains
// independently authorized provider APIs, including arbitrary custom kinds;
// applyClusterScopedTopologyRBAC filters those by exact node GVR instead.
// Calico policy kinds are also excluded because one NodeKind can represent
// either projectcalico.org or crd.projectcalico.org; the actual topology nodes
// are filtered by their exact API group and resource below.
func (s *Server) deniedClusterScopedTopoKinds(r *http.Request) map[topology.NodeKind]bool {
	deny := make(map[topology.NodeKind]bool)
	disc := k8s.GetResourceDiscovery()
	for _, ck := range topology.ClusterScopedKinds {
		if ck.Kind == topology.KindNodeClass {
			continue
		}
		if topology.IsCalicoPolicyKind(ck.Kind) {
			continue
		}
		if ck.Group != "" && disc != nil {
			if _, ok := disc.GetResourceWithGroup(ck.Resource, ck.Group); !ok {
				continue
			}
		}
		if !s.canRead(r, ck.Group, ck.Resource, "", "list") {
			deny[ck.Kind] = true
		}
	}
	return deny
}

func (s *Server) applyClusterScopedTopologyRBAC(r *http.Request, topo *topology.Topology) {
	if topo == nil {
		return
	}
	if deny := s.deniedClusterScopedTopoKinds(r); len(deny) > 0 {
		topo.StripNodeKinds(deny)
	}
	allowedCalico := make(map[topology.SARTuple]bool)
	for _, tuple := range topo.CalicoPolicyRBACTuples() {
		if s.canRead(r, tuple.Group, tuple.Resource, tuple.Namespace, "list") {
			allowedCalico[tuple] = true
		}
	}
	topo.StripCalicoPoliciesExcept(allowedCalico)
	allowedNodeClasses := make(map[topology.SARTuple]bool)
	for _, tuple := range topo.NodeClassRBACTuples() {
		if s.canRead(r, tuple.Group, tuple.Resource, "", "list") {
			allowedNodeClasses[tuple] = true
		}
	}
	topo.StripNodeClassesExcept(allowedNodeClasses)

	// Cluster-scoped Crossplane XR/MR nodes carry unbounded provider CRD kinds
	// the fixed denylist can't cover; authorize each by its exact GVR.
	allowedDynamic := make(map[topology.SARTuple]bool)
	for _, tuple := range topo.ClusterScopedDynamicRBACTuples() {
		if s.canRead(r, tuple.Group, tuple.Resource, "", "list") {
			allowedDynamic[tuple] = true
		}
	}
	topo.StripClusterScopedDynamicExcept(allowedDynamic)
}

// parseNamespaces parses the namespace filter from query parameters.
// Supports both "namespaces" (comma-separated, preferred) and "namespace" (single, backward compat).
func parseNamespaces(query url.Values) []string {
	// Prefer "namespaces" (plural, comma-separated)
	if ns := query.Get("namespaces"); ns != "" {
		parts := strings.Split(ns, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return dedupeStrings(result)
	}
	// Fall back to "namespace" (singular) for backward compatibility
	if ns := query.Get("namespace"); ns != "" {
		return []string{ns}
	}
	return nil
}

// appendSlice appends elements from a typed slice (returned as any) into a []any.
// This is needed because K8s listers return different concrete slice types (e.g. []*corev1.Pod).
func appendSlice(dst []any, src any) []any {
	v := reflect.ValueOf(src)
	for i := 0; i < v.Len(); i++ {
		dst = append(dst, v.Index(i).Interface())
	}
	return dst
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, map[string]any{"nodes": []any{}, "edges": []any{}})
		return
	}
	viewMode := r.URL.Query().Get("view")

	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	if viewMode == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}
	if r.URL.Query().Get("policyEffect") == "true" {
		opts.ShowPolicyEffect = true
	}
	if r.URL.Query().Get("includeReplicaSets") == "true" {
		opts.IncludeReplicaSets = true
	}

	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := builder.Build(opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Strip cluster-scoped resources (Nodes, Karpenter NodePool / NodeClaim,
	// GatewayClass, PV, StorageClass, …) the user can't list. Topology pulls
	// them from the SA-populated cache regardless of namespace scope, so
	// without this strip a namespace-restricted user with cluster-wide pod
	// access would enumerate cluster infrastructure they have no RBAC for.
	s.applyClusterScopedTopologyRBAC(r, topo)

	// Marshal once so we can record the exact wire size in perfstats.
	// (writeJSON streams, which would force a counting-writer wrapper.)
	data, err := json.Marshal(topo)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	perfstats.RecordTopologyPayload(len(data))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Authoritative per-user filter from the discovery return value. Reading
	// permCache.Get back after a transient discovery failure conflates
	// "cluster-admin sentinel" with "cache miss" and would silently leak the
	// full namespace list to a restricted user during apiserver flakes.
	allowedNames := s.parseNamespacesForUser(r)
	if allowedNames != nil && len(allowedNames) == 0 {
		s.writeJSON(w, []map[string]any{})
		return
	}
	var allowedSet map[string]bool
	if allowedNames != nil {
		allowedSet = make(map[string]bool, len(allowedNames))
		for _, ns := range allowedNames {
			allowedSet[ns] = true
		}
	}

	lister := cache.Namespaces()
	if lister == nil {
		// Cluster-wide Namespaces informer isn't available — fall back to
		// GetAccessibleNamespaces (SA-listed). Apply the same per-user
		// filter so restricted users don't see SA-visible names they
		// have no access to.
		accessible, _ := k8s.GetAccessibleNamespaces(r.Context())
		result := make([]map[string]any, 0, len(accessible))
		for _, name := range accessible {
			if allowedSet != nil && !allowedSet[name] {
				continue
			}
			result = append(result, map[string]any{"name": name, "status": "Active"})
		}
		if len(result) == 0 {
			s.writeError(w, http.StatusForbidden, "insufficient permissions to list namespaces")
			return
		}
		s.writeJSON(w, result)
		return
	}

	namespaces, err := lister.List(labels.Everything())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]map[string]any, 0, len(namespaces))
	for _, ns := range namespaces {
		if allowedSet != nil && !allowedSet[ns.Name] {
			continue
		}
		result = append(result, map[string]any{
			"name":   ns.Name,
			"status": string(ns.Status.Phase),
		})
	}

	s.writeJSON(w, result)
}

func (s *Server) handleAPIResources(w http.ResponseWriter, r *http.Request) {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource discovery not available")
		return
	}

	resources, err := discovery.GetAPIResources()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type apiResourceResponse struct {
		k8score.APIResource
		Featured bool `json:"featured,omitempty"`
	}
	result := make([]apiResourceResponse, 0, len(resources))
	for _, resource := range resources {
		result = append(result, apiResourceResponse{
			APIResource: resource,
			Featured:    isFeaturedKubernetesAPI(resource.Group, resource.Kind),
		})
	}
	s.writeJSON(w, result)
}

// preflightResourceList runs the per-user RBAC gates shared by the REST
// (/api/resources/{kind}) and AI (/api/ai/resources/{kind}) list paths.
// It assumes the caller has already populated `namespaces` via
// parseNamespacesForUser (which primes the canI cache that canRead relies on)
// and has classified the kind for cluster-scope.
//
// Returns the (possibly-rewritten) namespace slice that downstream cache
// reads should use. When ok=false the gate denied or the user has no
// namespace access; (status, msg) carry the canonical HTTP response. REST
// callers historically convert denies to a 200 with `[]` to avoid leaking
// kind existence; the AI path returns the explicit status so agents see the
// failure. Same gates run in the same order on both paths — the response
// shape is the only thing that differs.
func (s *Server) preflightResourceList(r *http.Request, kind, group string, namespaces []string) (finalNamespaces []string, status int, msg string, ok bool) {
	// "namespaces" is cluster-scoped at the K8s API. Full Namespace objects
	// (labels, annotations, spec) require explicit list-namespaces SAR.
	// AllowedNamespaces is NOT a sufficient fallback: list-pods-in-alpha
	// SAR-confirms namespace existence and pod read access, not get-namespace-
	// alpha (which would require ClusterRole on namespaces). The namespace
	// picker uses /api/namespaces, which serves a synthesized {name, status}
	// view filtered by AllowedNamespaces — restricted users keep their picker
	// without leaking Namespace metadata via this resource-browser path.
	isNamespacesKind := kind == "namespaces" || kind == "namespace"
	if isNamespacesKind {
		if !s.canRead(r, "", "namespaces", "", "list") {
			return nil, http.StatusForbidden, "insufficient permissions to list namespaces", false
		}
		return nil, 0, "", true // full lister output for SAR-authorized users
	}

	// Cluster-only kinds (Nodes, PVs, StorageClasses, ClusterRoles, cluster-
	// scoped CRDs) have no namespace dimension — gate via SAR. Run BEFORE the
	// noNamespaceAccess check so a user with explicit cluster-scoped RBAC but
	// no namespace access can still read those resources.
	isClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
	if isClusterScoped {
		if !s.canRead(r, gvrGroup, gvrResource, "", "list") {
			return nil, http.StatusForbidden, fmt.Sprintf("insufficient permissions to list %s", kind), false
		}
		// Cluster-scoped reads have no namespace dimension. Once the
		// resource-level SAR passes, force the later typed/dynamic cache paths
		// through their cluster-wide branch even if the user also has a
		// namespace view preference.
		return nil, 0, "", true
	}

	if noNamespaceAccess(namespaces) {
		return namespaces, http.StatusForbidden, "no namespace access", false
	}

	// Per-kind RBAC inside a namespace. Helm release storage IS K8s Secrets,
	// so the chart can grant the SA cluster-wide secrets (rbac.secrets,
	// rbac.helm, auth.mode != "none", or cloud.enabled — see deploy/helm/
	// radar/templates/clusterrole.yaml). When any of those triggers fires
	// the cache holds every secret in the cluster, so per-user RBAC must
	// gate the read. Other namespaced kinds are deferred.
	if kind == "secrets" || kind == "secret" {
		if auth.UserFromContext(r.Context()) != nil {
			if namespaces == nil {
				// Auth user with cluster-wide namespace access (e.g. picked up
				// via DiscoverNamespaces stage 1: cluster-wide list pods). The
				// cache will serve all secrets — gate on cluster-scope SAR.
				if !s.canRead(r, "", "secrets", "", "list") {
					return nil, http.StatusForbidden, "insufficient permissions to list secrets", false
				}
			} else {
				namespaces = s.filterNamespacesByCanRead(r, "", "secrets", "list", namespaces)
				if len(namespaces) == 0 {
					return namespaces, http.StatusForbidden, "insufficient permissions to list secrets", false
				}
			}
		}
	}

	return namespaces, 0, "", true
}

func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := normalizeKind(chi.URLParam(r, "kind"))
	group := r.URL.Query().Get("group") // API group for CRD disambiguation
	// include follows /api/search's body-verbosity vocabulary: "summary" =
	// same shape with heavy subtrees stripped per kind profile (see
	// resource_summary.go), "raw" or absent = full objects. Unknown values
	// are rejected with 400 — same posture as /api/search's parseInclude.
	includeSummary, err := parseResourcesInclude(r.URL.Query().Get("include"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// parseNamespacesForUser primes the per-user perm cache (triggers
	// DiscoverNamespaces if needed). canRead below relies on it.
	namespaces := s.parseNamespacesForUser(r)

	// Shared RBAC gate. REST converts denies to 200 with `[]` (legacy shape
	// the frontend tolerates and that doesn't leak kind existence); the AI path
	// returns the explicit status.
	finalNamespaces, _, _, ok := s.preflightResourceList(r, kind, group, namespaces)
	if !ok {
		s.writeJSON(w, []any{})
		return
	}
	namespaces = finalNamespaces

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var result any

	// listPerNs is a helper that merges results across multiple namespaces.
	// listAll returns all items; listNs returns items for a single namespace.
	listPerNs := func(listAll func() (any, error), listNs func(string) (any, error)) (any, error) {
		if namespaces == nil {
			return listAll()
		}
		if len(namespaces) == 1 {
			return listNs(namespaces[0])
		}
		var merged []any
		for _, ns := range namespaces {
			items, err := listNs(ns)
			if err != nil {
				return nil, err
			}
			merged = appendSlice(merged, items)
		}
		return merged, nil
	}

	// forbiddenMsg returns a 403 error for RBAC-restricted resource types
	forbiddenMsg := func(resourceKind string) {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to list %s", resourceKind))
	}

	// notReadyOrForbidden returns 503 when a deferred resource is still syncing,
	// or 403 when RBAC denied access. Callers use this for deferred resource types
	// (configmaps, secrets, events, etc.) where a nil lister can mean either case.
	notReadyOrForbidden := func(resourceKind string) {
		if cache.IsDeferredPending(resourceKind) {
			s.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("%s are still loading, please retry shortly", resourceKind))
			return
		}
		forbiddenMsg(resourceKind)
	}

	// A non-empty group routes to the dynamic/CRD cache so CRDs whose plural
	// collides with a core kind (e.g. KNative "services" vs core "services")
	// reach the right resource. Built-in workloads addressed by their real group
	// (e.g. deployments?group=apps) live in the typed cache, so they must fall
	// through to the typed switch below — TypedKindOwnsGroup keeps them off the
	// dynamic path (which has no informer for built-ins). Cluster-scoped gating
	// is already done at the top of this handler via k8s.ClassifyKindScope.
	if group != "" && !k8s.TypedKindOwnsGroup(kind, group) {
		if len(namespaces) > 0 {
			var merged []any
			for _, ns := range namespaces {
				items, listErr := cache.ListDynamicWithGroup(r.Context(), kind, ns, group)
				if listErr != nil {
					if strings.Contains(listErr.Error(), "unknown resource kind") {
						s.writeError(w, http.StatusBadRequest, listErr.Error())
						return
					}
					if apierrors.IsForbidden(listErr) || apierrors.IsUnauthorized(listErr) {
						forbiddenMsg(kind)
						return
					}
					log.Printf("[resources] Failed to list %s in namespace %s (group=%s): %v", kind, ns, group, listErr)
					s.writeError(w, http.StatusInternalServerError, listErr.Error())
					return
				}
				for _, item := range items {
					merged = append(merged, item)
				}
			}
			result = merged
		} else {
			result, err = cache.ListDynamicWithGroup(r.Context(), kind, "", group)
			if err != nil {
				if strings.Contains(err.Error(), "unknown resource kind") {
					s.writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
					forbiddenMsg(kind)
					return
				}
				log.Printf("[resources] Failed to list %s (group=%s): %v", kind, group, err)
				s.writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		if includeSummary {
			result = applySummaryStrip(result)
		}
		s.writeJSON(w, result)
		return
	}

	// Try typed cache for known resource types first
	switch kind {
	case "pods":
		if cache.Pods() == nil {
			forbiddenMsg("pods")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Pods().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Pods().Pods(ns).List(labels.Everything()) },
		)
	case "services":
		if cache.Services() == nil {
			forbiddenMsg("services")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Services().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Services().Services(ns).List(labels.Everything()) },
		)
	case "deployments":
		if cache.Deployments() == nil {
			forbiddenMsg("deployments")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Deployments().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Deployments().Deployments(ns).List(labels.Everything()) },
		)
	case "daemonsets":
		if cache.DaemonSets() == nil {
			forbiddenMsg("daemonsets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.DaemonSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.DaemonSets().DaemonSets(ns).List(labels.Everything()) },
		)
	case "statefulsets":
		if cache.StatefulSets() == nil {
			forbiddenMsg("statefulsets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.StatefulSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.StatefulSets().StatefulSets(ns).List(labels.Everything()) },
		)
	case "replicasets":
		if cache.ReplicaSets() == nil {
			// ReplicaSets lister uses isEnabled (not isReady) — available before deferred sync completes.
			// Nil here means RBAC denied, not deferred-pending.
			forbiddenMsg("replicasets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.ReplicaSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.ReplicaSets().ReplicaSets(ns).List(labels.Everything()) },
		)
	case "ingresses":
		if cache.Ingresses() == nil {
			forbiddenMsg("ingresses")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Ingresses().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Ingresses().Ingresses(ns).List(labels.Everything()) },
		)
	case "configmaps":
		if cache.ConfigMaps() == nil {
			notReadyOrForbidden("configmaps")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.ConfigMaps().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.ConfigMaps().ConfigMaps(ns).List(labels.Everything()) },
		)
	case "secrets":
		lister := cache.Secrets()
		if lister == nil {
			notReadyOrForbidden("secrets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return lister.List(labels.Everything()) },
			func(ns string) (any, error) { return lister.Secrets(ns).List(labels.Everything()) },
		)
	case "events":
		if cache.Events() == nil {
			notReadyOrForbidden("events")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Events().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Events().Events(ns).List(labels.Everything()) },
		)
	case "persistentvolumeclaims", "pvcs":
		if cache.PersistentVolumeClaims() == nil {
			notReadyOrForbidden("persistentvolumeclaims")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.PersistentVolumeClaims().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.PersistentVolumeClaims().PersistentVolumeClaims(ns).List(labels.Everything())
			},
		)
	case "roles":
		if cache.Roles() == nil {
			forbiddenMsg("roles")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Roles().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Roles().Roles(ns).List(labels.Everything()) },
		)
	case "clusterroles":
		if cache.ClusterRoles() == nil {
			forbiddenMsg("clusterroles")
			return
		}
		result, err = cache.ClusterRoles().List(labels.Everything())
	case "rolebindings":
		if cache.RoleBindings() == nil {
			forbiddenMsg("rolebindings")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.RoleBindings().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.RoleBindings().RoleBindings(ns).List(labels.Everything()) },
		)
	case "clusterrolebindings":
		if cache.ClusterRoleBindings() == nil {
			forbiddenMsg("clusterrolebindings")
			return
		}
		result, err = cache.ClusterRoleBindings().List(labels.Everything())
	case "jobs":
		if cache.Jobs() == nil {
			forbiddenMsg("jobs")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Jobs().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Jobs().Jobs(ns).List(labels.Everything()) },
		)
	case "cronjobs":
		if cache.CronJobs() == nil {
			forbiddenMsg("cronjobs")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.CronJobs().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.CronJobs().CronJobs(ns).List(labels.Everything()) },
		)
	case "hpas", "horizontalpodautoscalers":
		if cache.HorizontalPodAutoscalers() == nil {
			// HPA lister uses isEnabled (not isReady) — available before deferred sync completes.
			forbiddenMsg("horizontalpodautoscalers")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.HorizontalPodAutoscalers().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(ns).List(labels.Everything())
			},
		)
	case "nodes":
		if cache.Nodes() == nil {
			forbiddenMsg("nodes")
			return
		}
		result, err = cache.Nodes().List(labels.Everything())
	case "namespaces":
		if cache.Namespaces() == nil {
			forbiddenMsg("namespaces")
			return
		}
		// SAR gate above already filtered: cluster-admin / no-auth fell
		// through with namespaces=nil; restricted users early-returned [].
		result, err = cache.Namespaces().List(labels.Everything())
	case "persistentvolumes", "pvs":
		if cache.PersistentVolumes() == nil {
			notReadyOrForbidden("persistentvolumes")
			return
		}
		result, err = cache.PersistentVolumes().List(labels.Everything())
	case "storageclasses", "sc":
		if cache.StorageClasses() == nil {
			notReadyOrForbidden("storageclasses")
			return
		}
		result, err = cache.StorageClasses().List(labels.Everything())
	case "poddisruptionbudgets", "pdbs":
		if cache.PodDisruptionBudgets() == nil {
			notReadyOrForbidden("poddisruptionbudgets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.PodDisruptionBudgets().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.PodDisruptionBudgets().PodDisruptionBudgets(ns).List(labels.Everything())
			},
		)
	case "serviceaccounts":
		// ServiceAccounts are in the deferred informer batch, but the typed
		// lister object is available before sync (isEnabled is true). Calling
		// .List() pre-sync would return empty, which the frontend renders as
		// "No ServiceAccount found" — misleading when 46 actually exist.
		// notReadyOrForbidden distinguishes "still syncing" (503) from
		// "RBAC denied" (403).
		if cache.ServiceAccounts() == nil {
			notReadyOrForbidden("serviceaccounts")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.ServiceAccounts().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.ServiceAccounts().ServiceAccounts(ns).List(labels.Everything())
			},
		)
	case "ingressclasses":
		if cache.IngressClasses() == nil {
			forbiddenMsg("ingressclasses")
			return
		}
		result, err = cache.IngressClasses().List(labels.Everything())
	case "limitranges":
		if cache.LimitRanges() == nil {
			notReadyOrForbidden("limitranges")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.LimitRanges().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.LimitRanges().LimitRanges(ns).List(labels.Everything())
			},
		)
	case "resourcequotas":
		if cache.ResourceQuotas() == nil {
			notReadyOrForbidden("resourcequotas")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.ResourceQuotas().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.ResourceQuotas().ResourceQuotas(ns).List(labels.Everything())
			},
		)
	case "networkpolicies", "netpol":
		if cache.NetworkPolicies() == nil {
			notReadyOrForbidden("networkpolicies")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.NetworkPolicies().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.NetworkPolicies().NetworkPolicies(ns).List(labels.Everything())
			},
		)
	default:
		// Fall back to dynamic cache for CRDs and other unknown resources
		if len(namespaces) > 0 {
			var merged []any
			for _, ns := range namespaces {
				items, listErr := cache.ListDynamicWithGroup(r.Context(), kind, ns, group)
				if listErr != nil {
					if strings.Contains(listErr.Error(), "unknown resource kind") {
						s.writeError(w, http.StatusBadRequest, listErr.Error())
						return
					}
					log.Printf("[resources] Failed to list %s in namespace %s: %v", kind, ns, listErr)
					s.writeError(w, http.StatusInternalServerError, listErr.Error())
					return
				}
				for _, item := range items {
					merged = append(merged, item)
				}
			}
			result = merged
		} else {
			result, err = cache.ListDynamicWithGroup(r.Context(), kind, "", group)
			if err != nil {
				if strings.Contains(err.Error(), "unknown resource kind") {
					s.writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				s.writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if includeSummary {
		result = summarizeTypedList(kind, result)
		result = applySummaryStrip(result)
	}
	s.writeJSON(w, result)
}

// normalizeKind converts K8s kind names to lowercase for case-insensitive matching
// E.g., "Job" -> "job", "Deployment" -> "deployment"
func normalizeKind(kind string) string {
	return strings.ToLower(kind)
}

// setTypeMeta sets the APIVersion and Kind fields on typed resources.
// Delegates to k8s.SetTypeMeta.
func setTypeMeta(resource any) {
	k8s.SetTypeMeta(resource)
}

func hpaDiagnosisFor(resource any) *hpadiag.Diagnosis {
	hpa, ok := resource.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok {
		return nil
	}
	return hpadiag.Analyze(hpa)
}

// preflightResourceGet runs the per-user RBAC gates that must pass before any
// single-resource GET fetch. Mirrors the kind/scope-aware logic used by both
// the REST handler (handleGetResource) and the AI handler (handleAIGetResource)
// so future RBAC adjustments stay in lockstep across both surfaces.
//
// Inputs are the already-normalized (kind, namespace, name, group); callers
// must collapse the cluster-scoped "_" placeholder before calling. Returns
// (status, message, ok=true) when the request passes the gates, or
// (status, message, ok=false) with the HTTP status + body the caller should
// emit on deny.
//
// Three gates, run in this order:
//  1. kind == "namespaces"        → full Namespace object requires get-namespaces SAR
//  2. cluster-scoped (Node/CRD/…) → per-kind get SAR (ClassifyKindScope)
//  3. namespaced                   → namespace access via getUserNamespaces,
//     plus per-namespace get SAR for Secrets
func (s *Server) preflightResourceGet(r *http.Request, kind, namespace, name, group string) (int, string, bool) {
	isNamespacesKind := kind == "namespaces" || kind == "namespace"
	isClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
	switch {
	case isNamespacesKind:
		// Full Namespace object access requires explicit get-namespaces SAR.
		// Read access to resources IN a namespace (list pods etc.) does not
		// imply read access to the Namespace object itself. Restricted users
		// without ClusterRole on namespaces get 403 here.
		if !s.canRead(r, "", "namespaces", "", "get") {
			return http.StatusForbidden, fmt.Sprintf("no access to namespace %q", name), false
		}
	case isClusterScoped:
		if !s.canRead(r, gvrGroup, gvrResource, "", "get") {
			return http.StatusForbidden, fmt.Sprintf("no access to %s (cluster-scoped resource requires explicit RBAC)", kind), false
		}
	case namespace != "":
		// Namespaced kind: verify namespace access.
		allowed := s.getUserNamespaces(r, []string{namespace})
		if noNamespaceAccess(allowed) {
			return http.StatusForbidden, fmt.Sprintf("no access to namespace %q", namespace), false
		}
		// Per-kind RBAC inside the namespace for Secrets — the chart can
		// grant the SA cluster-wide secrets (Helm release visibility), so
		// namespace-list discovery is not a sufficient gate here. The list
		// handler has the matching list-SAR.
		if (kind == "secrets" || kind == "secret") && !s.canRead(r, "", "secrets", namespace, "get") {
			return http.StatusForbidden, fmt.Sprintf("no access to secrets in namespace %q", namespace), false
		}
	default:
		// Empty namespace and not a recognized cluster-scoped kind: an empty
		// namespace means the target is cluster-scoped, but ClassifyKindScope
		// couldn't identify it (an undiscovered CRD), so no SAR ran. Fail closed —
		// serving such a resource ungated would let the caller read a cluster-
		// scoped manifest they may lack `get` on (esp. via the Argo diff token).
		return http.StatusForbidden, fmt.Sprintf("cannot verify access to %q (unrecognized cluster-scoped resource)", kind), false
	}
	return 0, "", true
}

func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group") // API group for CRD disambiguation

	// Handle cluster-scoped resources: "_" is used as placeholder for empty namespace
	if namespace == "_" {
		namespace = ""
	}

	// Cluster-scoped GETs (Node, ClusterRole, cluster-scoped CRDs, …) are
	// gated per-kind via SAR. Run BEFORE the namespace access check so
	// users with explicit cluster-scoped RBAC but no namespace access can
	// still get the resource. ClassifyKindScope catches both static cluster-
	// only kinds and dynamic cluster-scoped CRDs (via discovery).
	//
	// "namespaces" is cluster-scoped at the K8s API but exposed as a per-user
	// filtered list — gate the GET via the user's namespace access for the
	// requested name, not via cluster-scoped SAR.
	if status, msg, ok := s.preflightResourceGet(r, kind, namespace, name, group); !ok {
		s.writeError(w, status, msg)
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var resource any
	var err error

	// forbiddenGet returns a 403 error for RBAC-restricted resource types
	forbiddenGet := func(resourceKind string) {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to access %s", resourceKind))
	}

	// notReadyOrForbiddenGet is the single-resource counterpart of notReadyOrForbidden (see handleListResources).
	notReadyOrForbiddenGet := func(resourceKind string) {
		if cache.IsDeferredPending(resourceKind) {
			s.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("%s are still loading, please retry shortly", resourceKind))
			return
		}
		forbiddenGet(resourceKind)
	}

	// A non-empty group routes to the dynamic/CRD cache so CRDs whose plural
	// collides with a core kind (e.g. KNative serving.knative.dev/services vs
	// core "services") reach the right resource. But the frontend also threads the
	// real apiGroup for BUILT-IN workloads (e.g. apps/Deployment), and those
	// live in the typed cache, not the dynamic one — so a built-in addressed by
	// its own group must still take the typed path below. Without this guard,
	// deployments?group=apps fell through to the dynamic cache and 400'd with
	// "unknown resource kind: deployments (group: apps)". Cluster-scoped gating
	// is already done at the top of this handler via k8s.ClassifyKindScope.
	if group != "" && !k8s.TypedKindOwnsGroup(kind, group) {
		resource, err = cache.GetDynamicWithGroup(r.Context(), kind, namespace, name, group)
		if err != nil {
			if strings.Contains(err.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.Contains(err.Error(), "not found") {
				s.writeError(w, http.StatusNotFound, err.Error())
				return
			}
			log.Printf("[resources] Failed to get %s %s/%s (group=%s): %v", kind, namespace, name, group, err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		setTypeMeta(resource)

		// Get relationships from cached topology. Pass the already-fetched
		// resource so ManagedBy synthesis disambiguates by group (avoids
		// kind/plural collisions like Knative Service vs core Service).
		var relationships *topology.Relationships
		if cachedTopo, relIdx := s.broadcaster.GetCachedTopologyWithIndex(); cachedTopo != nil {
			relationships = topology.GetRelationshipsWithObject(kind, namespace, name, resource, cachedTopo,
				k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
				k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
		}

		s.writeJSON(w, topology.ResourceWithRelationships{
			Resource:      resource,
			Relationships: relationships,
			HPADiagnosis:  hpaDiagnosisFor(resource),
		})
		return
	}

	// Try typed cache for known resource types first
	switch kind {
	case "pods", "pod":
		if cache.Pods() == nil {
			forbiddenGet("pods")
			return
		}
		resource, err = cache.Pods().Pods(namespace).Get(name)
	case "services", "service":
		if cache.Services() == nil {
			forbiddenGet("services")
			return
		}
		resource, err = cache.Services().Services(namespace).Get(name)
	case "deployments", "deployment":
		if cache.Deployments() == nil {
			forbiddenGet("deployments")
			return
		}
		resource, err = cache.Deployments().Deployments(namespace).Get(name)
	case "daemonsets", "daemonset":
		if cache.DaemonSets() == nil {
			forbiddenGet("daemonsets")
			return
		}
		resource, err = cache.DaemonSets().DaemonSets(namespace).Get(name)
	case "statefulsets", "statefulset":
		if cache.StatefulSets() == nil {
			forbiddenGet("statefulsets")
			return
		}
		resource, err = cache.StatefulSets().StatefulSets(namespace).Get(name)
	case "replicasets", "replicaset":
		if cache.ReplicaSets() == nil {
			forbiddenGet("replicasets")
			return
		}
		resource, err = cache.ReplicaSets().ReplicaSets(namespace).Get(name)
	case "ingresses", "ingress":
		if cache.Ingresses() == nil {
			forbiddenGet("ingresses")
			return
		}
		resource, err = cache.Ingresses().Ingresses(namespace).Get(name)
	case "configmaps", "configmap":
		if cache.ConfigMaps() == nil {
			notReadyOrForbiddenGet("configmaps")
			return
		}
		resource, err = cache.ConfigMaps().ConfigMaps(namespace).Get(name)
	case "secrets", "secret":
		lister := cache.Secrets()
		if lister == nil {
			notReadyOrForbiddenGet("secrets")
			return
		}
		resource, err = lister.Secrets(namespace).Get(name)
	case "events", "event":
		if cache.Events() == nil {
			notReadyOrForbiddenGet("events")
			return
		}
		resource, err = cache.Events().Events(namespace).Get(name)
	case "persistentvolumeclaims", "persistentvolumeclaim", "pvcs", "pvc":
		if cache.PersistentVolumeClaims() == nil {
			notReadyOrForbiddenGet("persistentvolumeclaims")
			return
		}
		resource, err = cache.PersistentVolumeClaims().PersistentVolumeClaims(namespace).Get(name)
	case "hpas", "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		if cache.HorizontalPodAutoscalers() == nil {
			forbiddenGet("horizontalpodautoscalers")
			return
		}
		resource, err = cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
	case "jobs", "job":
		if cache.Jobs() == nil {
			forbiddenGet("jobs")
			return
		}
		resource, err = cache.Jobs().Jobs(namespace).Get(name)
	case "cronjobs", "cronjob":
		if cache.CronJobs() == nil {
			forbiddenGet("cronjobs")
			return
		}
		resource, err = cache.CronJobs().CronJobs(namespace).Get(name)
	case "nodes", "node":
		if cache.Nodes() == nil {
			forbiddenGet("nodes")
			return
		}
		resource, err = cache.Nodes().Get(name)
	case "namespaces", "namespace":
		if cache.Namespaces() == nil {
			forbiddenGet("namespaces")
			return
		}
		resource, err = cache.Namespaces().Get(name)
	case "persistentvolumes", "persistentvolume", "pvs", "pv":
		if cache.PersistentVolumes() == nil {
			notReadyOrForbiddenGet("persistentvolumes")
			return
		}
		resource, err = cache.PersistentVolumes().Get(name)
	case "storageclasses", "storageclass", "sc":
		if cache.StorageClasses() == nil {
			notReadyOrForbiddenGet("storageclasses")
			return
		}
		resource, err = cache.StorageClasses().Get(name)
	case "poddisruptionbudgets", "poddisruptionbudget", "pdbs", "pdb":
		if cache.PodDisruptionBudgets() == nil {
			notReadyOrForbiddenGet("poddisruptionbudgets")
			return
		}
		resource, err = cache.PodDisruptionBudgets().PodDisruptionBudgets(namespace).Get(name)
	case "networkpolicies", "networkpolicy", "netpol":
		if cache.NetworkPolicies() == nil {
			notReadyOrForbiddenGet("networkpolicies")
			return
		}
		resource, err = cache.NetworkPolicies().NetworkPolicies(namespace).Get(name)
	case "serviceaccounts", "serviceaccount":
		if cache.ServiceAccounts() == nil {
			notReadyOrForbiddenGet("serviceaccounts")
			return
		}
		resource, err = cache.ServiceAccounts().ServiceAccounts(namespace).Get(name)
	case "ingressclasses", "ingressclass":
		if cache.IngressClasses() == nil {
			forbiddenGet("ingressclasses")
			return
		}
		resource, err = cache.IngressClasses().Get(name)
	case "limitranges", "limitrange":
		if cache.LimitRanges() == nil {
			notReadyOrForbiddenGet("limitranges")
			return
		}
		resource, err = cache.LimitRanges().LimitRanges(namespace).Get(name)
	case "resourcequotas", "resourcequota":
		if cache.ResourceQuotas() == nil {
			notReadyOrForbiddenGet("resourcequotas")
			return
		}
		resource, err = cache.ResourceQuotas().ResourceQuotas(namespace).Get(name)
	case "roles", "role":
		if cache.Roles() == nil {
			forbiddenGet("roles")
			return
		}
		resource, err = cache.Roles().Roles(namespace).Get(name)
	case "clusterroles", "clusterrole":
		if cache.ClusterRoles() == nil {
			forbiddenGet("clusterroles")
			return
		}
		resource, err = cache.ClusterRoles().Get(name)
	case "rolebindings", "rolebinding":
		if cache.RoleBindings() == nil {
			forbiddenGet("rolebindings")
			return
		}
		resource, err = cache.RoleBindings().RoleBindings(namespace).Get(name)
	case "clusterrolebindings", "clusterrolebinding":
		if cache.ClusterRoleBindings() == nil {
			forbiddenGet("clusterrolebindings")
			return
		}
		resource, err = cache.ClusterRoleBindings().Get(name)
	default:
		// Fall back to dynamic cache for CRDs and other unknown resources
		// Use group to disambiguate when multiple API groups have similar resource names
		resource, err = cache.GetDynamicWithGroup(r.Context(), kind, namespace, name, group)
		if err != nil {
			if strings.Contains(err.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.Contains(err.Error(), "not found") {
				s.writeError(w, http.StatusNotFound, err.Error())
				return
			}
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Set APIVersion and Kind for typed resources (informers don't populate these)
	setTypeMeta(resource)

	// Get relationships from cached topology. Pass the already-fetched
	// resource so ManagedBy synthesis uses the authoritative object instead
	// of a group-blind kind/name lookup.
	var relationships *topology.Relationships
	if cachedTopo, relIdx := s.broadcaster.GetCachedTopologyWithIndex(); cachedTopo != nil {
		relationships = topology.GetRelationshipsWithObject(kind, namespace, name, resource, cachedTopo,
			k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
			k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
	}

	// Return resource with relationships
	response := topology.ResourceWithRelationships{
		Resource:      resource,
		Relationships: relationships,
		HPADiagnosis:  hpaDiagnosisFor(resource),
	}

	// Enrich TLS secrets with parsed certificate info
	if secret, ok := resource.(*corev1.Secret); ok && secret.Type == corev1.SecretTypeTLS {
		if certPEM, exists := secret.Data["tls.crt"]; exists && len(certPEM) > 0 {
			certs := topology.ParsePEMCertificates(certPEM)
			if len(certs) > 0 {
				response.CertificateInfo = &SecretCertificateInfo{Certificates: certs}
			}
		}
	}

	s.writeJSON(w, response)
}

// handlePodMetrics fetches metrics for a specific pod from the metrics.k8s.io API
func (s *Server) handlePodMetrics(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	metrics, err := k8s.GetPodMetrics(r.Context(), namespace, name)
	if err != nil {
		if k8score.MetricsAPIUnavailable(err) {
			s.writeError(w, http.StatusNotFound, "Pod metrics not found (metrics-server may not be installed)")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, metrics)
}

// handleNodeMetrics fetches metrics for a specific node from the metrics.k8s.io API
func (s *Server) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !s.canRead(r, "", "nodes", "", "get") {
		s.writeError(w, http.StatusForbidden, "no access to nodes (cluster-scoped resource requires explicit RBAC)")
		return
	}

	metrics, err := k8s.GetNodeMetrics(r.Context(), name)
	if err != nil {
		if k8score.MetricsAPIUnavailable(err) {
			s.writeError(w, http.StatusNotFound, "Node metrics not found (metrics-server may not be installed)")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, metrics)
}

const (
	metricsAPIServiceKind  = "APIService"
	metricsAPIServiceGroup = "apiregistration.k8s.io"
)

var metricsAPIServiceNames = []string{
	"v1.metrics.k8s.io",
	"v1beta1.metrics.k8s.io",
}

func metricsAPIServiceNamesForVersion(version string) []string {
	if version == "" {
		return metricsAPIServiceNames
	}
	selected := version + ".metrics.k8s.io"
	names := make([]string, 0, len(metricsAPIServiceNames)+1)
	names = append(names, selected)
	for _, name := range metricsAPIServiceNames {
		if name != selected {
			names = append(names, name)
		}
	}
	return names
}

var metricsAPIServiceDiagnosisMemo = metricsAPIServiceDiagnosisCache{
	ttl: 5 * time.Second,
}

type metricsAPIServiceDiagnosisCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[metricsAPIServiceDiagnosisKey]metricsAPIServiceDiagnosisEntry
}

type metricsAPIServiceDiagnosisKey struct {
	includeConditionMessage bool
	metricsVersion          string
}

type metricsAPIServiceDiagnosisEntry struct {
	contextName string
	expiresAt   time.Time
	diagnosis   string
}

func (c *metricsAPIServiceDiagnosisCache) get(contextName string, key metricsAPIServiceDiagnosisKey, build func() (string, bool)) string {
	if c == nil || c.ttl <= 0 {
		diagnosis, _ := build()
		return diagnosis
	}

	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[metricsAPIServiceDiagnosisKey]metricsAPIServiceDiagnosisEntry, 4)
	}
	if entry, ok := c.entries[key]; ok && entry.contextName == contextName && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.diagnosis
	}
	c.mu.Unlock()

	diagnosis, cacheable := build()
	if !cacheable {
		return diagnosis
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[metricsAPIServiceDiagnosisKey]metricsAPIServiceDiagnosisEntry, 4)
	}
	now := time.Now()
	if entry, ok := c.entries[key]; ok && entry.contextName != contextName && now.Before(entry.expiresAt) {
		return diagnosis
	}
	c.entries[key] = metricsAPIServiceDiagnosisEntry{
		contextName: contextName,
		diagnosis:   diagnosis,
		expiresAt:   now.Add(c.ttl),
	}
	return diagnosis
}

func metricsHistoryCollectionError(ctx context.Context, source, errMsg string, includeAPIServiceConditionMessage bool) (string, string, string, bool) {
	if errMsg == "" {
		return "", "", "", false
	}
	if k8score.MetricsAPIUnavailable(fmt.Errorf("failed to get %s metrics: %s", strings.ToLower(source), errMsg)) {
		return fmt.Sprintf("%s metrics not found (metrics-server may not be installed)", source), errMsg, metricsUnavailableDiagnosis(ctx, includeAPIServiceConditionMessage), true
	}
	return errMsg, "", "", false
}

func metricsUnavailableDiagnosis(ctx context.Context, includeAPIServiceConditionMessage bool) string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return ""
	}

	contextName := k8s.GetContextName()
	metricsVersion := ""
	if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		if gvr, ok := discovery.GetGVRWithGroup("nodes", k8score.MetricsAPIGroup); ok {
			metricsVersion = gvr.Version
		}
	}
	key := metricsAPIServiceDiagnosisKey{includeConditionMessage: includeAPIServiceConditionMessage, metricsVersion: metricsVersion}
	return metricsAPIServiceDiagnosisMemo.get(contextName, key, func() (string, bool) {
		for _, name := range metricsAPIServiceNamesForVersion(metricsVersion) {
			apiService, err := cache.GetDynamicWithGroup(ctx, metricsAPIServiceKind, "", name, metricsAPIServiceGroup)
			if err == nil {
				return metricsAPIServiceLookupDiagnosis(name, apiService, nil, includeAPIServiceConditionMessage), isMetricsAPIServiceLookupCacheable(apiService, nil)
			}
			if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
				continue
			}
			return metricsAPIServiceLookupDiagnosis(name, nil, err, includeAPIServiceConditionMessage), false
		}
		return "The metrics.k8s.io APIService is not registered. Install metrics-server or restore that APIService.", true
	})
}

func isMetricsAPIServiceLookupCacheable(apiService *unstructured.Unstructured, err error) bool {
	if err == nil {
		return apiService != nil
	}
	return apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound)
}

func metricsAPIServiceLookupDiagnosis(apiServiceName string, apiService *unstructured.Unstructured, err error, includeConditionMessage bool) string {
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
			return fmt.Sprintf("The %s APIService is not registered. Install metrics-server or restore that APIService.", apiServiceName)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ""
		}
		log.Printf("[metrics] Failed to inspect %s APIService for metrics unavailable diagnosis: %v", apiServiceName, err)
		return ""
	}
	if apiService == nil {
		return ""
	}
	return metricsAPIServiceDiagnosis(apiServiceName, apiService, includeConditionMessage)
}

func metricsAPIServiceDiagnosis(apiServiceName string, apiService *unstructured.Unstructured, includeConditionMessage bool) string {
	condition, found := conditions.Find(apiService, "Available")
	if !found {
		return fmt.Sprintf("The %s APIService exists but has no Available condition. Check metrics-server and API aggregation status.", apiServiceName)
	}
	reasonSuffix := ""
	if condition.Reason != "" {
		reasonSuffix = " (" + condition.Reason + ")"
	}
	messageSuffix := ""
	if includeConditionMessage {
		messageSuffix = metricsAPIServiceConditionMessageSuffix(condition.Message)
	}

	switch condition.Status {
	case "True":
		return fmt.Sprintf("The %s APIService is Available, but metrics reads still fail. Check metrics-server logs and API aggregation errors.", apiServiceName)
	case "False", "Unknown":
		return metricsAPIServiceDiagnosisSentence(
			"The "+apiServiceName+" APIService is not Available"+reasonSuffix+messageSuffix,
			"Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		)
	default:
		return metricsAPIServiceDiagnosisSentence(
			"The "+apiServiceName+" APIService has an unexpected Available status"+reasonSuffix+messageSuffix,
			"Check metrics-server and API aggregation status.",
		)
	}
}

func metricsAPIServiceConditionMessageSuffix(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	message = strings.TrimRight(message, ":;,")
	if message == "" {
		return ""
	}

	const maxRunes = 180
	runes := []rune(message)
	if len(runes) > maxRunes {
		message = string(runes[:maxRunes]) + "..."
	}
	return ": " + message
}

func metricsAPIServiceDiagnosisSentence(subject, action string) string {
	if strings.HasSuffix(subject, ".") || strings.HasSuffix(subject, "?") || strings.HasSuffix(subject, "!") {
		return subject + " " + action
	}
	return subject + ". " + action
}

// handlePodMetricsHistory returns historical metrics for a specific pod
func (s *Server) handlePodMetricsHistory(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	store := k8s.GetMetricsHistory()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Metrics history not available")
		return
	}

	includeAPIServiceConditionMessage := s.canRead(r, metricsAPIServiceGroup, "apiservices", "", "get")
	history := podMetricsHistoryResponse(r.Context(), store.GetPodMetricsHistory(namespace, name), namespace, name, store.CollectionHealth(), includeAPIServiceConditionMessage)
	s.writeJSON(w, history)
}

func podMetricsHistoryResponse(ctx context.Context, history *k8s.PodMetricsHistory, namespace, name string, health k8s.MetricsCollectionHealth, includeAPIServiceConditionMessage bool) *k8s.PodMetricsHistory {
	if history == nil {
		history = &k8s.PodMetricsHistory{
			Namespace:  namespace,
			Name:       name,
			Containers: []k8s.ContainerMetricsHistory{},
		}
	}
	if health.PodMetrics.ConsecutiveErrors > 0 {
		history.CollectionError, history.RawCollectionError, history.MetricsUnavailableDiagnosis, history.MetricsUnavailable = metricsHistoryCollectionError(ctx, "Pod", health.PodMetrics.LastError, includeAPIServiceConditionMessage)
	}
	return history
}

// handleNodeMetricsHistory returns historical metrics for a specific node
func (s *Server) handleNodeMetricsHistory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !s.canRead(r, "", "nodes", "", "get") {
		s.writeError(w, http.StatusForbidden, "no access to nodes (cluster-scoped resource requires explicit RBAC)")
		return
	}

	store := k8s.GetMetricsHistory()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Metrics history not available")
		return
	}

	includeAPIServiceConditionMessage := s.canRead(r, metricsAPIServiceGroup, "apiservices", "", "get")
	history := nodeMetricsHistoryResponse(r.Context(), store.GetNodeMetricsHistory(name), name, store.CollectionHealth(), includeAPIServiceConditionMessage)
	s.writeJSON(w, history)
}

func nodeMetricsHistoryResponse(ctx context.Context, history *k8s.NodeMetricsHistory, name string, health k8s.MetricsCollectionHealth, includeAPIServiceConditionMessage bool) *k8s.NodeMetricsHistory {
	if history == nil {
		history = &k8s.NodeMetricsHistory{
			Name:       name,
			DataPoints: []k8s.MetricsDataPoint{},
		}
	}
	if health.NodeMetrics.ConsecutiveErrors > 0 {
		history.CollectionError, history.RawCollectionError, history.MetricsUnavailableDiagnosis, history.MetricsUnavailable = metricsHistoryCollectionError(ctx, "Node", health.NodeMetrics.LastError, includeAPIServiceConditionMessage)
	}
	return history
}

// handleTopPods returns the latest metrics for all pods (bulk endpoint for table view)
func (s *Server) handleTopPods(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []k8s.TopPodMetrics{})
		return
	}

	// Build metrics lookup (may be empty if metrics-server is unavailable)
	metricsMap := make(map[string]*k8s.TopPodMetrics)
	var containerUsage map[string]map[string]k8s.ContainerResourceMetrics
	if store := k8s.GetMetricsHistory(); store != nil {
		raw := store.GetAllPodMetricsLatest()
		for i := range raw {
			metricsMap[raw[i].Namespace+"/"+raw[i].Name] = &raw[i]
		}
		containerUsage = store.GetAllPodContainerMetricsLatest()
	}

	// Get pod lister from cache to enrich with requests/limits
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		// No cache — return metrics-only data
		result := make([]k8s.TopPodMetrics, 0, len(metricsMap))
		for _, m := range metricsMap {
			if !namespaceAllowed(namespaces, m.Namespace) {
				continue
			}
			result = append(result, *m)
		}
		s.writeJSON(w, result)
		return
	}

	var pods []*corev1.Pod
	if namespaces == nil {
		var err error
		pods, err = cache.Pods().List(labels.Everything())
		if err != nil {
			log.Printf("[metrics] Failed to list pods for top pods: %v", err)
			s.writeError(w, http.StatusInternalServerError, "Failed to list pods")
			return
		}
	} else {
		for _, ns := range namespaces {
			items, err := cache.Pods().Pods(ns).List(labels.Everything())
			if err != nil {
				log.Printf("[metrics] Failed to list pods for top pods in filtered namespace: %v", err)
				s.writeError(w, http.StatusInternalServerError, "Failed to list pods")
				return
			}
			pods = append(pods, items...)
		}
	}

	result := make([]k8s.TopPodMetrics, 0, len(pods))
	for _, pod := range pods {
		key := pod.Namespace + "/" + pod.Name
		entry := k8s.TopPodMetrics{
			Namespace: pod.Namespace,
			Name:      pod.Name,
		}

		// Merge usage metrics if available
		if m, ok := metricsMap[key]; ok {
			entry.CPU = m.CPU
			entry.Memory = m.Memory
		}

		// Sum requests and limits over the pod's running containers (regular
		// containers plus native sidecars) so they align with how usage is
		// summed — otherwise a native sidecar's usage inflates the pod's
		// over-limit percentage.
		totals := k8s.SumRunningContainerResources(pod)
		entry.CPURequest = totals.CPURequest
		entry.CPULimit = totals.CPULimit
		entry.MemoryRequest = totals.MemoryRequest
		entry.MemoryLimit = totals.MemoryLimit

		// Per-container breakdown drives the table's per-container display.
		// Nil for single-running-container pods, where the client falls back
		// to the pod-level sums above.
		entry.Containers = k8s.BuildPodContainerMetrics(pod, containerUsage[key])

		result = append(result, entry)
	}

	s.writeJSON(w, result)
}

// handleTopNodes returns the latest metrics for all nodes (bulk endpoint for table view)
func (s *Server) handleTopNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	if !s.canRead(r, "", "nodes", "", "list") {
		s.writeJSON(w, []k8s.TopNodeMetrics{})
		return
	}

	// Build metrics lookup (may be empty if metrics-server is unavailable)
	metricsMap := make(map[string]*k8s.TopNodeMetrics)
	if store := k8s.GetMetricsHistory(); store != nil {
		raw := store.GetAllNodeMetricsLatest()
		for i := range raw {
			metricsMap[raw[i].Name] = &raw[i]
		}
	}

	// Count running pods per node
	cache := k8s.GetResourceCache()
	podCounts := make(map[string]int)
	if cache != nil {
		if podLister := cache.Pods(); podLister != nil {
			pods, err := podLister.List(labels.Everything())
			if err != nil {
				log.Printf("[metrics] Failed to list pods for node pod counts: %v", err)
			} else {
				for _, pod := range pods {
					if pod.Spec.NodeName != "" && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
						podCounts[pod.Spec.NodeName]++
					}
				}
			}
		}
	}

	// List all nodes from cache
	var nodes []*corev1.Node
	if cache != nil {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			var err error
			nodes, err = nodeLister.List(labels.Everything())
			if err != nil {
				log.Printf("[metrics] Failed to list nodes: %v", err)
				s.writeError(w, http.StatusInternalServerError, "Failed to list nodes")
				return
			}
		}
	}
	if len(nodes) == 0 {
		s.writeJSON(w, []k8s.TopNodeMetrics{})
		return
	}

	result := make([]k8s.TopNodeMetrics, 0, len(nodes))
	for _, node := range nodes {
		entry := k8s.TopNodeMetrics{Name: node.Name}

		if m, ok := metricsMap[node.Name]; ok {
			entry.CPU = m.CPU
			entry.Memory = m.Memory
			entry.ObservedAt = m.ObservedAt
		}

		entry.PodCount = podCounts[node.Name]

		if cpu := node.Status.Allocatable[corev1.ResourceCPU]; !cpu.IsZero() {
			entry.CPUAllocatable = cpu.MilliValue() * 1000000 // millicores to nanocores
		}
		if mem := node.Status.Allocatable[corev1.ResourceMemory]; !mem.IsZero() {
			entry.MemoryAllocatable = mem.Value()
		}

		result = append(result, entry)
	}

	s.writeJSON(w, result)
}

// handleTopResources returns ranked live metrics for agents and compact
// diagnostics. It is intentionally separate from /metrics/top/{pods,nodes},
// which back UI tables and preserve their unsorted array shape.
func (s *Server) handleTopResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		kind = k8s.TopMetricsKindPods
	}
	opts := k8s.NormalizeTopMetricsOptions(k8s.TopMetricsOptions{
		Kind:      kind,
		Namespace: q.Get("namespace"),
		Sort:      q.Get("sort"),
		Limit:     parseLimit(q.Get("limit")),
	})

	if opts.Kind == k8s.TopMetricsKindNodes {
		if !s.canRead(r, "", "nodes", "", "list") {
			s.writeJSON(w, k8s.TopMetricsResponse{
				Kind:   opts.Kind,
				Sort:   opts.Sort,
				Reason: "no access to nodes (cluster-scoped resource requires explicit RBAC)",
			})
			return
		}
		s.writeJSON(w, k8s.BuildTopMetrics(opts))
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if opts.Namespace != "" {
		if !namespaceAllowed(namespaces, opts.Namespace) {
			s.writeJSON(w, k8s.TopMetricsResponse{
				Kind:      opts.Kind,
				Sort:      opts.Sort,
				Namespace: opts.Namespace,
				Reason:    "no access to namespace",
			})
			return
		}
		s.writeJSON(w, k8s.BuildTopMetrics(opts))
		return
	}
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, k8s.TopMetricsResponse{Kind: opts.Kind, Sort: opts.Sort, Reason: "no namespace access"})
		return
	}
	if namespaces == nil {
		s.writeJSON(w, k8s.BuildTopMetrics(opts))
		return
	}
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
		s.writeJSON(w, k8s.BuildTopMetrics(opts))
		return
	}
	s.writeError(w, http.StatusBadRequest, "namespace is required when access is limited to multiple namespaces")
}

func namespaceAllowed(namespaces []string, namespace string) bool {
	if namespaces == nil {
		return true
	}
	for _, ns := range namespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	eventsLister := cache.Events()
	if eventsLister == nil {
		s.writeError(w, http.StatusForbidden, "insufficient permissions to list events")
		return
	}

	var events any
	var err error

	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	} else if len(namespaces) == 1 {
		events, err = eventsLister.Events(namespaces[0]).List(labels.Everything())
	} else if len(namespaces) > 1 {
		var merged []any
		for _, ns := range namespaces {
			items, listErr := eventsLister.Events(ns).List(labels.Everything())
			if listErr != nil {
				s.writeError(w, http.StatusInternalServerError, listErr.Error())
				return
			}
			merged = appendSlice(merged, items)
		}
		events = merged
	} else {
		events, err = eventsLister.List(labels.Everything())
	}

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, events)
}

// clampMinSeqToPage bounds the advertised retention floor (the store's oldest
// retained seq) to the lowest seq actually delivered on this page. pageMinSeq
// is 0 for an empty page — nothing to be inconsistent with — so the raw floor
// stands. A genuine gap (low seqs evicted before the query, so pageMinSeq is
// already at or above the floor) is preserved, because min() keeps the floor.
// The clamp only bites when a fresh floor read has risen above a seq still
// present in the body (e.g. eviction during the slow RBAC filter), which would
// otherwise make a consumer record a false coverage gap and skip delivered
// events.
func clampMinSeqToPage(retainedFloor, pageMinSeq int64) int64 {
	if pageMinSeq > 0 && pageMinSeq < retainedFloor {
		return pageMinSeq
	}
	return retainedFloor
}

// handleChanges returns timeline events using the unified timeline.TimelineEvent format.
// This is the main timeline API endpoint - it queries the timeline store directly.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	}
	kind := r.URL.Query().Get("kind")
	name := r.URL.Query().Get("name")
	sinceStr := r.URL.Query().Get("since")
	sinceSeqStr := r.URL.Query().Get("since_seq")
	limitStr := r.URL.Query().Get("limit")
	filterPreset := r.URL.Query().Get("filter")
	includeK8sEvents := r.URL.Query().Get("include_k8s_events") != "false" // default true
	includeManaged := r.URL.Query().Get("include_managed") == "true"       // default false
	includeDeleted := r.URL.Query().Get("include_deleted") != "false"      // default true
	sourcesParam := r.URL.Query().Get("sources")                           // comma-separated, e.g. "k8s_event"

	// Parse since timestamp
	var since time.Time
	if sinceStr != "" {
		if ts, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = ts
		}
	}

	// Parse limit (default 200)
	limit := 200
	if limitStr != "" {
		if l, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil && l > 0 {
			if limit > 10000 {
				limit = 10000
			}
		}
	}

	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not available")
		return
	}

	// Build query options
	if filterPreset == "" {
		filterPreset = "default"
	}
	opts := timeline.QueryOptions{
		Namespaces:       namespaces,
		Since:            since,
		Limit:            limit,
		IncludeManaged:   includeManaged,
		ExcludeDeleted:   !includeDeleted,
		IncludeK8sEvents: includeK8sEvents,
		FilterPreset:     filterPreset,
		// The persistent store retains events from previously-connected
		// clusters; the timeline view answers for the current one only.
		ClusterContext: k8s.ActiveClusterContext(),
	}
	if sinceSeqStr != "" {
		n, err := strconv.ParseInt(sinceSeqStr, 10, 64)
		if err != nil || n < 0 {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid since_seq %q (expected a non-negative integer)", sinceSeqStr))
			return
		}
		opts.SinceSeq = n
		// An explicit since_seq — including 0 — selects seq paging (ascending
		// arrival order). since_seq=0 is the full-backfill page one: "every
		// row, oldest arrival first", resumable via the returned max seq.
		// Callers that want the newest-first full fetch omit the parameter
		// (the shipped web client only sends since_seq when its cursor > 0).
		opts.SeqPaging = true
	}
	if kind != "" {
		opts.Kinds = []string{kind}
	}
	if name != "" {
		opts.Names = []string{name}
	}
	if sourcesParam != "" {
		validSources := map[timeline.EventSource]bool{
			timeline.SourceInformer:   true,
			timeline.SourceK8sEvent:   true,
			timeline.SourceHistorical: true,
		}
		for raw := range strings.SplitSeq(sourcesParam, ",") {
			src := timeline.EventSource(strings.TrimSpace(raw))
			if src == "" {
				continue
			}
			if !validSources[src] {
				s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid source %q (valid: informer, k8s_event, historical)", src))
				return
			}
			opts.Sources = append(opts.Sources, src)
		}
	}

	events, err := store.Query(r.Context(), opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Cursor progress must be derived from the page BEFORE the RBAC filter
	// below: rows the user can't read still advance the delta frontier, or a
	// run of unreadable rows would pin a delta client's cursor in place while
	// it re-fetches the same page forever.
	//
	// Known limitation: rows dropped inside store.Query (managed resources,
	// excluded K8s events, presets — and every filter in the memory store)
	// can't advance maxSeq, so a client whose newest rows are all filtered
	// re-reads that filtered tail on every poll. A re-scan inefficiency, not
	// data loss: every matching row is still delivered. Worst case is the
	// SQLite store, whose SQL LIMIT applies before the Go-side content filter
	// — a matching row buried behind more than `limit` consecutive filtered
	// rows in seq order never surfaces through the delta path and reaches the
	// client only via its periodic full resync. A precise fix needs a
	// same-snapshot store max-seq that ignores content filters; deferred as
	// not worth the concurrency risk here.
	var maxSeq, pageMinSeq int64
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
		if pageMinSeq == 0 || e.Seq < pageMinSeq {
			pageMinSeq = e.Seq
		}
	}
	// Sample the retained floor here, from the same pre-filter moment maxSeq is
	// taken — before filterEventsByRBAC below issues its (slow, SAR-bound)
	// SubjectAccessReview round-trips. Reading it after the filter would let a
	// busy ring evict mid-request and raise OldestSeq above seqs still in this
	// response body, so the emitted floor could exceed a seq we actually
	// deliver. The clamp below closes any residual skew, but sampling early
	// keeps the two values consistent to begin with.
	retainedFloor := store.Stats().OldestSeq

	events = s.filterEventsByRBAC(r, events)

	// The store epoch validates delta cursors: seq restarts from 1 when the
	// store is re-created (process restart, context switch), so a client
	// holding a cursor from another epoch must full-resync instead of
	// trusting an empty delta as "nothing new".
	w.Header().Set("X-Radar-Timeline-Epoch", strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10))
	if maxSeq > 0 {
		w.Header().Set("X-Radar-Timeline-Max-Seq", strconv.FormatInt(maxSeq, 10))
	}
	// The store's oldest retained seq lets a consumer pulling forward from a
	// cursor detect that events below its cursor were evicted while it was
	// behind. Clamp it to the lowest seq actually delivered in this response
	// (pageMinSeq, computed pre-RBAC-filter so it aligns with maxSeq): the
	// header must never claim a floor above a seq present in the body, or a
	// consumer would record a false coverage gap and skip events it received.
	// A genuine gap — low seqs evicted before this query, so pageMinSeq is
	// already high — is preserved, since min() keeps the true floor. Mirrors
	// the Max-Seq header's marshaling and its skip-when-zero convention: an
	// empty store reports OldestSeq==0, so the header is omitted, not sent as 0.
	if minSeq := clampMinSeqToPage(retainedFloor, pageMinSeq); minSeq > 0 {
		w.Header().Set("X-Radar-Timeline-Min-Seq", strconv.FormatInt(minSeq, 10))
	}
	s.writeJSON(w, events)
}

// filterEventsByRBAC drops timeline events the calling user lacks RBAC to read,
// authorizing each event's exact kind via SubjectAccessReview.
//
// Namespace membership (parseNamespacesForUser) is the upstream gate, but it is
// not sufficient on its own: within a namespace the user CAN see, they may lack
// read on a specific kind (e.g. `list pods` but not `list secrets`) — the event
// still carries the resource name, labels, owner and a change summary, so a
// namespace-only gate leaks the existence of resources the user can't read.
// This closes that gap on both axes:
//   - namespaced events → require (group, resource) read in that namespace;
//   - cluster-scoped events (namespace=="") → require the cluster-scoped read.
//
// canRead memoizes per request on UserPermissions.canI, so repeated kinds are a
// map hit. Events whose kind can't be resolved (unknown CRD mid-discovery) fail
// closed. Auth disabled → canReadUser short-circuits to allow, so this is a
// no-op for the local single-user case.
func (s *Server) filterEventsByRBAC(r *http.Request, events []timeline.TimelineEvent) []timeline.TimelineEvent {
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		// Auth off → nothing to filter; skip GVR resolution entirely.
		return events
	}

	// Resolve each event's GVR once and collect the distinct
	// (group, resource, namespace) tuples to authorize.
	type key struct{ group, resource, namespace string }
	type resolution struct {
		ok bool
		k  key
	}
	resolved := make([]resolution, len(events))
	distinct := make(map[key]struct{})
	for i, e := range events {
		g, res, clusterScoped, ok := k8s.ResolveChangeGVR(e.Kind, k8s.GroupFromAPIVersion(e.APIVersion))
		// Cluster-scoped kinds authorize at namespace "": the event row may carry
		// a namespace (a K8s Event about a Node stores the Event's own namespace),
		// and a namespaced SAR is strictly broader than the cluster-scoped read.
		ns := e.Namespace
		if clusterScoped {
			ns = ""
		}
		resolved[i] = resolution{ok: ok, k: key{g, res, ns}}
		if ok {
			distinct[key{g, res, ns}] = struct{}{}
		}
	}

	// Prime the parent UserPermissions entry once so the parallel canReadUser
	// calls below share its SAR memo instead of racing to populate it.
	if s.permCache.Get(user.Username) == nil {
		_ = s.getUserNamespaces(r, []string{})
	}

	// Authorize distinct tuples in bounded parallel — a broad timeline load can
	// span many (kind, namespace) pairs and a serial SAR loop would stack the
	// round-trips. Mirrors filterNamespacesByCanRead / capabilities probing.
	allow := make(map[key]bool, len(distinct))
	var mu sync.Mutex
	const maxConcurrent = 16
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	ctx := r.Context()
	for k := range distinct {
		wg.Add(1)
		go func(k key) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ok := s.canReadUser(ctx, user, k.group, k.resource, k.namespace, "list")
			mu.Lock()
			allow[k] = ok
			mu.Unlock()
		}(k)
	}
	wg.Wait()

	filtered := events[:0]
	for i, e := range events {
		// Unresolvable kind → fail closed (drop). Otherwise keep only if the
		// per-kind SAR for this namespace (or cluster scope) allowed it.
		if resolved[i].ok && allow[resolved[i].k] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// changeAuthorizerForCtx returns a per-kind authorizer bound to the ctx user, for
// the shared k8s.ChangeReadAllowed gate. Callers on a request path have already
// primed the permission cache via parseNamespacesForUser, so canReadUser hits the
// memo. Nil user (auth off) is handled by canReadUser (returns true).
func (s *Server) changeAuthorizerForCtx(ctx context.Context) func(group, resource, namespace string) bool {
	user := auth.UserFromContext(ctx)
	return func(group, resource, namespace string) bool {
		return s.canReadUser(ctx, user, group, resource, namespace, "list")
	}
}

// filterTimelineEventsByRBAC drops timeline events the ctx user can't read, via
// the shared per-kind gate. For the low-volume secondary surfaces (dashboard,
// diagnose); the high-volume /api/changes path uses filterEventsByRBAC with its
// dedupe+parallel SAR. Auth off → returned unchanged.
func (s *Server) filterTimelineEventsByRBAC(ctx context.Context, events []timeline.TimelineEvent) []timeline.TimelineEvent {
	if auth.UserFromContext(ctx) == nil {
		return events
	}
	authz := s.changeAuthorizerForCtx(ctx)
	out := events[:0]
	for _, e := range events {
		if k8s.ChangeReadAllowed(e.Kind, e.APIVersion, e.Namespace, authz) {
			out = append(out, e)
		}
	}
	return out
}

// handleChangeChildren returns child resource changes for a given parent workload
func (s *Server) handleChangeChildren(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	ownerKind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	ownerName := chi.URLParam(r, "name")

	// Gate on the owner's namespace before touching the store: a user who can't
	// see this namespace must not read change history for workloads in it. The
	// per-kind SAR below (filterEventsByRBAC) is the authoritative gate; this is
	// the cheap RBAC-ceiling pre-check. getUserNamespaces (not
	// parseNamespacesForUser) so the header's namespace *view* pick can't hide a
	// namespace the user has real access to.
	if allowed := s.getUserNamespaces(r, nil); !namespaceInAllowed(allowed, namespace) {
		s.writeJSON(w, []timeline.TimelineEvent{})
		return
	}

	sinceStr := r.URL.Query().Get("since")

	var since time.Time
	if sinceStr != "" {
		if ts, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = ts
		}
	} else {
		// Default to last hour
		since = time.Now().Add(-1 * time.Hour)
	}

	store := timeline.GetStore()
	if store == nil {
		s.writeJSON(w, []timeline.TimelineEvent{})
		return
	}

	children, err := store.GetChangesForOwner(r.Context(), ownerKind, namespace, ownerName, k8s.ActiveClusterContext(), since, 100)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	children = s.filterEventsByRBAC(r, children)

	s.writeJSON(w, children)
}

// namespaceInAllowed reports whether `namespace` is within an allowed set.
// nil allowed means cluster-wide access (all namespaces); an empty non-nil
// slice means no access. Mirrors the nil-vs-empty convention used throughout
// the per-user namespace filtering.
func namespaceInAllowed(allowed []string, namespace string) bool {
	if allowed == nil {
		return true
	}
	return slices.Contains(allowed, namespace)
}

// handleApplyResource creates or updates a Kubernetes resource from YAML.
// Supports ?mode=create (strict) or ?mode=apply (default, server-side apply).
// Supports ?dryRun=true for validation without persisting.
// Accepts multi-document YAML (split on ---).
func (s *Server) handleApplyResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	yamlContent := strings.TrimSpace(string(body))
	if yamlContent == "" {
		s.writeError(w, http.StatusBadRequest, "request body is empty")
		return
	}

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "apply"
	}
	if mode != "apply" && mode != "create" {
		s.writeError(w, http.StatusBadRequest, "mode must be 'apply' or 'create'")
		return
	}
	dryRun := r.URL.Query().Get("dryRun") == "true"
	force := r.URL.Query().Get("force") == "true"
	reviewedContext := r.URL.Query().Get("reviewedContext")
	reviewedResourceVersions := make(map[int]string)
	if encoded := r.URL.Query().Get("reviewedVersions"); encoded != "" {
		if dryRun {
			s.writeError(w, http.StatusBadRequest, "reviewed resource versions require a non-dry-run request")
			return
		}
		if err := json.Unmarshal([]byte(encoded), &reviewedResourceVersions); err != nil {
			s.writeError(w, http.StatusBadRequest, "reviewedVersions must be a document-index to resourceVersion map")
			return
		}
	}

	client, contextName := s.getDynamicClientSnapshotForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	if reviewedContext != "" && reviewedContext != contextName {
		s.writeError(w, http.StatusConflict, "cluster context changed after review; review the YAML again before applying")
		return
	}

	// Split multi-document YAML
	docs := k8s.SplitYAMLDocuments(yamlContent)
	for index := range reviewedResourceVersions {
		if index < 0 || index >= len(docs) {
			s.writeError(w, http.StatusBadRequest, "reviewedVersions contains an invalid document index")
			return
		}
	}

	var results []k8s.ApplyResourceResult
	for i, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		reviewedResourceVersion, reviewed := reviewedResourceVersions[i]
		result, err := k8s.ApplyResourceWithClient(r.Context(), k8s.ApplyResourceOptions{
			YAML:                    doc,
			Mode:                    mode,
			DryRun:                  dryRun,
			Force:                   force,
			ExpectedResourceVersion: reviewedResourceVersion,
			ExpectedResourceAbsent:  reviewed && reviewedResourceVersion == "",
		}, client)
		if err != nil {
			errMsg := err.Error()
			if len(docs) > 1 {
				errMsg = fmt.Sprintf("document %d: %s", i+1, errMsg)
			}
			if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
				s.writeApplyResourceError(w, http.StatusConflict, errMsg, results, i, len(docs))
				return
			}
			if apierrors.IsForbidden(err) {
				s.writeApplyResourceError(w, http.StatusForbidden, errMsg, results, i, len(docs))
				return
			}
			if apierrors.IsNotFound(err) {
				s.writeApplyResourceError(w, http.StatusNotFound, errMsg, results, i, len(docs))
				return
			}
			if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
				s.writeApplyResourceError(w, http.StatusUnprocessableEntity, errMsg, results, i, len(docs))
				return
			}
			if strings.Contains(err.Error(), "invalid YAML") || strings.Contains(err.Error(), "must include") {
				s.writeApplyResourceError(w, http.StatusBadRequest, errMsg, results, i, len(docs))
				return
			}
			log.Printf("[apply] Failed to apply resource: %v", err)
			s.writeApplyResourceError(w, http.StatusInternalServerError, errMsg, results, i, len(docs))
			return
		}
		auth.AuditLog(r, result.Namespace, result.Name)
		results = append(results, *result)
	}

	if len(results) == 0 {
		s.writeError(w, http.StatusBadRequest, "no valid YAML documents found")
		return
	}

	s.writeJSON(w, results)
}

// handleUpdateResource updates a Kubernetes resource from YAML
func (s *Server) handleUpdateResource(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Read request body (YAML content)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Update the resource (use impersonated client when auth is enabled)
	auth.AuditLog(r, namespace, name)
	client, contextName := s.getDynamicClientSnapshotForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	// The editor resubmits the full live manifest, so an unforced apply would
	// conflict on every field owned by Helm/Flux/Argo/a controller. Default to
	// force; the editor's checkbox sends force=false to opt out.
	force := r.URL.Query().Get("force") != "false"
	expectedResourceVersion := r.URL.Query().Get("resourceVersion")
	reviewedContext := r.URL.Query().Get("reviewedContext")
	if reviewedContext != "" && reviewedContext != contextName {
		s.writeError(w, http.StatusConflict, "cluster context changed after review; review the YAML again before saving")
		return
	}
	result, err := k8s.UpdateResourceWithClient(r.Context(), k8s.UpdateResourceOptions{
		Kind:                    kind,
		Namespace:               namespace,
		Name:                    name,
		YAML:                    string(body),
		Force:                   force,
		ExpectedResourceVersion: expectedResourceVersion,
	}, client)
	if err != nil {
		if apierrors.IsConflict(err) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "invalid YAML") || strings.Contains(err.Error(), "mismatch") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, result)
}

// handleDeleteResource deletes a Kubernetes resource
func (s *Server) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	force := r.URL.Query().Get("force") == "true"
	group := r.URL.Query().Get("group")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.DeleteResourceWithClient(r.Context(), k8s.DeleteResourceOptions{
		Kind:      kind,
		Group:     group,
		Namespace: namespace,
		Name:      name,
		Force:     force,
	}, client)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "stuck in Terminating state") {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		log.Printf("[delete] Failed to delete %s %s/%s (force=%v): %v", kind, namespace, name, force, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCascadeDeletePreview returns a preview of resources that would be garbage-collected
// if the specified resource is deleted (via Kubernetes owner reference cascade).
func (s *Server) handleCascadeDeletePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	if namespace == "_" {
		namespace = ""
	}

	cachedTopo := s.broadcaster.GetCachedTopology()
	dp := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
	preview := topology.GetCascadeDeletePreview(topology.ResourceRef{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Group:     group,
	}, cachedTopo, dp)

	s.writeJSON(w, preview)
}

// handleTriggerCronJob creates a Job from a CronJob
func (s *Server) handleTriggerCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	result, err := k8s.TriggerCronJobWithClient(r.Context(), namespace, name, client)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]any{
		"message": "Job created successfully",
		"jobName": result.GetName(),
	})
}

// handleSuspendCronJob suspends a CronJob
func (s *Server) handleSuspendCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.SetCronJobSuspendWithClient(r.Context(), namespace, name, true, client)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"message": "CronJob suspended"})
}

// handleResumeCronJob resumes a suspended CronJob
func (s *Server) handleResumeCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.SetCronJobSuspendWithClient(r.Context(), namespace, name, false, client)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"message": "CronJob resumed"})
}

// handleRestartWorkload performs a rolling restart on a Deployment, StatefulSet, or DaemonSet
func (s *Server) handleRestartWorkload(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Validate that this is a restartable workload type
	validKinds := map[string]bool{
		"deployments":  true,
		"statefulsets": true,
		"daemonsets":   true,
		"rollouts":     true,
	}
	normalizedKind := k8score.NormalizeWorkloadKind(strings.ToLower(kind))
	if !validKinds[normalizedKind] {
		s.writeError(w, http.StatusBadRequest, "only Deployments, StatefulSets, DaemonSets, and Rollouts can be restarted")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.RestartWorkloadWithClient(r.Context(), kind, namespace, name, client)
	if err != nil {
		// Restart reaches a terminating Rollout through get(), so it carries the
		// same sentinels rollback does.
		if normalizedKind == "rollouts" {
			s.writeRolloutError(w, err, "restart", namespace, name)
			return
		}
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"message": "Workload restart initiated"})
}

// handleScaleWorkload scales a Deployment or StatefulSet to a new replica count
func (s *Server) handleScaleWorkload(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Parse request body
	var req struct {
		Replicas int32 `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate replica count
	if req.Replicas < 0 {
		s.writeError(w, http.StatusBadRequest, "replicas cannot be negative")
		return
	}
	if req.Replicas > 10000 {
		s.writeError(w, http.StatusBadRequest, "replicas cannot exceed 10000")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.ScaleWorkloadWithClient(r.Context(), kind, namespace, name, req.Replicas, client)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not supported") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[scale] Failed to scale %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]any{
		"message":  "Workload scaled",
		"replicas": req.Replicas,
	})
}

var rollbackableWorkloadKinds = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
	"rollouts":     true,
}

// handleWorkloadRevisions returns the revision history for a Deployment, StatefulSet, DaemonSet, or Rollout
func (s *Server) handleWorkloadRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Validate that this is a rollbackable workload type
	if !rollbackableWorkloadKinds[k8score.NormalizeWorkloadKind(strings.ToLower(kind))] {
		s.writeError(w, http.StatusBadRequest, "revision history only available for Deployments, StatefulSets, DaemonSets, and Rollouts")
		return
	}

	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	revisions, err := k8s.ListWorkloadRevisionsWithClient(r.Context(), kind, namespace, name, client)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[revisions] Failed to list revisions for %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, revisions)
}

// handleRollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a previous revision
func (s *Server) handleRollbackWorkload(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Parse request body
	var req struct {
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate revision
	if req.Revision <= 0 {
		s.writeError(w, http.StatusBadRequest, "revision must be a positive integer")
		return
	}

	// Validate that this is a rollbackable workload type
	normalizedKind := k8score.NormalizeWorkloadKind(strings.ToLower(kind))
	if !rollbackableWorkloadKinds[normalizedKind] {
		s.writeError(w, http.StatusBadRequest, "rollback only available for Deployments, StatefulSets, DaemonSets, and Rollouts")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.RollbackWorkloadWithClient(r.Context(), kind, namespace, name, req.Revision, client)
	if err != nil {
		// Rollouts carry sentinel errors (unchanged template, unsupported
		// workloadRef, terminating) that the substring checks below can't map.
		if normalizedKind == "rollouts" {
			s.writeRolloutError(w, err, "rollback", namespace, name)
			return
		}
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not supported") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[rollback] Failed to rollback %s %s/%s to revision %d: %v", kind, namespace, name, req.Revision, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{
		"message": fmt.Sprintf("Rollback to revision %d initiated", req.Revision),
	})
}

// Session management handlers

// SessionCounts returns counts of active sessions
type SessionCounts struct {
	PortForwards   int `json:"portForwards"`
	ExecSessions   int `json:"execSessions"`
	LocalTerminals int `json:"localTerminals"`
	Total          int `json:"total"`
}

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	pf := GetPortForwardCount()
	exec := GetExecSessionCount()
	lt := GetLocalTermSessionCount()
	s.writeJSON(w, SessionCounts{
		PortForwards:   pf,
		ExecSessions:   exec,
		LocalTerminals: lt,
		Total:          pf + exec + lt,
	})
}

// StopAllSessions terminates all active port forwards and exec sessions
func StopAllSessions() {
	log.Println("Stopping all active sessions...")
	StopAllPortForwards()
	StopAllExecSessions()
}

// Context switching handlers

func (s *Server) handleListContexts(w http.ResponseWriter, r *http.Request) {
	contexts, err := k8s.GetAvailableContexts()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, contexts)
}

func (s *Server) handleSwitchContext(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "context name is required")
		return
	}

	// URL-decode the context name (handles special chars like : and / in AWS ARNs)
	decodedName, err := url.PathUnescape(name)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid context name encoding")
		return
	}
	name = decodedName

	// Check if we're in-cluster mode
	if k8s.IsInCluster() {
		s.writeError(w, http.StatusBadRequest, "cannot switch context when running in-cluster")
		return
	}

	if err := k8s.PerformContextSwitch(name); err != nil {
		// A preflight rejection fails before any teardown — the current cluster is
		// still connected, so don't poison the global connection status.
		if errors.Is(err, k8s.ErrContextSwitchPreflight) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   name,
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Per-user state (permCache, namespace picks, capabilities cache) is
	// cleared by the OnContextSwitch callback registered in New().
	// PerformContextSwitch published the connected status while still holding
	// the context-operation lock; publishing again here would race a queued
	// operation's teardown.

	// Return the new cluster info
	info, err := k8s.GetClusterInfo(r.Context())
	if err != nil {
		// Context switched successfully but couldn't get info - still return success
		s.writeJSON(w, map[string]string{"status": "ok", "context": name})
		return
	}

	s.writeJSON(w, info)
}

// Connection status handlers (for graceful startup)

func (s *Server) handleConnectionStatus(w http.ResponseWriter, r *http.Request) {
	status := k8s.GetConnectionStatus()

	response := map[string]any{
		"state":           status.State,
		"context":         status.Context,
		"clusterName":     status.ClusterName,
		"error":           status.Error,
		"errorType":       status.ErrorType,
		"progressMessage": status.ProgressMsg,
		// Lets the browser stand down its auto-retry for the whole auth-loss
		// episode, even when the live errorType flips to non-auth values.
		"authRecoveryOwed": k8s.RuntimeAuthRecoveryOwed(),
	}
	// Context enumeration re-reads kubeconfig files (under the client write
	// lock in multi-file mode) — too expensive for the UI's perpetual
	// fallback poll, which opts out via ?contexts=0.
	if r.URL.Query().Get("contexts") != "0" {
		contexts, _ := k8s.GetAvailableContexts() // Always works (reads kubeconfig)
		response["contexts"] = contexts
	}

	s.writeJSON(w, response)
}

func (s *Server) handleConnectionRetry(w http.ResponseWriter, r *http.Request) {
	ctx := k8s.GetContextName()
	if ctx == "" {
		s.writeError(w, http.StatusBadRequest, "no context configured")
		return
	}

	if err := k8s.RetryCurrentConnection(); err != nil {
		if errors.Is(err, k8s.ErrContextSwitchPreflight) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, k8s.ErrReconnectSuperseded) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		errorType := k8s.ClassifyError(err)
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   ctx,
			Error:     err.Error(),
			ErrorType: errorType,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		if encodeErr := json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "errorType": errorType}); encodeErr != nil {
			log.Printf("Failed to encode connection retry error response: %v", encodeErr)
		}
		return
	}

	// RetryCurrentConnection published the connected status under the
	// context-operation lock; a second publish here would race a queued
	// operation's teardown.
	s.writeJSON(w, k8s.GetConnectionStatus())
}

// CAPI handlers

func (s *Server) handleCAPIClusterKubeconfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	ns := chi.URLParam(r, "ns")
	name := chi.URLParam(r, "name")
	if ns == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "namespace and name are required")
		return
	}

	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// CAPI stores workload cluster kubeconfig in a Secret named "{cluster-name}-kubeconfig"
	secretName := name + "-kubeconfig"
	secret, err := client.CoreV1().Secrets(ns).Get(r.Context(), secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("kubeconfig secret %q not found in namespace %q", secretName, ns))
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to read kubeconfig secret in namespace %q", ns))
			return
		}
		log.Printf("[capi] Failed to get kubeconfig secret %s/%s: %v", ns, secretName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The kubeconfig is stored in the "value" key
	kubeconfigData, ok := secret.Data["value"]
	if !ok {
		s.writeError(w, http.StatusNotFound, "kubeconfig secret does not contain 'value' key")
		return
	}

	// Return as YAML download
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"-kubeconfig.yaml"))
	if _, err := w.Write(kubeconfigData); err != nil {
		log.Printf("[capi] Failed to write kubeconfig response for %s/%s: %v", ns, name, err)
	}
}

func (s *Server) handleCAPIClusterConnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	ns := chi.URLParam(r, "ns")
	name := chi.URLParam(r, "name")
	if ns == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "namespace and name are required")
		return
	}

	if k8s.IsInCluster() {
		s.writeError(w, http.StatusBadRequest, "cannot connect to workload cluster when running in-cluster")
		return
	}

	client, managementBinding := s.getClientSafetySnapshotForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// Fetch the kubeconfig Secret
	secretName := name + "-kubeconfig"
	secret, err := client.CoreV1().Secrets(ns).Get(r.Context(), secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("kubeconfig secret %q not found in namespace %q", secretName, ns))
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to read kubeconfig secret in namespace %q", ns))
			return
		}
		log.Printf("[capi] Failed to get kubeconfig secret %s/%s: %v", ns, secretName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	kubeconfigData, ok := secret.Data["value"]
	if !ok {
		s.writeError(w, http.StatusNotFound, "kubeconfig secret does not contain 'value' key")
		return
	}

	// Parse the workload cluster kubeconfig
	newConfig, err := clientcmd.Load(kubeconfigData)
	if err != nil {
		log.Printf("[capi] Failed to parse kubeconfig from secret %s/%s: %v", ns, secretName, err)
		s.writeError(w, http.StatusInternalServerError, "failed to parse kubeconfig: "+err.Error())
		return
	}

	// Determine the context name to use from the workload kubeconfig
	contextName := newConfig.CurrentContext
	if contextName == "" {
		// Use the first available context
		for ctxName := range newConfig.Contexts {
			contextName = ctxName
			break
		}
	}
	if contextName == "" {
		s.writeError(w, http.StatusBadRequest, "workload cluster kubeconfig contains no contexts")
		return
	}

	// Merge into the user's kubeconfig. The returned qualifiedName reflects
	// any disambiguation the registry had to do (e.g. if another file already
	// owned this context name). Always switch using the qualified name.
	safetyBinding := k8s.CAPIClusterSafetyBinding(managementBinding, ns, name)
	qualifiedName, mergedPath, created, err := k8s.MergeAndSwitchContext(kubeconfigData, contextName, safetyBinding)
	if err != nil {
		log.Printf("[capi] Failed to merge kubeconfig for cluster %s/%s: %v", ns, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to connect: "+err.Error())
		return
	}

	if err := k8s.PerformContextSwitch(qualifiedName); err != nil {
		discarded := k8s.DiscardFailedMergedContext(mergedPath, created)
		if discarded {
			log.Printf("[capi] Discarded inactive kubeconfig after failed switch to %q", qualifiedName)
		}
		if errors.Is(err, k8s.ErrContextSwitchPreflight) {
			s.writeError(w, http.StatusBadRequest, "failed to switch context: "+err.Error())
			return
		}
		statusContext := qualifiedName
		if discarded {
			statusContext = k8s.GetContextName()
		}
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   statusContext,
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		s.writeError(w, http.StatusInternalServerError, "failed to switch context: "+err.Error())
		return
	}

	// Per-user state cleared via the OnContextSwitch callback (see New()).
	// Connected status was published by PerformContextSwitch under the
	// context-operation lock.

	// Use %q on user-influenced values (context name derived from an uploaded
	// kubeconfig YAML, temp path partly includes the system TMPDIR) so a
	// crafted context name can't inject forged log lines when Radar's stderr
	// is scraped by a log aggregator. CodeQL alert "Log entries created from
	// user input".
	log.Printf("[capi] Connected to workload cluster %s/%s (context: %q, kubeconfig: %q)", ns, name, qualifiedName, mergedPath)

	s.writeJSON(w, map[string]string{
		"status":  "connected",
		"context": qualifiedName,
	})
}

// Helper methods

func (s *Server) writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	// Nil slices serialize as "null" in JSON — normalize to empty array "[]"
	// to avoid frontend errors when the response is expected to be an array.
	if data == nil || (reflect.TypeOf(data) != nil && reflect.TypeOf(data).Kind() == reflect.Slice && reflect.ValueOf(data).IsNil()) {
		data = []any{}
	}
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Can't change HTTP status at this point, but log for debugging
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		log.Printf("Failed to encode error response: %v", err)
	}
}

func (s *Server) writeApplyResourceError(w http.ResponseWriter, status int, message string, results []k8s.ApplyResourceResult, failedIndex, total int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload := struct {
		Error       string                    `json:"error"`
		Results     []k8s.ApplyResourceResult `json:"results,omitempty"`
		FailedIndex int                       `json:"failedIndex"`
		Total       int                       `json:"total"`
	}{
		Error:       message,
		Results:     results,
		FailedIndex: failedIndex,
		Total:       total,
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("Failed to encode apply error response: %v", err)
	}
}

// writeErrorCode is writeError plus a stable machine-readable `error_code`
// the frontend branches on (e.g. cloud_role_insufficient → "your role can't do
// this" instead of a generic auth failure).
func (s *Server) writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message, "error_code": code}); err != nil {
		log.Printf("Failed to encode error response: %v", err)
	}
}

// requireCloudRole gates a mutating handler on the caller's Cloud role tier,
// mirroring internal/helm's gate. Returns true if the request should proceed.
//
// Callers with no Cloud role (OSS, OIDC, or running outside Cloud's tunnel)
// bypass the gate — radar OSS keeps using only K8s RBAC for authz, so the
// single-user laptop case is never 403'd out of its own config. The gate is
// strictly additive for Cloud-attributed callers: when their tier is below
// `min`, returns 403 with error_code=cloud_role_insufficient.
func (s *Server) requireCloudRole(w http.ResponseWriter, r *http.Request, min auth.CloudRole, opName string) bool {
	role := auth.CloudRoleFromContext(r.Context())
	if role.AtLeast(min) {
		return true
	}
	username := "unknown"
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	log.Printf("[settings] Cloud role %q denied %s for user %q (need at least %q): %q", role, opName, username, min, r.URL.Path)
	s.writeErrorCode(w, http.StatusForbidden, auth.ErrCodeCloudRoleInsufficient,
		"Your Radar Cloud role ("+role.String()+") cannot "+opName+". Requires "+string(min)+" or higher.")
	return false
}

// requireConnected returns false and writes a 503 error if not connected to cluster.
// Use at the start of handlers that require an active cluster connection.
func (s *Server) requireConnected(w http.ResponseWriter) bool {
	if !k8s.IsConnected() {
		s.writeError(w, http.StatusServiceUnavailable, "Not connected to cluster")
		return false
	}
	return true
}

// Auth handlers and helpers

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	for _, c := range auth.ClearSessionCookie(r) {
		http.SetCookie(w, c)
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"status": "logged out"}
	// Clearing Radar's cookie alone doesn't switch users: the proxy
	// re-injects the identity header on the next request. Redirect to the
	// proxy's sign-out URL so the upstream session is torn down too.
	if s.authConfig.ProxyLogoutURL != "" {
		resp["redirectTo"] = s.authConfig.ProxyLogoutURL
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"authEnabled": s.authConfig.Enabled(),
		"authMode":    s.authConfig.Mode,
	}
	// Tells the frontend whether proxy-mode logout can tear down the upstream
	// session (vs only clearing Radar's cookie), so it can warn the user.
	if s.authConfig.Mode == "proxy" {
		resp["proxyLogoutConfigured"] = s.authConfig.ProxyLogoutURL != ""
	}
	if user := auth.UserFromContext(r.Context()); user != nil {
		resp["username"] = user.Username
		resp["groups"] = user.Groups
		// Pre-compute the Cloud role so the frontend doesn't have to
		// re-parse `cloud:<tier>` group prefixes. Empty string means
		// "not running under Cloud" (OSS deploy or no role group).
		if role := auth.CloudRoleFromGroups(user.Groups); role != auth.RoleNone {
			resp["cloudRole"] = string(role)
		}
	}
	s.writeJSON(w, resp)
}

// getDynamicClientForRequest returns an impersonated dynamic client when auth is enabled,
// or the shared client when auth is disabled. Returns nil if impersonation fails
// (never falls back to the ServiceAccount client). Callers must handle nil.
func (s *Server) getDynamicClientForRequest(r *http.Request) dynamic.Interface {
	client, _ := s.getDynamicClientSnapshotForRequest(r)
	return client
}

func (s *Server) getDynamicClientSnapshotForRequest(r *http.Request) (dynamic.Interface, string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		client, contextName, err := k8s.ImpersonatedDynamicClientSnapshot(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", k8s.SanitizeForLog(user.Username), err)
			return nil, contextName
		}
		return client, contextName
	}
	return k8s.GetDynamicClientSnapshot()
}

// getConfigForRequest returns an impersonated REST config when auth is enabled,
// or the shared config when auth is disabled. Returns nil if impersonation fails
// (never falls back to the ServiceAccount config). Callers must handle nil.
func (s *Server) getConfigForRequest(r *http.Request) *rest.Config {
	config, _ := s.getConfigSnapshotForRequest(r)
	return config
}

func (s *Server) getConfigSnapshotForRequest(r *http.Request) (*rest.Config, string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		cfg, contextName, err := k8s.ImpersonatedConfigSnapshot(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", k8s.SanitizeForLog(user.Username), err)
			return nil, contextName
		}
		return cfg, contextName
	}
	return k8s.GetConfigSnapshot()
}

// getClientForRequest returns an impersonated typed client when auth is enabled,
// or the shared client when auth is disabled. Returns nil if impersonation fails
// (never falls back to the ServiceAccount client). Callers must handle nil.
func (s *Server) getClientForRequest(r *http.Request) kubernetes.Interface {
	if user := auth.UserFromContext(r.Context()); user != nil {
		client, err := k8s.ImpersonatedClient(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", k8s.SanitizeForLog(user.Username), err)
			return nil
		}
		return client
	}
	// Typed-nil guard: k8s.GetClient returns *Clientset, and wrapping a nil
	// pointer in kubernetes.Interface produces a non-nil interface. Callers
	// do `if client == nil { ... }`, which would slip past and NPE on the
	// first method call.
	if c := k8s.GetClient(); c != nil {
		return c
	}
	return nil
}

func (s *Server) getClientSafetySnapshotForRequest(r *http.Request) (kubernetes.Interface, string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		client, binding, err := k8s.ImpersonatedClientSafetySnapshot(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", k8s.SanitizeForLog(user.Username), err)
			return nil, binding
		}
		return client, binding
	}
	client, binding := k8s.GetClientSafetySnapshot()
	if client == nil {
		return nil, binding
	}
	return client, binding
}

// getUserNamespaces returns namespace filtering for the current user.
// When auth is disabled, returns the requested namespaces unchanged.
// When auth is enabled, intersects with the user's allowed namespaces.
func (s *Server) getUserNamespaces(r *http.Request, requested []string) []string {
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		return requested
	}

	perms := s.permCache.Get(user.Username)
	if perms != nil {
		log.Printf("[auth] Using cached permissions for %s: allowed=%v", user.Username, perms.AllowedNamespaces == nil)
	}
	if perms == nil {
		log.Printf("[auth] No cached permissions for %s — discovering namespaces", user.Username)
		// Discover namespaces synchronously on first request
		client := k8s.GetClient()
		if client == nil {
			log.Printf("[auth] K8s client not available for namespace discovery (user=%s) — denying access", k8s.SanitizeForLog(user.Username))
			return []string{} // fail-closed: cannot verify permissions
		}

		// Get all namespace names from cache
		var allNamespaces []string
		if cache := k8s.GetResourceCache(); cache != nil {
			if nsLister := cache.Namespaces(); nsLister != nil {
				nsList, _ := nsLister.List(labels.Everything())
				for _, ns := range nsList {
					allNamespaces = append(allNamespaces, ns.Name)
				}
			}
		}
		// Fallback for namespace-scoped SAs: when the cluster-wide namespace
		// informer is unavailable (SA lacks list-namespaces RBAC), the lister
		// is empty and DiscoverNamespaces' per-namespace SAR loop has nothing
		// to iterate — every non-admin user gets [] even when they have RBAC
		// on the SA's bound namespace. Seed candidates from the kubeconfig
		// context / --namespace fallback so those users get surfaced.
		if len(allNamespaces) == 0 {
			if accessible, _ := k8s.GetAccessibleNamespaces(r.Context()); len(accessible) > 0 {
				allNamespaces = accessible
			}
		}

		allowed, err := auth.DiscoverNamespaces(r.Context(), client, user.Username, user.Groups, allNamespaces)
		if err != nil {
			log.Printf("[auth] Failed to discover namespaces for %s: %v — denying access (fail-closed)", k8s.SanitizeForLog(user.Username), err)
			return []string{} // fail-closed: no access on discovery error
		}

		log.Printf("[auth] DiscoverNamespaces result for %s: allowed=%v (nil=all, []=none)", user.Username, allowed)
		perms = &auth.UserPermissions{AllowedNamespaces: allowed}
		s.permCache.Set(user.Username, perms)
	}

	return auth.FilterNamespacesForUser(requested, user, perms)
}

// handleSSE wraps the SSEBroadcaster's HandleSSE with per-user namespace filtering.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	// SSE is usually opened without an explicit namespace filter so the
	// frontend can keep one all-namespace stream and filter topology/events
	// locally. Do not fall back to the saved namespace picker here; only
	// server-filter when the request explicitly asks for it (large-cluster
	// mode), while still intersecting with RBAC for authenticated users.
	namespaces := parseNamespaces(r.URL.Query())
	if namespaces == nil {
		namespaces = s.getUserNamespaces(r, nil)
	} else {
		namespaces = s.getUserNamespaces(r, namespaces)
	}
	// Re-encode filtered namespaces back into query params for the broadcaster
	q := r.URL.Query()
	q.Del("namespace")
	q.Del("namespaces")
	if namespaces != nil {
		if len(namespaces) == 0 {
			// User has no namespace access — use impossible filter to block all events
			q.Set("namespaces", "__no_access__")
		} else {
			q.Set("namespaces", strings.Join(namespaces, ","))
		}
	}
	r.URL.RawQuery = q.Encode()
	// Cluster-scoped topology kinds (Nodes, PV, StorageClass, NodePool, …) have
	// no namespace to filter on, so strip the ones this user can't list — the
	// same gate the REST /api/topology handler applies. Resolved here (the
	// request is available) and threaded through so the broadcast loop never
	// runs a SAR.
	deny := s.deniedClusterScopedTopoKinds(r)
	// Namespace objects are cluster-scoped too (a Namespace k8s_event carries
	// namespace=""), but Namespace is deliberately not in the topology table.
	// Deny its change frames when the user can't list namespaces, so a
	// namespace-restricted user doesn't learn namespace names via SSE.
	if !s.canRead(r, "", "namespaces", "", "list") {
		if deny == nil {
			deny = map[topology.NodeKind]bool{}
		}
		deny[topology.KindNamespace] = true
	}
	// Per-kind authorizer for change (k8s_event) frames, bound to this request's
	// user + context so the broadcast goroutine can SAR-gate diff-bearing frames
	// without a request. Prime the permission cache once here (the request is
	// available) so the closure's canReadUser calls hit the memo. When auth is
	// off, UserFromContext is nil and canReadUser short-circuits to allow.
	user := auth.UserFromContext(r.Context())
	if user != nil && s.permCache != nil && s.permCache.Get(user.Username) == nil {
		_ = s.getUserNamespaces(r, []string{})
	}
	s.broadcaster.HandleSSE(w, r, deny, s.newSSEChangeAuthorizer(r.Context(), user))
}

const (
	// sseChangeAuthTTL bounds how long an SSE client's per-frame authorization
	// decision is cached before re-checking, so a revoked grant propagates within
	// the window (matching the REST permission cache's cadence).
	sseChangeAuthTTL = 2 * time.Minute
	// sseChangeAuthSARTimeout caps a single authorization SAR issued from the
	// broadcast goroutine, so one hung apiserver call can't stall broadcasts.
	sseChangeAuthSARTimeout = 5 * time.Second
	// sseChangeAuthNegativeTTL caps how long a transient SAR failure (apiserver
	// unreachable, error, or timeout) is remembered as a fail-closed deny. Short
	// so a momentary blip clears within seconds, but non-zero so a degraded
	// apiserver doesn't re-pay the SAR timeout on every frame for the same tuple
	// in the single broadcast goroutine.
	sseChangeAuthNegativeTTL = 10 * time.Second
	// sseChangeAuthMemoCap bounds one connection's authorization memo. Past it,
	// expired entries are swept before the next insert so a long-lived
	// all-namespace stream can't accumulate them without bound. Soft: a
	// legitimately large live working set may exceed it.
	sseChangeAuthMemoCap = 8192
)

// newSSEChangeAuthorizer returns the per-kind authorizer for one SSE client's
// change frames, backed by a connection-lived memo.
//
// Without the memo, every qualifying change frame for a long-lived client would
// run a fresh, UNCACHED SubjectAccessReview serially inside the single broadcast
// goroutine (canReadUser only writes back to the shared permission cache when
// that entry exists, and the SSE path primes it only once at subscribe, so it
// TTLs out): stalling every client and multiplying apiserver SAR load by
// client × (kind, namespace). The memo survives that cache expiry; its own TTL
// preserves RBAC-change propagation; and the bounded SAR context stops a hung
// apiserver call from wedging the broadcast loop.
//
// The memo keys on the current context name so a kubeconfig context switch —
// which leaves SSE connections open (they receive a context_changed frame, not
// a disconnect) — can't authorize new-cluster frames with the previous
// cluster's decisions; post-switch keys miss and re-run against the new
// apiserver, mirroring the shared cache's own context stamping. nil user
// (auth off) is a passthrough.
func (s *Server) newSSEChangeAuthorizer(ctx context.Context, user *auth.User) func(group, resource, namespace, verb string) bool {
	if user == nil || s.permCache == nil {
		return func(_, _, _, _ string) bool { return true }
	}
	base := func(group, resource, namespace, verb string) (bool, bool) {
		sarCtx, cancel := context.WithTimeout(ctx, sseChangeAuthSARTimeout)
		defer cancel()
		return s.canReadUserSAR(sarCtx, user, group, resource, namespace, verb)
	}
	return memoizedAuthorizer(base, sseChangeAuthTTL, sseChangeAuthNegativeTTL, sseChangeAuthMemoCap, k8s.GetContextName, time.Now)
}

// authMemoEntry is one cached authorization decision in an SSE connection's memo.
type authMemoEntry struct {
	allowed bool
	expires time.Time
}

// sweepExpiredAuthMemo deletes every entry whose TTL has elapsed as of now,
// reclaiming space in a long-lived connection's authorization memo, and returns
// the number removed. The caller holds the memo's lock.
func sweepExpiredAuthMemo(memo map[string]authMemoEntry, now time.Time) int {
	removed := 0
	for k, e := range memo {
		if !now.Before(e.expires) {
			delete(memo, k)
			removed++
		}
	}
	return removed
}

// memoizedAuthorizer wraps an authorization predicate with a per-(context, verb,
// group, resource, namespace) TTL memo so repeated lookups don't re-issue the
// SAR. Keying on contextName scopes decisions to the cluster they were made
// against. base returns (allowed, authoritative):
//
//   - An authoritative allow/deny is cached for the full ttl.
//   - A non-authoritative result (transient SAR failure: no client, error, or
//     timeout) is a fail-closed deny cached only for the short negativeTTL — long
//     enough that a degraded apiserver doesn't re-pay the SAR timeout on every
//     frame for the same tuple in the single broadcast goroutine, short enough
//     that a momentary blip can't deny a readable tuple for the whole ttl.
//   - If the cluster context changes while base() is in flight, the verdict was
//     decided against a different apiserver than key names: the frame fails
//     closed and nothing is cached, so the next frame re-evaluates cleanly.
//
// maxEntries soft-bounds the memo: past it, expired entries are swept (time-gated
// so a large live working set doesn't trigger an O(n) sweep every frame) before
// the next insert. contextName and now are injectable for tests.
func memoizedAuthorizer(base func(group, resource, namespace, verb string) (bool, bool), ttl, negativeTTL time.Duration, maxEntries int, contextName func() string, now func() time.Time) func(group, resource, namespace, verb string) bool {
	var mu sync.Mutex
	memo := make(map[string]authMemoEntry)
	var lastSweep time.Time
	return func(group, resource, namespace, verb string) bool {
		ctxName := ""
		if contextName != nil {
			ctxName = contextName()
		}
		key := ctxName + "\x00" + verb + "\x00" + group + "\x00" + resource + "\x00" + namespace
		t := now()

		mu.Lock()
		if e, ok := memo[key]; ok && t.Before(e.expires) {
			mu.Unlock()
			return e.allowed
		}
		mu.Unlock()

		allowed, authoritative := base(group, resource, namespace, verb)

		// A context switch that landed while base() ran decided this verdict
		// against a different apiserver than key names. Fail closed for the frame
		// and don't cache; the next frame re-evaluates against the new cluster.
		if contextName != nil && contextName() != ctxName {
			return false
		}

		expiry := ttl
		if !authoritative {
			expiry = negativeTTL
		}

		mu.Lock()
		if maxEntries > 0 && len(memo) >= maxEntries && (lastSweep.IsZero() || t.Sub(lastSweep) >= negativeTTL) {
			sweepExpiredAuthMemo(memo, t)
			lastSweep = t
		}
		memo[key] = authMemoEntry{allowed: allowed, expires: t.Add(expiry)}
		mu.Unlock()
		return allowed
	}
}

// Settings handlers

// cloudMode reports whether Radar is running under Radar Cloud. Reads
// the resolved deployment mode from internal/cloud (which normalizes
// the RADAR_CLOUD_MODE env var via strconv.ParseBool, so common typos
// like "True" / "1" don't silently degrade to OSS mode). When true,
// user-scoped settings fields (theme, pinnedKinds) are owned by
// Cloud's user_preferences table — not settings.json — because a
// single in-cluster Radar is shared across every Cloud user of the
// cluster and can't meaningfully store per-user state.
func cloudMode() bool {
	return cloud.Mode()
}

// deploymentMode resolves the deployment topology that the frontend
// branches on. Cloud beats in-cluster (Cloud is in-cluster + tunnel,
// but the user-visible behavior is the cloud-tunnel half), and
// in-cluster comes from kubeconfig bootstrap setting context name to
// the literal "in-cluster" sentinel.
func deploymentMode() k8s.DeploymentMode {
	if cloudMode() {
		return k8s.DeploymentModeCloud
	}
	if k8s.IsInCluster() || k8s.GetKubeconfigSummary().Mode == "in-cluster" {
		return k8s.DeploymentModeInCluster
	}
	return k8s.DeploymentModeLocal
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	loaded := settings.Load()
	// Desktop's own state: on a shared instance this would hand every viewer
	// the cluster name from whenever this $HOME last ran the Desktop app.
	loaded.LastDesktopContext = nil
	if cloudMode() {
		// Strip user-scoped fields — Cloud's intercept layer fills them from
		// user_preferences. Audit stays because it's cluster-shared policy.
		loaded.Theme = ""
		loaded.PinnedKinds = nil
	}
	s.writeJSON(w, loaded)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var patch settings.Settings
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Under cloud mode, reject writes to user-scoped fields. Cloud's
	// intercept layer splits the PUT before forwarding — this is a
	// defense-in-depth check so a raw call that bypasses the intercept
	// doesn't silently succeed and cause a cluster-shared settings.json
	// to get mutated by one user.
	if cloudMode() && (patch.Theme != "" || patch.PinnedKinds != nil) {
		s.writeError(w, http.StatusBadRequest, "theme and pinnedKinds are managed by Radar Cloud; use /api/preferences instead")
		return
	}
	result, err := settings.Update(func(current *settings.Settings) {
		if patch.Theme != "" {
			current.Theme = patch.Theme
		}
		if patch.PinnedKinds != nil {
			current.PinnedKinds = patch.PinnedKinds
		}
	})
	if err != nil {
		log.Printf("[settings] Failed to save settings: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The response echoes the merged struct — same reason as handleGetSettings.
	result.LastDesktopContext = nil
	if cloudMode() {
		result.Theme = ""
		result.PinnedKinds = nil
	}
	s.writeJSON(w, result)
}

// Config handlers (persistent startup configuration)

// configResponse bundles the on-disk config file with the effective startup
// config so the UI can show "currently running" hints for values that differ.
type configResponse struct {
	File      config.Config `json:"file"`
	Effective config.Config `json:"effective"`
	IsDesktop bool          `json:"isDesktop"`
	// OpenCostManaged tells Settings that an explicit startup flag owns the
	// running value even when the persisted file changes.
	OpenCostManaged bool `json:"openCostCurrencyManaged,omitempty"`
	// PrometheusHeaderKeys lists the configured Prometheus header names so the UI
	// can show what's set without ever receiving the (secret) values.
	PrometheusHeaderKeys []string `json:"prometheusHeaderKeys,omitempty"`
	// ArgoCDTokenSet tells the UI a token is configured without exposing it.
	ArgoCDTokenSet bool `json:"argoCdTokenSet,omitempty"`
	// ArgoCDEnvManaged marks the integration as provisioned from the environment
	// (RADAR_ARGOCD_TOKEN / _TOKEN_FILE) — the UI renders it read-only, since the
	// PUT handler refuses changes to a declaratively-configured integration.
	ArgoCDEnvManaged bool `json:"argoCdEnvManaged,omitempty"`
	// ArgoCDEnvError is set when environment provisioning was attempted but failed
	// (bad token file, invalid URL, …) — the read-only card shows the reason so a
	// misconfigured declarative credential isn't invisible behind one startup log.
	ArgoCDEnvError string `json:"argoCdEnvError,omitempty"`
	// ArgoCDCLISession is the detected Argo CD CLI login (server + user, no
	// token), so the UI can offer "use your CLI session" only when it will work.
	ArgoCDCLISession *argoapi.CLISession `json:"argoCdCliSession,omitempty"`
}

// handleGetConfig returns the on-disk config file alongside the effective startup config.
// PrometheusHeaders are redacted — they may contain Bearer tokens / tenant IDs and the
// diagnostics endpoint already masks them as a presence bool.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	file := config.Load()
	normalizedCurrency, err := config.NormalizeOpenCostCurrency(file.OpenCostCurrency)
	if err != nil {
		normalizedCurrency = ""
	}
	file.OpenCostCurrency = normalizedCurrency
	headerKeys := make([]string, 0, len(file.PrometheusHeaders))
	for k := range file.PrometheusHeaders {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)
	file.PrometheusHeaders = nil
	tokenSet := file.ArgoCDToken != ""
	file.ArgoCDToken = ""
	// When the integration is environment-managed, the on-disk URL/TLS are ignored;
	// surface the effective env values (and the token-set signal) so the read-only
	// Settings card shows the real endpoint rather than stale disk config. When env
	// provisioning was attempted but failed, surface the reason instead — there is
	// no token, so the card shows an error state rather than a phantom "configured".
	envManaged := false
	envError := ""
	if envURL, envInsecure, ok := argocd.EnvManagedConfig(); ok {
		envManaged = true
		// Env-managed ignores the on-disk config entirely — present the effective env
		// values (all empty in the errored state, so neither a stale disk URL nor a
		// stale disk token-set signal leaks). Only a successfully-seeded env token
		// counts as set.
		file.ArgoCDURL = envURL
		file.ArgoCDInsecureTLS = envInsecure
		tokenSet = false
		if envError = argocd.EnvManagedError(); envError == "" {
			tokenSet = true
		}
	}
	resp := configResponse{
		File:                 file,
		IsDesktop:            version.IsDesktop(),
		OpenCostManaged:      s.currencyManaged,
		PrometheusHeaderKeys: headerKeys,
		ArgoCDTokenSet:       tokenSet,
		ArgoCDEnvManaged:     envManaged,
		ArgoCDEnvError:       envError,
	}
	// Best-effort: surface a detected Argo CD CLI login so the UI can offer it.
	// A malformed CLI config just means "no session offered", never a failure.
	if sess, err := argocd.CLISession(); err == nil {
		resp.ArgoCDCLISession = sess
	}
	if s.effectiveConfig != nil {
		effective := *s.effectiveConfig
		effective.PrometheusHeaders = nil
		effective.ArgoCDToken = ""
		resp.Effective = effective
	}
	s.writeJSON(w, resp)
}

// handlePutConfig replaces the entire config file. Most changes take effect on next restart;
// the OpenCost currency override is also applied unless an explicit startup flag owns it.
// Unlike handlePutSettings (which merges fields), this is a full replacement.
// PrometheusHeaders and the Argo CD token are preserved from the on-disk file: the GET
// response redacts them, so a UI round-trip would otherwise silently wipe the user's
// credentials.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	var updated config.Config
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	normalizedCurrency, err := config.NormalizeOpenCostCurrency(updated.OpenCostCurrency)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid OpenCost currency: "+err.Error())
		return
	}
	updated.OpenCostCurrency = normalizedCurrency
	result, err := config.Update(func(c *config.Config) {
		// Integration connection fields are owned exclusively by the live
		// /api/integrations/* endpoints, not this startup-config PUT. Preserve
		// ALL of them so a full-config save (which is a full replacement) can
		// never disturb a live integration — even if it races an in-flight
		// Apply/Connect or echoes back the redacted token as empty.
		preserved := struct {
			promHeaders      map[string]string
			promHeadersEnv   map[string]string
			promURL          string
			argoURL          string
			argoToken        string
			argoInsecure     bool
			argoTokenContext string
			argoTokenBinding string
		}{
			promHeaders:      c.PrometheusHeaders,
			promHeadersEnv:   c.PrometheusHeadersFromEnv,
			promURL:          c.PrometheusURL,
			argoURL:          c.ArgoCDURL,
			argoToken:        c.ArgoCDToken,
			argoInsecure:     c.ArgoCDInsecureTLS,
			argoTokenContext: c.ArgoCDTokenContext,
			argoTokenBinding: c.ArgoCDTokenBinding,
		}
		*c = updated
		c.PrometheusHeaders = preserved.promHeaders
		c.PrometheusHeadersFromEnv = preserved.promHeadersEnv
		c.PrometheusURL = preserved.promURL
		c.ArgoCDURL = preserved.argoURL
		c.ArgoCDToken = preserved.argoToken
		c.ArgoCDInsecureTLS = preserved.argoInsecure
		c.ArgoCDTokenContext = preserved.argoTokenContext
		c.ArgoCDTokenBinding = preserved.argoTokenBinding
	})
	if err != nil {
		log.Printf("[config] Failed to save config: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.openCostCurrency != nil && !s.currencyManaged {
		s.openCostCurrency.SetOverride(result.OpenCostCurrency)
	}
	result.PrometheusHeaders = nil
	result.ArgoCDToken = ""
	s.writeJSON(w, result)
}

// handleApplyPrometheusURL re-points the running Prometheus client at a new URL
// immediately and persists it. The Prometheus URL is one of the few settings
// that doesn't need a restart: the metrics path reads it from a mutable global
// per query, so re-pointing it live is safe and saves operators a restart loop
// when tuning discovery. Reset() drops the cached connection so the next probe
// rediscovers against the new URL rather than reusing the old endpoint. The
// response carries the live reachability result so the UI can confirm the URL
// actually works. An empty URL reverts to auto-discovery.
func (s *Server) handleApplyPrometheusURL(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	// No requireConnected: persisting + applying a manual URL needs no cluster
	// (the probe hits the URL over HTTP), so operators can point at an external
	// Prometheus even while the cluster is unreachable, like handlePutConfig.
	var body struct {
		PrometheusURL string `json:"prometheusUrl"`
		// Headers is a pointer so we can tell "not editing headers" (nil — keep
		// what's on disk) apart from "clear all headers" (present but empty). The
		// UI only sends it when the user touched the header editor, since GET
		// redacts the values and can't round-trip them.
		Headers *map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rawURL := strings.TrimSpace(body.PrometheusURL)

	// Reject anything startup would log.Fatalf on, so "Apply now" can't persist a
	// config that bricks the next launch. Empty reverts to auto-discovery.
	if rawURL != "" {
		if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			s.writeError(w, http.StatusBadRequest, "Prometheus URL must be a valid HTTP(S) URL (e.g., http://prometheus-server.monitoring:9090)")
			return
		}
	}

	var headers map[string]string
	if body.Headers != nil {
		headers = make(map[string]string, len(*body.Headers))
		for k, v := range *body.Headers {
			if k = strings.TrimSpace(k); k != "" {
				headers[k] = v
			}
		}
	}

	// Persist first: a failed disk write must not leave the running client
	// pointed somewhere the on-disk config disagrees with.
	if _, err := config.Update(func(c *config.Config) {
		c.PrometheusURL = rawURL
		if body.Headers != nil {
			c.PrometheusHeaders = headers
		}
	}); err != nil {
		log.Printf("[config] Failed to persist Prometheus URL: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Apply to the running client. Reset() drops the cached connection so the
	// probe below rediscovers against the new URL instead of the old endpoint.
	prometheuspkg.SetManualURL(rawURL)
	traffic.SetMetricsURL(rawURL)
	if body.Headers != nil {
		prometheuspkg.SetHeaders(headers)
		traffic.SetMetricsHeaders(headers)
	}
	prometheuspkg.Reset()
	if s.openCostCurrency != nil {
		s.openCostCurrency.Invalidate()
	}

	resp := struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address,omitempty"`
		Error     string `json:"error,omitempty"`
	}{}
	if client := prometheuspkg.GetClient(); client != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		addr, _, err := client.EnsureConnected(ctx)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Connected = true
			resp.Address = addr
		}
	}
	s.writeJSON(w, resp)
}

// Debug handlers for event pipeline diagnostics

// handleDebugEvents returns event pipeline metrics and recent drops. The
// aggregate counters/stats carry no resource identity, but RecentDrops name
// individual resources (kind/namespace/name) — filter those to what the caller
// may read so this diagnostic endpoint isn't a side channel around the timeline
// RBAC gate.
func (s *Server) handleDebugEvents(w http.ResponseWriter, r *http.Request) {
	response := timeline.GetDebugEventsResponse()
	// Compose both protections: scope to the active cluster (a straggler drop from
	// a previous cluster, recorded in the async informer-shutdown window, must not
	// surface here) AND per-user RBAC (drop records name resources).
	response.RecentDrops = s.filterDropsByRBAC(r, timeline.DropsForCluster(response.RecentDrops, k8s.ActiveClusterContext()))
	s.writeJSON(w, response)
}

// filterDropsByRBAC drops records for resources the caller can't read, using the
// same per-kind SAR as the timeline gate. Auth off → returned unchanged. canRead
// memoizes, and the drop ring is small, so a serial loop is cheap.
func (s *Server) filterDropsByRBAC(r *http.Request, drops []timeline.DropRecord) []timeline.DropRecord {
	if auth.UserFromContext(r.Context()) == nil {
		return drops
	}
	out := drops[:0]
	for _, d := range drops {
		group, resource, clusterScoped, ok := k8s.ResolveChangeGVR(d.Kind, "")
		if !ok {
			continue // unresolved kind → fail closed
		}
		ns := d.Namespace
		if clusterScoped {
			ns = ""
		}
		if s.canRead(r, group, resource, ns, "list") {
			out = append(out, d)
		}
	}
	return out
}

// handleDebugEventsDiagnose diagnoses why events for a specific resource might be missing
func (s *Server) handleDebugEventsDiagnose(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	namespace := r.URL.Query().Get("namespace")
	name := r.URL.Query().Get("name")

	if kind == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name query parameters are required")
		return
	}

	// RBAC + cluster scoping both run inside GetDiagnosis before recommendations:
	// ActiveClusterContext scopes the store query and the stamped drop history to
	// the current cluster, and `allow` authorizes each returned row per-kind using
	// its own apiVersion (disambiguating a Kind that collides with a builtin — a
	// namespaced CRD Kind=Node must not ride the caller's `list nodes`). Auth off →
	// nil filter → no-op.
	var allow func(kind, apiVersion, namespace string) bool
	if user := auth.UserFromContext(r.Context()); user != nil && s.permCache != nil {
		authz := s.changeAuthorizerForCtx(r.Context())
		allow = func(kind, apiVersion, namespace string) bool {
			return k8s.ChangeReadAllowed(kind, apiVersion, namespace, authz)
		}
	}
	response := timeline.GetDiagnosis(kind, namespace, name, k8s.ActiveClusterContext(), allow)
	s.writeJSON(w, response)
}

// handleDebugInformers returns the list of dynamic informers currently running
func (s *Server) handleDebugInformers(w http.ResponseWriter, r *http.Request) {
	dynCache := k8s.GetDynamicResourceCache()
	if dynCache == nil {
		s.writeJSON(w, map[string]any{
			"typedInformers":   16,
			"dynamicInformers": 0,
			"watchedResources": []string{},
		})
		return
	}

	gvrs := dynCache.GetWatchedResources()
	resources := make([]string, len(gvrs))
	for i, gvr := range gvrs {
		if gvr.Group != "" {
			resources[i] = gvr.Resource + "." + gvr.Group
		} else {
			resources[i] = gvr.Resource
		}
	}

	s.writeJSON(w, map[string]any{
		"typedInformers":   16,
		"dynamicInformers": len(gvrs),
		"watchedResources": resources,
	})
}
