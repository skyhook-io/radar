// Package schedulinginsight projects controller-owned scheduling facts into a
// shared observation envelope while retaining provider-native evidence. It
// does not reproduce scheduler math.
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
	admissionGatedByAnnotation = "kueue.x-k8s.io/admission-gated-by"

	maxAdmissionChecks = 8 // Kueue v0.19.2 validates status.admissionChecks at this maximum.

	maxProjectedPodSetAssignments  = 8 // Keep context bounded; the raw Workload retains Kueue's full 18-entry API maximum.
	maxProjectedResourcesPerPodSet = 7 // Bound the cross-product; the raw Workload retains every assigned resource.
	maxPrimaryMessageBytes         = 256
	maxAdmissionMessageBytes       = 120
)

type workloadCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	ObservedGeneration int64
	LastTransitionTime string
}

// ForResource returns a scheduling summary only for an exact supported GVK.
func ForResource(obj runtime.Object, tier resourcecontext.ContextTier) *resourcecontext.SchedulingSummary {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GroupVersionKind() != kueueWorkloadV1Beta2 {
		return nil
	}

	conditions := workloadConditions(u)
	admissionGates, hasIncompleteAdmissionCheck := admissionChecks(u, tier)
	gates := append(admissionGates, preemptionGates(u)...)
	sortSchedulingGates(gates)
	active, hasActive, _ := unstructured.NestedBool(u.Object, "spec", "active")
	inactive := hasActive && !active
	admissionGatedByController := isAdmissionGatedByController(u, conditions)

	kueue := &resourcecontext.KueueScheduling{
		Phase:                     workloadPhase(conditions),
		Outcome:                   workloadOutcome(conditions),
		PodsReady:                 conditionSummaryIfPresent(conditions, "PodsReady", maxPrimaryMessageBytes),
		WaitingForReplacementPods: conditionSummaryIfPresent(conditions, "WaitingForReplacementPods", maxPrimaryMessageBytes),
		RequeueState:              requeueState(u),
		ConcurrentAdmission:       concurrentAdmission(u),
	}
	if hasActive {
		kueue.Active = &active
	}
	kueue.PodSetAssignments, kueue.PodSetAssignmentsTruncated = podSetAssignments(u)

	observation := resourcecontext.SchedulingObservation{
		Source:            resourcecontext.SchedulingSourceKueue,
		Domain:            resourcecontext.SchedulingDomainAdmission,
		SubjectGeneration: u.GetGeneration(),
		Subject: resourcecontext.ContextRef{
			Kind:      kueueWorkloadV1Beta2.Kind,
			Group:     kueueWorkloadV1Beta2.Group,
			Namespace: u.GetNamespace(),
			Name:      u.GetName(),
		},
		Decision:         workloadDecision(conditions, inactive, hasIncompleteAdmissionCheck, admissionGatedByController),
		PrimaryCondition: primaryCondition(conditions, inactive),
		Queues:           queueRefs(u),
		Gates:            gates,
		Disruptions:      disruptionConditions(conditions),
		Kueue:            kueue,
	}

	return &resourcecontext.SchedulingSummary{
		Observations: []resourcecontext.SchedulingObservation{observation},
	}
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
		observedGeneration, _ := condition["observedGeneration"].(int64)
		transition, _ := condition["lastTransitionTime"].(string)
		result[conditionType] = workloadCondition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: observedGeneration,
			LastTransitionTime: transition,
		}
	}
	return result
}

func workloadPhase(conditions map[string]workloadCondition) resourcecontext.KueuePhase {
	if _, ok := trueCondition(conditions, "Finished"); ok {
		return resourcecontext.KueuePhaseFinished
	}
	if _, ok := trueCondition(conditions, "Admitted"); ok {
		return resourcecontext.KueuePhaseAdmitted
	}
	if _, ok := trueCondition(conditions, "QuotaReserved"); ok {
		return resourcecontext.KueuePhaseQuotaReserved
	}
	return resourcecontext.KueuePhasePending
}

