// Package executioninsight projects controller-owned execution facts into a
// compact, controller-neutral summary. It does not discover or infer children.
package executioninsight

import (
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

var jobSetV1Alpha2 = schema.GroupVersionKind{
	Group:   "jobset.x-k8s.io",
	Version: "v1alpha2",
	Kind:    "JobSet",
}

const maxStateMessageBytes = 256

type executionCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime string
}

// ForResource returns an execution summary only for an exact supported GVK.
func ForResource(obj runtime.Object, tier resourcecontext.ContextTier) *resourcecontext.ExecutionSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	switch u.GroupVersionKind() {
	case jobSetV1Alpha2:
		return forJobSet(u, tier)
	case rayServiceV1:
		return forRayService(u, tier)
	default:
		return nil
	}
}

func forJobSet(u *unstructured.Unstructured, tier resourcecontext.ContextTier) *resourcecontext.ExecutionSummary {
	counts, observed := jobSetCounts(u)
	conditions := jobSetConditions(u)
	_, hasStatus, _ := unstructured.NestedMap(u.Object, "status")
	stage, state := jobSetStage(u, conditions, counts, observed, hasStatus, tier)

	summary := &resourcecontext.ExecutionSummary{
		Controller: "jobset",
		Stage:      stage,
		State:      state,
		Counts:     &counts,
		Restarts:   jobSetRestarts(u),
	}
	return summary
}

func jobSetCounts(u *unstructured.Unstructured) (resourcecontext.ExecutionCounts, bool) {
	counts := resourcecontext.ExecutionCounts{}
	replicatedJobs, _, _ := unstructured.NestedSlice(u.Object, "spec", "replicatedJobs")
	counts.DeclaredRoles = int64(len(replicatedJobs))
	for _, item := range replicatedJobs {
		role, ok := item.(map[string]any)
		if !ok {
			continue
		}
		replicas, found, _ := unstructured.NestedInt64(role, "replicas")
		if !found {
			replicas = 1
		}
		counts.DeclaredJobs += replicas
	}

	statuses, observed, _ := unstructured.NestedSlice(u.Object, "status", "replicatedJobsStatus")
	if !observed {
		return counts, false
	}

	var observedRoles, ready, active, succeeded, failed, suspended int64
	for _, item := range statuses {
		status, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(status, "name")
		if name == "" {
			continue
		}
		observedRoles++
		ready += int64Field(status, "ready")
		active += int64Field(status, "active")
		succeeded += int64Field(status, "succeeded")
		failed += int64Field(status, "failed")
		suspended += int64Field(status, "suspended")
	}
	counts.ObservedRoles = &observedRoles
	counts.ReadyJobs = &ready
	counts.ActiveJobs = &active
	counts.SucceededJobs = &succeeded
	counts.FailedJobs = &failed
	counts.SuspendedJobs = &suspended
	return counts, true
}

func int64Field(object map[string]any, field string) int64 {
	value, _, _ := unstructured.NestedInt64(object, field)
	return value
}

func jobSetRestarts(u *unstructured.Unstructured) *resourcecontext.ExecutionRestartCounts {
	restarts := &resourcecontext.ExecutionRestartCounts{}
	if value, found, _ := unstructured.NestedInt64(u.Object, "status", "restarts"); found {
		restarts.Global = &value
	}
	if value, found, _ := unstructured.NestedInt64(u.Object, "status", "restartsCountTowardsMax"); found {
		restarts.GlobalCountTowardsMax = &value
	}

	statuses, _, _ := unstructured.NestedSlice(u.Object, "status", "replicatedJobsStatus")
	var individual, individualRoles, individualCounted, individualCountedRoles int64
	for _, item := range statuses {
		status, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if values, found, _ := unstructured.NestedSlice(status, "jobRestarts"); found {
			individualRoles++
			individual += sumInt64Slice(values)
		}
		if values, found, _ := unstructured.NestedSlice(status, "jobRestartsCountTowardsMax"); found {
			individualCountedRoles++
			individualCounted += sumInt64Slice(values)
		}
	}
	if individualRoles > 0 {
		restarts.Individual = &individual
		restarts.IndividualRoles = &individualRoles
	}
	if individualCountedRoles > 0 {
		restarts.IndividualCountTowardsMax = &individualCounted
		restarts.IndividualCountedRoles = &individualCountedRoles
	}

	if restarts.Global == nil && restarts.GlobalCountTowardsMax == nil &&
		restarts.Individual == nil && restarts.IndividualCountTowardsMax == nil {
		return nil
	}
	return restarts
}

