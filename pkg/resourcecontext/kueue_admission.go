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
// outranks a Blocker that only says no_decision or unrecognized, which is
// what Kueue writes right after an eviction. Attention classifies State.
// Stale means the evidence that decided State was written for an earlier
// metadata.generation, so State is the last reported state, not a confirmed
// current one. Inactive, Blocker and Disruption are the facts behind State
// and may be present together; each carries its own evidence.
type KueueAdmissionState struct {
	State      KueueAdmissionCode  `json:"state"`
	Attention  KueueAttention      `json:"attention"`
	Stale      bool                `json:"stale,omitempty"`
	Inactive   *KueueAdmissionFact `json:"inactive,omitempty"`
	Blocker    *KueueAdmissionFact `json:"blocker,omitempty"`
	Disruption *KueueAdmissionFact `json:"disruption,omitempty"`
}

// KueueAttention says what State asks of a person, from the Workload's own
// evidence. It is not a severity: how long a Workload may wait before that
// is a problem is the consumer's call.
type KueueAttention string

const (
	// KueueAttentionNone means reserved, admitted or finished.
	KueueAttentionNone KueueAttention = "none"
	// KueueAttentionWaiting means normal waiting that Kueue or another
	// controller resolves without anyone changing configuration.
	KueueAttentionWaiting KueueAttention = "waiting"
	// KueueAttentionPaused means someone withdrew the Workload on purpose.
	KueueAttentionPaused KueueAttention = "paused"
	// KueueAttentionActionNeeded means it will not be admitted until someone
	// changes the Workload, its queue or its quota.
	KueueAttentionActionNeeded KueueAttention = "action_needed"
	// KueueAttentionUnknown means the Workload alone cannot tell; for example
	// an inactive queue can be a stop policy or broken configuration.
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
// read from. Stale is set only for condition evidence whose
// observedGeneration is behind the Workload's generation. Spec evidence is
// current by definition; Kueue records no generation for an AdmissionCheck
// state, so its freshness is not established either way. A fact without a
// Source has no native evidence: only no_decision, when Kueue has reported
// no QuotaReserved condition at all.
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
	// recorded no cause of its own: a person or another controller set it.
	// Kueue's acknowledgment, when present, is a separate Evicted disruption.
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
	// KueueAdmissionNoDecision means Kueue has not reported an admission
	// attempt yet.
	KueueAdmissionNoDecision KueueAdmissionCode = "no_decision"
	// KueueAdmissionInsufficientQuota means Kueue reports at least one flavor
	// for every PodSet that could fit once quota is released.
	KueueAdmissionInsufficientQuota KueueAdmissionCode = "insufficient_quota"
	// KueueAdmissionExceedsQuota means Kueue reports a PodSet that no flavor
	// could fit even with all quota free: a request over the most the queue
	// or cohort can provide.
	KueueAdmissionExceedsQuota KueueAdmissionCode = "exceeds_quota"
	// KueueAdmissionNoMatchingFlavor means no ResourceFlavor matches the
	// Workload's node selectors or tolerations.
	KueueAdmissionNoMatchingFlavor KueueAdmissionCode = "no_matching_flavor"
	// KueueAdmissionTopologyPlacementFailed means topology requirements
	// could not be placed.
	KueueAdmissionTopologyPlacementFailed KueueAdmissionCode = "topology_placement_failed"
	// KueueAdmissionWaitingForPreemption means preempted Workloads have not
	// released their quota yet.
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
	// KueueAdmissionInvalidWorkload means Kueue reports a problem with the
	// Workload's own configuration: resource validation, LimitRange, usage it
	// cannot record, or another misconfiguration.
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

// KueueWorkloadClassification is the Go-side result of classifying one
// Workload, for callers that classify many Workloads without building a
// SchedulingObservation for each.
type KueueWorkloadClassification struct {
	Phase     KueuePhase
	Outcome   KueueOutcome
	Admission KueueAdmissionState
}
