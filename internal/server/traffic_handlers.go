package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/traffic"
)

// namespaceLookup builds a set for membership tests. A nil input (all-namespace
// access from parseNamespacesForUser) returns nil, which flowVisibleForNamespaces
// treats as "no restriction".
func namespaceLookup(namespaces []string) map[string]bool {
	if namespaces == nil {
		return nil
	}
	set := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		set[ns] = true
	}
	return set
}

// flowVisibleForNamespaces reports whether a flow may be shown to a user whose
// allowed namespaces are `allowed` (nil = all-namespace access). The traffic
// source can only filter by a single namespace or none, so multi-namespace
// users are filtered here: a flow is visible when either endpoint is in an
// allowed namespace, so a user sees traffic to and from services in the
// namespaces they can read. External/empty-namespace endpoints alone don't
// make a flow visible.
func flowVisibleForNamespaces(flow traffic.Flow, allowed map[string]bool) bool {
	if allowed == nil {
		return true
	}
	return (flow.Source.Namespace != "" && allowed[flow.Source.Namespace]) ||
		(flow.Destination.Namespace != "" && allowed[flow.Destination.Namespace])
}

// handleGetTrafficSources returns available traffic sources and recommendations
// GET /api/traffic/sources
func (s *Server) handleGetTrafficSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	response, err := manager.DetectSources(ctx)
	if err != nil {
		log.Printf("[traffic] Error detecting sources: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to detect traffic sources")
		return
	}

	s.writeJSON(w, response)
}

// handleGetTrafficFlows returns aggregated flow data
// GET /api/traffic/flows
func (s *Server) handleGetTrafficFlows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	// Parse query parameters
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	}
	sinceStr := r.URL.Query().Get("since")

	opts := traffic.DefaultFlowOptions()
	// Traffic only supports single namespace filter
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}

	if sinceStr != "" {
		duration, err := time.ParseDuration(sinceStr)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid 'since' duration format: %s (expected format like '5m', '1h')", sinceStr))
			return
		}
		opts.Since = duration
	}

	response, err := manager.GetFlows(ctx, opts)
	if err != nil {
		log.Printf("[traffic] Error getting flows: %v", err)
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// The source only filters by a single namespace; restrict multi-namespace
	// users here so flows outside their allowed namespaces aren't returned.
	flows := response.Flows
	if allowed := namespaceLookup(namespaces); allowed != nil {
		kept := make([]traffic.Flow, 0, len(flows))
		for _, f := range flows {
			if flowVisibleForNamespaces(f, allowed) {
				kept = append(kept, f)
			}
		}
		flows = kept
	}

	// Aggregate flows by service pair
	aggregated := traffic.AggregateFlows(flows)

	result := map[string]any{
		"source":     response.Source,
		"timestamp":  response.Timestamp,
		"flows":      flows,
		"aggregated": aggregated,
	}
	if response.Warning != "" {
		result["warning"] = response.Warning
	}
	s.writeJSON(w, result)
}

// handleTrafficFlowsStream provides SSE stream of traffic flows
// GET /api/traffic/flows/stream
func (s *Server) handleTrafficFlowsStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	// Enforce per-user namespace access (parseNamespacesForUser intersects the
	// requested ?namespace= with the user's RBAC-allowed namespaces).
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeError(w, http.StatusForbidden, "no namespace access")
		return
	}
	allowed := namespaceLookup(namespaces)

	opts := traffic.FlowOptions{
		Follow: true,
	}
	// The source filters by a single namespace; multi-namespace users are
	// filtered per-flow below.
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}

	flowCh, err := manager.StreamFlows(ctx, opts)
	if err != nil {
		log.Printf("[traffic] Error starting flow stream: %v", err)
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// Send initial connection event
	if _, err := w.Write([]byte("event: connected\ndata: {}\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// Heartbeat ticker
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case flow, ok := <-flowCh:
			if !ok {
				return
			}

			if !flowVisibleForNamespaces(flow, allowed) {
				continue
			}

			data, err := json.Marshal(flow)
			if err != nil {
				log.Printf("[traffic] Error marshaling flow: %v", err)
				// Notify client of the error
				if _, writeErr := w.Write([]byte("event: error\ndata: {\"error\":\"Failed to serialize flow data\"}\n\n")); writeErr != nil {
					return
				}
				flusher.Flush()
				continue
			}

			if _, err := w.Write([]byte("event: flow\ndata: " + string(data) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := w.Write([]byte("event: heartbeat\ndata: {}\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleSetTrafficSource sets the active traffic source
// POST /api/traffic/source
func (s *Server) handleSetTrafficSource(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	var req struct {
		Source string `json:"source"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Source == "" {
		s.writeError(w, http.StatusBadRequest, "Source name required")
		return
	}

	if err := manager.SetActiveSource(req.Source); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{
		"active": req.Source,
	})
}

// handleGetActiveTrafficSource returns the currently active traffic source
// GET /api/traffic/source
func (s *Server) handleGetActiveTrafficSource(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	active := manager.GetActiveSourceName()

	s.writeJSON(w, map[string]string{
		"active": active,
	})
}

// handleTrafficConnect establishes connection to the traffic source
// This may start a port-forward to metrics service if running locally
// POST /api/traffic/connect
func (s *Server) handleTrafficConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	connInfo, err := manager.Connect(ctx)
	if err != nil {
		log.Printf("[traffic] Error connecting: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, connInfo)
}

// handleTrafficConnectionStatus returns current connection status
// GET /api/traffic/connection
func (s *Server) handleTrafficConnectionStatus(w http.ResponseWriter, r *http.Request) {
	manager := traffic.GetManager()
	if manager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Traffic manager not initialized")
		return
	}

	connInfo := manager.GetConnectionInfo()
	s.writeJSON(w, connInfo)
}
