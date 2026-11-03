package schedulinginsight

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	rc "github.com/skyhook-io/radar/pkg/resourcecontext"
)

// ClassifyKueueWorkload classifies one kueue.x-k8s.io Workload (v1beta1 or
// v1beta2) from its own object. It is the single authority for Kueue
// admission-state codes: list, Issues and Capacity consumers call it per
// Workload instead of interpreting conditions themselves.
func ClassifyKueueWorkload(obj runtime.Object) (rc.KueueWorkloadClassification, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || !isKueueWorkload(u) {
		return rc.KueueWorkloadClassification{}, false
	}
	return classifyWorkload(u, workloadConditions(u)), true
}

func classifyWorkload(u *unstructured.Unstructured, conditions map[string]workloadCondition) rc.KueueWorkloadClassification {
	phase := workloadPhase(conditions)
	return rc.KueueWorkloadClassification{
		Phase:     phase,
		Outcome:   workloadOutcome(conditions),
		Admission: admissionState(u, conditions, phase),
	}
}

func admissionState(u *unstructured.Unstructured, conditions map[string]workloadCondition, phase rc.KueuePhase) rc.KueueAdmissionState {
	c := classifier{u: u, conditions: conditions, generation: u.GetGeneration()}
	state := rc.KueueAdmissionState{Disruption: c.disruption()}
	if phase != rc.KueuePhaseFinished {
		state.Inactive = c.inactive()
		if state.Inactive == nil {
			state.Blocker = c.blocker(phase)
		}
	}

	stale := false
	switch {
	case phase == rc.KueuePhaseFinished:
		state.State, stale = rc.KueueAdmissionFinished, c.staleCondition("Finished")
	case state.Inactive != nil:
		state.State, stale = state.Inactive.Code, state.Inactive.Stale
	case state.Blocker != nil:
		state.State, stale = state.Blocker.Code, state.Blocker.Stale
	case state.Disruption != nil:
		state.State, stale = state.Disruption.Code, state.Disruption.Stale
	case phase == rc.KueuePhaseAdmitted:
		state.State, stale = rc.KueueAdmissionAdmitted, c.staleCondition("Admitted")
	default:
		state.State, stale = rc.KueueAdmissionQuotaReserved, c.staleCondition("QuotaReserved")
	}
	if stale {
		state.State = rc.KueueAdmissionStale
	}
	return state
}

type classifier struct {
	u          *unstructured.Unstructured
	conditions map[string]workloadCondition
	generation int64
}

func (c classifier) isStale(condition workloadCondition) bool {
	return condition.ObservedGeneration > 0 && c.generation > 0 && condition.ObservedGeneration < c.generation
}

func (c classifier) staleCondition(conditionType string) bool {
	condition, ok := c.conditions[conditionType]
	return ok && c.isStale(condition)
}

func (c classifier) conditionFact(code rc.KueueAdmissionCode, condition workloadCondition) *rc.KueueAdmissionFact {
	return &rc.KueueAdmissionFact{
		Code:    code,
		Source:  rc.KueueFactSourceCondition,
		Name:    condition.Type,
		Reason:  condition.Reason,
		Message: truncateMessage(condition.Message, maxPrimaryMessageBytes),
		Stale:   c.isStale(condition),
	}
}

func (c classifier) inactive() *rc.KueueAdmissionFact {
	if active, found, _ := unstructured.NestedBool(c.u.Object, "spec", "active"); found && !active {
		return &rc.KueueAdmissionFact{Code: rc.KueueAdmissionDeactivated, Source: rc.KueueFactSourceSpec, Name: "active"}
	}
	if target, ok := trueCondition(c.conditions, "DeactivationTarget"); ok {
		return c.conditionFact(rc.KueueAdmissionDeactivationPending, target)
	}
	if quota, ok := c.conditions["QuotaReserved"]; ok && quota.Status == "False" && quota.Reason == "OnHold" {
		return c.conditionFact(rc.KueueAdmissionOnHold, quota)
	}
	return nil
}

