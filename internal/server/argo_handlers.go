package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

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
		log.Printf("[argo] Dynamic client unavailable for sync Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}
	var opts gitops.ArgoSyncOptions
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid sync request: %v", err))
			return
		}
	}
	opts = enforceArgoSelectiveSyncSafety(opts)
	result, err := gitops.SyncArgoApp(r.Context(), client, namespace, name, opts)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "sync", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

func enforceArgoSelectiveSyncSafety(opts gitops.ArgoSyncOptions) gitops.ArgoSyncOptions {
	if len(opts.Resources) == 0 {
		return opts
	}
	falseValue := false
	opts.Revision = ""
	opts.Prune = &falseValue
	opts.ApplyOnly = &falseValue
	return opts
}

func (s *Server) handleArgoValidateResource(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for validating Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}
	var opts gitops.ArgoSyncOptions
	if r.Body == nil || r.ContentLength == 0 {
		s.writeError(w, http.StatusBadRequest, "validation request requires exactly one resource")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid validation request: %v", err))
		return
	}
	if len(opts.Resources) != 1 || opts.Resources[0].Kind == "" || opts.Resources[0].Name == "" {
		s.writeError(w, http.StatusBadRequest, "validation request requires exactly one resource with kind and name")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := gitops.ValidateArgoResource(ctx, client, namespace, name, opts.Resources[0], opts)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "validate resource", namespace, name)
		return
	}
	s.writeJSON(w, result)
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
		log.Printf("[argo] Dynamic client unavailable for refresh Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
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

// handleArgoRollback rolls an Application back to a prior history entry by ID.
func (s *Server) handleArgoRollback(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for rollback Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	var opts gitops.ArgoRollbackOptions
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid rollback request: %v", err))
			return
		}
	}
	// Match the core function's contract (RollbackArgoApp rejects <= 0).
	// Reject negatives at the HTTP boundary so they 400 instead of falling
	// through to a generic 500 from the operation layer.
	if opts.ID <= 0 {
		s.writeError(w, http.StatusBadRequest, "rollback request requires positive id")
		return
	}

	result, err := gitops.RollbackArgoApp(r.Context(), client, namespace, name, opts)
	if err != nil {
		s.writeGitOpsError(w, err, "argo", "rollback", namespace, name)
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
		log.Printf("[argo] Dynamic client unavailable for terminate Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
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
		log.Printf("[argo] Dynamic client unavailable for suspend Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
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
		log.Printf("[argo] Dynamic client unavailable for resume Application %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
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

// writeGitOpsError maps gitops operation errors to HTTP status codes via
// errors.Is on typed sentinels (defined in pkg/gitops) so the mapping doesn't
// drift if upstream wording changes. Every branch logs so 4xx outcomes
// remain visible to operators scraping server logs.
func (s *Server) writeGitOpsError(w http.ResponseWriter, err error, module, action, namespace, name string) {
	msg := err.Error()
	var status int
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case apierrors.IsNotFound(err):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err):
		status = http.StatusForbidden
	case errors.Is(err, gitops.ErrHistoryEntryNotFound):
		status = http.StatusNotFound
	case errors.Is(err, gitops.ErrOperationInProgress):
		status = http.StatusConflict
	case errors.Is(err, gitops.ErrResourceTerminating):
		// 409 Conflict: the resource state ("being deleted") conflicts
		// with the request. Same status family as ErrOperationInProgress
		// — both signal "request is well-formed but the resource isn't
		// in a state where this verb can run".
		status = http.StatusConflict
	case errors.Is(err, gitops.ErrNoOperationInProgress):
		status = http.StatusBadRequest
	case errors.Is(err, gitops.ErrInvalidResourceSelection):
		status = http.StatusBadRequest
	default:
		status = http.StatusInternalServerError
	}
	log.Printf("[%s] %s %s/%s -> %d: %v", module, action, sanitizeForLog(namespace), sanitizeForLog(name), status, err)
	s.writeError(w, status, msg)
}
