package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
)

// VeleroSchedulePauseRequest binds the schedule the caller reviewed.
type VeleroSchedulePauseRequest struct {
	// ResourceVersion of the Schedule shown in the confirmation; a schedule
	// edited since is refused.
	ResourceVersion string `json:"resourceVersion"`
	// SkipImmediately is written to spec.skipImmediately: true skips the run
	// due when the schedule resumes, false runs it, and null (or absent) leaves
	// it to the Velero server's --schedule-skip-immediately, as `velero
	// schedule pause|unpause` does without --skip-immediately.
	SkipImmediately *bool `json:"skipImmediately"`
}

// VeleroSchedulePauseResponse is the schedule's state after the request.
type VeleroSchedulePauseResponse struct {
	Paused bool `json:"paused"`
	// Changed is false when the schedule was already in that state, which
	// `velero schedule pause` also leaves alone.
	Changed bool `json:"changed"`
}

// handleVeleroSchedulePause pauses or resumes a Schedule, as `velero schedule
// pause` and `velero schedule unpause` do: it sets spec.paused and writes
// spec.skipImmediately.
//
//	POST /api/velero/schedules/{namespace}/{name}/pause    {"resourceVersion": "...", "skipImmediately": null}
//	POST /api/velero/schedules/{namespace}/{name}/resume
//
// Both the read and the patch run as the caller, so the apiserver decides
// (`get schedules`, `patch schedules`), and the patch carries the reviewed
// resourceVersion, so an edit landing in between is refused, not overwritten.
func (s *Server) handleVeleroSchedulePause(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireConnected(w) {
			return
		}
		namespace := chi.URLParam(r, "namespace")
		name := chi.URLParam(r, "name")
		if s.refuseOutsideVeleroPin(w, namespace) {
			return
		}
		var req VeleroSchedulePauseRequest
		if err := decodeBoundedJSONBody(w, r, 4<<10, &req); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
				return
			}
			s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.ResourceVersion == "" {
			s.writeError(w, http.StatusBadRequest, "resourceVersion of the reviewed schedule is required")
			return
		}

		auth.AuditLog(r, namespace, name)
		client := s.getDynamicClientForRequest(r)
		if client == nil {
			s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
			return
		}
		schedule, err := client.Resource(veleroScheduleGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			s.writeSchedulePauseError(w, err, namespace, name, "read")
			return
		}
		// Already in that state (a confirmation sent again after its answer was
		// lost, or another user got there first): the CLI leaves it alone too.
		if current, _, _ := unstructured.NestedBool(schedule.Object, "spec", "paused"); current == paused {
			s.writeJSON(w, VeleroSchedulePauseResponse{Paused: paused})
			return
		}
		if schedule.GetResourceVersion() != req.ResourceVersion {
			s.writeError(w, http.StatusConflict, "the schedule changed since you opened it; review it again")
			return
		}
		var skip any
		if req.SkipImmediately != nil {
			skip = *req.SkipImmediately
		}
		err = integration.MergePatchAtVersion(r.Context(), client, veleroScheduleGVR, schedule, map[string]any{
			"spec": map[string]any{"paused": paused, "skipImmediately": skip},
		})
		if err != nil {
			s.writeSchedulePauseError(w, err, namespace, name, "patch")
			return
		}
		s.writeJSON(w, VeleroSchedulePauseResponse{Paused: paused, Changed: true})
	}
}

func (s *Server) writeSchedulePauseError(w http.ResponseWriter, err error, namespace, name, step string) {
	switch {
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, "schedule not found")
	case apierrors.IsForbidden(err) && step == "read":
		s.writeError(w, http.StatusForbidden, "no access to this Velero schedule")
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to change this Velero schedule")
	case apierrors.IsConflict(err):
		s.writeError(w, http.StatusConflict, "the schedule changed since you opened it; review it again")
	default:
		log.Printf("[velero] Failed to %s schedule %s/%s: %v", step, sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not change the schedule")
	}
}