func workloadOutcome(conditions map[string]workloadCondition) resourcecontext.KueueOutcome {
	finished, ok := trueCondition(conditions, "Finished")
	if !ok {
		return ""
	}
	switch finished.Reason {
	case "Succeeded":
		return resourcecontext.KueueOutcomeSucceeded
	case "Failed", "FailedToStart", "OutOfSync", "OwnerNotFound":
		return resourcecontext.KueueOutcomeFailed
	default:
		return ""
	}
}

func workloadDecision(conditions map[string]workloadCondition, inactive, hasIncompleteAdmissionCheck, admissionGatedByController bool) resourcecontext.SchedulingDecision {
	if _, finished := trueCondition(conditions, "Finished"); finished {
		if _, admitted := trueCondition(conditions, "Admitted"); admitted {
			return resourcecontext.SchedulingDecisionSatisfied
		}
		if hasFalseCondition(conditions, "Admitted") || hasFalseCondition(conditions, "QuotaReserved") || hasIncompleteAdmissionCheck {
			return resourcecontext.SchedulingDecisionUnsatisfied
		}
		return resourcecontext.SchedulingDecisionUnknown
	}
	if inactive || isOnHold(conditions) {
		return resourcecontext.SchedulingDecisionHeld
	}
	if _, blocked := trueCondition(conditions, "BlockedOnPreemptionGates"); blocked {
		return resourcecontext.SchedulingDecisionUnsatisfied
	}
	if admissionGatedByController {
		return resourcecontext.SchedulingDecisionHeld
	}
	if _, admitted := trueCondition(conditions, "Admitted"); admitted {
		return resourcecontext.SchedulingDecisionSatisfied
	}
	if hasFalseCondition(conditions, "Admitted") || hasFalseCondition(conditions, "QuotaReserved") || hasIncompleteAdmissionCheck {
		return resourcecontext.SchedulingDecisionUnsatisfied
	}
	return resourcecontext.SchedulingDecisionUnknown
}

func isAdmissionGatedByController(u *unstructured.Unstructured, conditions map[string]workloadCondition) bool {
	quotaReserved, ok := conditions["QuotaReserved"]
	return ok && quotaReserved.Status == "False" && quotaReserved.Reason == "AdmissionGated" &&
		u.GetAnnotations()[admissionGatedByAnnotation] != ""
}

func isOnHold(conditions map[string]workloadCondition) bool {
	condition, ok := conditions["QuotaReserved"]
	return ok && condition.Status == "False" && condition.Reason == "OnHold"
}

func hasFalseCondition(conditions map[string]workloadCondition, conditionType string) bool {
	condition, ok := conditions[conditionType]
	return ok && condition.Status == "False"
}

func trueCondition(conditions map[string]workloadCondition, conditionType string) (workloadCondition, bool) {
	condition, ok := conditions[conditionType]
	return condition, ok && condition.Status == "True"
}

func primaryCondition(conditions map[string]workloadCondition, inactive bool) *resourcecontext.ConditionSummary {
	if _, ok := trueCondition(conditions, "Finished"); ok {
		return conditionSummaryIfPresent(conditions, "Finished", maxPrimaryMessageBytes)
	}
	if inactive {
		if _, ok := trueCondition(conditions, "DeactivationTarget"); ok {
			return conditionSummaryIfPresent(conditions, "DeactivationTarget", maxPrimaryMessageBytes)
		}
	}
	if _, ok := trueCondition(conditions, "BlockedOnPreemptionGates"); ok {
		return conditionSummaryIfPresent(conditions, "BlockedOnPreemptionGates", maxPrimaryMessageBytes)
	}
	if admitted, ok := conditions["Admitted"]; ok {
		if admitted.Status == "False" && admitted.Reason == "NoReservation" {
			if _, hasQuota := conditions["QuotaReserved"]; hasQuota {
				return conditionSummaryIfPresent(conditions, "QuotaReserved", maxPrimaryMessageBytes)
			}
		}
		return conditionSummaryIfPresent(conditions, "Admitted", maxPrimaryMessageBytes)
	}
	return conditionSummaryIfPresent(conditions, "QuotaReserved", maxPrimaryMessageBytes)
}

