package rollouts

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Patch payloads, verbatim from kubectl-argo-rollouts.
const (
	abortPatch = `{"status":{"abort":true}}`
	retryPatch = `{"status":{"abort":false}}`

	unpausePatch = `{"spec":{"paused":false}}`

	clearPauseConditionsPatch                   = `{"status":{"pauseConditions":null}}`
	clearPauseConditionsAndControllerPausePatch = `{"status":{"pauseConditions":null, "controllerPause":false, "currentStepIndex":%d}}`
	clearPauseConditionsPatchWithStep           = `{"status":{"pauseConditions":null, "currentStepIndex":%d}}`

	promoteFullPatch = `{"status":{"promoteFull":true}}`
)

func result(operation, namespace, name, message string) OperationResult {
	return OperationResult{Message: message, Operation: operation, Namespace: namespace, Name: name}
}

// A paused Rollout needs spec.paused cleared alongside the status patch.
func unpauseSpecPatch(ro *unstructured.Unstructured) []byte {
	if !isPaused(ro) {
		return nil
	}
	return []byte(unpausePatch)
}

// Abort reverts traffic to the stable ReplicaSet without touching spec. The
// fastest way out of a bad rollout; Retry resumes it afterwards.
func Abort(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	if _, err := get(ctx, client, namespace, name); err != nil {
		return OperationResult{}, err
	}
	if err := patchStatusThenSpec(ctx, client, namespace, name, []byte(abortPatch), nil); err != nil {
		return OperationResult{}, err
	}
	return result("abort", namespace, name,
		fmt.Sprintf("Rollout %s/%s: abort sent — traffic reverts to the stable revision once the controller applies it", namespace, name)), nil
}

// Retry clears an abort so the rollout resumes from its current step.
func Retry(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	if _, err := get(ctx, client, namespace, name); err != nil {
		return OperationResult{}, err
	}
	if err := patchStatusThenSpec(ctx, client, namespace, name, []byte(retryPatch), nil); err != nil {
		return OperationResult{}, err
	}
	return result("retry", namespace, name,
		fmt.Sprintf("Rollout %s/%s: retry sent — the controller resumes the rollout from its current step", namespace, name)), nil
}

// Promote clears the current pause. The controller advances the step itself once
// pauseConditions clears; only an Inconclusive analysis needs it moved here.
func Promote(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	ro, err := get(ctx, client, namespace, name)
	if err != nil {
		return OperationResult{}, err
	}

	var statusPatch []byte
	var stepIndex *int64
	next := nextStepIndex(ro)

	switch {
	case isInconclusive(ro) && pauseConditionCount(ro) > 0 && controllerPause(ro):
		stepIndex = &next
		statusPatch = []byte(fmt.Sprintf(clearPauseConditionsAndControllerPausePatch, next))
	case pauseConditionCount(ro) > 0:
		statusPatch = []byte(clearPauseConditionsPatch)
	case len(canarySteps(ro)) > 0:
		// Nothing is paused, so clearing pauseConditions would be a no-op: mid
		// analysis or experiment the step index is what has to move.
		stepIndex = &next
		statusPatch = []byte(fmt.Sprintf(clearPauseConditionsPatchWithStep, next))
	}

	specPatch := unpauseSpecPatch(ro)
	if err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, specPatch); err != nil {
		return OperationResult{}, err
	}
	if statusPatch == nil && specPatch == nil {
		res := result("promote", namespace, name,
			fmt.Sprintf("Rollout %s/%s has nothing paused to promote", namespace, name))
		res.NoChange = true
		return res, nil
	}
	res := result("promote", namespace, name,
		fmt.Sprintf("Rollout %s/%s: promote sent — the controller advances the rollout once it applies it", namespace, name))
	res.StepIndex = stepIndex
	return res, nil
}

// PromoteFull skips every remaining step, pause, and analysis, taking the
// canary straight to 100%. The emergency-hotfix path.
func PromoteFull(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	ro, err := get(ctx, client, namespace, name)
	if err != nil {
		return OperationResult{}, err
	}

	// A rollback changes the pod template moments before this runs. Until the controller
	// has observed it, it reconciles from a status copy that predates the patch and drops
	// promoteFull, and the stable hashes below still describe the pre-rollback revision.
	deadline := time.Now().Add(promoteFullSettleTimeout)
	ro, err = awaitObservedSpec(ctx, client, namespace, name, ro, deadline)
	if err != nil {
		return OperationResult{}, err
	}

	// CLI parity: Argo omits the promoteFull patch when there is no new revision.
	var statusPatch []byte
	if !currentIsStable(ro) {
		statusPatch = []byte(promoteFullPatch)
	}
	specPatch := unpauseSpecPatch(ro)

	if err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, specPatch); err != nil {
		return OperationResult{}, err
	}

	if statusPatch == nil && specPatch == nil {
		res := result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s is already at its final revision — there was nothing to skip", namespace, name))
		res.NoChange = true
		return res, nil
	}

	if !repatchUntilPromoted(ctx, client, namespace, name, deadline) {
		return result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s: promotion sent, but the controller has not confirmed it — check the Rollout before relying on it", namespace, name)), nil
	}

	return result("promote-full", namespace, name,
		fmt.Sprintf("Rollout %s/%s promoted to full — remaining steps, pauses, and analysis skipped", namespace, name)), nil
}

