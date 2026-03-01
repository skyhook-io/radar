package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/topology"
	"github.com/skyhook-io/radar/internal/updater"
	"github.com/skyhook-io/radar/internal/version"
)

// Server is the Explorer HTTP server
type Server struct {
	router      *chi.Mux
	broadcaster *SSEBroadcaster
	port        int
	devMode     bool
	staticFS    fs.FS
	startTime   time.Time
	listener    net.Listener
	updater     *updater.Updater
	mcpHandler  http.Handler
	diagConfig  *DiagConfig
}

// Config holds server configuration
type Config struct {
	Port       int
	DevMode    bool         // Serve frontend from filesystem instead of embedded
	StaticFS   embed.FS     // Embedded frontend files
	StaticRoot string       // Path within StaticFS
	MCPHandler http.Handler // MCP server handler (nil = MCP disabled)
	DiagConfig *DiagConfig  // Sanitized config for diagnostics endpoint
}

// New creates a new server instance
func New(cfg Config) *Server {
	s := &Server{
		router:      chi.NewRouter(),
		broadcaster: NewSSEBroadcaster(),
		port:        cfg.Port,
		devMode:     cfg.DevMode,
		startTime:   time.Now(),
		mcpHandler:  cfg.MCPHandler,
		diagConfig:  cfg.DiagConfig,
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
	r := s.router

	// Middleware (applied to all routes)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Note: Timeout middleware is applied per-group below to exempt streaming endpoints

	// CORS for development
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: true,
	}))

	// pprof routes for profiling (dev mode only, but always available for debugging)
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

	// API routes
	r.Route("/api", func(r chi.Router) {
		// Streaming endpoints (SSE/WebSocket) - no timeout
		r.Get("/events/stream", s.broadcaster.HandleSSE)
		r.Get("/pods/{namespace}/{name}/logs/stream", s.handlePodLogsStream)
		r.Get("/pods/{namespace}/{name}/exec", s.handlePodExec)
		r.Get("/pods/{namespace}/{name}/files/download", s.handlePodFileDownload)
		r.Get("/workloads/{kind}/{namespace}/{name}/logs/stream", s.handleWorkloadLogsStream)

		// All other API routes get a 60-second timeout
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(60 * time.Second))

			r.Get("/health", s.handleHealth)
			r.Get("/diagnostics", s.handleDiagnostics)
			r.Get("/version-check", s.handleVersionCheck)
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/dashboard/crds", s.handleDashboardCRDs)
			r.Get("/dashboard/helm", s.handleDashboardHelm)
			r.Get("/cluster-info", s.handleClusterInfo)
			r.Get("/capabilities", s.handleCapabilities)
			r.Get("/topology", s.handleTopology)
			r.Get("/namespaces", s.handleNamespaces)
			r.Get("/api-resources", s.handleAPIResources)
			r.Get("/resources/{kind}", s.handleListResources)
			r.Get("/resources/{kind}/{namespace}/{name}", s.handleGetResource)
			r.Put("/resources/{kind}/{namespace}/{name}", s.handleUpdateResource)
			r.Delete("/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			r.Get("/secrets/certificate-expiry", s.handleSecretCertExpiry)
			r.Get("/events", s.handleEvents)
			r.Get("/changes", s.handleChanges)
			r.Get("/changes/{kind}/{namespace}/{name}/children", s.handleChangeChildren)

			// Pod logs (non-streaming)
			r.Get("/pods/{namespace}/{name}/logs", s.handlePodLogs)

			// Pod debug (ephemeral container)
			r.Post("/pods/{namespace}/{name}/debug", s.handleCreateDebugContainer)

			// Pod file browser
			r.Get("/pods/{namespace}/{name}/files", s.handlePodFileList)

			// Metrics (from metrics.k8s.io API)
			r.Get("/metrics/pods/{namespace}/{name}", s.handlePodMetrics)
			r.Get("/metrics/nodes/{name}", s.handleNodeMetrics)
			r.Get("/metrics/pods/{namespace}/{name}/history", s.handlePodMetricsHistory)
			r.Get("/metrics/nodes/{name}/history", s.handleNodeMetricsHistory)
			r.Get("/metrics/top/pods", s.handleTopPods)
			r.Get("/metrics/top/nodes", s.handleTopNodes)

			// Port forwarding
			r.Get("/portforwards", s.handleListPortForwards)
			r.Post("/portforwards", s.handleStartPortForward)
			r.Delete("/portforwards/{id}", s.handleStopPortForward)
			r.Get("/portforwards/available/{type}/{namespace}/{name}", s.handleGetAvailablePorts)

			// Active sessions (for context switch confirmation)
			r.Get("/sessions", s.handleGetSessions)

			// CronJob operations
			r.Post("/cronjobs/{namespace}/{name}/trigger", s.handleTriggerCronJob)
			r.Post("/cronjobs/{namespace}/{name}/suspend", s.handleSuspendCronJob)
			r.Post("/cronjobs/{namespace}/{name}/resume", s.handleResumeCronJob)

			// Workload restart, scale, rollback
			r.Post("/workloads/{kind}/{namespace}/{name}/restart", s.handleRestartWorkload)
			r.Post("/workloads/{kind}/{namespace}/{name}/scale", s.handleScaleWorkload)
			r.Get("/workloads/{kind}/{namespace}/{name}/revisions", s.handleWorkloadRevisions)
			r.Post("/workloads/{kind}/{namespace}/{name}/rollback", s.handleRollbackWorkload)

			// Workload logs (non-streaming)
			r.Get("/workloads/{kind}/{namespace}/{name}/logs", s.handleWorkloadLogs)
			r.Get("/workloads/{kind}/{namespace}/{name}/pods", s.handleWorkloadPods)

			// Helm routes
			helmHandlers := helm.NewHandlers()
			helmHandlers.RegisterRoutes(r)

			// Image inspection routes
			imageHandlers := images.NewHandlers()
			imageHandlers.RegisterRoutes(r)

			// Prometheus metrics routes
			prometheuspkg.RegisterRoutes(r)

			// OpenCost routes
			opencost.RegisterRoutes(r)

			// FluxCD routes
			r.Post("/flux/{kind}/{namespace}/{name}/reconcile", s.handleFluxReconcile)
			r.Post("/flux/{kind}/{namespace}/{name}/sync-with-source", s.handleFluxSyncWithSource)
			r.Post("/flux/{kind}/{namespace}/{name}/suspend", s.handleFluxSuspend)
			r.Post("/flux/{kind}/{namespace}/{name}/resume", s.handleFluxResume)

			// ArgoCD routes
			r.Post("/argo/applications/{namespace}/{name}/sync", s.handleArgoSync)
			r.Post("/argo/applications/{namespace}/{name}/refresh", s.handleArgoRefresh)
			r.Post("/argo/applications/{namespace}/{name}/terminate", s.handleArgoTerminate)
			r.Post("/argo/applications/{namespace}/{name}/suspend", s.handleArgoSuspend)
			r.Post("/argo/applications/{namespace}/{name}/resume", s.handleArgoResume)

			// AI resource preview (minified output for MCP/debugging)
			r.Get("/ai/resources/{kind}", s.handleAIListResources)
			r.Get("/ai/resources/{kind}/{namespace}/{name}", s.handleAIGetResource)

			// Debug routes (for event pipeline diagnostics)
			r.Get("/debug/events", s.handleDebugEvents)
			r.Get("/debug/events/diagnose", s.handleDebugEventsDiagnose)
			r.Get("/debug/informers", s.handleDebugInformers)

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

			// Connection status routes (for graceful startup)
			r.Get("/connection", s.handleConnectionStatus)
			r.Post("/connection/retry", s.handleConnectionRetry)

			// GitHub star status and action
			r.Get("/github/starred", s.handleGitHubStarStatus)
			r.Post("/github/star", s.handleGitHubStar)
			r.Post("/github/dismiss", s.handleGitHubDismiss)

			// Settings (persisted user preferences)
			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)

			// Desktop routes
			r.Post("/desktop/open-url", s.handleDesktopOpenURL)
			r.Post("/desktop/update", s.handleDesktopUpdateStart)
			r.Get("/desktop/update/status", s.handleDesktopUpdateStatus)
			r.Post("/desktop/update/apply", s.handleDesktopUpdateApply)
		})

		// Traffic streaming (no timeout)
		r.Get("/traffic/flows/stream", s.handleTrafficFlowsStream)
	})

	// MCP server (Model Context Protocol for AI tools)
	if s.mcpHandler != nil {
		r.Mount("/mcp", s.mcpHandler)
	}

	// Static files (frontend) - SPA fallback to index.html
	if s.staticFS != nil {
		r.Handle("/*", spaHandler(http.FS(s.staticFS)))
	} else if s.devMode {
		// In dev mode, serve from web/dist
		r.Handle("/*", spaHandler(http.Dir("web/dist")))
	}
}

