package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
)

// handleArgoSync triggers a sync operation on an ArgoCD Application
func (s *Server) handleArgoSync(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for sync Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}
	result, err := gitops.SyncArgoApp(r.Context(), client, namespace, name)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "sync", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleArgoRefresh triggers a refresh (re-read from git) on an ArgoCD Application
func (s *Server) handleArgoRefresh(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	refreshType := r.URL.Query().Get("type")
	if refreshType == "" {
		refreshType = "normal"
	} else if refreshType != "normal" && refreshType != "hard" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid refresh type %q: must be 'normal' or 'hard'", refreshType))
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for refresh Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.RefreshArgoApp(r.Context(), client, namespace, name, refreshType)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "refresh", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleArgoTerminate terminates an ongoing sync operation
func (s *Server) handleArgoTerminate(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for terminate Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.TerminateArgoSync(r.Context(), client, namespace, name)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "terminate", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleArgoSuspend disables automated sync on an ArgoCD Application
func (s *Server) handleArgoSuspend(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for suspend Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.SetArgoAutoSync(r.Context(), client, namespace, name, false)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "suspend", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleArgoResume re-enables automated sync on an ArgoCD Application
func (s *Server) handleArgoResume(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for resume Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.SetArgoAutoSync(r.Context(), client, namespace, name, true)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "resume", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// toGitOpsResponse converts a gitops.OperationResult to the REST response type.
func toGitOpsResponse(r gitops.OperationResult) GitOpsOperationResponse {
	resp := GitOpsOperationResponse{
		Message:     r.Message,
		Operation:   r.Operation,
		Tool:        r.Tool,
		Resource:    GitOpsResourceRef{Kind: r.Kind, Name: r.Name, Namespace: r.Namespace},
		RequestedAt: r.RequestedAt,
	}
	if r.Source != nil {
		resp.Source = &GitOpsResourceRef{Kind: r.Source.Kind, Name: r.Source.Name, Namespace: r.Source.Namespace}
	}
	return resp
}

// writeGitOpsError maps gitops operation errors to appropriate HTTP status codes.
func (s *Server) writeGitOpsError(w http.ResponseWriter, err error, module, action, namespace, name string) {
	msg := err.Error()

	// Check typed K8s API errors first (preserved through %w wrapping)
	if apierrors.IsNotFound(err) {
		s.writeError(w, http.StatusNotFound, msg)
		return
	}
	if apierrors.IsForbidden(err) {
		s.writeError(w, http.StatusForbidden, msg)
		return
	}

	// Application-level errors from gitops operations
	switch {
	case strings.Contains(msg, "not found"):
		s.writeError(w, http.StatusNotFound, msg)
	case strings.Contains(msg, "already in progress"):
		s.writeError(w, http.StatusConflict, msg)
	case strings.Contains(msg, "no sync operation in progress"):
		s.writeError(w, http.StatusBadRequest, msg)
	default:
		log.Printf("[%s] Failed to %s %s/%s: %v", module, action, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, msg)
	}
}