// disruption reports a current eviction. Kueue resets Evicted and Preempted
// to False at the next quota reservation, so a True condition means the
// Workload has not reserved quota again since.
func (c classifier) disruption() *rc.KueueAdmissionFact {
	if preempted, ok := trueCondition(c.conditions, "Preempted"); ok {
		return c.conditionFact(rc.KueueAdmissionPreempted, preempted)
	}
	evicted, ok := trueCondition(c.conditions, "Evicted")
	if !ok {
		return nil
	}
	switch evicted.Reason {
	case "Preempted":
		return c.conditionFact(rc.KueueAdmissionPreempted, evicted)
	case "PodsReadyTimeout":
		return c.conditionFact(rc.KueueAdmissionPodsReadyTimeout, evicted)
	default:
		return c.conditionFact(rc.KueueAdmissionEvicted, evicted)
	}
}

// blocker picks one reason a Workload still competing for admission is not
// admitted. A rejected check outranks everything because Kueue will not admit
// past it; backoff outranks the quota condition because a Workload in backoff
// is not in the queue. While pending, only the QuotaReserved condition
// decides: Kueue resets check states to Pending on eviction, and
// Admitted=False NoReservation only restates the missing reservation.
func (c classifier) blocker(phase rc.KueuePhase) *rc.KueueAdmissionFact {
	if phase == rc.KueuePhaseAdmitted || phase == rc.KueuePhaseFinished {
		return nil
	}
	checks := c.admissionChecks()
	for _, check := range checks {
		if check.state == "Rejected" {
			return check.fact(rc.KueueAdmissionCheckRejected)
		}
	}
	if requeued, ok := c.conditions["Requeued"]; ok && requeued.Status == "False" &&
		(requeued.Reason == "PodsReadyTimeout" || requeued.Reason == "AdmissionCheck") {
		return c.conditionFact(rc.KueueAdmissionRequeueBackoff, requeued)
	}
	if phase == rc.KueuePhaseQuotaReserved {
		return c.reservedBlocker(checks)
	}
	quota, ok := c.conditions["QuotaReserved"]
	if !ok {
		return &rc.KueueAdmissionFact{Code: rc.KueueAdmissionNoDecision}
	}
	return c.conditionFact(c.quotaBlockerCode(quota), quota)
}

func (c classifier) reservedBlocker(checks []checkState) *rc.KueueAdmissionFact {
	admitted, hasAdmitted := c.conditions["Admitted"]
	if hasAdmitted && admitted.Status == "False" {
		switch admitted.Reason {
		case "UnsatisfiedChecks", "UnsatisfiedAdmissionChecks":
			return c.conditionFact(rc.KueueAdmissionChecksPending, admitted)
		case "PendingDelayedTopologyRequests":
			return c.conditionFact(rc.KueueAdmissionTopologyAssignmentPending, admitted)
		}
	}
	for _, check := range checks {
		if check.state != "Ready" {
			return check.fact(rc.KueueAdmissionChecksPending)
		}
	}
	if hasAdmitted && admitted.Status == "False" && !strings.HasPrefix(admitted.Reason, "NoReservation") {
		return c.conditionFact(rc.KueueAdmissionUnrecognized, admitted)
	}
	return nil
}

func (c classifier) quotaBlockerCode(quota workloadCondition) rc.KueueAdmissionCode {
	if gate, ok := trueCondition(c.conditions, "BlockedOnPreemptionGates"); ok && gate.Message == quota.Message {
		// The scheduler writes both conditions with one message; later attempts
		// rewrite QuotaReserved but leave the gate condition True.
		return rc.KueueAdmissionPreemptionGated
	}
	switch quota.Reason {
	case "WaitingForQuota":
		return rc.KueueAdmissionInsufficientQuota
	case "ExceedsMaxQuota":
		return rc.KueueAdmissionExceedsQuota
	case "NoMatchingFlavor":
		return rc.KueueAdmissionNoMatchingFlavor
	case "TopologyPlacementFailed":
		return rc.KueueAdmissionTopologyPlacementFailed
	case "WaitingForPreemptedWorkloads":
		return rc.KueueAdmissionWaitingForPreemption
	case "PreemptionGated":
		return rc.KueueAdmissionPreemptionGated
	case "PendingEvaluation":
		return rc.KueueAdmissionNoDecision
	case "Waiting", "WaitingForPodsReady":
		return rc.KueueAdmissionWaitingForPodsReady
	case "DRAResourcesUnresolved", "UnrecordableResourceUsage":
		return rc.KueueAdmissionInvalidWorkload
	case "AdmissionGated":
		if c.u.GetAnnotations()[admissionGatedByAnnotation] != "" {
			return rc.KueueAdmissionAdmissionGated
		}
		return rc.KueueAdmissionUnrecognized
	case "Pending", "Inadmissible", "Misconfigured", "Suspended":
		if code := messageBlockerCode(quota.Message); code != "" {
			return code
		}
		if quota.Reason == "Misconfigured" {
			return rc.KueueAdmissionInvalidWorkload
		}
	}
	return rc.KueueAdmissionUnrecognized
}

