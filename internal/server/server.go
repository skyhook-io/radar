package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
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
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/topology"
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
}

// Config holds server configuration
type Config struct {
	Port       int
	DevMode    bool     // Serve frontend from filesystem instead of embedded
	StaticFS   embed.FS // Embedded frontend files
	StaticRoot string   // Path within StaticFS
}

// New creates a new server instance
func New(cfg Config) *Server {
	s := &Server{
		router:      chi.NewRouter(),
		broadcaster: NewSSEBroadcaster(),
		port:        cfg.Port,
		devMode:     cfg.DevMode,
		startTime:   time.Now(),
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
	})

	// API routes
	r.Route("/api", func(r chi.Router) {
		// Streaming endpoints (SSE/WebSocket) - no timeout
		r.Get("/events/stream", s.broadcaster.HandleSSE)
		r.Get("/pods/{namespace}/{name}/logs/stream", s.handlePodLogsStream)
		r.Get("/pods/{namespace}/{name}/exec", s.handlePodExec)
		r.Get("/workloads/{kind}/{namespace}/{name}/logs/stream", s.handleWorkloadLogsStream)

		// All other API routes get a 60-second timeout
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(60 * time.Second))

			r.Get("/health", s.handleHealth)
			r.Get("/version-check", s.handleVersionCheck)
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/dashboard/crds", s.handleDashboardCRDs)
			r.Get("/cluster-info", s.handleClusterInfo)
			r.Get("/capabilities", s.handleCapabilities)
			r.Get("/topology", s.handleTopology)
			r.Get("/namespaces", s.handleNamespaces)
			r.Get("/api-resources", s.handleAPIResources)
			r.Get("/resources/{kind}", s.handleListResources)
			r.Get("/resources/{kind}/{namespace}/{name}", s.handleGetResource)
			r.Put("/resources/{kind}/{namespace}/{name}", s.handleUpdateResource)
			r.Delete("/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			r.Get("/events", s.handleEvents)
			r.Get("/changes", s.handleChanges)
			r.Get("/changes/{kind}/{namespace}/{name}/children", s.handleChangeChildren)

			// Pod logs (non-streaming)
			r.Get("/pods/{namespace}/{name}/logs", s.handlePodLogs)

			// Pod debug (ephemeral container)
			r.Post("/pods/{namespace}/{name}/debug", s.handleCreateDebugContainer)

			// Metrics (from metrics.k8s.io API)
			r.Get("/metrics/pods/{namespace}/{name}", s.handlePodMetrics)
			r.Get("/metrics/nodes/{name}", s.handleNodeMetrics)
			r.Get("/metrics/pods/{namespace}/{name}/history", s.handlePodMetricsHistory)
			r.Get("/metrics/nodes/{name}/history", s.handleNodeMetricsHistory)

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

			// Workload restart
			r.Post("/workloads/{kind}/{namespace}/{name}/restart", s.handleRestartWorkload)
			r.Post("/workloads/{kind}/{namespace}/{name}/scale", s.handleScaleWorkload)

			// Workload logs (non-streaming)
			r.Get("/workloads/{kind}/{namespace}/{name}/logs", s.handleWorkloadLogs)
			r.Get("/workloads/{kind}/{namespace}/{name}/pods", s.handleWorkloadPods)

			// Helm routes
			helmHandlers := helm.NewHandlers()
			helmHandlers.RegisterRoutes(r)

			// Image inspection routes
			imageHandlers := images.NewHandlers()
			imageHandlers.RegisterRoutes(r)

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
		})

		// Traffic streaming (no timeout)
		r.Get("/traffic/flows/stream", s.handleTrafficFlowsStream)
	})

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

// Start starts the server
func (s *Server) Start() error {
	s.broadcaster.Start()

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("Starting Explorer server on http://localhost%s", addr)

	return http.ListenAndServe(addr, s.router)
}

