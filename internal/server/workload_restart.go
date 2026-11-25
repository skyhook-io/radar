package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kjson "sigs.k8s.io/json"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/rollouts"
	"github.com/skyhook-io/radar/pkg/workloadrestart"
)

const (
	errCodeWorkloadChanged      = "workload_changed"
	errCodeWorkloadTerminating  = "workload_terminating"
	errCodeWorkloadNotFound     = "workload_not_found"
	errCodeRestartNotApplicable = "restart_not_applicable"
	errCodeRestartNotStored     = "restart_not_stored"
)

func parseRestartPreconditions(body []byte) (workloadrestart.Preconditions, error) {
	var fields struct {
		UID             types.UID       `json:"uid"`
		ResourceVersion json.RawMessage `json:"resourceVersion"`
	}
	strictErrors, err := kjson.UnmarshalStrict(body, &fields)
	if err != nil {
		return workloadrestart.Preconditions{}, err
	}
	if len(strictErrors) > 0 {
		return workloadrestart.Preconditions{}, errors.Join(strictErrors...)
	}
	if strings.TrimSpace(string(fields.UID)) == "" {
		return workloadrestart.Preconditions{}, errors.New("uid is required")
	}
	review := workloadrestart.Preconditions{UID: fields.UID}
	if fields.ResourceVersion != nil {
		if err := json.Unmarshal(fields.ResourceVersion, &review.ResourceVersion); err != nil || review.ResourceVersion == "" {
			return workloadrestart.Preconditions{}, errors.New("resourceVersion must be a nonempty string")
		}
	}
	return review, nil
}

// handleRestartWorkload restarts a Deployment, StatefulSet, DaemonSet or Argo
// Rollout, reading and patching as the caller (as Radar on an unauthenticated
// listener, behind requireSameOrigin). Every 200 carries message, restartedAt
// and changed.
//
// An empty body keeps the unconditional restart. A body ({uid, resourceVersion?},
// advertised as workloadRestartPreconditions; an older Radar ignores it, so
// callers check the flag first) restarts only the reviewed object: each patch is
// pinned to the UID and to the version Radar just read. Without resourceVersion,
// a conflict whose version moved is retried up to three times, so status churn
// doesn't refuse the restart; with it, any change is final. A missing or blank
// uid, a null or empty version, or an unknown or repeated field is 400 before any
// Kubernetes call, and a body over 1 KiB is 413.
//
// Reviewed refusals: 404 workload_not_found; 409 workload_changed (UID or pinned
// version changed, or every retry conflicted), workload_terminating,
// workload_paused, restart_not_applicable (OnDelete or a positive StatefulSet
// partition: the marker would not restart every pod), and restart_not_stored
// (admission rewrote or removed the marker; restartedAt is what was stored).
// changed:false means an equivalent marker, or a Rollout restartAt in the same
// second or later, already covers the request.
func (s *Server) handleRestartWorkload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		}
		return
	}
	var review workloadrestart.Preconditions
	if len(body) > 0 {
		review, err = parseRestartPreconditions(body)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	normalizedKind := k8score.NormalizeWorkloadKind(strings.ToLower(kind))
	switch normalizedKind {
	case "deployments", "statefulsets", "daemonsets", "rollouts":
	default:
		s.writeError(w, http.StatusBadRequest, k8score.ErrUnsupportedRestartKind.Error())
		return
	}
	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		const message = "cluster client not available — check cluster connection"
		if review.UID != "" {
			s.writeErrorCode(w, http.StatusServiceUnavailable, errCodeClusterUnavailable, message)
		} else {
			s.writeError(w, http.StatusServiceUnavailable, message)
		}
		return
	}
	var result workloadrestart.Result
	if review.UID != "" {
		result, err = k8score.NewWorkloadManager(client, nil).RestartReviewedWorkload(r.Context(), kind, namespace, name, review)
		if err != nil {
			s.writeReviewedRestartError(w, kind, namespace, name, result, err)
			return
		}
	} else {
		result, err = k8s.RestartWorkloadWithClient(r.Context(), kind, namespace, name, client)
		if err != nil {
			s.writeUnconditionalRestartError(w, normalizedKind, namespace, name, err)
			return
		}
	}
	s.writeJSON(w, struct {
		Message string `json:"message"`
		workloadrestart.Result
	}{"Workload restart initiated", result})
}

func (s *Server) writeUnconditionalRestartError(w http.ResponseWriter, normalizedKind, namespace, name string, err error) {
	// Restart reaches a terminating Rollout through get(), so it carries the
	// same sentinels rollback does.
	if normalizedKind == "rollouts" {
		s.writeRolloutError(w, err, "restart", namespace, name)
		return
	}
	if errors.Is(err, k8score.ErrRestartPaused) {
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadPaused, err.Error())
		return
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if apierrors.IsForbidden(err) {
		s.writeError(w, http.StatusForbidden, err.Error())
		return
	}
	s.writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) writeReviewedRestartError(w http.ResponseWriter, kind, namespace, name string, result workloadrestart.Result, err error) {
	var status apierrors.APIStatus
	switch {
	case errors.Is(err, workloadrestart.ErrChanged):
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadChanged, err.Error())
	case errors.Is(err, workloadrestart.ErrNotStored):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		s.writeJSON(w, map[string]string{"error": err.Error(), "error_code": errCodeRestartNotStored, "restartedAt": result.RestartedAt})
	case errors.Is(err, k8score.ErrRestartPaused):
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadPaused, err.Error())
	case errors.Is(err, k8score.ErrRestartNotApplicable):
		s.writeErrorCode(w, http.StatusConflict, errCodeRestartNotApplicable, err.Error())
	case errors.Is(err, k8score.ErrWorkloadTerminating), errors.Is(err, rollouts.ErrResourceTerminating):
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadTerminating, err.Error())
	case apierrors.IsNotFound(err):
		s.writeErrorCode(w, http.StatusNotFound, errCodeWorkloadNotFound, "the reviewed workload was not found")
	case errors.As(err, &status) && status.Status().Code >= 400 && status.Status().Code < 500:
		s.writeError(w, int(status.Status().Code), err.Error())
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		s.writeError(w, http.StatusGatewayTimeout, "Kubernetes timed out; check the workload before retrying")
	default:
		log.Printf("[workloads] Failed to restart %s %s/%s: %v", sanitizeForLog(kind), sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
}