// messageBlockerCode splits Kueue's coarse reasons, which carry the cause
// only in the message. Phrases are Kueue's own (v0.18 through v0.20).
func messageBlockerCode(message string) rc.KueueAdmissionCode {
	switch {
	case strings.Contains(message, "namespace doesn't match ClusterQueue selector"):
		return rc.KueueAdmissionNamespaceNotEligible
	case strings.HasPrefix(message, "LocalQueue "):
		return queueMessageCode(message, rc.KueueAdmissionLocalQueueMissing, rc.KueueAdmissionLocalQueueStopped, rc.KueueAdmissionLocalQueueInactive)
	case strings.HasPrefix(message, "ClusterQueue "):
		return queueMessageCode(message, rc.KueueAdmissionClusterQueueMissing, rc.KueueAdmissionClusterQueueStopped, rc.KueueAdmissionClusterQueueInactive)
	case strings.Contains(message, "has failed admission checks"):
		return rc.KueueAdmissionChecksPending
	case strings.Contains(message, "waiting for all admitted workloads to be in PodsReady condition"):
		return rc.KueueAdmissionWaitingForPodsReady
	case strings.HasPrefix(message, "AdmissionGatedBy cleared"):
		return rc.KueueAdmissionNoDecision
	case strings.HasPrefix(message, "resources validation failed"), strings.HasPrefix(message, "resources didn't satisfy LimitRange constraints"):
		return rc.KueueAdmissionInvalidWorkload
	}
	// A flavor-assignment message joins one clause per flavor and resource
	// across PodSets. Claiming the Workload can never fit needs every quota
	// clause to say so, so any clause that could fit later wins.
	switch {
	case strings.Contains(message, "insufficient unused quota"):
		return rc.KueueAdmissionInsufficientQuota
	case strings.Contains(message, "insufficient quota"):
		return rc.KueueAdmissionExceedsQuota
	case strings.Contains(message, "untolerated taint"), strings.Contains(message, "doesn't match node affinity"), strings.Contains(message, "unavailable in ClusterQueue"):
		return rc.KueueAdmissionNoMatchingFlavor
	}
	return ""
}

func queueMessageCode(message string, missing, stopped, inactive rc.KueueAdmissionCode) rc.KueueAdmissionCode {
	switch {
	case strings.HasSuffix(message, " doesn't exist"), strings.HasSuffix(message, " not found"), strings.HasSuffix(message, " is terminating or missing"):
		return missing
	case strings.HasSuffix(message, " is stopped"):
		return stopped
	case strings.HasSuffix(message, " is inactive"):
		return inactive
	}
	return ""
}

type checkState struct {
	name, state, message string
}

func (s checkState) fact(code rc.KueueAdmissionCode) *rc.KueueAdmissionFact {
	return &rc.KueueAdmissionFact{
		Code:    code,
		Source:  rc.KueueFactSourceAdmissionCheck,
		Name:    s.name,
		Reason:  s.state,
		Message: truncateMessage(s.message, maxPrimaryMessageBytes),
	}
}

func (c classifier) admissionChecks() []checkState {
	raw, _, _ := unstructured.NestedSlice(c.u.Object, "status", "admissionChecks")
	checks := make([]checkState, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" {
			continue
		}
		state, _ := entry["state"].(string)
		message, _ := entry["message"].(string)
		checks = append(checks, checkState{name: name, state: state, message: message})
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if left, right := admissionCheckRank(checks[i].state), admissionCheckRank(checks[j].state); left != right {
			return left < right
		}
		return checks[i].name < checks[j].name
	})
	return checks
}