// Stop gracefully stops the server
func (s *Server) Stop() {
	s.broadcaster.Stop()
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
	runtimeStats["typedInformers"] = 16  // Fixed count of typed informers in cache.go
	runtimeStats["dynamicInformers"] = dynamicInformerCount

	s.writeJSON(w, map[string]any{
		"status":        status,
		"resourceCount": cache.GetResourceCount(),
		"timeline":      timelineStats,
		"runtime":       runtimeStats,
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

	namespaces, err := cache.Namespaces().List(labels.Everything())
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

	// Try typed cache for known resource types first
	switch kind {
	case "pods":
		result, err = listPerNs(
			func() (any, error) { return cache.Pods().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Pods().Pods(ns).List(labels.Everything()) },
		)
	case "services":
		result, err = listPerNs(
			func() (any, error) { return cache.Services().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Services().Services(ns).List(labels.Everything()) },
		)
	case "deployments":
		result, err = listPerNs(
			func() (any, error) { return cache.Deployments().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Deployments().Deployments(ns).List(labels.Everything()) },
		)
	case "daemonsets":
		result, err = listPerNs(
			func() (any, error) { return cache.DaemonSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.DaemonSets().DaemonSets(ns).List(labels.Everything()) },
		)
	case "statefulsets":
		result, err = listPerNs(
			func() (any, error) { return cache.StatefulSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.StatefulSets().StatefulSets(ns).List(labels.Everything()) },
		)
	case "replicasets":
		result, err = listPerNs(
			func() (any, error) { return cache.ReplicaSets().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.ReplicaSets().ReplicaSets(ns).List(labels.Everything()) },
		)
	case "ingresses":
		result, err = listPerNs(
			func() (any, error) { return cache.Ingresses().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Ingresses().Ingresses(ns).List(labels.Everything()) },
		)
	case "configmaps":
		result, err = listPerNs(
			func() (any, error) { return cache.ConfigMaps().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.ConfigMaps().ConfigMaps(ns).List(labels.Everything()) },
		)
	case "secrets":
		lister := cache.Secrets()
		if lister == nil {
			// Secrets not available (RBAC not granted)
			result = []interface{}{}
		} else {
			result, err = listPerNs(
				func() (any, error) { return lister.List(labels.Everything()) },
				func(ns string) (any, error) { return lister.Secrets(ns).List(labels.Everything()) },
			)
		}
	case "events":
		result, err = listPerNs(
			func() (any, error) { return cache.Events().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Events().Events(ns).List(labels.Everything()) },
		)
	case "persistentvolumeclaims", "pvcs":
		result, err = listPerNs(
			func() (any, error) { return cache.PersistentVolumeClaims().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.PersistentVolumeClaims().PersistentVolumeClaims(ns).List(labels.Everything())
			},
		)
	case "jobs":
		result, err = listPerNs(
			func() (any, error) { return cache.Jobs().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.Jobs().Jobs(ns).List(labels.Everything()) },
		)
	case "cronjobs":
		result, err = listPerNs(
			func() (any, error) { return cache.CronJobs().List(labels.Everything()) },
			func(ns string) (any, error) { return cache.CronJobs().CronJobs(ns).List(labels.Everything()) },
		)
	case "hpas", "horizontalpodautoscalers":
		result, err = listPerNs(
			func() (any, error) { return cache.HorizontalPodAutoscalers().List(labels.Everything()) },
			func(ns string) (any, error) {
				return cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(ns).List(labels.Everything())
			},
		)
	case "nodes":
		result, err = cache.Nodes().List(labels.Everything())
	case "namespaces":
		result, err = cache.Namespaces().List(labels.Everything())
	default:
		// Fall back to dynamic cache for CRDs and other unknown resources
		if len(namespaces) > 0 {
			var merged []any
			for _, ns := range namespaces {
				items, listErr := cache.ListDynamic(r.Context(), kind, ns)
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
			result, err = cache.ListDynamic(r.Context(), kind, "")
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
// Kubernetes informers don't populate these fields, but users expect to see them in YAML.
func setTypeMeta(resource any) {
	switch r := resource.(type) {
	case *corev1.Pod:
		r.APIVersion = "v1"
		r.Kind = "Pod"
	case *corev1.Service:
		r.APIVersion = "v1"
		r.Kind = "Service"
	case *corev1.Node:
		r.APIVersion = "v1"
		r.Kind = "Node"
	case *corev1.Namespace:
		r.APIVersion = "v1"
		r.Kind = "Namespace"
	case *corev1.ConfigMap:
		r.APIVersion = "v1"
		r.Kind = "ConfigMap"
	case *corev1.Secret:
		r.APIVersion = "v1"
		r.Kind = "Secret"
	case *corev1.PersistentVolumeClaim:
		r.APIVersion = "v1"
		r.Kind = "PersistentVolumeClaim"
	case *appsv1.Deployment:
		r.APIVersion = "apps/v1"
		r.Kind = "Deployment"
	case *appsv1.DaemonSet:
		r.APIVersion = "apps/v1"
		r.Kind = "DaemonSet"
	case *appsv1.StatefulSet:
		r.APIVersion = "apps/v1"
		r.Kind = "StatefulSet"
	case *appsv1.ReplicaSet:
		r.APIVersion = "apps/v1"
		r.Kind = "ReplicaSet"
	case *networkingv1.Ingress:
		r.APIVersion = "networking.k8s.io/v1"
		r.Kind = "Ingress"
	case *batchv1.Job:
		r.APIVersion = "batch/v1"
		r.Kind = "Job"
	case *batchv1.CronJob:
		r.APIVersion = "batch/v1"
		r.Kind = "CronJob"
	case *autoscalingv2.HorizontalPodAutoscaler:
		r.APIVersion = "autoscaling/v2"
		r.Kind = "HorizontalPodAutoscaler"
	}
	// Unstructured resources (CRDs) already have APIVersion and Kind set
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

	// Try typed cache for known resource types first
	switch kind {
	case "pods", "pod":
		resource, err = cache.Pods().Pods(namespace).Get(name)
	case "services", "service":
		resource, err = cache.Services().Services(namespace).Get(name)
	case "deployments", "deployment":
		resource, err = cache.Deployments().Deployments(namespace).Get(name)
	case "daemonsets", "daemonset":
		resource, err = cache.DaemonSets().DaemonSets(namespace).Get(name)
	case "statefulsets", "statefulset":
		resource, err = cache.StatefulSets().StatefulSets(namespace).Get(name)
	case "replicasets", "replicaset":
		resource, err = cache.ReplicaSets().ReplicaSets(namespace).Get(name)
	case "ingresses", "ingress":
		resource, err = cache.Ingresses().Ingresses(namespace).Get(name)
	case "configmaps", "configmap":
		resource, err = cache.ConfigMaps().ConfigMaps(namespace).Get(name)
	case "secrets", "secret":
		lister := cache.Secrets()
		if lister == nil {
			s.writeError(w, http.StatusForbidden, "secrets access not available (RBAC not granted)")
			return
		}
		resource, err = lister.Secrets(namespace).Get(name)
	case "persistentvolumeclaims", "persistentvolumeclaim", "pvcs", "pvc":
		resource, err = cache.PersistentVolumeClaims().PersistentVolumeClaims(namespace).Get(name)
	case "hpas", "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		resource, err = cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
	case "jobs", "job":
		resource, err = cache.Jobs().Jobs(namespace).Get(name)
	case "cronjobs", "cronjob":
		resource, err = cache.CronJobs().CronJobs(namespace).Get(name)
	case "nodes", "node":
		resource, err = cache.Nodes().Get(name)
	case "namespaces", "namespace":
		resource, err = cache.Namespaces().Get(name)
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
		// Return empty history instead of error - metrics may not have been collected yet
		history = &k8s.PodMetricsHistory{
			Namespace:  namespace,
			Name:       name,
			Containers: []k8s.ContainerMetricsHistory{},
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
		// Return empty history instead of error
		history = &k8s.NodeMetricsHistory{
			Name:       name,
			DataPoints: []k8s.MetricsDataPoint{},
		}
	}

	s.writeJSON(w, history)
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

	var events any
	var err error

	if len(namespaces) == 1 {
		events, err = cache.Events().Events(namespaces[0]).List(labels.Everything())
	} else if len(namespaces) > 1 {
		var merged []any
		for _, ns := range namespaces {
			items, listErr := cache.Events().Events(ns).List(labels.Everything())
			if listErr != nil {
				s.writeError(w, http.StatusInternalServerError, listErr.Error())
				return
			}
			merged = appendSlice(merged, items)
		}
		events = merged
	} else {
		events, err = cache.Events().List(labels.Everything())
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

	err := k8s.DeleteResource(r.Context(), kind, namespace, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
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

	s.writeJSON(w, map[string]interface{}{
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
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
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
