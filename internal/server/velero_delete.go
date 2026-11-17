package server

import (
	"context"
	"errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"log"
	"net/http"
	"time"

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
)

// VeleroDeleteBackupRequest binds the deletion to the backup the caller
// reviewed.
type VeleroDeleteBackupRequest struct {
	// UID of the Backup shown in the confirmation. A backup recreated under the
	// same name since is refused.
	UID string `json:"uid"`
}

// VeleroDeleteBackupResponse names the request Velero is working from.
type VeleroDeleteBackupResponse struct {
	Request string `json:"request"`
}

// VeleroDeletionResponse is where one deletion stands.
type VeleroDeletionResponse struct {
	veleropkg.DeletionReading
	Request string `json:"request,omitempty"`
	// Created is when the request was made, so a settled one can say how long
	// ago (Velero keeps processed requests for a day).
	Created *time.Time `json:"created,omitempty"`
	// ServerStopped: the Velero server is not running, so a waiting request
	// stays waiting until it starts.
	ServerStopped bool `json:"serverStopped,omitempty"`
}

// handleVeleroDeleteBackup asks Velero to delete a backup.
//
//	POST /api/velero/backups/{namespace}/{name}/delete   {"uid": "..."}
//
// Read and create run as the caller, so the apiserver decides: reading the
// Backup needs `get backups`, following needs `list deletebackuprequests`, and submission needs `create deletebackuprequests`.
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
	if req.UID == "" {
		s.writeError(w, http.StatusBadRequest, "uid of the reviewed backup is required")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	requests := client.Resource(veleroDeleteRequestGVR).Namespace(namespace)
	existing, err := newestVeleroDeletion(r.Context(), requests, name, req.UID, true)
	if err != nil {
		s.veleroDeletionLookupError(w, namespace, name, err)
		return
	}
	if existing != nil {
		s.writeJSON(w, VeleroDeleteBackupResponse{Request: existing.GetName()})
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

	if deployments, read := s.veleroDeployments(r, k8s.GetResourceCache(), namespace); read && veleropkg.ServerConfigFromDeployments(deployments, true).Down() {
		s.writeError(w, http.StatusConflict, "The Velero server is stopped; start it before deleting a backup.")
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
		veleropkg.DeleteBackupRequestFor(namespace, name, req.UID), metav1.CreateOptions{})
	switch {
	case err == nil:
		s.writeJSON(w, VeleroDeleteBackupResponse{Request: created.GetName()})
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
//	GET /api/velero/backups/{namespace}/{name}/deletion?uid=<backup uid>
//
// Read as the caller. The request and the backup are both read: Velero deletes
// a successful request along with the backup, so the request alone can't say
// how a deletion ended.
func (s *Server) handleVeleroBackupDeletion(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		s.writeError(w, http.StatusBadRequest, "uid is required")
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

	request, err := newestVeleroDeletion(r.Context(), client.Resource(veleroDeleteRequestGVR).Namespace(namespace), name, uid, false)
	if err != nil {
		s.veleroDeletionLookupError(w, namespace, name, err)
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
	if request != nil {
		resp.Request = request.GetName()
		if created := request.GetCreationTimestamp(); !created.IsZero() {
			at := created.UTC()
			resp.Created = &at
		}
	}
	if resp.State == veleropkg.DeletionWaiting {
		if deployments, read := s.veleroDeployments(r, k8s.GetResourceCache(), namespace); read {
			resp.ServerStopped = veleropkg.ServerConfigFromDeployments(deployments, true).Down()
		}
	}
	s.writeJSON(w, resp)
}

// openOnly lets a retry reuse an outstanding request even if a newer processed
// request is still retained. All reads use the caller's namespace client.
func newestVeleroDeletion(ctx context.Context, requests dynamic.ResourceInterface, name, uid string, openOnly bool) (*unstructured.Unstructured, error) {
	selector := labels.Set{veleropkg.BackupNameLabel: veleropkg.ValidLabelValue(name), veleropkg.BackupUIDLabel: uid}.AsSelector().String()
	list, err := requests.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	var newest *unstructured.Unstructured
	for i := range list.Items {
		request := &list.Items[i]
		phase, _, _ := unstructured.NestedString(request.Object, "status", "phase")
		if openOnly && phase == "Processed" {
			continue
		}
		if newest == nil || request.GetCreationTimestamp().After(newest.GetCreationTimestamp().Time) || (request.GetCreationTimestamp().Time.Equal(newest.GetCreationTimestamp().Time) && request.GetName() > newest.GetName()) {
			newest = request
		}
	}
	return newest, nil
}

func (s *Server) veleroDeletionLookupError(w http.ResponseWriter, namespace, name string, err error) {
	if apierrors.IsForbidden(err) {
		s.writeError(w, http.StatusForbidden, "no access to list Velero delete requests")
		return
	}
	log.Printf("[velero] Failed to list DeleteBackupRequests for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
	s.writeError(w, http.StatusServiceUnavailable, "could not read Velero delete requests")
}
