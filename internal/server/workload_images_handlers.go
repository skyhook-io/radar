package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

func (s *Server) handleGetWorkloadImages(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	inventory, err := k8s.GetWorkloadImagesWithClient(r.Context(), kind, namespace, name, client)
	if err != nil {
		s.writeWorkloadImageError(w, err, "read", kind, namespace, name)
		return
	}
	s.writeJSON(w, inventory)
}

func (s *Server) handleSetWorkloadImages(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	var req struct {
		Updates []k8s.WorkloadImageUpdate `json:"updates"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	result, err := k8s.SetWorkloadImagesWithClient(r.Context(), kind, namespace, name, req.Updates, client)
	if err != nil {
		s.writeWorkloadImageError(w, err, "update", kind, namespace, name)
		return
	}
	clearApplicationsCache()
	s.writeJSON(w, result)
}

func (s *Server) writeWorkloadImageError(w http.ResponseWriter, err error, action, kind, namespace, name string) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case apierrors.IsNotFound(err):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err):
		status = http.StatusForbidden
	case apierrors.IsConflict(err), errors.Is(err, k8score.ErrImageWorkloadTerminating):
		status = http.StatusConflict
	case errors.Is(err, k8score.ErrUnsupportedImageWorkload),
		errors.Is(err, k8score.ErrInvalidImageUpdate),
		errors.Is(err, rollouts.ErrWorkloadRefUnsupported):
		status = http.StatusBadRequest
	case apierrors.IsInvalid(err):
		status = http.StatusUnprocessableEntity
	}
	if status == http.StatusInternalServerError {
		log.Printf("[images] Failed to %s images for %s %s/%s: %v", action, sanitizeForLog(kind), sanitizeForLog(namespace), sanitizeForLog(name), err)
	}
	s.writeError(w, status, err.Error())
}
