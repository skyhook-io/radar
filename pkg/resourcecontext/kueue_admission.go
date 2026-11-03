package resourcecontext

// KueueAdmissionState sorts what one Kueue Workload's own status reports
// into independent axes. It is not a causal diagnosis: queues, namespaces and
// AdmissionChecks appear only as the Workload names them, never read
// independently, so a reported missing LocalQueue is the Workload's claim,
// not a NotFound observed by Radar.
//
// KueueScheduling.Phase is the lifecycle axis. State is the one code to
// group or count by, resolved in this order: finished, then Inactive, then
// Blocker, then Disruption, then admitted or quota_reserved. A Disruption
// outranks a no_decision Blocker, and an unrecognized one that only repeats
// the eviction message, which is what Kueue writes right after an eviction.
// Attention classifies State.
//
// Inactive, Blocker and Disruption are the facts behind State; each carries
// its own evidence and several can be present. Which ones are reported: a
// finished Workload has no Inactive or Blocker; an inactive one has no
// Blocker; an admitted one has no Blocker; Disruption is reported whenever an
// eviction is current. A missing fact is therefore not evidence on its own:
// no Blocker on an inactive Workload says nothing about its queue.
//
// Stale is set when the evidence that decided State was written for an
// earlier metadata.generation: State is then the last reported state, not a
// confirmed current one. Its absence means not known to be stale, not
// confirmed current: Kueue may omit observedGeneration, and AdmissionCheck
// states carry none.
type KueueAdmissionState struct {
	State      KueueAdmissionCode  `json:"state"`
	Attention  KueueAttention      `json:"attention"`
	Stale      bool                `json:"stale,omitempty"`
	Inactive   *KueueAdmissionFact `json:"inactive,omitempty"`
	Blocker    *KueueAdmissionFact `json:"blocker,omitempty"`
	Disruption *KueueAdmissionFact `json:"disruption,omitempty"`
}

// KueueAttention is Radar's guidance on what State asks of a person, from the
// Workload's own evidence and its current reported configuration. It is not
// a severity or a guarantee: how long a Workload may wait before that is a
// problem is the consumer's call.
type KueueAttention string

const (
	// KueueAttentionNone means reserved, admitted or finished.
	KueueAttentionNone KueueAttention = "none"
	// KueueAttentionWaiting means Kueue or another controller is expected to
	// move the Workload on; the reported state names no change anyone must
	// make.
	KueueAttentionWaiting KueueAttention = "waiting"
	// KueueAttentionPaused means the Workload was withdrawn on purpose and
	// Kueue recorded no failure as the cause.
	KueueAttentionPaused KueueAttention = "paused"
	// KueueAttentionActionNeeded means, as reported, it will not be admitted
	// without a change to the Workload, its queue or quota, or a controller
	// that should have acted.
	KueueAttentionActionNeeded KueueAttention = "action_needed"
	// KueueAttentionUnknown means the Workload alone cannot tell: an inactive
	// queue can be a stop policy or broken configuration, and an unrecognized
	// reason is not known to be actionable.
	KueueAttentionUnknown KueueAttention = "unknown"
)

// Clone returns a copy that shares no facts with s.
func (s KueueAdmissionState) Clone() KueueAdmissionState {
	clone := func(fact *KueueAdmissionFact) *KueueAdmissionFact {
		if fact == nil {
			return nil
		}
		copied := *fact
		return &copied
	}
	s.Inactive, s.Blocker, s.Disruption = clone(s.Inactive), clone(s.Blocker), clone(s.Disruption)
	return s
}

// KueueAdmissionFact is one classified fact and the native evidence it was
// read from. Name is the condition type, the AdmissionCheck name or the spec
// field, by Source; Reason is the condition's reason or the check's state.
// Message is display text: trimmed, and cut to 256 bytes with an ellipsis;
// the Workload's own status holds the complete message. Stale is set only
// for condition evidence whose observedGeneration is behind the Workload's
// generation; its absence means not known to be stale. Spec evidence is
// current by definition; Kueue records no generation for an AdmissionCheck
// state. A fact without a Source has no native evidence: only no_decision,
// when Kueue has reported no QuotaReserved condition at all.
type KueueAdmissionFact struct {
	Code    KueueAdmissionCode `json:"code"`
	Source  KueueFactSource    `json:"source,omitempty"`
	Name    string             `json:"name,omitempty"`
	Reason  string             `json:"reason,omitempty"`
	Message string             `json:"message,omitempty"`
	Stale   bool               `json:"stale,omitempty"`
}