func conditionSummaryIfPresent(conditions map[string]workloadCondition, conditionType string, maxMessageBytes int) *resourcecontext.ConditionSummary {
	condition, ok := conditions[conditionType]
	if !ok {
		return nil
	}
	return &resourcecontext.ConditionSummary{
		Type:               condition.Type,
		Status:             condition.Status,
		Reason:             condition.Reason,
		Message:            truncateMessage(condition.Message, maxMessageBytes),
		ObservedGeneration: condition.ObservedGeneration,
		LastTransitionTime: condition.LastTransitionTime,
	}
}

func disruptionConditions(conditions map[string]workloadCondition) []resourcecontext.ConditionSummary {
	result := make([]resourcecontext.ConditionSummary, 0, 3)
	for _, conditionType := range []string{"Evicted", "Preempted", "DeactivationTarget"} {
		if _, ok := trueCondition(conditions, conditionType); !ok {
			continue
		}
		summary := conditionSummaryIfPresent(conditions, conditionType, maxPrimaryMessageBytes)
		result = append(result, *summary)
	}
	return result
}

func queueRefs(u *unstructured.Unstructured) []resourcecontext.SchedulingQueue {
	result := make([]resourcecontext.SchedulingQueue, 0, 2)
	if queueName, _, _ := unstructured.NestedString(u.Object, "spec", "queueName"); queueName != "" {
		result = append(result, resourcecontext.SchedulingQueue{
			Name:  queueName,
			Roles: []resourcecontext.SchedulingQueueRole{resourcecontext.SchedulingQueueSubmission},
			Ref: &resourcecontext.ContextRef{
				Kind:      "LocalQueue",
				Group:     kueueWorkloadV1Beta2.Group,
				Namespace: u.GetNamespace(),
				Name:      queueName,
			},
		})
	}
	if clusterQueue, _, _ := unstructured.NestedString(u.Object, "status", "admission", "clusterQueue"); clusterQueue != "" {
		result = append(result, resourcecontext.SchedulingQueue{
			Name:  clusterQueue,
			Roles: []resourcecontext.SchedulingQueueRole{resourcecontext.SchedulingQueueEntitlement},
			Ref: &resourcecontext.ContextRef{
				Kind:  "ClusterQueue",
				Group: kueueWorkloadV1Beta2.Group,
				Name:  clusterQueue,
			},
		})
	}
	return result
}

func admissionChecks(u *unstructured.Unstructured, tier resourcecontext.ContextTier) ([]resourcecontext.SchedulingGate, bool) {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "status", "admissionChecks")
	if !ok {
		return nil, false
	}
	gates := make([]resourcecontext.SchedulingGate, 0, len(raw))
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
		nativeState, _ := state["state"].(string)
		decision := admissionCheckDecision(nativeState)
		if decision == resourcecontext.SchedulingDecisionUnsatisfied {
			hasIncomplete = true
		}
		gate := resourcecontext.SchedulingGate{
			Kind: resourcecontext.SchedulingGateAdmissionCheck,
			Name: name,
			Ref: &resourcecontext.ContextRef{
				Kind:  "AdmissionCheck",
				Group: kueueWorkloadV1Beta2.Group,
				Name:  name,
			},
			NativeState: nativeState,
			Decision:    decision,
		}
		gate.LastTransitionTime, _ = state["lastTransitionTime"].(string)
		gate.RequeueAfterSeconds = int64Pointer(state["requeueAfterSeconds"])
		gate.RetryCount = int64Pointer(state["retryCount"])
		if decision == resourcecontext.SchedulingDecisionUnsatisfied || tier == resourcecontext.TierDiagnostic {
			message, _ := state["message"].(string)
			messageLimit := maxAdmissionMessageBytes
			if tier == resourcecontext.TierDiagnostic {
				messageLimit = maxPrimaryMessageBytes
			}
			gate.Message = truncateMessage(message, messageLimit)
		}
		gates = append(gates, gate)
	}
	return gates, hasIncomplete
}

