package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
)

// Restore follow-through and the restore-validation record. The recovery
// snapshot is what a person watching a new Cluster bootstrap from backups
// needs: the Cluster's phase, the Pods doing the recovery (the full-recovery
// Job's Pod and its init containers, then the instances), their Jobs and the
// Warning events about them. Every read uses the caller's identity; a read the
// caller may not make is reported as coverage, never as "nothing there".

func (s *Server) handleCNPGClusterRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	dyn, typed := reader.dynamic, reader.Clients.Typed
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	cluster, err := dyn.Resource(cnpgsvc.ClusterGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeCNPGReadError(w, err, cnpgsvc.GrantGetCluster, namespace, name)
		return
	}
	resp := reader.RecoverySnapshot(r.Context(), typed, cluster)
	s.writeJSON(w, resp)
}

func (s *Server) writeCNPGReadError(w http.ResponseWriter, err error, g Grant, namespace, name string) {
	switch {
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("CloudNativePG Cluster %s/%s not found", namespace, name))
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "This needs "+g.In(namespace).String())
	default:
		log.Printf("[cnpg] Failed to read Cluster %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "failed to read CloudNativePG Cluster: "+err.Error())
	}
}

// handleCNPGRestoreValidation serves POST /api/cnpg/clusters/{ns}/{name}/restore-validation:
// records the caller's validation note on the restored Cluster as an
// annotation, with an impersonated merge patch bound to the reviewed context
// and Cluster UID.
func (s *Server) handleCNPGRestoreValidation(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	req, dyn, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	auth.AuditLog(r, namespace, name)
	if s.grantPermission(r, cnpgsvc.GrantPatchClusters.In(namespace)) == integration.PermissionDenied {
		s.writeError(w, http.StatusForbidden, "Recording a validation note needs "+cnpgsvc.GrantPatchClusters.In(namespace).String())
		return
	}
	recordedBy := ""
	if user := auth.UserFromContext(r.Context()); user != nil {
		recordedBy = user.Username
	}
	note, err := cnpgsvc.RecordCNPGRestoreValidation(r.Context(), dyn, namespace, name, req, recordedBy, time.Now())
	if err != nil {
		s.writeCNPGActionError(w, err, "restore-validation", namespace, name)
		return
	}
	log.Printf("[cnpg] restore validation recorded on Cluster %s/%s", sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, note)
}

// handleCNPGRestoreCapability answers whether the caller may create the
// restored Cluster in a namespace: create clusters there, refused like other
// webhook-bound writes while the operator's webhook rejects them. The restore
// dialog asks it whichever way it was opened (Cluster, Backup, ObjectStore).
func (s *Server) handleCNPGRestoreCapability(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if errs := validation.IsDNS1123Label(namespace); namespace == "" || len(errs) > 0 {
		s.writeError(w, http.StatusBadRequest, "namespace must be a valid namespace name")
		return
	}
	s.writeJSON(w, s.cnpgReader(r).RestoreCapability(r.Context(), namespace))
}