// KueueFactSource names where a fact's evidence lives on the Workload.
type KueueFactSource string

const (
	// KueueFactSourceCondition is a status.conditions entry: Name is its
	// type, Reason its native reason.
	KueueFactSourceCondition KueueFactSource = "condition"
	// KueueFactSourceAdmissionCheck is a status.admissionChecks entry: Name is
	// the check, Reason its native state.
	KueueFactSourceAdmissionCheck KueueFactSource = "admission_check"
	// KueueFactSourceSpec is a spec field: Name is the field path.
	KueueFactSourceSpec KueueFactSource = "spec"
)

// KueueAdmissionCode is one classification code. State may hold any code;
// each fact holds only codes of its own axis.
type KueueAdmissionCode string

// State-only codes.
const (
	// KueueAdmissionFinished is the finished lifecycle; the result is Outcome.
	KueueAdmissionFinished KueueAdmissionCode = "finished"
	// KueueAdmissionAdmitted means admitted with no current disruption.
	KueueAdmissionAdmitted KueueAdmissionCode = "admitted"
	// KueueAdmissionQuotaReserved means quota is reserved and the Workload
	// reports nothing blocking admission.
	KueueAdmissionQuotaReserved KueueAdmissionCode = "quota_reserved"
)

// Inactive codes: the Workload itself is withdrawn from admission and is not
// waiting for it.
const (
	// KueueAdmissionDeactivated means spec.active is false and Kueue has
	// recorded no cause of its own. With no other eviction the fact cites the
	// spec field and someone set it on purpose; Kueue's acknowledgment is then
	// a separate Evicted disruption. When the Workload was already evicted for
	// another reason, Kueue (v0.18) keeps that reason and records no cause, so
	// the fact cites that Evicted condition and Attention is unknown.
	KueueAdmissionDeactivated KueueAdmissionCode = "deactivated"
	// KueueAdmissionDeactivatedByKueue means spec.active is false and Kueue
	// recorded why it deactivated the Workload (a rejected AdmissionCheck,
	// the requeuing limit, the maximum execution time); Reason keeps it.
	KueueAdmissionDeactivatedByKueue KueueAdmissionCode = "deactivated_by_kueue"
	// KueueAdmissionDeactivationPending means Kueue set DeactivationTarget
	// but spec.active is not false yet; Reason carries the cause.
	KueueAdmissionDeactivationPending KueueAdmissionCode = "deactivation_pending"
	// KueueAdmissionOnHold means Kueue released the quota reservation on
	// purpose and will not requeue the Workload.
	KueueAdmissionOnHold KueueAdmissionCode = "on_hold"
)

