package executioninsight

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

var rayServiceV1 = schema.GroupVersionKind{
	Group:   "ray.io",
	Version: "v1",
	Kind:    "RayService",
}

func forRayService(u *unstructured.Unstructured, tier resourcecontext.ContextTier) *resourcecontext.ExecutionSummary {
	conditions := executionConditionsAt(u.Object, "status", "conditions")
	_, hasStatus, _ := unstructured.NestedMap(u.Object, "status")
	stage, state := rayServiceStage(u, conditions, hasStatus, tier)

	summary := &resourcecontext.ExecutionSummary{
		Controller: "rayservice",
		Stage:      stage,
		State:      state,
		Runtimes: &resourcecontext.ExecutionRuntimes{
			Active:  rayServiceRuntime(u, "activeServiceStatus", tier),
			Pending: rayServiceRuntime(u, "pendingServiceStatus", tier),
		},
	}
	if summary.Runtimes.Active == nil && summary.Runtimes.Pending == nil {
		summary.Runtimes = nil
	}
	return summary
}

func rayServiceStage(
	u *unstructured.Unstructured,
	conditions map[string]executionCondition,
	hasStatus bool,
	tier resourcecontext.ContextTier,
) (resourcecontext.ExecutionStage, *resourcecontext.ExecutionState) {
	if condition, ok := conditionWithStatus(conditions, "Ready", "False"); ok &&
		(condition.Reason == "ValidationFailed" || condition.Reason == "InitializingTimeout") {
		return resourcecontext.ExecutionFailed, executionState(condition, tier)
	}
	if condition, ok := conditionWithStatus(conditions, "Suspended", "True"); ok {
		return resourcecontext.ExecutionSuspended, executionState(condition, tier)
	}
	if condition, ok := conditionWithStatus(conditions, "Suspending", "True"); ok {
		return resourcecontext.ExecutionSuspended, executionState(condition, tier)
	}
	if suspended, found, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); found && suspended {
		return resourcecontext.ExecutionSuspended, nil
	}
	if condition, ok := conditionWithStatus(conditions, "RollbackInProgress", "True"); ok {
		return resourcecontext.ExecutionUpdating, executionState(condition, tier)
	}
	if condition, ok := conditionWithStatus(conditions, "UpgradeInProgress", "True"); ok {
		return resourcecontext.ExecutionUpdating, executionState(condition, tier)
	}
	if condition, ok := conditionWithStatus(conditions, "Ready", "True"); ok {
		return resourcecontext.ExecutionRunning, executionState(condition, tier)
	}
	if condition, ok := conditions["Ready"]; ok {
		if condition.Status == "False" && condition.Reason == "Initializing" {
			return resourcecontext.ExecutionStarting, executionState(condition, tier)
		}
		return resourcecontext.ExecutionPending, executionState(condition, tier)
	}
	if hasStatus {
		return resourcecontext.ExecutionPending, nil
	}
	return resourcecontext.ExecutionSubmitted, nil
}

func rayServiceRuntime(u *unstructured.Unstructured, field string, tier resourcecontext.ContextTier) *resourcecontext.ExecutionRuntime {
	status, found, _ := unstructured.NestedMap(u.Object, "status", field)
	if !found {
		return nil
	}
	clusterName, _, _ := unstructured.NestedString(status, "rayClusterName")
	if clusterName == "" {
		return nil
	}

	runtime := &resourcecontext.ExecutionRuntime{
		ClusterName:           clusterName,
		TargetCapacityPercent: int64PointerAt(status, "targetCapacity"),
		TrafficRoutedPercent:  int64PointerAt(status, "trafficRoutedPercent"),
	}
	if condition, ok := selectRayClusterCondition(executionConditionsAt(status, "rayClusterStatus", "conditions")); ok {
		runtime.RuntimeState = executionState(condition, tier)
	}
	return runtime
}

func int64PointerAt(object map[string]any, fields ...string) *int64 {
	value, found, _ := unstructured.NestedInt64(object, fields...)
	if !found {
		return nil
	}
	return &value
}

func selectRayClusterCondition(conditions map[string]executionCondition) (executionCondition, bool) {
	for _, candidate := range []struct {
		conditionType string
		status        string
	}{
		{conditionType: "ReplicaFailure", status: "True"},
		{conditionType: "RayClusterSuspended", status: "True"},
		{conditionType: "RayClusterSuspending", status: "True"},
		{conditionType: "HeadPodReady", status: "False"},
		{conditionType: "HeadPodReady", status: "Unknown"},
		{conditionType: "RayClusterProvisioned", status: "False"},
		{conditionType: "RayClusterProvisioned", status: "Unknown"},
		{conditionType: "HeadPodReady", status: "True"},
		{conditionType: "RayClusterProvisioned", status: "True"},
	} {
		if condition, ok := conditionWithStatus(conditions, candidate.conditionType, candidate.status); ok {
			return condition, true
		}
	}
	return executionCondition{}, false
}

func conditionWithStatus(conditions map[string]executionCondition, conditionType, status string) (executionCondition, bool) {
	condition, ok := conditions[conditionType]
	return condition, ok && condition.Status == status
}
