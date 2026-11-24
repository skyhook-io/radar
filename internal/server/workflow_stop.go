package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
)

var argoWorkflowGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}

// Annotations Radar sets on a Workflow it stops. stopped-by is the
// authenticated caller (absent when Radar runs without authentication).
const (
	workflowStoppedByAnnotation = "radar.skyhook.io/stopped-by"
	workflowStoppedAtAnnotation = "radar.skyhook.io/stopped-at"
)

// WorkflowStopRequest picks how to stop. Terminate (the default) ends the
// run now, skipping exit handlers; Stop ends it after running them, as
// `argo terminate` and `argo stop` do.
type WorkflowStopRequest struct {
	Strategy *string `json:"strategy"`
	// ResourceVersion binds the stop to a Workflow a caller already checked.
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// WorkflowStopResponse is the Workflow's state after the request.
type WorkflowStopResponse struct {
	// Shutdown is the resulting value of spec.shutdown.
	Shutdown string `json:"shutdown"`
	// Changed reports whether this request changed spec.shutdown.
	Changed bool `json:"changed"`
}

// workflowFinishedPhases have nothing left to stop.
var workflowFinishedPhases = map[string]bool{"Succeeded": true, "Failed": true, "Error": true}

// handleWorkflowStop stops an Argo Workflow by setting spec.shutdown, which
// the workflow controller acts on.
//
//	POST /api/workflows/{namespace}/{name}/stop  {"strategy": "Terminate"|"Stop", "resourceVersion": "..."}
//
// The read and the merge patch run as the caller, so the apiserver decides
// (`get` and `patch workflows`); without authentication that is Radar's own
// identity, like every other workload action. The patch carries the
// resourceVersion that was read, so a Workflow that finishes or changes in
// between is refused (409), not overwritten. Radar records who stopped it and
// when; the caller can't supply annotations. A finished Workflow is 409.
// Terminate can escalate Stop, never the reverse. Setting shutdown asks the
// Workflow controller to stop asynchronously. Callers that checked the Workflow
// before calling can supply its resourceVersion as an additional precondition.
func (s *Server) handleWorkflowStop(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	req := &WorkflowStopRequest{Strategy: ptr.To("Terminate")}
	if err := decodeBoundedJSONBody(w, r, 1<<10, &req); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req == nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body: the body must be a JSON object")
		return
	}
	strategy := ptr.Deref(req.Strategy, "")
	if strategy != "Terminate" && strategy != "Stop" {
		s.writeError(w, http.StatusBadRequest, "strategy must be Terminate or Stop")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	wf, err := client.Resource(argoWorkflowGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeWorkflowStopError(w, err, namespace, name, "read")
		return
	}
	if req.ResourceVersion != "" && req.ResourceVersion != wf.GetResourceVersion() {
		s.writeErrorCode(w, http.StatusConflict, "workflow_changed", "the workflow changed before stopping it; look again")
		return
	}
	if phase, _, _ := unstructured.NestedString(wf.Object, "status", "phase"); workflowFinishedPhases[phase] {
		s.writeErrorCode(w, http.StatusConflict, "workflow_finished", "the workflow has already finished ("+phase+")")
		return
	}
	if current, _, _ := unstructured.NestedString(wf.Object, "spec", "shutdown"); current == "Terminate" || (current == "Stop" && strategy == "Stop") {
		s.writeJSON(w, WorkflowStopResponse{Shutdown: current})
		return
	}
	annotations := map[string]any{
		workflowStoppedAtAnnotation: time.Now().UTC().Format(time.RFC3339),
		workflowStoppedByAnnotation: nil,
	}
	if u := auth.UserFromContext(r.Context()); u != nil && u.Username != "" {
		annotations[workflowStoppedByAnnotation] = u.Username
	}
	updated, err := integration.ApplyVersionedMergePatch(r.Context(), client, argoWorkflowGVR, wf, map[string]any{
		"metadata": map[string]any{"annotations": annotations},
		"spec":     map[string]any{"shutdown": strategy},
	})
	if err != nil {
		s.writeWorkflowStopError(w, err, namespace, name, "patch")
		return
	}
	shutdown, _, _ := unstructured.NestedString(updated.Object, "spec", "shutdown")
	if shutdown != strategy {
		s.writeError(w, http.StatusUnprocessableEntity, "Kubernetes did not store the requested workflow shutdown strategy")
		return
	}
	s.writeJSON(w, WorkflowStopResponse{Shutdown: shutdown, Changed: true})
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