func preemptionGates(u *unstructured.Unstructured) []resourcecontext.SchedulingGate {
	gatesByName := make(map[string]resourcecontext.SchedulingGate)
	spec, _, _ := unstructured.NestedSlice(u.Object, "spec", "preemptionGates")
	for _, item := range spec {
		gate, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := gate["name"].(string)
		if name == "" {
			continue
		}
		gatesByName[name] = resourcecontext.SchedulingGate{
			Kind:        resourcecontext.SchedulingGatePreemption,
			Name:        name,
			NativeState: "Closed",
			Decision:    resourcecontext.SchedulingDecisionUnsatisfied,
		}
	}
	status, _, _ := unstructured.NestedSlice(u.Object, "status", "preemptionGates")
	for _, item := range status {
		state, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := state["name"].(string)
		if name == "" {
			continue
		}
		gate, exists := gatesByName[name]
		if !exists {
			continue
		}
		position, _ := state["position"].(string)
		transition, _ := state["lastTransitionTime"].(string)
		gate.NativeState = position
		gate.Decision = preemptionGateDecision(position)
		gate.LastTransitionTime = transition
		gatesByName[name] = gate
	}
	gates := make([]resourcecontext.SchedulingGate, 0, len(gatesByName))
	for _, gate := range gatesByName {
		gates = append(gates, gate)
	}
	return gates
}

func preemptionGateDecision(position string) resourcecontext.SchedulingDecision {
	switch position {
	case "Open":
		return resourcecontext.SchedulingDecisionSatisfied
	case "Closed":
		return resourcecontext.SchedulingDecisionUnsatisfied
	default:
		return resourcecontext.SchedulingDecisionUnknown
	}
}

func sortSchedulingGates(gates []resourcecontext.SchedulingGate) {
	sort.SliceStable(gates, func(i, j int) bool {
		leftDecision := schedulingDecisionRank(gates[i].Decision)
		rightDecision := schedulingDecisionRank(gates[j].Decision)
		if leftDecision != rightDecision {
			return leftDecision < rightDecision
		}
		leftKind := schedulingGateKindRank(gates[i].Kind)
		rightKind := schedulingGateKindRank(gates[j].Kind)
		if leftKind != rightKind {
			return leftKind < rightKind
		}
		leftState := schedulingGateNativeStateRank(gates[i])
		rightState := schedulingGateNativeStateRank(gates[j])
		if leftState != rightState {
			return leftState < rightState
		}
		return gates[i].Name < gates[j].Name
	})
}

func schedulingDecisionRank(decision resourcecontext.SchedulingDecision) int {
	switch decision {
	case resourcecontext.SchedulingDecisionUnsatisfied:
		return 0
	case resourcecontext.SchedulingDecisionHeld:
		return 1
	case resourcecontext.SchedulingDecisionSatisfied:
		return 2
	default:
		return 3
	}
}

func schedulingGateKindRank(kind resourcecontext.SchedulingGateKind) int {
	switch kind {
	case resourcecontext.SchedulingGateAdmissionCheck:
		return 0
	case resourcecontext.SchedulingGatePreemption:
		return 1
	default:
		return 2
	}
}

func schedulingGateNativeStateRank(gate resourcecontext.SchedulingGate) int {
	if gate.Kind == resourcecontext.SchedulingGateAdmissionCheck {
		return admissionCheckRank(gate.NativeState)
	}
	return 0
}

func admissionCheckDecision(state string) resourcecontext.SchedulingDecision {
	switch state {
	case "Ready":
		return resourcecontext.SchedulingDecisionSatisfied
	case "Pending", "Retry", "Rejected":
		return resourcecontext.SchedulingDecisionUnsatisfied
	default:
		return resourcecontext.SchedulingDecisionUnknown
	}
}

