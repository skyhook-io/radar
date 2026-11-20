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
// finish reason Kueue does not name as success or failure. QueueName is the
// submission LocalQueue (empty when the Workload names none). With concurrent
// admission, Kueue schedules a parent's variants rather than the parent, so a
// pending parent reports no decision of its own and its variants carry the
// answer: Variant and Parent mark the two. Deleting marks a Workload with a
// deletion timestamp.
type KueueClassification struct {
	Phase     rc.KueuePhase
	Outcome   rc.KueueOutcome
	Admission rc.KueueAdmissionState
	QueueName string
	Variant   bool
	Parent    bool
	Deleting  bool
}

// AwaitingAdmission reports whether the Workload counts as waiting for
// admission: it names a queue, is not a concurrent-admission variant or being
// deleted, is not admitted or finished, and is not withdrawn. A parent counts
// once for its job. Consumers that count waiting Workloads use this rather
// than restating the rule.
func (c KueueClassification) AwaitingAdmission() bool {
	return c.QueueName != "" && !c.Variant && !c.Deleting && c.Admission.Inactive == nil &&
		c.Phase != rc.KueuePhaseAdmitted && c.Phase != rc.KueuePhaseFinished
}

// ClassifyKueueWorkload classifies one Workload from its own object, as an
// *unstructured.Unstructured of kueue.x-k8s.io/v1beta2 or v1beta1 kind
// Workload (how Radar's dynamic cache holds it). ok is false, with a zero
// result, for anything else; true says only that the object was classified.
// It is the single authority for Kueue admission-state codes: list, Issues
// and Capacity consumers must call it per Workload rather than interpret
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
	queueName, _, _ := unstructured.NestedString(u.Object, "spec", "queueName")
	admission := admissionState(u, conditions, phase)
	parent := u.GetLabels()[concurrentAdmissionParentLabel] == "true"
	if parent && admission.State == rc.KueueAdmissionNoDecision {
		// Kueue never schedules the parent itself; its variants say why.
		admission.Attention = rc.KueueAttentionUnknown
	}
	return KueueClassification{
		Phase:     phase,
		Outcome:   workloadOutcome(conditions),
		Admission: admission,
		QueueName: queueName,
		Variant:   concurrentAdmission(u) != nil,
		Parent:    parent,
		Deleting:  u.GetDeletionTimestamp() != nil,
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
	// With quota still reserved, a current eviction is releasing it; the
	// blocker it leaves behind (checks reset to Pending) is not news yet.
	case state.Blocker != nil && (state.Disruption == nil || (phase != rc.KueuePhaseQuotaReserved && !c.uninformative(state.Blocker))):
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
		// fact cites for its cause; Kueue's own flip of spec.active also bumps
		// the generation past the condition that recorded the cause.
		state.State, state.Stale = decided.Code, decided.Stale && !(decided == state.Inactive && c.specInactive())
		// A blocker read because quota is reserved rests on that reservation,
		// so a stale QuotaReserved makes it stale too. A rejected check or a
		// backoff stands on its own evidence.
		if decided == state.Blocker && phase == rc.KueuePhaseQuotaReserved && c.staleCondition("QuotaReserved") &&
			decided.Code != rc.KueueAdmissionCheckRejected && decided.Code != rc.KueueAdmissionRequeueBackoff {
			state.Stale = true
		}
	}
	state.Attention = attention(state.State)
	if state.State == rc.KueueAdmissionDeactivated && state.Inactive.Source == rc.KueueFactSourceCondition {
		state.Attention = rc.KueueAttentionUnknown
	}
	return state
}

const (
	externalAdmissionGateMessage   = "Admission is gated by"
	concurrentAdmissionParentLabel = "kueue.x-k8s.io/concurrent-admission-parent"
)

