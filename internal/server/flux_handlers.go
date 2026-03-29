package server

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
)

// handleFluxReconcile triggers a reconciliation by setting the reconcile annotation
func (s *Server) handleFluxReconcile(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	entry, err := gitops.ResolveFluxKind(kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.ReconcileFlux(r.Context(), client, entry, namespace, name)
	if err != nil {
		s.writeGitOpsError(w, err, "flux", "reconcile", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleFluxSuspend suspends a Flux resource by setting spec.suspend=true
func (s *Server) handleFluxSuspend(w http.ResponseWriter, r *http.Request) {
	s.fluxSetSuspend(w, r, true)
}

// handleFluxResume resumes a suspended Flux resource by setting spec.suspend=false
func (s *Server) handleFluxResume(w http.ResponseWriter, r *http.Request) {
	s.fluxSetSuspend(w, r, false)
}

// fluxSetSuspend is a helper that sets the spec.suspend field
func (s *Server) fluxSetSuspend(w http.ResponseWriter, r *http.Request, suspend bool) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	entry, err := gitops.ResolveFluxKind(kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	action := "suspend"
	if !suspend {
		action = "resume"
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.SetFluxSuspend(r.Context(), client, entry, namespace, name, suspend)
	if err != nil {
		s.writeGitOpsError(w, err, "flux", action, namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}

// handleFluxSyncWithSource reconciles the source first, then the resource
func (s *Server) handleFluxSyncWithSource(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Validate kind early — bad input is a 400, not a 500
	if _, err := gitops.ResolveFluxKind(kind); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	result, err := gitops.SyncFluxWithSource(r.Context(), client, kind, namespace, name)
	if err != nil {
		s.writeGitOpsError(w, err, "flux", "sync-with-source", namespace, name)
		return
	}

	s.writeJSON(w, toGitOpsResponse(result))
}
