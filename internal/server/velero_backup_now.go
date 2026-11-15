package server

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

var (
	veleroScheduleGVR = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "schedules"}
	veleroBackupGVR   = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "backups"}
)

// VeleroBackupNowRequest binds the schedule the caller reviewed.
type VeleroBackupNowRequest struct {
	// ResourceVersion of the Schedule shown in the confirmation. A schedule
	// edited since is refused, so the backup is the one that was reviewed.
	ResourceVersion string `json:"resourceVersion"`
}

// VeleroBackupNowResponse names the Backup created.
type VeleroBackupNowResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// handleVeleroBackupNow creates a Backup from a Schedule's template, as
// `velero backup create --from-schedule` does.
//
//	POST /api/velero/schedules/{namespace}/{name}/backup   {"resourceVersion": "..."}
//
// Read and create both run as the caller, so the apiserver decides: reading
// the Schedule needs `get schedules`, creating the run needs `create backups`.
// It is one of the schedule's runs (it carries the schedule-name label), so
// Velero holds the schedule's next run while it is in progress.
func (s *Server) handleVeleroBackupNow(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	var req VeleroBackupNowRequest
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
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, "schedule not found")
		return
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to this Velero schedule")
		return
	default:
		log.Printf("[velero] Failed to read schedule %s/%s for a backup: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the schedule")
		return
	}
	if schedule.GetResourceVersion() != req.ResourceVersion {
		s.writeError(w, http.StatusConflict, "the schedule changed since you opened it; review it again before backing up")
		return
	}

	backup, err := veleropkg.BackupFromSchedule(schedule, time.Now())
	if errors.Is(err, veleropkg.ErrScheduleCannotRun) {
		s.writeError(w, http.StatusConflict, "this schedule's name is longer than 63 characters, so Velero cannot label a backup from it and the backup would fail")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}

	created, err := client.Resource(veleroBackupGVR).Namespace(namespace).Create(r.Context(), backup, metav1.CreateOptions{})
	switch {
	case err == nil:
	case apierrors.IsAlreadyExists(err):
		s.writeError(w, http.StatusConflict, "a backup from this schedule was created this second; wait a moment and try again")
		return
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to create Velero backups in namespace "+namespace)
		return
	default:
		log.Printf("[velero] Failed to create a backup from schedule %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "could not create the backup")
		return
	}
	s.writeJSON(w, VeleroBackupNowResponse{Namespace: created.GetNamespace(), Name: created.GetName()})
}