// spaHandler serves static files, falling back to index.html for SPA routing
func spaHandler(fsys http.FileSystem) http.Handler {
	fileServer := http.FileServer(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try to open the file
		f, err := fsys.Open(path)
		if err != nil {
			// File doesn't exist - serve index.html for SPA routing
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		defer f.Close()

		// Check if it's a directory (and not the root)
		stat, err := f.Stat()
		if err != nil || (stat.IsDir() && path != "/") {
			// For directories without index.html, serve root index.html
			r.URL.Path = "/"
		}

		fileServer.ServeHTTP(w, r)
	})
}

// Start starts the server. If port is 0, an OS-assigned port is used.
func (s *Server) Start() error {
	return s.StartWithReady(nil)
}

// StartWithReady starts the server and signals on the ready channel once it
// is accepting connections. If port is 0, an OS-assigned port is used.
func (s *Server) StartWithReady(ready chan<- struct{}) error {
	s.broadcaster.Start()

	addr := fmt.Sprintf(":%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	s.listener = ln

	log.Printf("Starting Explorer server on http://localhost:%d", s.ActualPort())

	if ready != nil {
		close(ready)
	}

	return http.Serve(ln, s.router)
}

// ActualPort returns the port the server is listening on.
// Useful when configured with port 0 (OS-assigned).
func (s *Server) ActualPort() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.port
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

// Handler returns the server's HTTP handler for use with httptest.
func (s *Server) Handler() http.Handler {
	return s.router
}

// Stop gracefully stops the server and releases the listening port.
func (s *Server) Stop() {
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

	// Get timeline store stats (informational only - doesn't affect overall status)
	var timelineStats map[string]any
	if store := timeline.GetStore(); store != nil {
		stats := store.Stats()
		timelineStats = map[string]any{
			"total_events": stats.TotalEvents,
			"store_errors": timeline.GetStoreErrorCount(),
			"total_drops":  timeline.GetTotalDropCount(),
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
	info := version.CheckForUpdate(r.Context())
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
	caps, err := k8s.CheckCapabilities(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caps.MCPEnabled = s.mcpHandler != nil

	// Include resource permissions if cache is available
	if cache := k8s.GetResourceCache(); cache != nil {
		enabled := cache.GetEnabledResources()
		caps.Resources = &k8s.ResourcePermissions{
			Pods:                     enabled["pods"],
			Services:                 enabled["services"],
			Deployments:              enabled["deployments"],
			DaemonSets:               enabled["daemonsets"],
			StatefulSets:             enabled["statefulsets"],
			ReplicaSets:              enabled["replicasets"],
			Ingresses:                enabled["ingresses"],
			ConfigMaps:               enabled["configmaps"],
			Secrets:                  enabled["secrets"],
			Events:                   enabled["events"],
			PersistentVolumeClaims:   enabled["persistentvolumeclaims"],
			Nodes:                    enabled["nodes"],
			Namespaces:               enabled["namespaces"],
			Jobs:                     enabled["jobs"],
			CronJobs:                 enabled["cronjobs"],
			HorizontalPodAutoscalers: enabled["horizontalpodautoscalers"],
		}
	}

	s.writeJSON(w, caps)
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
		return result
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
	namespaces := parseNamespaces(r.URL.Query())
	viewMode := r.URL.Query().Get("view")

	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	if viewMode == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}

	builder := topology.NewBuilder()
	topo, err := builder.Build(opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, topo)
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

	lister := cache.Namespaces()
	if lister == nil {
		s.writeError(w, http.StatusForbidden, "insufficient permissions to list namespaces")
		return
	}

	namespaces, err := lister.List(labels.Everything())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]map[string]any, 0, len(namespaces))
	for _, ns := range namespaces {
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

	s.writeJSON(w, resources)
}

func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespaces := parseNamespaces(r.URL.Query())
	group := r.URL.Query().Get("group") // API group for CRD disambiguation

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var result any
	var err error

	// listPerNs is a helper that merges results across multiple namespaces.
	// listAll returns all items; listNs returns items for a single namespace.
	listPerNs := func(listAll func() (any, error), listNs func(string) (any, error)) (any, error) {
		if len(namespaces) == 0 {
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
			forbiddenMsg("configmaps")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.ConfigMaps().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.ConfigMaps().ConfigMaps(ns).List(labels.Everything()) },
		)
	case "secrets":
		lister := cache.Secrets()
		if lister == nil {
			forbiddenMsg("secrets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return lister.List(labels.Everything()) },
			func(ns string) (any, error) { return lister.Secrets(ns).List(labels.Everything()) },
		)
	case "events":
		if cache.Events() == nil {
			forbiddenMsg("events")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.Events().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Events().Events(ns).List(labels.Everything()) },
		)
	case "persistentvolumeclaims", "pvcs":
		if cache.PersistentVolumeClaims() == nil {
			forbiddenMsg("persistentvolumeclaims")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.PersistentVolumeClaims().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.PersistentVolumeClaims().PersistentVolumeClaims(ns).List(labels.Everything())
			},
		)
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
		result, err = cache.Namespaces().List(labels.Everything())
	case "persistentvolumes", "pvs":
		if cache.PersistentVolumes() == nil {
			forbiddenMsg("persistentvolumes")
			return
		}
		result, err = cache.PersistentVolumes().List(labels.Everything())
	case "storageclasses", "sc":
		if cache.StorageClasses() == nil {
			forbiddenMsg("storageclasses")
			return
		}
		result, err = cache.StorageClasses().List(labels.Everything())
	case "poddisruptionbudgets", "pdbs":
		if cache.PodDisruptionBudgets() == nil {
			forbiddenMsg("poddisruptionbudgets")
			return
		}
		result, err = listPerNs(
			func() (any, error) { return cache.PodDisruptionBudgets().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.PodDisruptionBudgets().PodDisruptionBudgets(ns).List(labels.Everything())
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
			forbiddenGet("configmaps")
			return
		}
		resource, err = cache.ConfigMaps().ConfigMaps(namespace).Get(name)
	case "secrets", "secret":
		lister := cache.Secrets()
		if lister == nil {
			forbiddenGet("secrets")
			return
		}
		resource, err = lister.Secrets(namespace).Get(name)
	case "events", "event":
		if cache.Events() == nil {
			forbiddenGet("events")
			return
		}
		resource, err = cache.Events().Events(namespace).Get(name)
	case "persistentvolumeclaims", "persistentvolumeclaim", "pvcs", "pvc":
		if cache.PersistentVolumeClaims() == nil {
			forbiddenGet("persistentvolumeclaims")
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
			forbiddenGet("persistentvolumes")
			return
		}
		resource, err = cache.PersistentVolumes().Get(name)
	case "storageclasses", "storageclass", "sc":
		if cache.StorageClasses() == nil {
			forbiddenGet("storageclasses")
			return
		}
		resource, err = cache.StorageClasses().Get(name)
	case "poddisruptionbudgets", "poddisruptionbudget", "pdbs", "pdb":
		if cache.PodDisruptionBudgets() == nil {
			forbiddenGet("poddisruptionbudgets")
			return
		}
		resource, err = cache.PodDisruptionBudgets().PodDisruptionBudgets(namespace).Get(name)
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

	// Get relationships from cached topology
	var relationships *topology.Relationships
	if cachedTopo := s.broadcaster.GetCachedTopology(); cachedTopo != nil {
		relationships = topology.GetRelationships(kind, namespace, name, cachedTopo)
	}

	// Return resource with relationships
	response := topology.ResourceWithRelationships{
		Resource:      resource,
		Relationships: relationships,
	}

	// Enrich TLS secrets with parsed certificate info
	if secret, ok := resource.(*corev1.Secret); ok && secret.Type == corev1.SecretTypeTLS {
		if certPEM, exists := secret.Data["tls.crt"]; exists && len(certPEM) > 0 {
			certs := parsePEMCertificates(certPEM)
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

	metrics, err := k8s.GetPodMetrics(r.Context(), namespace, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
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

	metrics, err := k8s.GetNodeMetrics(r.Context(), name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, "Node metrics not found (metrics-server may not be installed)")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, metrics)
}

// handlePodMetricsHistory returns historical metrics for a specific pod
func (s *Server) handlePodMetricsHistory(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	store := k8s.GetMetricsHistory()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Metrics history not available")
		return
	}

	history := store.GetPodMetricsHistory(namespace, name)
	if history == nil {
		// Return empty history — include collection error if metrics are failing
		history = &k8s.PodMetricsHistory{
			Namespace:  namespace,
			Name:       name,
			Containers: []k8s.ContainerMetricsHistory{},
		}
		health := store.CollectionHealth()
		if health.PodMetrics.ConsecutiveErrors > 0 {
			history.CollectionError = health.PodMetrics.LastError
		}
	}

	s.writeJSON(w, history)
}

// handleNodeMetricsHistory returns historical metrics for a specific node
func (s *Server) handleNodeMetricsHistory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	store := k8s.GetMetricsHistory()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Metrics history not available")
		return
	}

	history := store.GetNodeMetricsHistory(name)
	if history == nil {
		history = &k8s.NodeMetricsHistory{
			Name:       name,
			DataPoints: []k8s.MetricsDataPoint{},
		}
		health := store.CollectionHealth()
		if health.NodeMetrics.ConsecutiveErrors > 0 {
			history.CollectionError = health.NodeMetrics.LastError
		}
	}

	s.writeJSON(w, history)
}

