package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

// `patch rollouts` does not imply `patch rollouts/status`, so the status verbs
// are probed separately — otherwise a button appears and then 403s on click.
type RolloutCapabilities struct {
	Abort       bool `json:"abort"`
	Retry       bool `json:"retry"`
	Promote     bool `json:"promote"`
	PromoteFull bool `json:"promoteFull"`
	SkipStep    bool `json:"skipStep"`
	Rollback    bool `json:"rollback"`
	Restart     bool `json:"restart"`
	SetImage    bool `json:"setImage"`
	// Strategy lets the UI hide step-relative actions on blueGreen Rollouts.
	Strategy string `json:"strategy"`
	// Terminating suppresses every action; the Rollout is being deleted.
	Terminating bool `json:"terminating"`
}

type rolloutOperation func(context.Context, dynamic.Interface, string, string) (rollouts.OperationResult, error)

var rolloutOperations = map[string]rolloutOperation{
	"abort":        rollouts.Abort,
	"retry":        rollouts.Retry,
	"promote":      rollouts.Promote,
	"promote-full": rollouts.PromoteFull,
	"skip-step":    rollouts.SkipCurrentStep,
}

// Rollback is served by /workloads/rollouts/... instead — same operation shape as
// a Deployment rollback, and it reuses the shared revision-history UI.
func (s *Server) handleRolloutOperation(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	action := chi.URLParam(r, "action")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	op, ok := rolloutOperations[action]
	if !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown rollout action %q: must be abort, retry, promote, promote-full, or skip-step", action))
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[rollouts] Dynamic client unavailable for %q Rollout %s/%s", action, sanitizeForLog(namespace), sanitizeForLog(name))
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	result, err := op(r.Context(), client, namespace, name)
	if err != nil {
		s.writeRolloutError(w, err, action, namespace, name)
		return
	}

	s.writeJSON(w, result)
}

// canReadWorkloadRefSource reports whether the caller may read the object a workloadRef
// Rollout keeps its pod template on. Rollouts that carry their own template need nothing.
func (s *Server) canReadWorkloadRefSource(r *http.Request, ro *unstructured.Unstructured, namespace string) bool {
	if _, _, ok := rollouts.WorkloadRef(ro); !ok {
		return true
	}
	target, err := rollouts.ResolveTemplateTarget(ro)
	if err != nil {
		// Nothing readable to gate on; promote-full fails on its own terms instead.
		return true
	}
	return s.canRead(r, target.GVR.Group, target.GVR.Resource, namespace, "get")
}

// A workloadRef undo lands on the referenced workload, not the Rollout.
func rollbackAuthTarget(ro *unstructured.Unstructured) (group, resource string, supported bool) {
	target, err := rollouts.ResolveTemplateTarget(ro)
	if err != nil {
		return "", "", false
	}
	return target.GVR.Group, target.GVR.Resource, true
}

// handleRolloutCapabilities reports which actions the caller can perform.
func (s *Server) handleRolloutCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	ro, err := client.Resource(rollouts.GVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("Rollout %s/%s not found", namespace, name))
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[rollouts] Failed to get Rollout %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	const group = "argoproj.io"
	canPatch := s.canRead(r, group, "rollouts", namespace, "patch")
	canPatchStatus := s.canReadSubresource(r, group, "rollouts", "status", namespace, "patch")
	strategy := rollouts.StrategyOf(ro)
	terminating := !ro.GetDeletionTimestamp().IsZero()

	statusVerb := canPatchStatus && !terminating
	mainVerb := canPatch && !terminating
	stepVerb := statusVerb && strategy == rollouts.StrategyCanary

	// Promoting a workloadRef Rollout reads the referenced workload to tell whether the
	// controller has caught up, so offering the action without that read hands the user
	// a button that fails.
	promoteFullVerb := statusVerb && s.canReadWorkloadRefSource(r, ro, namespace)

	rbGroup, rbResource, rbSupported := rollbackAuthTarget(ro)
	rollbackVerb := rbSupported && !terminating &&
		s.canRead(r, rbGroup, rbResource, namespace, "patch")
	imageGroup, imageResource, imageNeedsGet, imageSupported := k8score.WorkloadImageTargetForRollout(ro)
	setImageVerb := imageSupported && !terminating &&
		s.canRead(r, imageGroup, imageResource, namespace, "patch") &&
		(!imageNeedsGet || s.canRead(r, imageGroup, imageResource, namespace, "get"))

	s.writeJSON(w, RolloutCapabilities{
		Abort:       statusVerb,
		Retry:       statusVerb,
		Promote:     statusVerb,
		PromoteFull: promoteFullVerb,
		SkipStep:    stepVerb,
		Rollback:    rollbackVerb,
		Restart:     mainVerb,
		SetImage:    setImageVerb,
		Strategy:    string(strategy),
		Terminating: terminating,
	})
}

// handleRolloutAnalysisRuns returns the Rollout's full AnalysisRun history —
// not just the 4 "currently active" slots status itself points at. Gated on
// listing analysisruns directly (a different resource/verb from the
// Rollout's own patch grant), matching the RBAC/Policy/Velero/CNPG
// reverse-lookup pattern elsewhere in this file's sibling handlers.
func (s *Server) handleRolloutAnalysisRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	const group = "argoproj.io"
	if !s.canRead(r, group, "analysisruns", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "not allowed to list AnalysisRuns in this namespace")
		return
	}

	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	items, err := rollouts.ListAnalysisRuns(r.Context(), client, namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("Rollout %s/%s not found", namespace, name))
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[rollouts] Failed to list AnalysisRuns for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []rollouts.AnalysisRunSummary{}
	}

	s.writeJSON(w, map[string]any{"items": items})
}

// errCodeControllerNotCaughtUp lets a caller act on WHY a promotion was refused. The
// status alone cannot: 503 is also what a lost cluster connection answers, and a caller
// that reads only the code would tell the operator to retry something that will not
// recover on its own.
const errCodeControllerNotCaughtUp = "controller_not_caught_up"

func (s *Server) writeRolloutError(w http.ResponseWriter, err error, action, namespace, name string) {
	var status int
	var code string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case apierrors.IsNotFound(err), errors.Is(err, rollouts.ErrRevisionNotFound):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err):
		status = http.StatusForbidden
	case errors.Is(err, rollouts.ErrResourceTerminating),
		errors.Is(err, rollouts.ErrTemplateUnchanged),
		errors.Is(err, rollouts.ErrAlreadyAtLastStep):
		status = http.StatusConflict
	case errors.Is(err, rollouts.ErrNoSteps), errors.Is(err, rollouts.ErrWorkloadRefUnsupported):
		status = http.StatusBadRequest
	// The controller is a dependency that is lagging or down — the request was fine, so
	// the caller should retry rather than treat this as a bug in radar.
	case errors.Is(err, rollouts.ErrControllerNotCaughtUp):
		status = http.StatusServiceUnavailable
		code = errCodeControllerNotCaughtUp
	default:
		status = http.StatusInternalServerError
	}
	log.Printf("[rollouts] %q %s/%s -> %d: %v", action, sanitizeForLog(namespace), sanitizeForLog(name), status, err)
	if code != "" {
		s.writeErrorCode(w, status, code, err.Error())
		return
	}
	s.writeError(w, status, err.Error())
}
