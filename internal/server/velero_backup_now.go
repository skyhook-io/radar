package server

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

const veleroSubmissionAnnotation = "radar.skyhook.io/submission"

var (
	veleroSubmissionID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	veleroScheduleGVR  = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "schedules"}
	veleroBackupGVR    = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "backups"}
)

// VeleroBackupNowRequest binds the schedule the caller reviewed.
type VeleroBackupNowRequest struct {
	// ResourceVersion of the Schedule shown in the confirmation. A schedule
	// edited since is refused, so the backup is the one that was reviewed.
	ResourceVersion string `json:"resourceVersion"`
	Submission      string `json:"submission"`
	// Name the confirmation chose for the backup, <schedule>-<14 digits>.
	// Repeating a request whose answer was lost returns the backup it already
	// created instead of starting another. Optional; the server names it
	// otherwise.
	Name string `json:"name,omitempty"`
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
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
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
	if !veleroSubmissionID.MatchString(req.Submission) {
		s.writeError(w, http.StatusBadRequest, "submission must be 8–64 letters, digits or hyphens")
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
	// The same confirmation sent again after its answer was lost: the backup
	// it asked for exists, so say so, even if the schedule has changed since.
	if req.Name != "" && backupNowName(name).MatchString(req.Name) {
		if existing, getErr := client.Resource(veleroBackupGVR).Namespace(namespace).Get(r.Context(), req.Name, metav1.GetOptions{}); getErr == nil {
			if existing.GetAnnotations()[veleroSubmissionAnnotation] != req.Submission {
				s.writeError(w, http.StatusConflict, "another backup was created with this name; review again")
				return
			}
			s.writeJSON(w, VeleroBackupNowResponse{Namespace: existing.GetNamespace(), Name: existing.GetName()})
			return
		}
	}
	if deployments, read := s.veleroDeployments(r, k8s.GetResourceCache(), namespace); read {
		server := veleropkg.ServerConfigFromDeployments(deployments, true)
		if server.Down() {
			s.writeError(w, http.StatusConflict, "The Velero server is stopped; start it before backing up.")
			return
		}
		if server.FlagsKnown() && server.BackupsDisabled {
			s.writeError(w, http.StatusConflict, "The Velero backup controller is disabled (--restore-only or --disable-controllers=backup); enable it before backing up.")
			return
		}
	}
	if schedule.GetResourceVersion() != req.ResourceVersion {
		s.writeError(w, http.StatusConflict, "the schedule changed since you opened it; review it again before backing up")
		return
	}

	if req.Name != "" && !backupNowName(name).MatchString(req.Name) {
		s.writeError(w, http.StatusBadRequest, "name must be the schedule's name followed by a 14-digit timestamp")
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

	if req.Name != "" {
		backup.SetName(req.Name)
	}

	annotations := backup.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[veleroSubmissionAnnotation] = req.Submission
	backup.SetAnnotations(annotations)

	created, err := client.Resource(veleroBackupGVR).Namespace(namespace).Create(r.Context(), backup, metav1.CreateOptions{})
	switch {
	case err == nil:
	case apierrors.IsAlreadyExists(err):
		// The same confirmation sent again (its answer was lost): the backup
		// it asked for exists, so say so rather than start a second one.
		if backup.GetName() != "" {
			if existing, getErr := client.Resource(veleroBackupGVR).Namespace(namespace).Get(r.Context(), backup.GetName(), metav1.GetOptions{}); getErr == nil &&
				existing.GetAnnotations()[veleroSubmissionAnnotation] == req.Submission {
				s.writeJSON(w, VeleroBackupNowResponse{Namespace: existing.GetNamespace(), Name: existing.GetName()})
				return
			}
		}
		s.writeError(w, http.StatusConflict, "another backup was created with this name; review again")
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

// backupNowName matches the names a confirmation may choose for a schedule's
// backup: what Velero's CLI names it, <schedule>-<UTC timestamp>.
func backupNowName(schedule string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(schedule) + `-\d{14}$`)
}