var (
	promoteFullSettleTimeout  = 15 * time.Second
	promoteFullSettleInterval = 500 * time.Millisecond
)

// Reconciling a new template, the controller writes status from a copy read before the
// promoteFull patch and silently drops it, so the patch is reapplied until the Rollout
// reaches its final revision. Reports whether it got there: a caller that claims the
// steps were skipped without this confirmation is guessing.
func repatchUntilPromoted(ctx context.Context, client dynamic.Interface, namespace, name string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(promoteFullSettleInterval):
		}

		ro, err := get(ctx, client, namespace, name)
		if err != nil {
			return false
		}
		if currentIsStable(ro) {
			return true
		}
		if promoteFullPending(ro) {
			continue
		}
		if err := patchStatusThenSpec(ctx, client, namespace, name, []byte(promoteFullPatch), unpauseSpecPatch(ro)); err != nil {
			return false
		}
	}
	return false
}

// awaitObservedSpec blocks until the controller has reconciled the template as it stands,
// returning the Rollout as it looked then. Promoting earlier is discarded in silence.
func awaitObservedSpec(ctx context.Context, client dynamic.Interface, namespace, name string, ro *unstructured.Unstructured, deadline time.Time) (*unstructured.Unstructured, error) {
	for {
		observed, err := controllerObservedSpec(ctx, client, ro)
		if err != nil {
			return nil, err
		}
		if observed {
			return ro, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("Rollout %s/%s: promoting now would be discarded — check that the controller is running, then retry: %w",
				namespace, name, ErrControllerNotCaughtUp)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(promoteFullSettleInterval):
		}
		if ro, err = get(ctx, client, namespace, name); err != nil {
			return nil, err
		}
	}
}

func promoteFullPending(ro *unstructured.Unstructured) bool {
	pending, _, _ := unstructured.NestedBool(ro.Object, "status", "promoteFull")
	return pending
}

// SkipCurrentStep advances past exactly one canary step. Blue-green Rollouts and
// step-less canaries have nothing to skip.
func SkipCurrentStep(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	ro, err := get(ctx, client, namespace, name)
	if err != nil {
		return OperationResult{}, err
	}

	if StrategyOf(ro) == StrategyBlueGreen {
		return OperationResult{}, fmt.Errorf("Rollout %s/%s uses the blueGreen strategy: %w", namespace, name, ErrNoSteps)
	}
	steps := canarySteps(ro)
	if len(steps) == 0 {
		return OperationResult{}, fmt.Errorf("Rollout %s/%s defines no canary steps: %w", namespace, name, ErrNoSteps)
	}
	if idx, ok := currentStepIndex(ro); ok && idx >= int64(len(steps)) {
		return OperationResult{}, fmt.Errorf("Rollout %s/%s is at step %d of %d: %w", namespace, name, idx, len(steps), ErrAlreadyAtLastStep)
	}

	next := nextStepIndex(ro)
	statusPatch := []byte(fmt.Sprintf(clearPauseConditionsPatchWithStep, next))

	if err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, unpauseSpecPatch(ro)); err != nil {
		return OperationResult{}, err
	}
	res := result("skip-step", namespace, name,
		fmt.Sprintf("Rollout %s/%s: step index moved to %d of %d", namespace, name, next, len(steps)))
	res.StepIndex = &next
	return res, nil
}

// Capped at the step count. An unset index means stepping hasn't started, so the
// next step is 1.
func nextStepIndex(ro *unstructured.Unstructured) int64 {
	steps := int64(len(canarySteps(ro)))
	idx, ok := currentStepIndex(ro)
	if !ok {
		if steps == 0 {
			return 0
		}
		return 1
	}
	if idx+1 > steps {
		return steps
	}
	return idx + 1
}

// Restart recreates the Rollout's pods in place via spec.restartAt, without
// creating a new revision or re-running the canary steps.
func Restart(ctx context.Context, client dynamic.Interface, namespace, name string) (OperationResult, error) {
	if _, err := get(ctx, client, namespace, name); err != nil {
		return OperationResult{}, err
	}

	restartAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{%q:%q}}`, RestartAtField, restartAt)
	if _, err := client.Resource(GVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	); err != nil {
		return OperationResult{}, fmt.Errorf("failed to restart Rollout: %w", err)
	}
	return result("restart", namespace, name,
		fmt.Sprintf("Rollout %s/%s restarting pods (restartAt %s)", namespace, name, restartAt)), nil
}