// handleTopPods returns the latest metrics for all pods (bulk endpoint for table view)
func (s *Server) handleTopPods(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	// Build metrics lookup (may be empty if metrics-server is unavailable)
	metricsMap := make(map[string]*k8s.TopPodMetrics)
	if store := k8s.GetMetricsHistory(); store != nil {
		raw := store.GetAllPodMetricsLatest()
		for i := range raw {
			metricsMap[raw[i].Namespace+"/"+raw[i].Name] = &raw[i]
		}
	}

	// Get pod lister from cache to enrich with requests/limits
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		// No cache — return metrics-only data
		result := make([]k8s.TopPodMetrics, 0, len(metricsMap))
		for _, m := range metricsMap {
			result = append(result, *m)
		}
		s.writeJSON(w, result)
		return
	}

	pods, err := cache.Pods().List(labels.Everything())
	if err != nil {
		log.Printf("[metrics] Failed to list pods for top pods: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to list pods")
		return
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

		// Sum requests and limits across all containers
		for _, c := range pod.Spec.Containers {
			if req, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				entry.CPURequest += req.MilliValue() * 1000000 // millicores to nanocores
			}
			if lim, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
				entry.CPULimit += lim.MilliValue() * 1000000
			}
			if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				entry.MemoryRequest += req.Value()
			}
			if lim, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				entry.MemoryLimit += lim.Value()
			}
		}

		result = append(result, entry)
	}

	s.writeJSON(w, result)
}

