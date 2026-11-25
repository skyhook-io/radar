package server

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

var argoWorkflowGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}

// WorkflowStopRequest targets the Workflow instance the caller reviewed.
type WorkflowStopRequest struct {
	UID             string `json:"uid"`
	Strategy        string `json:"strategy"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// WorkflowStopResponse is the Workflow's state after the request.
type WorkflowStopResponse struct {
	// Shutdown is the resulting value of spec.shutdown.
	Shutdown string `json:"shutdown"`
	// Changed reports whether Kubernetes accepted a patch for this request.
	Changed bool `json:"changed"`
}

// handleWorkflowStop sets spec.shutdown for Argo to reconcile asynchronously.
// Both the read and patch use the caller's Kubernetes identity, or Radar's
// identity when authentication is disabled. Terminate can escalate Stop.
func (s *Server) handleWorkflowStop(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	var req WorkflowStopRequest
	if err := decodeBoundedJSONBody(w, r, 1<<10, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Strategy != "Terminate" && req.Strategy != "Stop" {
		s.writeError(w, http.StatusBadRequest, "strategy must be Terminate or Stop")
		return
	}
	if req.UID == "" {
		s.writeError(w, http.StatusBadRequest, "uid is required")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	workflows := client.Resource(argoWorkflowGVR).Namespace(namespace)
	const maxPatchAttempts = 4
	for attempt := 1; attempt <= maxPatchAttempts; attempt++ {
		wf, err := workflows.Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeWorkflowStopError(w, err, namespace, name, "read")
			return
		}
		if req.UID != string(wf.GetUID()) {
			s.writeErrorCode(w, http.StatusConflict, "workflow_replaced", "a different workflow now has this name")
			return
		}
		if k8s.IsWorkflowTerminal(wf.Object) {
			s.writeErrorCode(w, http.StatusConflict, "workflow_finished", "the workflow has already finished")
			return
		}
		if current, _, _ := unstructured.NestedString(wf.Object, "spec", "shutdown"); current == "Terminate" || current == req.Strategy {
			s.writeJSON(w, WorkflowStopResponse{Shutdown: current})
			return
		}
		if req.ResourceVersion != "" && req.ResourceVersion != wf.GetResourceVersion() {
			s.writeErrorCode(w, http.StatusConflict, "workflow_changed", "the workflow changed before stopping it; look again")
			return
		}
		updated, err := integration.ApplyVersionedMergePatch(r.Context(), client, argoWorkflowGVR, wf, map[string]any{
			"spec": map[string]any{"shutdown": req.Strategy},
		})
		if apierrors.IsConflict(err) && req.ResourceVersion == "" && attempt < maxPatchAttempts {
			continue
		}
		if err != nil {
			s.writeWorkflowStopError(w, err, namespace, name, "patch")
			return
		}
		shutdown, _, _ := unstructured.NestedString(updated.Object, "spec", "shutdown")
		s.writeJSON(w, WorkflowStopResponse{Shutdown: shutdown, Changed: true})
		return
	}
}

func (s *Server) writeWorkflowStopError(w http.ResponseWriter, err error, namespace, name, step string) {
	switch {
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, "workflow not found")
	case apierrors.IsUnauthorized(err):
		s.writeError(w, http.StatusForbidden, "Kubernetes rejected the credentials for this request")
	case apierrors.IsForbidden(err) && step == "read":
		s.writeError(w, http.StatusForbidden, "failed to read workflow: "+err.Error())
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "failed to stop workflow: "+err.Error())
	case apierrors.IsConflict(err):
		s.writeErrorCode(w, http.StatusConflict, "workflow_changed", "the workflow changed while stopping it; look again")
	case apierrors.IsInvalid(err):
		s.writeError(w, http.StatusUnprocessableEntity, "Kubernetes rejected the workflow stop: "+err.Error())
	case apierrors.IsBadRequest(err):
		s.writeError(w, http.StatusBadRequest, "Kubernetes rejected the workflow stop: "+err.Error())
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		s.writeError(w, http.StatusGatewayTimeout, "Kubernetes timed out; check the workflow before retrying")
	case step == "read":
		log.Printf("[workflows] Failed to read workflow %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the workflow")
	default:
		log.Printf("[workflows] Failed to stop workflow %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "could not stop the workflow")
	}
}
