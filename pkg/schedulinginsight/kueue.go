// Package schedulinginsight projects controller-owned admission facts into a
// compact, controller-neutral summary. It does not reproduce scheduler math.
package schedulinginsight

import (
	"sort"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

var kueueWorkloadV1Beta2 = schema.GroupVersionKind{
	Group:   "kueue.x-k8s.io",
	Version: "v1beta2",
	Kind:    "Workload",
}

const (
	maxAdmissionChecks = 8
	maxResourceFlavors = 8
	maxMessageBytes    = 256
)

type workloadCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime string
}

// ForResource returns a scheduling summary only for an exact supported GVK.
func ForResource(obj runtime.Object, tier resourcecontext.ContextTier) *resourcecontext.SchedulingSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind() != kueueWorkloadV1Beta2 {
		return nil
	}

	conditions := workloadConditions(u)
	checks, checksTruncated, hasIncompleteCheck := admissionChecks(u, tier)
	flavors, flavorsTruncated := resourceFlavors(u)
	active, hasActive, _ := unstructured.NestedBool(u.Object, "spec", "active")
	stage, blocker := workloadStage(conditions, hasIncompleteCheck, hasActive && !active, tier)

	summary := &resourcecontext.SchedulingSummary{
		Controller:               "kueue",
		Stage:                    stage,
		Blocker:                  blocker,
		AdmissionChecks:          checks,
		AdmissionChecksTruncated: checksTruncated,
		Flavors:                  flavors,
		FlavorsTruncated:         flavorsTruncated,
	}

	if queueName, _, _ := unstructured.NestedString(u.Object, "spec", "queueName"); queueName != "" {
		summary.Queue = &resourcecontext.ContextRef{
			Kind:      "LocalQueue",
			Group:     kueueWorkloadV1Beta2.Group,
			Namespace: u.GetNamespace(),
			Name:      queueName,
		}
	}
	if clusterQueue, _, _ := unstructured.NestedString(u.Object, "status", "admission", "clusterQueue"); clusterQueue != "" {
		summary.ClusterQueue = &resourcecontext.ContextRef{
			Kind:  "ClusterQueue",
			Group: kueueWorkloadV1Beta2.Group,
			Name:  clusterQueue,
		}
	}
	if parent := parentWorkload(u); parent != nil {
		summary.ParentWorkload = parent
		summary.Variant = true
	}
	summary.Requeue = requeueState(u)
	return summary
}

func workloadConditions(u *unstructured.Unstructured) map[string]workloadCondition {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !ok {
		return nil
	}
	result := make(map[string]workloadCondition, len(raw))
	for _, item := range raw {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if conditionType == "" {
			continue
		}
		status, _ := condition["status"].(string)
		reason, _ := condition["reason"].(string)
		message, _ := condition["message"].(string)
		transition, _ := condition["lastTransitionTime"].(string)
		result[conditionType] = workloadCondition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: transition,
		}
	}
	return result
}