// preemptionGateIsCurrent reports whether BlockedOnPreemptionGates explains
// the current attempt. Kueue writes it together with QuotaReserved (v0.18
// with the same message, v0.19 with QuotaReserved reason AdmissionGated) and
// leaves it True after later attempts rewrite QuotaReserved for another
// reason, including an external admission gate, which Kueue also reports as
// AdmissionGated but with its own message.
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
	case rc.KueueAdmissionNoDecision, rc.KueueAdmissionInsufficientQuota,
		rc.KueueAdmissionWaitingForPreemption, rc.KueueAdmissionPreemptionGated, rc.KueueAdmissionAdmissionGated,
		rc.KueueAdmissionChecksPending, rc.KueueAdmissionTopologyAssignmentPending, rc.KueueAdmissionWaitingForPodsReady,
		rc.KueueAdmissionRequeueBackoff, rc.KueueAdmissionPreempted, rc.KueueAdmissionPodsReadyTimeout, rc.KueueAdmissionEvicted:
		return rc.KueueAttentionWaiting
	case rc.KueueAdmissionResourceUnavailable, rc.KueueAdmissionNamespaceNotEligible,
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
		Since:   condition.LastTransitionTime,
		Stale:   c.isStale(condition),
	}
}

func (c classifier) specInactive() bool {
	active, found, _ := unstructured.NestedBool(c.u.Object, "spec", "active")
	return found && !active
}

