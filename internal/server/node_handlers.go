package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// DrainRequest is the optional JSON body for the drain endpoint.
type DrainRequest struct {
	DeleteEmptyDirData *bool  `json:"deleteEmptyDirData,omitempty"` // Default true if omitted
	Force              bool   `json:"force"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
	Timeout            int    `json:"timeout,omitempty"` // seconds, default 60
}

func (s *Server) handleCordonNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	auth.AuditLog(r, "", nodeName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	if err := k8s.CordonNodeWithClient(r.Context(), nodeName, client); err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[node-ops] Failed to cordon node %s: %v", nodeName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"status": "ok", "message": "Node cordoned"})
}

func (s *Server) handleUncordonNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	auth.AuditLog(r, "", nodeName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	if err := k8s.UncordonNodeWithClient(r.Context(), nodeName, client); err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[node-ops] Failed to uncordon node %s: %v", nodeName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"status": "ok", "message": "Node uncordoned"})
}

func (s *Server) handleDrainNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	var req DrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// DeleteEmptyDirData defaults true (matching kubectl drain --delete-emptydir-data).
	// Most pods use emptyDir for tmp/caches; without this, drain skips almost everything.
	deleteLocal := true
	if req.DeleteEmptyDirData != nil {
		deleteLocal = *req.DeleteEmptyDirData
	}
	opts := k8s.DrainOptions{
		IgnoreDaemonSets:   true,
		DeleteEmptyDirData: deleteLocal,
		Force:              req.Force,
		GracePeriodSeconds: req.GracePeriodSeconds,
		Timeout:            60 * time.Second,
	}
	if req.Timeout > 0 {
		opts.Timeout = time.Duration(req.Timeout) * time.Second
	}

	auth.AuditLog(r, "", nodeName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	result, err := k8s.DrainNodeWithClient(r.Context(), nodeName, opts, client)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[node-ops] Failed to drain node %s: %v", nodeName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(result.Errors) > 0 {
		log.Printf("[node-ops] Drain node %s: %d evicted, %d errors: %v",
			nodeName, len(result.EvictedPods), len(result.Errors), result.Errors)
	} else {
		log.Printf("[node-ops] Drain node %s completed: %d pods evicted", nodeName, len(result.EvictedPods))
	}

	s.writeJSON(w, result)
}