func workloadStage(conditions map[string]workloadCondition, hasIncompleteCheck, inactive bool, tier resourcecontext.ContextTier) (resourcecontext.SchedulingStage, *resourcecontext.SchedulingBlocker) {
	if condition, ok := trueCondition(conditions, "Finished"); ok {
		stage := resourcecontext.SchedulingFinished
		switch condition.Reason {
		case "Failed", "FailedToStart", "OutOfSync", "OwnerNotFound":
			stage = resourcecontext.SchedulingFailed
		}
		return stage, nil
	}

	if _, admitted := trueCondition(conditions, "Admitted"); admitted {
		if _, ok := trueCondition(conditions, "PodsReady"); ok {
			return resourcecontext.SchedulingRunning, nil
		} else if replacement, ok := trueCondition(conditions, "WaitingForReplacementPods"); ok {
			return resourcecontext.SchedulingAdmitted, blockerFromCondition(replacement, tier)
		} else if podsReady, ok := conditions["PodsReady"]; ok && podsReady.Status != "True" {
			return resourcecontext.SchedulingAdmitted, blockerFromCondition(podsReady, tier)
		}
		return resourcecontext.SchedulingAdmitted, nil
	}

	if _, reserved := trueCondition(conditions, "QuotaReserved"); reserved {
		if admitted, ok := conditions["Admitted"]; ok && admitted.Status != "True" {
			if admitted.Reason == "UnsatisfiedAdmissionChecks" || admitted.Reason == "PendingDelayedTopologyRequests" {
				return resourcecontext.SchedulingExternalCheck, blockerFromCondition(admitted, tier)
			}
			return resourcecontext.SchedulingReserved, blockerFromCondition(admitted, tier)
		}
		if hasIncompleteCheck {
			return resourcecontext.SchedulingExternalCheck, nil
		}
		return resourcecontext.SchedulingReserved, nil
	}

	if _, requeued := trueCondition(conditions, "Requeued"); requeued {
		return resourcecontext.SchedulingRequeued, nil
	}
	if preempted, ok := trueCondition(conditions, "Preempted"); ok {
		return resourcecontext.SchedulingPreempted, blockerFromCondition(preempted, tier)
	}
	if evicted, ok := trueCondition(conditions, "Evicted"); ok {
		stage := resourcecontext.SchedulingEvicted
		if evicted.Reason == "Preempted" {
			stage = resourcecontext.SchedulingPreempted
		}
		return stage, blockerFromCondition(evicted, tier)
	}

	for _, conditionType := range []string{
		"WaitingForReplacementPods",
		"BlockedOnPreemptionGates",
		"DeactivationTarget",
	} {
		if condition, ok := trueCondition(conditions, conditionType); ok {
			return resourcecontext.SchedulingWaiting, blockerFromCondition(condition, tier)
		}
	}
	if quota, ok := conditions["QuotaReserved"]; ok && quota.Status != "True" {
		return resourcecontext.SchedulingWaiting, blockerFromCondition(quota, tier)
	}
	if admitted, ok := conditions["Admitted"]; ok && admitted.Status != "True" {
		return resourcecontext.SchedulingWaiting, blockerFromCondition(admitted, tier)
	}
	if inactive {
		return resourcecontext.SchedulingWaiting, nil
	}
	return resourcecontext.SchedulingSubmitted, nil
}

func trueCondition(conditions map[string]workloadCondition, conditionType string) (workloadCondition, bool) {
	condition, ok := conditions[conditionType]
	return condition, ok && condition.Status == "True"
}

func blockerFromCondition(condition workloadCondition, tier resourcecontext.ContextTier) *resourcecontext.SchedulingBlocker {
	blocker := &resourcecontext.SchedulingBlocker{
		Condition:       condition.Type,
		Status:          condition.Status,
		Reason:          condition.Reason,
		ReasonPrecision: reasonPrecision(condition),
	}
	if tier == resourcecontext.TierDiagnostic {
		blocker.Message = truncateMessage(condition.Message)
		blocker.LastTransitionTime = condition.LastTransitionTime
	}
	return blocker
}

func reasonPrecision(condition workloadCondition) resourcecontext.SchedulingReasonPrecision {
	if condition.Status == "True" {
		return ""
	}
	switch condition.Type {
	case "QuotaReserved":
		switch condition.Reason {
		case "NoMatchingFlavor", "WaitingForQuota", "ExceedsMaxQuota", "TopologyPlacementFailed",
			"WaitingForPreemptedWorkloads", "Misconfigured", "Suspended", "PendingEvaluation",
			"WaitingForPodsReady", "AdmissionGated":
			return resourcecontext.SchedulingReasonGranular
		case "Pending", "Waiting":
			return resourcecontext.SchedulingReasonCoarse
		}
	case "Admitted":
		switch condition.Reason {
		case "NoReservation", "UnsatisfiedAdmissionChecks", "PendingDelayedTopologyRequests":
			return resourcecontext.SchedulingReasonGranular
		}
	}
	return ""
}