func (c classifier) inactive() *rc.KueueAdmissionFact {
	if c.specInactive() {
		// Kueue records its own cause as DeactivationTarget until it flips
		// spec.active, then as the Evicted reason DeactivatedDueTo<Cause>; a
		// plain Deactivated eviction is someone else's change. Kueue keeps an
		// earlier eviction's reason when it records no deactivation target (and
		// v0.18 also for its own causes), so any other Evicted reason leaves the
		// cause unknown.
		if target, ok := trueCondition(c.conditions, "DeactivationTarget"); ok {
			return c.conditionFact(rc.KueueAdmissionDeactivatedByKueue, target)
		}
		evicted, evictedNow := trueCondition(c.conditions, "Evicted")
		switch {
		case evictedNow && kueueDeactivationCause(evicted.Reason) && !c.reactivatedSince(evicted):
			return c.conditionFact(rc.KueueAdmissionDeactivatedByKueue, evicted)
		case evictedNow && evicted.Reason != "Deactivated" && evicted.Reason != "InactiveWorkload":
			// Includes a Kueue cause from before a later reactivation: this
			// deactivation came after it, with no cause recorded.
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

// kueueDeactivationCause reports an Evicted reason in which Kueue recorded its
// own deactivation cause: DeactivatedDueTo<Cause>, or InactiveWorkload<Cause>
// before Kueue v0.10 renamed it.
func kueueDeactivationCause(reason string) bool {
	return strings.HasPrefix(reason, "DeactivatedDueTo") ||
		(strings.HasPrefix(reason, "InactiveWorkload") && reason != "InactiveWorkload")
}

// reactivatedSince reports whether the Workload was reactivated after this
// eviction was recorded. Kueue keeps an eviction's reason across a
// reactivation and a later manual deactivation, so its cause then belongs to
// the earlier deactivation. Generations order the two writes: a condition
// rewritten in place keeps its transition time but takes the current
// generation. Without both generations the order is unknown, so the cause is
// not trusted either.
func (c classifier) reactivatedSince(evicted workloadCondition) bool {
	requeued, ok := trueCondition(c.conditions, "Requeued")
	if !ok || requeued.Reason != "Reactivated" {
		return false
	}
	if evicted.ObservedGeneration == 0 || requeued.ObservedGeneration == 0 {
		return true
	}
	return evicted.ObservedGeneration < requeued.ObservedGeneration
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
	if requeued, ok := c.conditions["Requeued"]; ok && phase == rc.KueuePhasePending && requeued.Status == "False" &&
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
	// Cite the check itself when one is not Ready: its name and message say
	// more than Admitted's generic "not all checks ready".
	for _, check := range checks {
		if check.state != "Ready" {
			return check.fact(rc.KueueAdmissionChecksPending)
		}
	}
	if hasAdmitted && admitted.Status == "False" {
		switch admitted.Reason {
		case "UnsatisfiedChecks", "UnsatisfiedAdmissionChecks":
			return c.conditionFact(rc.KueueAdmissionChecksPending, admitted)
		case "PendingDelayedTopologyRequests":
			return c.conditionFact(rc.KueueAdmissionTopologyAssignmentPending, admitted)
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
		if strings.Contains(quota.Message, "unavailable in ClusterQueue") {
			return rc.KueueAdmissionResourceUnavailable
		}
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
	case "Deactivated":
		// Left behind for a moment after reactivation; an inactive Workload
		// never reaches the blocker.
		return rc.KueueAdmissionNoDecision
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
	case strings.HasPrefix(message, "AdmissionGatedBy cleared"), strings.HasSuffix(message, "waiting for quota reservation"):
		return rc.KueueAdmissionNoDecision
	case strings.Contains(message, "but will fit after these preemptions complete"),
		strings.Contains(message, "Pending the preemption of"),
		strings.Contains(message, "workload(s) failed, will retry"):
		// The same state the granular reason WaitingForPreemptedWorkloads names.
		return rc.KueueAdmissionWaitingForPreemption
	case strings.HasPrefix(message, "A more favorable variant is already admitted"):
		return rc.KueueAdmissionNoDecision
	case strings.HasPrefix(message, "Workload was evaluated mid-cycle and is deferred to the next scheduling cycle"),
		strings.HasPrefix(message, "Workload no longer fits after processing another workload"),
		strings.HasPrefix(message, "Workload has overlapping preemption targets with another workload"):
		// Skipped in favour of another Workload in the same cycle; Kueue's
		// granular reason for this is WaitingForQuota.
		return rc.KueueAdmissionInsufficientQuota
	case strings.HasPrefix(message, "resources validation failed"), strings.HasPrefix(message, "resources didn't satisfy LimitRange constraints"):
		return rc.KueueAdmissionInvalidWorkload
	}
	return flavorAssignmentCode(message)
}

var (
	unusedQuotaClause = regexp.MustCompile(`insufficient unused quota (?:in cohort )?for \S+ in flavor (\S+?),`)
	maxQuotaClause    = regexp.MustCompile(`insufficient quota for \S+ in flavor (\S+?),`)
	mismatchClauses   = []*regexp.Regexp{
		regexp.MustCompile(`untolerated taint \{[^}]*\} in flavor ([^,\s]+)`),
		regexp.MustCompile(`flavor (\S+) doesn't match node affinity`),
		regexp.MustCompile(`[Ff]lavor "?([^"\s]+)"? does not support TopologyAwareScheduling`),
		regexp.MustCompile(`[Ff]lavor "?([^"\s]+)"? does not contain the requested level`),
		regexp.MustCompile(`[Ff]lavor "?([^"\s]+)"? supports only TopologyAwareScheduling`),
		regexp.MustCompile(`[Ff]lavor "?([^"\s]+)"? does not contain a topology level required by topology spreading`),
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
	segments := strings.Split(message, "; couldn't assign flavors to pod set ")
	// Kueue cuts a condition message at 32 KiB and appends " ...". The cut
	// segment may have lost clauses for any flavor, so only the complete
	// segments before it can prove anything.
	if strings.HasSuffix(message, kueueTruncationSuffix) && len(message) >= kueueMaxConditionMessage {
		segments, undecided = segments[:len(segments)-1], true
	}
	for _, segment := range segments {
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
		mismatch := false
		// A flavor this Workload may not use (concurrent-admission variants,
		// nomination) is out of the running but says nothing about a fit.
		for _, match := range skippedFlavorClause.FindAllStringSubmatch(segment, -1) {
			ruledOut[match[1]] = true
			understood[match[1]]++
		}
		for _, pattern := range mismatchClauses {
			for _, match := range pattern.FindAllStringSubmatch(segment, -1) {
				ruledOut[match[1]], mismatch = true, true
				understood[match[1]]++
			}
		}
		// Every flavor the segment names must be accounted for: a flavor whose
		// only failure Radar does not understand might still fit, so it keeps
		// the PodSet from being called waiting or never-fitting.
		couldFit, unexplained := false, hasUnknownClause(segment)
		lower := strings.ToLower(segment)
		for _, flavor := range mentionedFlavors(segment) {
			switch {
			case ruledOut[flavor]:
			case flavorMentions(segment, lower, flavor) > understood[flavor]:
				unexplained = true
			case waiting[flavor]:
				couldFit = true
			}
		}
		switch {
		case strings.Contains(segment, "unavailable in ClusterQueue"):
			// The ClusterQueue offers no quota for a requested resource at all;
			// no flavor can hold this PodSet, whatever else the segment says.
			neverFits = rc.KueueAdmissionResourceUnavailable
		case couldFit && !unexplained:
		case unexplained:
			undecided = true
		case mismatch && !exceeded:
			// No placement at all outranks too little quota, as in Kueue's own
			// most-severe-reason ranking: more quota would not help.
			if neverFits != rc.KueueAdmissionResourceUnavailable {
				neverFits = rc.KueueAdmissionNoMatchingFlavor
			}
		case exceeded:
			if neverFits == "" {
				neverFits = rc.KueueAdmissionExceedsQuota
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

const (
	kueueMaxConditionMessage = 32 * 1024
	kueueTruncationSuffix    = " ..."
)

// knownClauses match whole clauses of a flavor-assignment segment. What is
// left after removing them is a clause Radar does not understand, even when it
// names no flavor.
var knownClauses = []*regexp.Regexp{
	regexp.MustCompile(`^(?:couldn't assign flavors to pod set )?[^:]+: `),
	regexp.MustCompile(`insufficient unused quota (?:in cohort )?for \S+ in flavor \S+?, \S+ more needed`),
	regexp.MustCompile(`insufficient quota for \S+ in flavor \S+?, previously considered podsets requests \([^)]*\) \+ current podset request \([^)]*\) > maximum capacity \([^)]*\)`),
	regexp.MustCompile(`untolerated taint \{[^}]*\} in flavor [^,\s]+`),
	regexp.MustCompile(`flavor \S+ doesn't match node affinity`),
	regexp.MustCompile(`[Ff]lavor "?[^"\s]+"? (?:does not support TopologyAwareScheduling|does not contain the requested level|supports only TopologyAwareScheduling|does not contain a topology level required by topology spreading)`),
	regexp.MustCompile(`could not assign \S+ flavor since the original workload is assigned: [^,\s]+`),
	regexp.MustCompile(`skipping flavor \S+ (?:due to WorkloadAllowedResourceFlavorAnnotation annotation|as it is not found in the nomination mapping for resource \S+)`),
	regexp.MustCompile(`resource \S+ unavailable in ClusterQueue`),
}

func hasUnknownClause(segment string) bool {
	for _, clause := range knownClauses {
		segment = clause.ReplaceAllString(segment, "")
	}
	return strings.Trim(segment, " ,") != ""
}

var skippedFlavorClause = regexp.MustCompile(`skipping flavor (\S+) (?:due to|as it is not found)`)

var anyFlavor = regexp.MustCompile(`(?i)\bflavor "?([^"\s,;]+)|(\S+) flavor since`)

// mentionedFlavors lists each flavor a segment names once, in either of
// Kueue's word orders.
func mentionedFlavors(segment string) []string {
	seen := map[string]bool{}
	var flavors []string
	for _, match := range anyFlavor.FindAllStringSubmatch(segment, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		// "spot flavor since …" also reads as "flavor since"; "since" is no flavor.
		if name == "" || name == "since" || seen[name] {
			continue
		}
		seen[name] = true
		flavors = append(flavors, name)
	}
	return flavors
}

// flavorMentions counts the clauses that name flavor, in either word order
// Kueue uses ("in flavor F,", "flavor F doesn't", `Flavor "F"`, "F flavor
// since"). It scans strings rather than compiling a pattern per flavor,
// because list consumers classify every Workload on each request.
func flavorMentions(segment, lower, flavor string) int {
	count := 0
	for _, prefix := range []string{"flavor " + strings.ToLower(flavor), `flavor "` + strings.ToLower(flavor) + `"`} {
		for rest := lower; ; {
			i := strings.Index(rest, prefix)
			if i < 0 {
				break
			}
			rest = rest[i+len(prefix):]
			if rest == "" || rest[0] == ',' || rest[0] == ' ' || rest[0] == ';' || strings.HasPrefix(prefix, `flavor "`) {
				count++
			}
		}
	}
	return count + strings.Count(segment, " "+flavor+" flavor ") + boolCount(strings.HasPrefix(segment, flavor+" flavor "))
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
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
	name, state, message, since string
}

func (s checkState) fact(code rc.KueueAdmissionCode) *rc.KueueAdmissionFact {
	return &rc.KueueAdmissionFact{
		Code:    code,
		Source:  rc.KueueFactSourceAdmissionCheck,
		Name:    s.name,
		Reason:  s.state,
		Message: truncateMessage(s.message, maxPrimaryMessageBytes),
		Since:   s.since,
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
		since, _ := entry["lastTransitionTime"].(string)
		checks = append(checks, checkState{name: name, state: state, message: message, since: since})
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if left, right := admissionCheckRank(checks[i].state), admissionCheckRank(checks[j].state); left != right {
			return left < right
		}
		return checks[i].name < checks[j].name
	})
	return checks
}