func sumInt64Slice(values []any) int64 {
	var total int64
	for _, value := range values {
		if count, ok := value.(int64); ok {
			total += count
		}
	}
	return total
}

func jobSetConditions(u *unstructured.Unstructured) map[string]executionCondition {
	return executionConditionsAt(u.Object, "status", "conditions")
}

func executionConditionsAt(object map[string]any, fields ...string) map[string]executionCondition {
	raw, ok, _ := unstructured.NestedSlice(object, fields...)
	if !ok {
		return nil
	}
	conditions := make(map[string]executionCondition, len(raw))
	for _, item := range raw {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if conditionType == "" {
			continue
		}
		conditions[conditionType] = executionCondition{
			Type:               conditionType,
			Status:             stringField(condition, "status"),
			Reason:             stringField(condition, "reason"),
			Message:            stringField(condition, "message"),
			LastTransitionTime: stringField(condition, "lastTransitionTime"),
		}
	}
	return conditions
}

func stringField(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}

func jobSetStage(
	u *unstructured.Unstructured,
	conditions map[string]executionCondition,
	counts resourcecontext.ExecutionCounts,
	observed, hasStatus bool,
	tier resourcecontext.ContextTier,
) (resourcecontext.ExecutionStage, *resourcecontext.ExecutionState) {
	terminalState, _, _ := unstructured.NestedString(u.Object, "status", "terminalState")
	switch terminalState {
	case "Failed":
		return resourcecontext.ExecutionFailed, stateForTrueCondition(conditions, "Failed", tier)
	case "Completed":
		return resourcecontext.ExecutionCompleted, stateForTrueCondition(conditions, "Completed", tier)
	}

	if condition, ok := trueCondition(conditions, "Failed"); ok {
		return resourcecontext.ExecutionFailed, executionState(condition, tier)
	}
	if condition, ok := trueCondition(conditions, "Completed"); ok {
		return resourcecontext.ExecutionCompleted, executionState(condition, tier)
	}
	if condition, ok := trueCondition(conditions, "Suspended"); ok {
		return resourcecontext.ExecutionSuspended, executionState(condition, tier)
	}
	if suspended, found, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); found && suspended {
		return resourcecontext.ExecutionSuspended, nil
	}
	if condition, ok := trueCondition(conditions, "RestartingJobSet"); ok {
		return resourcecontext.ExecutionRestarting, executionState(condition, tier)
	}
	if condition, ok := trueCondition(conditions, "StartupPolicyInProgress"); ok {
		return resourcecontext.ExecutionStarting, executionState(condition, tier)
	}
	if observed && (*counts.ActiveJobs > 0 || *counts.ReadyJobs > 0) {
		return resourcecontext.ExecutionRunning, nil
	}
	if hasStatus {
		return resourcecontext.ExecutionPending, nil
	}
	return resourcecontext.ExecutionSubmitted, nil
}

func trueCondition(conditions map[string]executionCondition, conditionType string) (executionCondition, bool) {
	condition, ok := conditions[conditionType]
	return condition, ok && condition.Status == "True"
}

func stateForTrueCondition(conditions map[string]executionCondition, conditionType string, tier resourcecontext.ContextTier) *resourcecontext.ExecutionState {
	condition, ok := trueCondition(conditions, conditionType)
	if !ok {
		return nil
	}
	return executionState(condition, tier)
}

func executionState(condition executionCondition, tier resourcecontext.ContextTier) *resourcecontext.ExecutionState {
	state := &resourcecontext.ExecutionState{
		Condition: condition.Type,
		Status:    condition.Status,
		Reason:    condition.Reason,
	}
	if tier == resourcecontext.TierDiagnostic {
		state.Message = truncateMessage(condition.Message)
		state.LastTransitionTime = condition.LastTransitionTime
	}
	return state
}

func truncateMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxStateMessageBytes {
		return message
	}
	const suffix = "…"
	cut := maxStateMessageBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return strings.TrimSpace(message[:cut]) + suffix
}