func admissionCheckRank(state string) int {
	switch state {
	case "Rejected":
		return 0
	case "Retry":
		return 1
	case "Pending":
		return 2
	case "Ready":
		return 3
	default:
		return 4
	}
}

func podSetAssignments(u *unstructured.Unstructured) ([]resourcecontext.KueuePodSetAssignment, bool) {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "status", "admission", "podSetAssignments")
	if !ok {
		return nil, false
	}
	assignments := make([]resourcecontext.KueuePodSetAssignment, 0, len(raw))
	for _, item := range raw {
		state, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := state["name"].(string)
		if name == "" {
			continue
		}
		resources, resourcesTruncated := resourceAssignments(state)
		assignments = append(assignments, resourcecontext.KueuePodSetAssignment{
			Name:               name,
			Count:              int64Pointer(state["count"]),
			Resources:          resources,
			ResourcesTruncated: resourcesTruncated,
		})
	}
	sort.SliceStable(assignments, func(i, j int) bool {
		return assignments[i].Name < assignments[j].Name
	})
	truncated := len(assignments) > maxProjectedPodSetAssignments
	if truncated {
		assignments = assignments[:maxProjectedPodSetAssignments]
	}
	return assignments, truncated
}

func resourceAssignments(state map[string]any) ([]resourcecontext.KueueResourceAssignment, bool) {
	flavors, _ := state["flavors"].(map[string]any)
	usage, _ := state["resourceUsage"].(map[string]any)
	names := make(map[string]struct{}, len(flavors)+len(usage))
	for name := range flavors {
		names[name] = struct{}{}
	}
	for name := range usage {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		iExtended := strings.Contains(ordered[i], "/")
		jExtended := strings.Contains(ordered[j], "/")
		if iExtended != jExtended {
			return iExtended
		}
		return ordered[i] < ordered[j]
	})
	truncated := len(ordered) > maxProjectedResourcesPerPodSet
	if truncated {
		ordered = ordered[:maxProjectedResourcesPerPodSet]
	}

	result := make([]resourcecontext.KueueResourceAssignment, 0, len(ordered))
	for _, name := range ordered {
		assignment := resourcecontext.KueueResourceAssignment{Name: name}
		if flavorName, ok := flavors[name].(string); ok && flavorName != "" {
			assignment.Flavor = flavorName
			assignment.FlavorRef = &resourcecontext.ContextRef{
				Kind:  "ResourceFlavor",
				Group: kueueWorkloadV1Beta2.Group,
				Name:  flavorName,
			}
		}
		if value, ok := usage[name].(string); ok {
			assignment.Usage = value
		}
		result = append(result, assignment)
	}
	return result, truncated
}

func concurrentAdmission(u *unstructured.Unstructured) *resourcecontext.KueueConcurrentAdmission {
	for _, owner := range u.GetOwnerReferences() {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if owner.APIVersion != kueueWorkloadV1Beta2.GroupVersion().String() || owner.Kind != kueueWorkloadV1Beta2.Kind {
			return nil
		}
		return &resourcecontext.KueueConcurrentAdmission{
			ParentName: owner.Name,
			ParentRef: &resourcecontext.ContextRef{
				Kind:      owner.Kind,
				Group:     kueueWorkloadV1Beta2.Group,
				Namespace: u.GetNamespace(),
				Name:      owner.Name,
			},
		}
	}
	return nil
}

func requeueState(u *unstructured.Unstructured) *resourcecontext.KueueRequeueState {
	count, hasCount, _ := unstructured.NestedInt64(u.Object, "status", "requeueState", "count")
	requeueAt, hasRequeueAt, _ := unstructured.NestedString(u.Object, "status", "requeueState", "requeueAt")
	if !hasCount && !hasRequeueAt {
		return nil
	}
	result := &resourcecontext.KueueRequeueState{RequeueAt: requeueAt}
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

func truncateMessage(message string, maxBytes int) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxBytes {
		return message
	}
	const suffix = "…"
	cut := maxBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return strings.TrimSpace(message[:cut]) + suffix
}
