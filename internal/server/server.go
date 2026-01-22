package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/skyhook-explorer/internal/helm"
	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
	"github.com/skyhook-io/skyhook-explorer/internal/topology"
)

// Server is the Explorer HTTP server
type Server struct {
	router      *chi.Mux
	broadcaster *SSEBroadcaster
	port        int
	devMode     bool
	staticFS    fs.FS
}

// Config holds server configuration
type Config struct {
	Port       int
	DevMode    bool      // Serve frontend from filesystem instead of embedded
	StaticFS   embed.FS  // Embedded frontend files
	StaticRoot string    // Path within StaticFS
}

// New creates a new server instance
func New(cfg Config) *Server {
	s := &Server{
		router:      chi.NewRouter(),
		broadcaster: NewSSEBroadcaster(),
		port:        cfg.Port,
		devMode:     cfg.DevMode,
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

	// Middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// CORS for development
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: true,
	}))

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/cluster-info", s.handleClusterInfo)
		r.Get("/topology", s.handleTopology)
		r.Get("/namespaces", s.handleNamespaces)
		r.Get("/api-resources", s.handleAPIResources)
		r.Get("/resources/{kind}", s.handleListResources)
		r.Get("/resources/{kind}/{namespace}/{name}", s.handleGetResource)
		r.Put("/resources/{kind}/{namespace}/{name}", s.handleUpdateResource)
		r.Delete("/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
		r.Get("/events", s.handleEvents)
		r.Get("/events/stream", s.broadcaster.HandleSSE)
		r.Get("/changes", s.handleChanges)
		r.Get("/changes/{kind}/{namespace}/{name}/children", s.handleChangeChildren)
		// Pod logs
		r.Get("/pods/{namespace}/{name}/logs", s.handlePodLogs)
		r.Get("/pods/{namespace}/{name}/logs/stream", s.handlePodLogsStream)

		// Pod exec (terminal)
		r.Get("/pods/{namespace}/{name}/exec", s.handlePodExec)

		// Port forwarding
		r.Get("/portforwards", s.handleListPortForwards)
		r.Post("/portforwards", s.handleStartPortForward)
		r.Delete("/portforwards/{id}", s.handleStopPortForward)
		r.Get("/portforwards/available/{type}/{namespace}/{name}", s.handleGetAvailablePorts)

		// Helm routes
		helmHandlers := helm.NewHandlers()
		helmHandlers.RegisterRoutes(r)
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

	s.writeJSON(w, map[string]any{
		"status":        status,
		"resourceCount": cache.GetResourceCount(),
	})
}

