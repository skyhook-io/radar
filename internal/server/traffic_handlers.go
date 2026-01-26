package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/skyhook-explorer/internal/traffic"
)

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
	namespace := r.URL.Query().Get("namespace")
	sinceStr := r.URL.Query().Get("since")

	opts := traffic.DefaultFlowOptions()
	opts.Namespace = namespace

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

	// Aggregate flows by service pair
	aggregated := traffic.AggregateFlows(response.Flows)

	result := map[string]interface{}{
		"source":     response.Source,
		"timestamp":  response.Timestamp,
		"flows":      response.Flows,
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

	// Parse query parameters
	namespace := r.URL.Query().Get("namespace")

	opts := traffic.FlowOptions{
		Namespace: namespace,
		Follow:    true,
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