// handleTopNodes returns the latest metrics for all nodes (bulk endpoint for table view)
func (s *Server) handleTopNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
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

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := parseNamespaces(r.URL.Query())

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

	if len(namespaces) == 1 {
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

// handleChanges returns timeline events using the unified timeline.TimelineEvent format.
// This is the main timeline API endpoint - it queries the timeline store directly.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := parseNamespaces(r.URL.Query())
	kind := r.URL.Query().Get("kind")
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")
	filterPreset := r.URL.Query().Get("filter")
	includeK8sEvents := r.URL.Query().Get("include_k8s_events") != "false" // default true
	includeManaged := r.URL.Query().Get("include_managed") == "true"       // default false

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
		IncludeK8sEvents: includeK8sEvents,
		FilterPreset:     filterPreset,
	}
	if kind != "" {
		opts.Kinds = []string{kind}
	}

	events, err := store.Query(r.Context(), opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, events)
}

// handleChangeChildren returns child resource changes for a given parent workload
func (s *Server) handleChangeChildren(w http.ResponseWriter, r *http.Request) {
	ownerKind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	ownerName := chi.URLParam(r, "name")
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

	children, err := store.GetChangesForOwner(r.Context(), ownerKind, namespace, ownerName, since, 100)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, children)
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

	// Update the resource
	result, err := k8s.UpdateResource(r.Context(), k8s.UpdateResourceOptions{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		YAML:      string(body),
	})
	if err != nil {
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

	err := k8s.DeleteResource(r.Context(), k8s.DeleteResourceOptions{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Force:     force,
	})
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

// handleTriggerCronJob creates a Job from a CronJob
func (s *Server) handleTriggerCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	result, err := k8s.TriggerCronJob(r.Context(), namespace, name)
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

	err := k8s.SetCronJobSuspend(r.Context(), namespace, name, true)
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

	err := k8s.SetCronJobSuspend(r.Context(), namespace, name, false)
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
	if !validKinds[strings.ToLower(kind)] {
		s.writeError(w, http.StatusBadRequest, "only Deployments, StatefulSets, DaemonSets, and Rollouts can be restarted")
		return
	}

	err := k8s.RestartWorkload(r.Context(), kind, namespace, name)
	if err != nil {
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

	err := k8s.ScaleWorkload(r.Context(), kind, namespace, name, req.Replicas)
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

// handleWorkloadRevisions returns the revision history for a Deployment, StatefulSet, or DaemonSet
func (s *Server) handleWorkloadRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Validate that this is a rollbackable workload type
	validKinds := map[string]bool{
		"deployments":  true,
		"statefulsets": true,
		"daemonsets":   true,
	}
	if !validKinds[strings.ToLower(kind)] {
		s.writeError(w, http.StatusBadRequest, "revision history only available for Deployments, StatefulSets, and DaemonSets")
		return
	}

	revisions, err := k8s.ListWorkloadRevisions(r.Context(), kind, namespace, name)
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
	validKinds := map[string]bool{
		"deployments":  true,
		"statefulsets": true,
		"daemonsets":   true,
	}
	if !validKinds[strings.ToLower(kind)] {
		s.writeError(w, http.StatusBadRequest, "rollback only available for Deployments, StatefulSets, and DaemonSets")
		return
	}

	err := k8s.RollbackWorkload(r.Context(), kind, namespace, name, req.Revision)
	if err != nil {
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
	PortForwards int `json:"portForwards"`
	ExecSessions int `json:"execSessions"`
	Total        int `json:"total"`
}

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	pf := GetPortForwardCount()
	exec := GetExecSessionCount()
	s.writeJSON(w, SessionCounts{
		PortForwards: pf,
		ExecSessions: exec,
		Total:        pf + exec,
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

	// Stop all active sessions before switching
	StopAllSessions()

	// Perform the context switch
	if err := k8s.PerformContextSwitch(name); err != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   name,
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Set connected state after successful switch
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})

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
	contexts, _ := k8s.GetAvailableContexts() // Always works (reads kubeconfig)

	s.writeJSON(w, map[string]any{
		"state":           status.State,
		"context":         status.Context,
		"clusterName":     status.ClusterName,
		"error":           status.Error,
		"errorType":       status.ErrorType,
		"progressMessage": status.ProgressMsg,
		"contexts":        contexts,
	})
}

func (s *Server) handleConnectionRetry(w http.ResponseWriter, r *http.Request) {
	ctx := k8s.GetContextName()
	if ctx == "" {
		s.writeError(w, http.StatusBadRequest, "no context configured")
		return
	}

	// Stop all active sessions before retrying
	StopAllSessions()

	// Reconnect to the same context (reuses PerformContextSwitch which handles full reinit)
	if err := k8s.PerformContextSwitch(ctx); err != nil {
		// Set disconnected state with error
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   ctx,
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Set connected state after successful reconnection
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})

	s.writeJSON(w, k8s.GetConnectionStatus())
}

// Helper methods

func (s *Server) writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
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

// requireConnected returns false and writes a 503 error if not connected to cluster.
// Use at the start of handlers that require an active cluster connection.
func (s *Server) requireConnected(w http.ResponseWriter) bool {
	if !k8s.IsConnected() {
		s.writeError(w, http.StatusServiceUnavailable, "Not connected to cluster")
		return false
	}
	return true
}

// Settings handlers

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, settings.Load())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var patch settings.Settings
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
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
	s.writeJSON(w, result)
}

// Debug handlers for event pipeline diagnostics

// handleDebugEvents returns event pipeline metrics and recent drops
func (s *Server) handleDebugEvents(w http.ResponseWriter, r *http.Request) {
	response := timeline.GetDebugEventsResponse()
	s.writeJSON(w, response)
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

	response := timeline.GetDiagnosis(kind, namespace, name)
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
