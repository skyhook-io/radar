package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

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
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: true,
	}))

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/cluster-info", s.handleClusterInfo)
		r.Get("/topology", s.handleTopology)
		r.Get("/namespaces", s.handleNamespaces)
		r.Get("/resources/{kind}", s.handleListResources)
		r.Get("/resources/{kind}/{namespace}/{name}", s.handleGetResource)
		r.Get("/events", s.handleEvents)
		r.Get("/events/stream", s.broadcaster.HandleSSE)
		r.Get("/changes", s.handleChanges)
		r.Get("/changes/{kind}/{namespace}/{name}/children", s.handleChangeChildren)
	})

	// Static files (frontend)
	if s.staticFS != nil {
		r.Handle("/*", http.FileServer(http.FS(s.staticFS)))
	} else if s.devMode {
		// In dev mode, serve from web/dist
		r.Handle("/*", http.FileServer(http.Dir("web/dist")))
	}
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
	case "events":
		if namespace != "" {
			result, err = cache.Events().Events(namespace).List(labels.Everything())
		} else {
			result, err = cache.Events().List(labels.Everything())
		}
	default:
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown resource kind: %s", kind))
		return
	}

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, result)
}

func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	var resource any
	var err error

	switch kind {
	case "pods":
		resource, err = cache.Pods().Pods(namespace).Get(name)
	case "services":
		resource, err = cache.Services().Services(namespace).Get(name)
	case "deployments":
		resource, err = cache.Deployments().Deployments(namespace).Get(name)
	case "daemonsets":
		resource, err = cache.DaemonSets().DaemonSets(namespace).Get(name)
	case "statefulsets":
		resource, err = cache.StatefulSets().StatefulSets(namespace).Get(name)
	case "replicasets":
		resource, err = cache.ReplicaSets().ReplicaSets(namespace).Get(name)
	case "ingresses":
		resource, err = cache.Ingresses().Ingresses(namespace).Get(name)
	case "configmaps":
		resource, err = cache.ConfigMaps().ConfigMaps(namespace).Get(name)
	case "secrets":
		resource, err = cache.Secrets().Secrets(namespace).Get(name)
	case "hpas":
		resource, err = cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
	case "jobs":
		resource, err = cache.Jobs().Jobs(namespace).Get(name)
	case "cronjobs":
		resource, err = cache.CronJobs().CronJobs(namespace).Get(name)
	default:
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown resource kind: %s", kind))
		return
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
