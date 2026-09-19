// Package executioninsight projects controller-owned execution facts into a
// shared lifecycle with typed controller detail. It does not infer children.
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

// ForResource returns an execution summary only for an exact supported GVK.
func ForResource(obj runtime.Object, tier resourcecontext.ContextTier) *resourcecontext.ExecutionSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind() != jobSetV1Alpha2 {
		return nil
	}

	detail := jobSetCounts(u)
	conditions := jobSetConditions(u)
	nativeState, _, _ := unstructured.NestedString(u.Object, "status", "terminalState")
	suspendRequested, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	phase, outcome, primary := jobSetPhase(nativeState, conditions, detail.Jobs, suspendRequested, tier)
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
	replicatedJobs, _, _ := unstructured.NestedSlice(u.Object, "spec", "replicatedJobs")
	detail.DeclaredRoles = int64(len(replicatedJobs))
	for _, item := range replicatedJobs {
		role, ok := item.(map[string]any)
		if !ok {
			continue
		}
		replicas, found, _ := unstructured.NestedInt64(role, "replicas")
		if !found {
			replicas = 1
		}
		detail.DeclaredJobs += replicas
	}
	statuses, observed, _ := unstructured.NestedSlice(u.Object, "status", "replicatedJobsStatus")
	if !observed {
		return detail
	}
	jobs := &resourcecontext.ChildJobCounts{}
	var observedRoles int64
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
		jobs.Ready += int64Field(status, "ready")
		jobs.Active += int64Field(status, "active")
		jobs.Succeeded += int64Field(status, "succeeded")
		jobs.Failed += int64Field(status, "failed")
		jobs.Suspended += int64Field(status, "suspended")
	}
	detail.ObservedRoles = &observedRoles
	detail.Jobs = jobs
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

func jobSetPhase(
	nativeState string,
	conditions map[string]resourcecontext.ConditionSummary,
	jobs *resourcecontext.ChildJobCounts,
	suspendRequested bool,
	tier resourcecontext.ContextTier,
) (resourcecontext.ExecutionPhase, resourcecontext.ExecutionOutcome, *resourcecontext.ConditionSummary) {
	switch nativeState {
	case "Failed":
		return resourcecontext.ExecutionFinished, resourcecontext.ExecutionFailed, conditionForExecution(conditions, "Failed", tier)
	case "Completed":
		return resourcecontext.ExecutionFinished, resourcecontext.ExecutionSucceeded, conditionForExecution(conditions, "Completed", tier)
	case "":
	default:
		return resourcecontext.ExecutionUnknown, "", nil
	}
	for _, terminal := range []struct {
		condition string
		outcome   resourcecontext.ExecutionOutcome
	}{{"Failed", resourcecontext.ExecutionFailed}, {"Completed", resourcecontext.ExecutionSucceeded}} {
		if condition := conditionForExecution(conditions, terminal.condition, tier); condition != nil {
			return resourcecontext.ExecutionFinished, terminal.outcome, condition
		}
	}
	// In-order resume retains Suspended until every role has started.
	if !suspendRequested {
		if condition := conditionForExecution(conditions, "StartupPolicyInProgress", tier); condition != nil {
			return resourcecontext.ExecutionActive, "", condition
		}
	}
	if condition := conditionForExecution(conditions, "Suspended", tier); condition != nil {
		return resourcecontext.ExecutionSuspended, "", condition
	}
	for _, conditionType := range []string{"RestartingJobSet", "StartupPolicyInProgress"} {
		if condition := conditionForExecution(conditions, conditionType, tier); condition != nil {
			return resourcecontext.ExecutionActive, "", condition
		}
	}
	if jobs == nil {
		return resourcecontext.ExecutionUnknown, "", nil
	}
	if jobs.Ready > 0 || jobs.Active > 0 || jobs.Succeeded > 0 || jobs.Failed > 0 || jobs.Suspended > 0 {
		return resourcecontext.ExecutionActive, "", nil
	}
	return resourcecontext.ExecutionPending, "", nil
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
