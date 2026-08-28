// Package rollouts implements control-plane operations for Argo Rollouts
// (argoproj.io/v1alpha1 Rollout): abort, retry, promote, skip-step, and undo.
// Patch payloads and target subresources match `kubectl argo rollouts`; there is
// no dependency on the Argo Rollouts Go module or a Rollouts API server.
package rollouts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var GVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "rollouts",
}

// A Rollout's revisions are its owned ReplicaSets, keyed by RevisionAnnotation.
var replicaSetGVR = schema.GroupVersionResource{
	Group:    "apps",
	Version:  "v1",
	Resource: "replicasets",
}

const (
	// The Rollouts analogue of deployment.kubernetes.io/revision.
	RevisionAnnotation = "rollout.argoproj.io/revision"

	// Must be stripped from any template written back into a Rollout's spec: the
	// controller derives the hash from template contents, so a stale hash makes
	// it compute a different one than the ReplicaSet it's matching against.
	PodTemplateHashLabel = "rollouts-pod-template-hash"

	RestartAtField = "restartAt"
)

var (
	ErrRevisionNotFound = errors.New("revision not found")

	// Undo target already matches the live template; the write would be a no-op.
	ErrTemplateUnchanged = errors.New("pod template already matches the requested revision")

	ErrWorkloadRefUnsupported = errors.New("unsupported workloadRef kind")
	ErrNoSteps                = errors.New("rollout has no canary steps")
	ErrAlreadyAtLastStep      = errors.New("rollout is already at its last step")
	ErrResourceTerminating    = errors.New("rollout is pending deletion")

	// ErrControllerNotCaughtUp means the Argo Rollouts controller has not reconciled the
	// change yet: a lagging dependency, not a bad request.
	ErrControllerNotCaughtUp = errors.New("Argo Rollouts controller has not reconciled the latest change")
)

type OperationResult struct {
	Message   string `json:"message"`
	Operation string `json:"operation"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision,omitempty"`
	StepIndex *int64 `json:"stepIndex,omitempty"`
	// NoChange marks a promotion that found nothing to promote, so callers can report
	// what happened rather than what was asked for. Set by Promote and PromoteFull,
	// which are the verbs that can legitimately be a no-op.
	NoChange bool `json:"noChange,omitempty"`
}

type Strategy string

const (
	StrategyCanary    Strategy = "canary"
	StrategyBlueGreen Strategy = "blueGreen"
	StrategyUnknown   Strategy = "unknown"
)

func get(ctx context.Context, client dynamic.Interface, namespace, name string) (*unstructured.Unstructured, error) {
	ro, err := client.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Rollout %s/%s not found: %w", namespace, name, err)
		}
		return nil, fmt.Errorf("failed to get Rollout %s/%s: %w", namespace, name, err)
	}
	if err := assertNotTerminating(ro, namespace, name); err != nil {
		return nil, err
	}
	return ro, nil
}

func assertNotTerminating(ro *unstructured.Unstructured, namespace, name string) error {
	if ro.GetDeletionTimestamp().IsZero() {
		return nil
	}
	suffix := ""
	if finalizers := ro.GetFinalizers(); len(finalizers) > 0 {
		suffix = fmt.Sprintf(" (finalizers: %s)", strings.Join(finalizers, ", "))
	}
	return fmt.Errorf("Rollout %s/%s is being deleted%s: %w", namespace, name, suffix, ErrResourceTerminating)
}

func StrategyOf(ro *unstructured.Unstructured) Strategy {
	if _, found, _ := unstructured.NestedMap(ro.Object, "spec", "strategy", "canary"); found {
		return StrategyCanary
	}
	if _, found, _ := unstructured.NestedMap(ro.Object, "spec", "strategy", "blueGreen"); found {
		return StrategyBlueGreen
	}
	return StrategyUnknown
}

func canarySteps(ro *unstructured.Unstructured) []any {
	steps, _, _ := unstructured.NestedSlice(ro.Object, "spec", "strategy", "canary", "steps")
	return steps
}

// Pointer field in the Rollout API — absent (not yet stepping) differs from 0.
func currentStepIndex(ro *unstructured.Unstructured) (int64, bool) {
	idx, found, err := unstructured.NestedInt64(ro.Object, "status", "currentStepIndex")
	if err != nil || !found {
		return 0, false
	}
	return idx, true
}

