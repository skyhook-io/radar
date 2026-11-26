package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/rollouts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// handleTriggerCronJob creates a Job from a CronJob. The optional body
// {"jobName": "..."} names the Job; without it the API server generates
// "<cronjob>-manual-xxxxx" (advertised as features.cronJobTriggerJobName).
func (s *Server) handleTriggerCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// A pointer so a JSON null, which decodes without error, can be refused.
	req := &struct {
		JobName string `json:"jobName"`
	}{}
	if err := decodeBoundedJSONBody(w, r, 4096, &req); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "trigger request is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req == nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body: the body must be a JSON object")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	result, err := k8s.TriggerCronJobWithClient(r.Context(), namespace, name, req.JobName, client)
	if err != nil {
		if errors.Is(err, k8score.ErrInvalidJobName) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if apierrors.IsAlreadyExists(err) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[cronjob] Failed to trigger %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]any{
		"message": "Job created successfully",
		"jobName": result.GetName(),
	})
}

// handleSuspendCronJob suspends a CronJob
func (s *Server) handleSuspendCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.SetCronJobSuspendWithClient(r.Context(), namespace, name, true, client)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"message": "CronJob suspended"})
}

// handleResumeCronJob resumes a suspended CronJob
func (s *Server) handleResumeCronJob(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.SetCronJobSuspendWithClient(r.Context(), namespace, name, false, client)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"message": "CronJob resumed"})
}

// handlePauseDeployment holds a Deployment's pod template changes without rolling them out.
func (s *Server) handlePauseDeployment(w http.ResponseWriter, r *http.Request) {
	s.setDeploymentPaused(w, r, true)
}

// handleResumeDeployment lets a paused Deployment roll out its pod template again.
func (s *Server) handleResumeDeployment(w http.ResponseWriter, r *http.Request) {
	s.setDeploymentPaused(w, r, false)
}

// setDeploymentPaused patches as the caller, so Kubernetes RBAC decides, and it
// returns 403 when the caller can't patch the Deployment. Without authentication
// it acts with Radar's own identity, and requireSameOrigin keeps a page on another
// site from reaching it.
func (s *Server) setDeploymentPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if k8score.NormalizeWorkloadKind(strings.ToLower(kind)) != "deployments" {
		s.writeError(w, http.StatusBadRequest, "only Deployments can be paused or resumed")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	if err := k8s.SetDeploymentPausedWithClient(r.Context(), namespace, name, paused, client); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			s.writeError(w, http.StatusNotFound, err.Error())
		case apierrors.IsForbidden(err):
			s.writeError(w, http.StatusForbidden, err.Error())
		default:
			log.Printf("[workload] Failed to set paused=%t on Deployment %s/%s: %v", paused, namespace, name, err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	message := "Deployment resumed"
	if paused {
		message = "Deployment paused"
	}
	s.writeJSON(w, map[string]string{"message": message})
}

// handleScaleWorkload scales a Deployment or StatefulSet to a new replica count
func (s *Server) handleScaleWorkload(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Parse request body
	var req struct {
		Replicas int32 `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate replica count
	if req.Replicas < 0 {
		s.writeError(w, http.StatusBadRequest, "replicas cannot be negative")
		return
	}
	if req.Replicas > 10000 {
		s.writeError(w, http.StatusBadRequest, "replicas cannot exceed 10000")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.ScaleWorkloadWithClient(r.Context(), kind, namespace, name, req.Replicas, client)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not supported") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[scale] Failed to scale %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]any{
		"message":  "Workload scaled",
		"replicas": req.Replicas,
	})
}

var rollbackableWorkloadKinds = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
	"rollouts":     true,
}

// handleWorkloadRevisions returns the revision history for a Deployment, StatefulSet, DaemonSet, or Rollout
func (s *Server) handleWorkloadRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Validate that this is a rollbackable workload type
	if !rollbackableWorkloadKinds[k8score.NormalizeWorkloadKind(strings.ToLower(kind))] {
		s.writeError(w, http.StatusBadRequest, "revision history only available for Deployments, StatefulSets, DaemonSets, and Rollouts")
		return
	}

	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	revisions, err := k8s.ListWorkloadRevisionsWithClient(r.Context(), kind, namespace, name, client)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[revisions] Failed to list revisions for %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, revisions)
}

// handleRollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a previous revision
func (s *Server) handleRollbackWorkload(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Parse request body
	var req struct {
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate revision
	if req.Revision <= 0 {
		s.writeError(w, http.StatusBadRequest, "revision must be a positive integer")
		return
	}

	// Validate that this is a rollbackable workload type
	normalizedKind := k8score.NormalizeWorkloadKind(strings.ToLower(kind))
	if !rollbackableWorkloadKinds[normalizedKind] {
		s.writeError(w, http.StatusBadRequest, "rollback only available for Deployments, StatefulSets, DaemonSets, and Rollouts")
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	err := k8s.RollbackWorkloadWithClient(r.Context(), kind, namespace, name, req.Revision, client)
	if err != nil {
		// Rollouts carry sentinel errors (unchanged template, unsupported
		// workloadRef, terminating) that the substring checks below can't map.
		if normalizedKind == "rollouts" {
			s.writeRolloutError(w, err, "rollback", namespace, name)
			return
		}
		status := workloadRollbackErrorStatus(err)
		if status == http.StatusInternalServerError {
			log.Printf("[rollback] Failed to rollback %s %s/%s to revision %d: %v", kind, namespace, name, req.Revision, err)
		}
		s.writeWorkloadRollbackError(w, status, err)
		return
	}

	s.writeJSON(w, map[string]string{
		"message": fmt.Sprintf("Rollback to revision %d initiated", req.Revision),
	})
}

// errCodeWorkloadPaused lets the UI point at its own Resume control instead of
// showing the server's text, which is written for API and agent callers.
const errCodeWorkloadPaused = "workload_paused"

func (s *Server) writeWorkloadRollbackError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, k8score.ErrWorkloadPaused) {
		s.writeErrorCode(w, status, errCodeWorkloadPaused, err.Error())
		return
	}
	s.writeError(w, status, err.Error())
}

// workloadRollbackErrorStatus maps a Deployment/StatefulSet/DaemonSet rollback
// error to its HTTP status. 409 covers the refusals the caller can act on: the
// workload changed mid-rollback, already runs the revision, or is paused.
func workloadRollbackErrorStatus(err error) int {
	switch {
	case apierrors.IsNotFound(err), errors.Is(err, k8score.ErrResourceNotFound):
		return http.StatusNotFound
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsConflict(err), errors.Is(err, rollouts.ErrTemplateUnchanged), errors.Is(err, k8score.ErrWorkloadPaused):
		return http.StatusConflict
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "not supported"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
