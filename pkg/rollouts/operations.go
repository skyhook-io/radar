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
	if _, err := patchStatusThenSpec(ctx, client, namespace, name, []byte(abortPatch), nil); err != nil {
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
	if _, err := patchStatusThenSpec(ctx, client, namespace, name, []byte(retryPatch), nil); err != nil {
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
	if _, err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, specPatch); err != nil {
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

	afterStatus, err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, specPatch)
	if err != nil {
		return OperationResult{}, err
	}

	if statusPatch == nil && specPatch == nil {
		res := result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s is already at its final revision — there was nothing to skip", namespace, name))
		res.NoChange = true
		return res, nil
	}

	switch repatchUntilPromoted(ctx, client, namespace, name, afterStatus, deadline) {
	case promoteFullLanded:
		return result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s promoted to full — remaining steps, pauses, and analysis skipped", namespace, name)), nil
	case promoteFullAccepted:
		return result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s: promote full is under way. The new revision takes over as its pods become ready; check the Rollout if they don't", namespace, name)), nil
	default:
		return result("promote-full", namespace, name,
			fmt.Sprintf("Rollout %s/%s: promotion sent, but the controller has not confirmed it — check the Rollout before relying on it", namespace, name)), nil
	}
}

var (
	promoteFullSettleTimeout  = 15 * time.Second
	promoteFullSettleInterval = 500 * time.Millisecond
)

type promoteFullOutcome int

const (
	promoteFullUnconfirmed promoteFullOutcome = iota
	// The controller has acted on the flag and kept it.
	promoteFullAccepted
	// The Rollout reached its final revision.
	promoteFullLanded
)

// Reconciling a new template, the controller writes status from a copy read before the
// promoteFull patch and silently drops it, so the patch is reapplied until it sticks.
//
// It has stuck once the controller has acted on it and kept it, as promoteFullTaken
// checks. From then on only a change to the template or steps, or reaching the final
// revision, clears the flag. Waiting for the final revision instead holds the caller
// for as long as the new pods take to become ready, which on a real app is the whole
// deadline.
//
// Unconfirmed means the controller was never seen acting on it: a caller that claims
// the steps were skipped without that is guessing.
func repatchUntilPromoted(ctx context.Context, client dynamic.Interface, namespace, name string, afterStatus *unstructured.Unstructured, deadline time.Time) promoteFullOutcome {
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return promoteFullUnconfirmed
		case <-time.After(promoteFullSettleInterval):
		}

		ro, err := get(ctx, client, namespace, name)
		if err != nil {
			return promoteFullUnconfirmed
		}
		if currentIsStable(ro) {
			return promoteFullLanded
		}
		if promoteFullPending(ro) {
			if promoteFullTaken(ctx, client, afterStatus, ro) {
				return promoteFullAccepted
			}
			continue
		}
		if afterStatus, err = patchStatusThenSpec(ctx, client, namespace, name, []byte(promoteFullPatch), unpauseSpecPatch(ro)); err != nil {
			return promoteFullUnconfirmed
		}
	}
	return promoteFullUnconfirmed
}

// promoteFullTaken reports that the controller has acted on the flag since our status
// write and kept it. The evidence is a canary with steps moved to its last step, or a
// pause the controller held (pauseConditions) released, which shows as the Paused
// condition going from True to False. A Rollout paused only through spec.paused does
// not count on the Paused condition, because the controller flips that as soon as our
// unpause lands. A Rollout with neither, such as a blue-green that has not reached its
// pause yet, keeps the old wait.
//
// The Rollout must not be aborted, paused again or InvalidSpec, and its spec must be the
// one we promoted. Past that, only reaching the final revision clears the flag: the
// controller sends a merge patch of the fields it changed, so even a write from a copy
// that predates the flag leaves it in place. Without the evidence the wait runs to the
// final revision or the deadline as before.
func promoteFullTaken(ctx context.Context, client dynamic.Interface, afterStatus, ro *unstructured.Unstructured) bool {
	if afterStatus == nil || conditionStatus(ro, "InvalidSpec") == "True" {
		return false
	}
	aborted, _, _ := unstructured.NestedBool(ro.Object, "status", "abort")
	if aborted || isPaused(ro) || pauseConditionCount(ro) > 0 {
		return false
	}
	resumed := pauseConditionCount(afterStatus) > 0 &&
		conditionStatus(afterStatus, "Paused") == "True" && conditionStatus(ro, "Paused") == "False"
	if !resumed && !sentToLastStep(afterStatus, ro) {
		return false
	}
	return specUnchangedSince(ctx, client, afterStatus, ro)
}

// specUnchangedSince reports that the spec is still the one we promoted and that the
// controller has reconciled it: the Rollout's generation is where our writes left it,
// and for a workloadRef Rollout the referenced workload's generation is the one the
// controller had observed when we wrote. A newer template or steps would drop the flag,
// and the controller can publish the new observedGeneration on a condition write before
// it gets to that, so the generations themselves are compared. A counter that is missing
// or not a number proves nothing. Any other spec write, an HPA resize included, also
// moves the generation and keeps the old wait.
func specUnchangedSince(ctx context.Context, client dynamic.Interface, afterStatus, ro *unstructured.Unstructured) bool {
	promoted := afterStatus.GetGeneration()
	if isPaused(afterStatus) {
		promoted++ // our own unpause, written after the status
	}
	if ro.GetGeneration() != promoted {
		return false
	}
	if observed, ok := generationCounter(ro, "observedGeneration"); !ok || observed < promoted {
		return false
	}
	if _, _, ok := WorkloadRef(ro); !ok {
		return true
	}
	target, err := ResolveTemplateTarget(ro)
	if err != nil {
		return false
	}
	ref, err := client.Resource(target.GVR).Namespace(ro.GetNamespace()).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	workload, ok := generationCounter(afterStatus, "workloadObservedGeneration")
	return ok && ref.GetGeneration() == workload
}

func sentToLastStep(afterStatus, ro *unstructured.Unstructured) bool {
	last := int64(len(canarySteps(ro)))
	if last == 0 {
		return false
	}
	before, _ := currentStepIndex(afterStatus)
	now, ok := currentStepIndex(ro)
	return before < last && ok && now == last
}

func conditionStatus(ro *unstructured.Unstructured, conditionType string) string {
	conds, _, _ := unstructured.NestedSlice(ro.Object, "status", "conditions")
	for _, c := range conds {
		cond, ok := c.(map[string]any)
		if ok && cond["type"] == conditionType {
			status, _ := cond["status"].(string)
			return status
		}
	}
	return ""
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

	if _, err := patchStatusThenSpec(ctx, client, namespace, name, statusPatch, unpauseSpecPatch(ro)); err != nil {
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
