package server

import (
	"errors"
	"log"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

// Deleting a Backup object does not delete a backup. The files stay in the
// bucket, the volume snapshots stay with the provider, and Velero's backup
// sync recreates the object from the bucket. Velero deletes a backup when asked
// through a DeleteBackupRequest, as `velero backup delete` does, so that is the
// only delete Radar offers for one.

var (
	veleroDeleteRequestGVR = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "deletebackuprequests"}
	veleroLocationGVR      = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "backupstoragelocations"}
	// A confirmation's ID, chosen by the browser when the dialog opens.
	veleroSubmissionID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
)

// VeleroDeleteBackupRequest binds the deletion to the backup the caller
// reviewed and to one confirmation.
type VeleroDeleteBackupRequest struct {
	// UID of the Backup shown in the confirmation. A backup recreated under the
	// same name since is refused.
	UID string `json:"uid"`
	// Submission identifies the confirmation. Sending it again after a lost
	// answer returns the request it already created.
	Submission string `json:"submission"`
}

// VeleroDeleteBackupResponse names the request Velero is working from.
type VeleroDeleteBackupResponse struct {
	Request string `json:"request"`
	UID     string `json:"uid"`
}

// VeleroDeletionResponse is where one deletion stands.
type VeleroDeletionResponse struct {
	veleropkg.DeletionReading
	// ServerStopped: the Velero server is not running, so a waiting request
	// stays waiting until it starts.
	ServerStopped bool `json:"serverStopped,omitempty"`
}

// handleVeleroDeleteBackup asks Velero to delete a backup.
//
//	POST /api/velero/backups/{namespace}/{name}/delete   {"uid": "...", "submission": "..."}
//
// Read and create run as the caller, so the apiserver decides: reading the
// Backup needs `get backups`, the request `create deletebackuprequests`.
// Refused up front when Velero would refuse: a backup still running, or a
// storage location that is read-only or unavailable.
func (s *Server) handleVeleroDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
	var req VeleroDeleteBackupRequest
	if err := decodeBoundedJSONBody(w, r, 4<<10, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.UID == "" || !veleroSubmissionID.MatchString(req.Submission) {
		s.writeError(w, http.StatusBadRequest, "uid of the reviewed backup and a submission ID are required")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	requests := client.Resource(veleroDeleteRequestGVR).Namespace(namespace)
	requestName := veleropkg.DeleteBackupRequestName(name, req.Submission)

	// The same confirmation sent again: its request exists, or Velero already
	// deleted it along with the backup. Either way it is not sent twice.
	if existing, err := requests.Get(r.Context(), requestName, metav1.GetOptions{}); err == nil {
		s.writeJSON(w, VeleroDeleteBackupResponse{Request: existing.GetName(), UID: req.UID})
		return
	}

	backup, err := client.Resource(veleroBackupGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		// Also the answer to a confirmation resent after Velero finished: it
		// removes a successful request together with the backup.
		s.writeError(w, http.StatusNotFound, "backup not found; it may already have been deleted")
		return
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to this Velero backup")
		return
	default:
		log.Printf("[velero] Failed to read backup %s/%s for deletion: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the backup")
		return
	}
	if string(backup.GetUID()) != req.UID {
		s.writeError(w, http.StatusConflict, "this is not the backup you reviewed: it was recreated under the same name. Review it again before deleting")
		return
	}

	// The location is read for the refusal only; a caller who can't read it
	// still gets Velero's own answer once it processes the request.
	var location *unstructured.Unstructured
	if loc, _ := veleropkg.LocationOf(backup); loc != "" {
		if got, getErr := client.Resource(veleroLocationGVR).Namespace(namespace).Get(r.Context(), loc, metav1.GetOptions{}); getErr == nil {
			location = got
		}
	}
	if refusal := veleropkg.DeletionRefusal(backup, location); refusal != "" {
		s.writeError(w, http.StatusConflict, refusal)
		return
	}

	created, err := requests.Create(r.Context(),
		veleropkg.DeleteBackupRequestFor(namespace, name, req.UID, req.Submission), metav1.CreateOptions{})
	switch {
	case err == nil:
		s.writeJSON(w, VeleroDeleteBackupResponse{Request: created.GetName(), UID: req.UID})
	case apierrors.IsAlreadyExists(err):
		// The same confirmation, sent concurrently.
		s.writeJSON(w, VeleroDeleteBackupResponse{Request: requestName, UID: req.UID})
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to create Velero delete requests")
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, "this cluster has no Velero delete requests")
	default:
		log.Printf("[velero] Failed to create DeleteBackupRequest for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not ask Velero to delete the backup")
	}
}

// handleVeleroBackupDeletion reports where one deletion stands.
//
//	GET /api/velero/backups/{namespace}/{name}/deletion?request=<name>&uid=<backup uid>
//
// Read as the caller. The request and the backup are both read: Velero deletes
// a successful request along with the backup, so the request alone can't say
// how a deletion ended.
func (s *Server) handleVeleroBackupDeletion(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	requestName, uid := r.URL.Query().Get("request"), r.URL.Query().Get("uid")
	if requestName == "" || uid == "" {
		s.writeError(w, http.StatusBadRequest, "request and uid are required")
		return
	}
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	var request *unstructured.Unstructured
	got, err := client.Resource(veleroDeleteRequestGVR).Namespace(namespace).Get(r.Context(), requestName, metav1.GetOptions{})
	switch {
	case err == nil:
		if got.GetLabels()[veleropkg.BackupUIDLabel] != uid || veleropkg.DeleteBackupRequestSpecName(got) != name {
			s.writeError(w, http.StatusConflict, "that request is for another backup")
			return
		}
		request = got
	case apierrors.IsNotFound(err):
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to read Velero delete requests")
		return
	default:
		log.Printf("[velero] Failed to read DeleteBackupRequest %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(requestName), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the delete request")
		return
	}

	exists := false
	backup, err := client.Resource(veleroBackupGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	switch {
	case err == nil:
		exists = string(backup.GetUID()) == uid
	case apierrors.IsNotFound(err):
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to this Velero backup")
		return
	default:
		log.Printf("[velero] Failed to read backup %s/%s for its deletion: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the backup")
		return
	}

	resp := VeleroDeletionResponse{DeletionReading: veleropkg.ReadDeletion(request, exists)}
	if resp.State == veleropkg.DeletionWaiting {
		if deployments, read := s.veleroDeployments(r, k8s.GetResourceCache(), namespace); read {
			resp.ServerStopped = veleropkg.ServerConfigFromDeployments(deployments, true).Down()
		}
	}
	s.writeJSON(w, resp)
}