// Blocker codes: why a Workload that is still competing for admission is
// not admitted, as its own status reports it.
const (
	// KueueAdmissionNoDecision means Kueue reports no decision for the
	// current attempt: nothing yet, pending evaluation, or a re-evaluation
	// after a gate cleared.
	KueueAdmissionNoDecision KueueAdmissionCode = "no_decision"
	// KueueAdmissionInsufficientQuota means Kueue reports at least one flavor
	// for every PodSet that could fit once quota is released.
	KueueAdmissionInsufficientQuota KueueAdmissionCode = "insufficient_quota"
	// KueueAdmissionExceedsQuota means Kueue reports a PodSet that no flavor
	// could fit even with all quota free: a request over the most the queue
	// or cohort can provide.
	KueueAdmissionExceedsQuota KueueAdmissionCode = "exceeds_quota"
	// KueueAdmissionNoMatchingFlavor means no ResourceFlavor can hold the
	// Workload: taints, node affinity or topology rule them out, or the
	// ClusterQueue offers no quota for a requested resource.
	KueueAdmissionNoMatchingFlavor KueueAdmissionCode = "no_matching_flavor"
	// KueueAdmissionTopologyPlacementFailed means topology requirements
	// could not be placed.
	KueueAdmissionTopologyPlacementFailed KueueAdmissionCode = "topology_placement_failed"
	// KueueAdmissionWaitingForPreemption means Kueue preempted other
	// Workloads for this one and waits for them to release their quota.
	KueueAdmissionWaitingForPreemption KueueAdmissionCode = "waiting_for_preemption"
	// KueueAdmissionPreemptionGated means a preemption gate stopped this
	// Workload from preempting others for quota.
	KueueAdmissionPreemptionGated KueueAdmissionCode = "preemption_gated"
	// KueueAdmissionAdmissionGated means an external controller gates
	// admission through the kueue.x-k8s.io/admission-gated-by annotation.
	KueueAdmissionAdmissionGated KueueAdmissionCode = "admission_gated"
	// KueueAdmissionNamespaceNotEligible means the ClusterQueue's namespace
	// selector does not match the Workload's namespace.
	KueueAdmissionNamespaceNotEligible KueueAdmissionCode = "namespace_not_eligible"
	// KueueAdmissionLocalQueueMissing means the submission LocalQueue does not
	// exist or is being deleted.
	KueueAdmissionLocalQueueMissing KueueAdmissionCode = "local_queue_missing"
	// KueueAdmissionLocalQueueInactive means the LocalQueue is stopped or
	// inactive. Kueue's writers word the same state either way, so the
	// Workload alone does not say whether it is a stop policy.
	KueueAdmissionLocalQueueInactive KueueAdmissionCode = "local_queue_inactive"
	// KueueAdmissionClusterQueueMissing means the ClusterQueue does not exist
	// or is being deleted.
	KueueAdmissionClusterQueueMissing KueueAdmissionCode = "cluster_queue_missing"
	// KueueAdmissionClusterQueueInactive means the ClusterQueue is stopped or
	// inactive. That can be a stop policy or broken configuration, and
	// Kueue's writers word a stopped queue either way, so the Workload alone
	// does not say which.
	KueueAdmissionClusterQueueInactive KueueAdmissionCode = "cluster_queue_inactive"
	// KueueAdmissionCheckRejected means an AdmissionCheck rejected the
	// Workload. By itself it does not mean the Workload was deactivated; it is
	// usually brief, because Kueue then deactivates the Workload with this
	// cause (deactivation_pending, then deactivated_by_kueue).
	KueueAdmissionCheckRejected KueueAdmissionCode = "admission_check_rejected"
	// KueueAdmissionChecksPending means admission waits on AdmissionChecks
	// that are not Ready.
	KueueAdmissionChecksPending KueueAdmissionCode = "admission_checks_pending"
	// KueueAdmissionTopologyAssignmentPending means quota is reserved and a
	// delayed topology assignment is not complete.
	KueueAdmissionTopologyAssignmentPending KueueAdmissionCode = "topology_assignment_pending"
	// KueueAdmissionWaitingForPodsReady means admission waits for other
	// admitted Workloads to become PodsReady.
	KueueAdmissionWaitingForPodsReady KueueAdmissionCode = "waiting_for_pods_ready"
	// KueueAdmissionRequeueBackoff means the Workload waits out the backoff
	// Kueue applies after an eviction before requeuing it.
	KueueAdmissionRequeueBackoff KueueAdmissionCode = "requeue_backoff"
	// KueueAdmissionDRAResourcesUnresolved means Kueue could not resolve the
	// Workload's DRA resources, which can be the claims or the cluster's
	// DeviceClasses and ResourceSlices; Kueue retries.
	KueueAdmissionDRAResourcesUnresolved KueueAdmissionCode = "dra_resources_unresolved"
	// KueueAdmissionInvalidWorkload means Kueue rejects the Workload's own
	// requests: resource validation, the namespace's LimitRange, or usage it
	// cannot record.
	KueueAdmissionInvalidWorkload KueueAdmissionCode = "invalid_workload"
	// KueueAdmissionUnrecognized means the reported reason has no code; the
	// fact keeps the native reason and message.
	KueueAdmissionUnrecognized KueueAdmissionCode = "unrecognized"
)

// Disruption codes: a current eviction that Kueue has not yet followed with
// a new quota reservation.
const (
	// KueueAdmissionPreempted means Kueue preempted the Workload.
	KueueAdmissionPreempted KueueAdmissionCode = "preempted"
	// KueueAdmissionPodsReadyTimeout means Kueue evicted the Workload because
	// its Pods were not ready in time.
	KueueAdmissionPodsReadyTimeout KueueAdmissionCode = "pods_ready_timeout"
	// KueueAdmissionEvicted is any other eviction; Reason keeps the cause.
	KueueAdmissionEvicted KueueAdmissionCode = "evicted"
)