func admissionChecks(u *unstructured.Unstructured, tier resourcecontext.ContextTier) ([]resourcecontext.SchedulingAdmissionCheck, bool, bool) {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "status", "admissionChecks")
	if !ok {
		return nil, false, false
	}
	checks := make([]resourcecontext.SchedulingAdmissionCheck, 0, len(raw))
	hasIncomplete := false
	for _, item := range raw {
		state, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := state["name"].(string)
		if name == "" {
			continue
		}
		checkState, _ := state["state"].(string)
		switch checkState {
		case "Pending", "Retry", "Rejected":
			hasIncomplete = true
		}
		check := resourcecontext.SchedulingAdmissionCheck{
			Check: resourcecontext.ContextRef{
				Kind:  "AdmissionCheck",
				Group: kueueWorkloadV1Beta2.Group,
				Name:  name,
			},
			State: checkState,
		}
		if tier == resourcecontext.TierDiagnostic {
			message, _ := state["message"].(string)
			check.Message = truncateMessage(message)
			check.LastTransitionTime, _ = state["lastTransitionTime"].(string)
			check.RequeueAfterSeconds = int64Pointer(state["requeueAfterSeconds"])
			check.RetryCount = int64Pointer(state["retryCount"])
		}
		checks = append(checks, check)
	}
	sort.SliceStable(checks, func(i, j int) bool {
		return checks[i].Check.Name < checks[j].Check.Name
	})
	truncated := len(checks) > maxAdmissionChecks
	if truncated {
		checks = checks[:maxAdmissionChecks]
	}
	return checks, truncated, hasIncomplete
}

func resourceFlavors(u *unstructured.Unstructured) ([]resourcecontext.ContextRef, bool) {
	assignments, ok, _ := unstructured.NestedSlice(u.Object, "status", "admission", "podSetAssignments")
	if !ok {
		return nil, false
	}
	names := make(map[string]struct{})
	for _, item := range assignments {
		assignment, ok := item.(map[string]any)
		if !ok {
			continue
		}
		flavors, ok := assignment["flavors"].(map[string]any)
		if !ok {
			continue
		}
		for _, rawName := range flavors {
			if name, ok := rawName.(string); ok && name != "" {
				names[name] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	truncated := len(ordered) > maxResourceFlavors
	if truncated {
		ordered = ordered[:maxResourceFlavors]
	}
	refs := make([]resourcecontext.ContextRef, 0, len(ordered))
	for _, name := range ordered {
		refs = append(refs, resourcecontext.ContextRef{
			Kind:  "ResourceFlavor",
			Group: kueueWorkloadV1Beta2.Group,
			Name:  name,
		})
	}
	return refs, truncated
}

func parentWorkload(u *unstructured.Unstructured) *resourcecontext.ContextRef {
	for _, owner := range u.GetOwnerReferences() {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if owner.APIVersion != kueueWorkloadV1Beta2.GroupVersion().String() || owner.Kind != kueueWorkloadV1Beta2.Kind {
			return nil
		}
		return &resourcecontext.ContextRef{
			Kind:      owner.Kind,
			Group:     kueueWorkloadV1Beta2.Group,
			Namespace: u.GetNamespace(),
			Name:      owner.Name,
		}
	}
	return nil
}

func requeueState(u *unstructured.Unstructured) *resourcecontext.SchedulingRequeue {
	count, hasCount, _ := unstructured.NestedInt64(u.Object, "status", "requeueState", "count")
	at, hasAt, _ := unstructured.NestedString(u.Object, "status", "requeueState", "requeueAt")
	if !hasCount && !hasAt {
		return nil
	}
	result := &resourcecontext.SchedulingRequeue{At: at}
	if hasCount {
		result.Count = &count
	}
	return result
}

func int64Pointer(value any) *int64 {
	if parsed, ok := value.(int64); ok {
		return &parsed
	}
	return nil
}

func truncateMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxMessageBytes {
		return message
	}
	const suffix = "…"
	cut := maxMessageBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return strings.TrimSpace(message[:cut]) + suffix
}
