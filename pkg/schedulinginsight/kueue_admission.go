package schedulinginsight

import (
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	rc "github.com/skyhook-io/radar/pkg/resourcecontext"
)

// KueueClassification is one Workload's lifecycle phase, terminal outcome and
// admission state. Outcome is empty until Phase is finished, and also for a
// finish reason Kueue does not name as success or failure.
type KueueClassification struct {
	Phase     rc.KueuePhase
	Outcome   rc.KueueOutcome
	Admission rc.KueueAdmissionState
}

// ClassifyKueueWorkload classifies one Workload from its own object, as an
// *unstructured.Unstructured of kueue.x-k8s.io/v1beta2 or v1beta1 kind
// Workload (how Radar's dynamic cache holds it). ok is false, with a zero
// result, for anything else; true says only that the object was classified.
// It is the single authority for Kueue admission-state codes: list, Issues
// and Capacity consumers call it per Workload instead of interpreting
// conditions themselves.
func ClassifyKueueWorkload(obj runtime.Object) (classification KueueClassification, ok bool) {
	u, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured || !isKueueWorkload(u) {
		return KueueClassification{}, false
	}
	return classifyWorkload(u, workloadConditions(u)), true
}

func classifyWorkload(u *unstructured.Unstructured, conditions map[string]workloadCondition) KueueClassification {
	phase := workloadPhase(conditions)
	return KueueClassification{
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

	decided := (*rc.KueueAdmissionFact)(nil)
	switch {
	case phase == rc.KueuePhaseFinished:
		state.State, state.Stale = rc.KueueAdmissionFinished, c.staleCondition("Finished")
	case state.Inactive != nil:
		decided = state.Inactive
	case state.Blocker != nil && (state.Disruption == nil || !c.uninformative(state.Blocker)):
		decided = state.Blocker
	case state.Disruption != nil:
		decided = state.Disruption
	case phase == rc.KueuePhaseAdmitted:
		state.State, state.Stale = rc.KueueAdmissionAdmitted, c.staleCondition("Admitted")
	default:
		state.State, state.Stale = rc.KueueAdmissionQuotaReserved, c.staleCondition("QuotaReserved")
	}
	if decided != nil {
		// spec.active=false is current by definition, whatever evidence the
		// fact cites for its cause.
		state.State, state.Stale = decided.Code, decided.Stale && decided.Code != rc.KueueAdmissionDeactivated
		// The phase chose which blocker rules applied, so a stale
		// QuotaReserved makes any blocker it led to stale too.
		if decided == state.Blocker && c.staleCondition("QuotaReserved") {
			state.Stale = true
		}
	}
	state.Attention = attention(state.State)
	if state.State == rc.KueueAdmissionDeactivated && state.Inactive.Source == rc.KueueFactSourceCondition {
		state.Attention = rc.KueueAttentionUnknown
	}
	return state
}

// preemptionGateIsCurrent reports whether BlockedOnPreemptionGates explains
// the current attempt. Kueue writes it together with QuotaReserved (v0.18
// with the same message, v0.19 with QuotaReserved reason AdmissionGated) and
// leaves it True after later attempts rewrite QuotaReserved for another
// reason, including an external admission gate, which Kueue also reports as
// AdmissionGated but with its own message.
const externalAdmissionGateMessage = "Admission is gated by"

func preemptionGateIsCurrent(conditions map[string]workloadCondition) bool {
	gate, ok := trueCondition(conditions, "BlockedOnPreemptionGates")
	if !ok {
		return false
	}
	quota, ok := conditions["QuotaReserved"]
	if !ok || quota.Status != "False" {
		return false
	}
	return quota.Reason == "PreemptionGated" || gate.Message == quota.Message ||
		(quota.Reason == "AdmissionGated" && !strings.HasPrefix(quota.Message, externalAdmissionGateMessage))
}

// uninformative is a blocker that says less than a current eviction: no
// decision yet, or the eviction message itself, which Kueue copies into
// QuotaReserved when it releases the quota. Any other unrecognized reason is
// news and stays the State.
func (c classifier) uninformative(blocker *rc.KueueAdmissionFact) bool {
	if blocker.Code == rc.KueueAdmissionNoDecision {
		return true
	}
	quota, hasQuota := c.conditions["QuotaReserved"]
	evicted, evictedNow := trueCondition(c.conditions, "Evicted")
	return blocker.Code == rc.KueueAdmissionUnrecognized && hasQuota && evictedNow && quota.Message == evicted.Message
}

func attention(code rc.KueueAdmissionCode) rc.KueueAttention {
	switch code {
	case rc.KueueAdmissionAdmitted, rc.KueueAdmissionQuotaReserved, rc.KueueAdmissionFinished:
		return rc.KueueAttentionNone
	case rc.KueueAdmissionDeactivated, rc.KueueAdmissionOnHold:
		return rc.KueueAttentionPaused
	case rc.KueueAdmissionNoDecision, rc.KueueAdmissionInsufficientQuota, rc.KueueAdmissionTopologyPlacementFailed,
		rc.KueueAdmissionWaitingForPreemption, rc.KueueAdmissionPreemptionGated, rc.KueueAdmissionAdmissionGated,
		rc.KueueAdmissionChecksPending, rc.KueueAdmissionTopologyAssignmentPending, rc.KueueAdmissionWaitingForPodsReady,
		rc.KueueAdmissionRequeueBackoff, rc.KueueAdmissionPreempted, rc.KueueAdmissionPodsReadyTimeout, rc.KueueAdmissionEvicted:
		return rc.KueueAttentionWaiting
	case rc.KueueAdmissionExceedsQuota, rc.KueueAdmissionNoMatchingFlavor, rc.KueueAdmissionNamespaceNotEligible,
		rc.KueueAdmissionLocalQueueMissing, rc.KueueAdmissionClusterQueueMissing, rc.KueueAdmissionCheckRejected,
		rc.KueueAdmissionDeactivationPending, rc.KueueAdmissionDeactivatedByKueue, rc.KueueAdmissionInvalidWorkload:
		return rc.KueueAttentionActionNeeded
	default:
		return rc.KueueAttentionUnknown
	}
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
		// Kueue records its own cause as DeactivationTarget until it flips
		// spec.active, then as the Evicted reason DeactivatedDueTo<Cause>; a
		// plain Deactivated eviction is someone else's change. Kueue v0.18 does
		// not rewrite an earlier eviction when it deactivates, so any other
		// Evicted reason leaves the cause unknown.
		if target, ok := trueCondition(c.conditions, "DeactivationTarget"); ok {
			return c.conditionFact(rc.KueueAdmissionDeactivatedByKueue, target)
		}
		evicted, evictedNow := trueCondition(c.conditions, "Evicted")
		switch {
		case evictedNow && strings.HasPrefix(evicted.Reason, "DeactivatedDueTo"):
			return c.conditionFact(rc.KueueAdmissionDeactivatedByKueue, evicted)
		case evictedNow && evicted.Reason != "Deactivated":
			return c.conditionFact(rc.KueueAdmissionDeactivated, evicted)
		}
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
// is not in the queue. Otherwise, while pending, the QuotaReserved condition
// decides: Kueue resets check states to Pending on eviction, and
// Admitted=False NoReservation only restates the missing reservation. When
// QuotaReserved only repeats the eviction message, a Requeued=False naming a
// stopped queue says what holds the Workload back.
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
	fact := c.conditionFact(c.quotaBlockerCode(quota), quota)
	if requeued, ok := c.conditions["Requeued"]; ok && requeued.Status == "False" && c.uninformative(fact) {
		switch requeued.Reason {
		case "ClusterQueueStopped":
			return c.conditionFact(rc.KueueAdmissionClusterQueueInactive, requeued)
		case "LocalQueueStopped":
			return c.conditionFact(rc.KueueAdmissionLocalQueueInactive, requeued)
		}
	}
	return fact
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
	if preemptionGateIsCurrent(c.conditions) {
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
		// The scheduler also uses it when Retry checks block nomination.
		if code := messageBlockerCode(quota.Message); code == rc.KueueAdmissionChecksPending {
			return code
		}
		return rc.KueueAdmissionNoDecision
	case "Waiting", "WaitingForPodsReady":
		return rc.KueueAdmissionWaitingForPodsReady
	case "DRAResourcesUnresolved":
		return rc.KueueAdmissionDRAResourcesUnresolved
	case "UnrecordableResourceUsage":
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
		return queueMessageCode(message, rc.KueueAdmissionLocalQueueMissing, rc.KueueAdmissionLocalQueueInactive)
	case strings.HasPrefix(message, "ClusterQueue "):
		return queueMessageCode(message, rc.KueueAdmissionClusterQueueMissing, rc.KueueAdmissionClusterQueueInactive)
	case strings.Contains(message, "has failed admission checks"):
		return rc.KueueAdmissionChecksPending
	case strings.Contains(message, "waiting for all admitted workloads to be in PodsReady condition"):
		return rc.KueueAdmissionWaitingForPodsReady
	case strings.HasPrefix(message, "AdmissionGatedBy cleared"):
		return rc.KueueAdmissionNoDecision
	case strings.Contains(message, "Pending the preemption of"):
		// The same state the granular reason WaitingForPreemptedWorkloads names.
		return rc.KueueAdmissionWaitingForPreemption
	case strings.HasPrefix(message, "resources validation failed"), strings.HasPrefix(message, "resources didn't satisfy LimitRange constraints"):
		return rc.KueueAdmissionInvalidWorkload
	}
	return flavorAssignmentCode(message)
}

var (
	unusedQuotaClause = regexp.MustCompile(`insufficient unused quota for \S+ in flavor (\S+?),`)
	maxQuotaClause    = regexp.MustCompile(`insufficient quota for \S+ in flavor (\S+?),`)
	mismatchClauses   = []*regexp.Regexp{
		regexp.MustCompile(`untolerated taint \{[^}]*\} in flavor ([^,\s]+)`),
		regexp.MustCompile(`flavor (\S+) doesn't match node affinity`),
		regexp.MustCompile(`[Ff]lavor "?([^"\s]+)"? does not support TopologyAwareScheduling`),
		regexp.MustCompile(`could not assign (\S+) flavor since the original workload is assigned`),
	}
)

// flavorAssignmentCode reads a scheduler flavor-assignment message: one
// "couldn't assign flavors to pod set" segment per PodSet that did not fit,
// listing clauses per flavor and resource. A flavor could fit later only if
// every clause naming it is understood and about unused quota; a resource
// over the maximum or a taint, affinity, topology or earlier-slice mismatch
// rules it out. Every PodSet must fit, so one PodSet with no flavor that
// could fit later decides the code, and a PodSet whose clauses are not all
// understood leaves it undecided rather than letting a quota wait stand for
// it.
func flavorAssignmentCode(message string) rc.KueueAdmissionCode {
	neverFits, undecided := rc.KueueAdmissionCode(""), false
	for _, segment := range strings.Split(message, "; couldn't assign flavors to pod set ") {
		understood := map[string]int{}
		waiting := map[string]bool{}
		for _, match := range unusedQuotaClause.FindAllStringSubmatch(segment, -1) {
			waiting[match[1]] = true
			understood[match[1]]++
		}
		ruledOut := map[string]bool{}
		exceeded := false
		for _, match := range maxQuotaClause.FindAllStringSubmatch(segment, -1) {
			ruledOut[match[1]], exceeded = true, true
			understood[match[1]]++
		}
		mismatch := strings.Contains(segment, "unavailable in ClusterQueue")
		for _, pattern := range mismatchClauses {
			for _, match := range pattern.FindAllStringSubmatch(segment, -1) {
				ruledOut[match[1]], mismatch = true, true
				understood[match[1]]++
			}
		}
		couldFit, unexplained := false, false
		for flavor := range waiting {
			switch {
			case ruledOut[flavor]:
			case flavorMentions(segment, flavor) > understood[flavor]:
				unexplained = true
			default:
				couldFit = true
			}
		}
		switch {
		case couldFit:
		case exceeded:
			neverFits = rc.KueueAdmissionExceedsQuota
		case mismatch && !unexplained:
			if neverFits == "" {
				neverFits = rc.KueueAdmissionNoMatchingFlavor
			}
		default:
			undecided = true
		}
	}
	switch {
	case neverFits != "":
		return neverFits
	case undecided:
		return ""
	}
	return rc.KueueAdmissionInsufficientQuota
}

// flavorMentions counts the clauses that name flavor, in either word order
// Kueue uses ("in flavor F", "flavor F doesn't", "F flavor since").
func flavorMentions(segment, flavor string) int {
	name := regexp.QuoteMeta(flavor)
	pattern := regexp.MustCompile(`(?i)flavor "?` + name + `"?(?:[,\s]|$)|(?:^|\s)` + name + ` flavor\b`)
	return len(pattern.FindAllStringIndex(segment, -1))
}

func queueMessageCode(message string, missing, inactive rc.KueueAdmissionCode) rc.KueueAdmissionCode {
	switch {
	case strings.HasSuffix(message, " doesn't exist"), strings.HasSuffix(message, " not found"), strings.HasSuffix(message, " is terminating or missing"):
		return missing
	case strings.HasSuffix(message, " is stopped"), strings.HasSuffix(message, " is inactive"):
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