func (s *Server) handleClusterInfo(w http.ResponseWriter, r *http.Request) {
	info, err := k8s.GetClusterInfo(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, info)
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	viewMode := r.URL.Query().Get("view")

	opts := topology.DefaultBuildOptions()
	opts.Namespace = namespace
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
	kind := chi.URLParam(r, "kind")
	namespace := r.URL.Query().Get("namespace")

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var result any
	var err error

	// Try typed cache for known resource types first
	switch kind {
	case "pods":
		if namespace != "" {
			result, err = cache.Pods().Pods(namespace).List(labels.Everything())
		} else {
			result, err = cache.Pods().List(labels.Everything())
		}
	case "services":
		if namespace != "" {
			result, err = cache.Services().Services(namespace).List(labels.Everything())
		} else {
			result, err = cache.Services().List(labels.Everything())
		}
	case "deployments":
		if namespace != "" {
			result, err = cache.Deployments().Deployments(namespace).List(labels.Everything())
		} else {
			result, err = cache.Deployments().List(labels.Everything())
		}
	case "daemonsets":
		if namespace != "" {
			result, err = cache.DaemonSets().DaemonSets(namespace).List(labels.Everything())
		} else {
			result, err = cache.DaemonSets().List(labels.Everything())
		}
	case "statefulsets":
		if namespace != "" {
			result, err = cache.StatefulSets().StatefulSets(namespace).List(labels.Everything())
		} else {
			result, err = cache.StatefulSets().List(labels.Everything())
		}
	case "replicasets":
		if namespace != "" {
			result, err = cache.ReplicaSets().ReplicaSets(namespace).List(labels.Everything())
		} else {
			result, err = cache.ReplicaSets().List(labels.Everything())
		}
	case "ingresses":
		if namespace != "" {
			result, err = cache.Ingresses().Ingresses(namespace).List(labels.Everything())
		} else {
			result, err = cache.Ingresses().List(labels.Everything())
		}
	case "configmaps":
		if namespace != "" {
			result, err = cache.ConfigMaps().ConfigMaps(namespace).List(labels.Everything())
		} else {
			result, err = cache.ConfigMaps().List(labels.Everything())
		}
	case "secrets":
		if namespace != "" {
			result, err = cache.Secrets().Secrets(namespace).List(labels.Everything())
		} else {
			result, err = cache.Secrets().List(labels.Everything())
		}
	case "events":
		if namespace != "" {
			result, err = cache.Events().Events(namespace).List(labels.Everything())
		} else {
			result, err = cache.Events().List(labels.Everything())
		}
	case "persistentvolumeclaims", "pvcs":
		if namespace != "" {
			result, err = cache.PersistentVolumeClaims().PersistentVolumeClaims(namespace).List(labels.Everything())
		} else {
			result, err = cache.PersistentVolumeClaims().List(labels.Everything())
		}
	case "jobs":
		if namespace != "" {
			result, err = cache.Jobs().Jobs(namespace).List(labels.Everything())
		} else {
			result, err = cache.Jobs().List(labels.Everything())
		}
	case "cronjobs":
		if namespace != "" {
			result, err = cache.CronJobs().CronJobs(namespace).List(labels.Everything())
		} else {
			result, err = cache.CronJobs().List(labels.Everything())
		}
	case "hpas", "horizontalpodautoscalers":
		if namespace != "" {
			result, err = cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).List(labels.Everything())
		} else {
			result, err = cache.HorizontalPodAutoscalers().List(labels.Everything())
		}
	case "nodes":
		result, err = cache.Nodes().List(labels.Everything())
	case "namespaces":
		result, err = cache.Namespaces().List(labels.Everything())
	default:
		// Fall back to dynamic cache for CRDs and other unknown resources
		result, err = cache.ListDynamic(r.Context(), kind, namespace)
		if err != nil {
			// Check if it's an unknown resource error
			if strings.Contains(err.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
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

func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

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
		resource, err = cache.Secrets().Secrets(namespace).Get(name)
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
		resource, err = cache.GetDynamic(r.Context(), kind, namespace, name)
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

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var events any
	var err error

	if namespace != "" {
		events, err = cache.Events().Events(namespace).List(labels.Everything())
	} else {
		events, err = cache.Events().List(labels.Everything())
	}

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, events)
}

// TimelineEvent represents a unified event in the timeline (either a change or K8s event)
type TimelineEvent struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"` // "change" or "k8s_event"
	Timestamp   time.Time      `json:"timestamp"`
	Kind        string         `json:"kind"`
	Namespace   string         `json:"namespace"`
	Name        string         `json:"name"`
	Operation   string         `json:"operation,omitempty"`   // For changes: add, update, delete
	Diff        *k8s.DiffInfo  `json:"diff,omitempty"`        // For changes with updates
	HealthState string         `json:"healthState,omitempty"` // For changes
	Owner       *k8s.OwnerInfo `json:"owner,omitempty"`       // For managed resources
	Reason      string         `json:"reason,omitempty"`      // For K8s events
	Message     string         `json:"message,omitempty"`     // For K8s events
	EventType   string         `json:"eventType,omitempty"`   // For K8s events: Normal, Warning
	Count       int32          `json:"count,omitempty"`       // For K8s events
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	kind := r.URL.Query().Get("kind")
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")
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
			if limit > 1000 {
				limit = 1000
			}
		}
	}

	var timeline []TimelineEvent

	// Get change records from history
	history := k8s.GetChangeHistory()
	if history != nil {
		changes := history.GetChanges(k8s.GetChangesOptions{
			Namespace:      namespace,
			Kind:           kind,
			Since:          since,
			Limit:          limit,
			IncludeManaged: includeManaged,
		})
		for _, c := range changes {
			timeline = append(timeline, TimelineEvent{
				ID:          c.ID,
				Type:        "change",
				Timestamp:   c.Timestamp,
				Kind:        c.Kind,
				Namespace:   c.Namespace,
				Name:        c.Name,
				Operation:   c.Operation,
				Diff:        c.Diff,
				HealthState: c.HealthState,
				Owner:       c.Owner,
			})
		}
	}

	// Get K8s events and merge
	if includeK8sEvents {
		cache := k8s.GetResourceCache()
		if cache != nil {
			var k8sEvents []*corev1.Event
			var err error

			if namespace != "" {
				k8sEvents, err = cache.Events().Events(namespace).List(labels.Everything())
			} else {
				k8sEvents, err = cache.Events().List(labels.Everything())
			}

			if err == nil {
				for _, e := range k8sEvents {
					// Filter by kind if specified
					if kind != "" && e.InvolvedObject.Kind != kind {
						continue
					}

					// Use lastTimestamp or firstTimestamp
					ts := e.LastTimestamp.Time
					if ts.IsZero() {
						ts = e.FirstTimestamp.Time
					}
					if ts.IsZero() {
						ts = e.CreationTimestamp.Time
					}

					// Filter by since
					if !since.IsZero() && ts.Before(since) {
						continue
					}

					// Try to get owner info from the involved object
					var owner *k8s.OwnerInfo
					if e.InvolvedObject.Kind == "Pod" {
						// Look up the pod to get its owner
						if pod, podErr := cache.Pods().Pods(e.Namespace).Get(e.InvolvedObject.Name); podErr == nil && pod != nil {
							for _, ref := range pod.OwnerReferences {
								if ref.Controller != nil && *ref.Controller {
									owner = &k8s.OwnerInfo{
										Kind: ref.Kind,
										Name: ref.Name,
									}
									break
								}
							}
						}
					} else if e.InvolvedObject.Kind == "ReplicaSet" {
						// Look up the ReplicaSet to get its owner (usually Deployment)
						if rs, rsErr := cache.ReplicaSets().ReplicaSets(e.Namespace).Get(e.InvolvedObject.Name); rsErr == nil && rs != nil {
							for _, ref := range rs.OwnerReferences {
								if ref.Controller != nil && *ref.Controller {
									owner = &k8s.OwnerInfo{
										Kind: ref.Kind,
										Name: ref.Name,
									}
									break
								}
							}
						}
					}

					timeline = append(timeline, TimelineEvent{
						ID:        string(e.UID),
						Type:      "k8s_event",
						Timestamp: ts,
						Kind:      e.InvolvedObject.Kind,
						Namespace: e.Namespace,
						Name:      e.InvolvedObject.Name,
						Reason:    e.Reason,
						Message:   e.Message,
						EventType: e.Type,
						Count:     e.Count,
						Owner:     owner,
					})
				}
			}
		}
	}

	// Sort by timestamp descending (most recent first)
	sort.Slice(timeline, func(i, j int) bool {
		return timeline[i].Timestamp.After(timeline[j].Timestamp)
	})

	// Apply limit
	if len(timeline) > limit {
		timeline = timeline[:limit]
	}

	s.writeJSON(w, timeline)
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

	var children []TimelineEvent

	history := k8s.GetChangeHistory()
	if history != nil {
		changes := history.GetChangesForOwner(ownerKind, namespace, ownerName, since, 100)
		for _, c := range changes {
			children = append(children, TimelineEvent{
				ID:          c.ID,
				Type:        "change",
				Timestamp:   c.Timestamp,
				Kind:        c.Kind,
				Namespace:   c.Namespace,
				Name:        c.Name,
				Operation:   c.Operation,
				Diff:        c.Diff,
				HealthState: c.HealthState,
				Owner:       c.Owner,
			})
		}
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

// Helper methods

func (s *Server) writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Unused but needed for imports
var _ = context.Background