func isPaused(ro *unstructured.Unstructured) bool {
	paused, _, _ := unstructured.NestedBool(ro.Object, "spec", "paused")
	return paused
}

// currentIsStable reports that the rolling-out revision is already the stable one,
// i.e. there is nothing left to promote.
func currentIsStable(ro *unstructured.Unstructured) bool {
	current, _, _ := unstructured.NestedString(ro.Object, "status", "currentPodHash")
	stable, _, _ := unstructured.NestedString(ro.Object, "status", "stableRS")
	return current == stable
}

// controllerObservedSpec reports that the controller has reconciled the pod template as
// it stands now. Until it has, status still describes the PREVIOUS revision, so
// currentIsStable is not evidence that there is nothing left to promote.
//
// A workloadRef Rollout keeps its template on another object, and an undo patches THAT
// object — the Rollout's own generation never moves, so it cannot answer the question.
// Argo publishes workloadObservedGeneration for that case.
func controllerObservedSpec(ctx context.Context, client dynamic.Interface, ro *unstructured.Unstructured) (bool, error) {
	refKind, _, ok := WorkloadRef(ro)
	if !ok {
		return observedAtLeast(ro, "observedGeneration", ro.GetGeneration()), nil
	}
	target, err := ResolveTemplateTarget(ro)
	if err != nil {
		// No known template source to read a generation from, so there is nothing to wait on.
		return true, nil
	}
	ref, err := client.Resource(target.GVR).Namespace(ro.GetNamespace()).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		// Treating an unreadable template source as "observed" would silently skip the
		// wait, which is the failure this check exists to prevent.
		return false, fmt.Errorf("cannot tell whether the controller has reconciled %s %s/%s, the template source for Rollout %s: %w",
			refKind, ro.GetNamespace(), target.Name, ro.GetName(), err)
	}
	return observedAtLeast(ro, "workloadObservedGeneration", ref.GetGeneration()), nil
}

// Argo writes these counters as strings, but the field is typed loosely enough that a
// numeric value has to be handled too — reading only strings would silently skip the wait.
//
// An absent counter, or one that is not a number at all, falls back to trusting status:
// older Argo wrote a spec hash here, and refusing to promote on those would break the
// operation outright.
func observedAtLeast(ro *unstructured.Unstructured, field string, generation int64) bool {
	value, found, err := unstructured.NestedFieldNoCopy(ro.Object, "status", field)
	if err != nil || !found {
		return true
	}
	switch observed := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(observed, 10, 64)
		if err != nil {
			return true
		}
		return parsed >= generation
	case int64:
		return observed >= generation
	case float64:
		return int64(observed) >= generation
	default:
		return true
	}
}

func pauseConditionCount(ro *unstructured.Unstructured) int {
	conds, _, _ := unstructured.NestedSlice(ro.Object, "status", "pauseConditions")
	return len(conds)
}

func controllerPause(ro *unstructured.Unstructured) bool {
	p, _, _ := unstructured.NestedBool(ro.Object, "status", "controllerPause")
	return p
}

// A canary sitting on an Inconclusive analysis run needs controllerPause cleared
// and the step advanced together, or the controller re-pauses on the same result.
func isInconclusive(ro *unstructured.Unstructured) bool {
	if StrategyOf(ro) != StrategyCanary {
		return false
	}
	phase, found, _ := unstructured.NestedString(ro.Object, "status", "canary", "currentStepAnalysisRunStatus", "status")
	return found && phase == "Inconclusive"
}

func patchStatusThenSpec(ctx context.Context, client dynamic.Interface, namespace, name string, statusPatch, specPatch []byte) error {
	ri := client.Resource(GVR).Namespace(namespace)

	if statusPatch != nil {
		if _, err := ri.Patch(ctx, name, types.MergePatchType, statusPatch, metav1.PatchOptions{}, "status"); err != nil {
			return fmt.Errorf("failed to patch Rollout status: %w", err)
		}
	}

	if specPatch != nil {
		if _, err := ri.Patch(ctx, name, types.MergePatchType, specPatch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("failed to patch Rollout: %w", err)
		}
	}
	return nil
}
