// Package executioninsight projects controller-owned execution facts into a
// shared lifecycle with typed controller detail. It does not infer children.
package executioninsight

import (
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/jobset"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const maxStateMessageBytes = 256

// ForResource returns an execution summary only for an exact supported GVK.
// The mapping follows JobSet v0.12.0; the served API version does not identify
// the controller release. Basic tier omits condition messages/times; diagnostic
// tier retains them with bounded messages.
func ForResource(obj runtime.Object, tier resourcecontext.ContextTier) *resourcecontext.ExecutionSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind() != jobset.GVK {
		return nil
	}

	detail := jobSetCounts(u)
	nativeState, _, _ := unstructured.NestedString(u.Object, "status", "terminalState")
	suspendRequested, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	phase, outcome, primary := jobSetPhase(jobset.Lifecycle(u), jobSetConditions(u), tier)
	detail.Restarts = jobSetRestarts(u)
	return &resourcecontext.ExecutionSummary{
		Controller:        resourcecontext.ExecutionControllerJobSet,
		SubjectGeneration: u.GetGeneration(),
		Phase:             phase,
		Outcome:           outcome,
		PrimaryCondition:  primary,
		NativeState:       nativeState,
		SuspendRequested:  &suspendRequested,
		JobSet:            &detail,
	}
}

func jobSetCounts(u *unstructured.Unstructured) resourcecontext.JobSetExecution {
	detail := resourcecontext.JobSetExecution{}
	detail.DeclaredRoles, detail.DeclaredJobs = jobset.Declared(u)
	observed := jobset.ObservedChildren(u)
	if observed == nil {
		return detail
	}
	detail.ObservedRoles = &observed.Roles
	detail.Jobs = &resourcecontext.ChildJobCounts{
		Ready:     observed.Ready,
		Active:    observed.Active,
		Succeeded: observed.Succeeded,
		Failed:    observed.Failed,
		Suspended: observed.Suspended,
	}
	return detail
}

func int64Field(object map[string]any, field string) int64 {
	value, _, _ := unstructured.NestedInt64(object, field)
	return value
}

func jobSetRestarts(u *unstructured.Unstructured) *resourcecontext.JobSetRestartCounts {
	restarts := &resourcecontext.JobSetRestartCounts{}
	if value, found, _ := unstructured.NestedInt64(u.Object, "status", "restarts"); found {
		restarts.Global = &value
	}
	// JobSet omits the counted field when its observed value is zero.
	if value, found, _ := unstructured.NestedInt64(u.Object, "status", "restartsCountTowardsMax"); found || restarts.Global != nil {
		restarts.GlobalCountTowardsMax = &value
	}

	statuses, observed, _ := unstructured.NestedSlice(u.Object, "status", "replicatedJobsStatus")
	var individual, individualCounted int64
	for _, item := range statuses {
		status, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if values, found, _ := unstructured.NestedSlice(status, "jobRestarts"); found {
			individual += sumInt64Slice(values)
		}
		if values, found, _ := unstructured.NestedSlice(status, "jobRestartsCountTowardsMax"); found {
			individualCounted += sumInt64Slice(values)
		}
	}
	// Unmaterialized arrays are zero for reported roles in JobSet.
	if observed {
		restarts.Individual = &individual
		restarts.IndividualCountTowardsMax = &individualCounted
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

func jobSetConditions(u *unstructured.Unstructured) map[string]resourcecontext.ConditionSummary {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !ok {
		return nil
	}
	conditions := make(map[string]resourcecontext.ConditionSummary, len(raw))
	for _, item := range raw {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if conditionType == "" {
			continue
		}
		conditions[conditionType] = resourcecontext.ConditionSummary{
			Type:               conditionType,
			Status:             stringField(condition, "status"),
			Reason:             stringField(condition, "reason"),
			Message:            stringField(condition, "message"),
			LastTransitionTime: stringField(condition, "lastTransitionTime"),
			ObservedGeneration: int64Field(condition, "observedGeneration"),
		}
	}
	return conditions
}

func stringField(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}

// jobSetPhase maps the shared lifecycle onto the execution vocabulary and
// attaches the deciding condition at the requested tier.
func jobSetPhase(
	state jobset.State,
	conditions map[string]resourcecontext.ConditionSummary,
	tier resourcecontext.ContextTier,
) (resourcecontext.ExecutionPhase, resourcecontext.ExecutionOutcome, *resourcecontext.ConditionSummary) {
	var primary *resourcecontext.ConditionSummary
	if state.Condition != "" {
		primary = conditionForExecution(conditions, state.Condition, tier)
	}
	return executionPhases[state.Phase], executionOutcomes[state.Outcome], primary
}

var executionPhases = map[jobset.Phase]resourcecontext.ExecutionPhase{
	jobset.PhasePending:   resourcecontext.ExecutionPending,
	jobset.PhaseActive:    resourcecontext.ExecutionActive,
	jobset.PhaseSuspended: resourcecontext.ExecutionSuspended,
	jobset.PhaseFinished:  resourcecontext.ExecutionFinished,
	jobset.PhaseUnknown:   resourcecontext.ExecutionUnknown,
}

var executionOutcomes = map[jobset.Outcome]resourcecontext.ExecutionOutcome{
	jobset.OutcomeSucceeded: resourcecontext.ExecutionSucceeded,
	jobset.OutcomeFailed:    resourcecontext.ExecutionFailed,
}

func conditionForExecution(conditions map[string]resourcecontext.ConditionSummary, conditionType string, tier resourcecontext.ContextTier) *resourcecontext.ConditionSummary {
	condition, ok := conditions[conditionType]
	if !ok || condition.Status != "True" {
		return nil
	}
	if tier == resourcecontext.TierDiagnostic {
		condition.Message = truncateMessage(condition.Message)
	} else {
		condition.Message = ""
		condition.LastTransitionTime = ""
	}
	return &condition
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
