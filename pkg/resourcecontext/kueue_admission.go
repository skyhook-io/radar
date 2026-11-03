package resourcecontext

// KueueAdmissionState sorts what one Kueue Workload's own status reports
// into independent axes. It is not a causal diagnosis: queues, namespaces and
// AdmissionChecks appear only as the Workload names them, never read
// independently, so a reported missing LocalQueue is the Workload's claim,
// not a NotFound observed by Radar.
//
// KueueScheduling.Phase is the lifecycle axis. State is the one code to
// group or count by, resolved in this order: finished, then Inactive, then
// Blocker, then Disruption, then admitted or quota_reserved. State becomes
// stale when the evidence that decided it was written for an earlier
// metadata.generation. Inactive, Blocker and Disruption are the facts behind
// State and may be present together; each carries its own evidence.
type KueueAdmissionState struct {
	State      KueueAdmissionCode  `json:"state"`
	Inactive   *KueueAdmissionFact `json:"inactive,omitempty"`
	Blocker    *KueueAdmissionFact `json:"blocker,omitempty"`
	Disruption *KueueAdmissionFact `json:"disruption,omitempty"`
}

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
	// KueueAdmissionStale means the evidence that decided State was written
	// for an earlier generation; the facts keep what it reported.
	KueueAdmissionStale KueueAdmissionCode = "stale"
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
	// KueueAdmissionDeactivated means spec.active is false. Kueue's
	// acknowledgment, when present, is a separate Evicted disruption.
	KueueAdmissionDeactivated KueueAdmissionCode = "deactivated"
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
	// KueueAdmissionInsufficientQuota means the requests are within the
	// queue's limits but its unused quota is not enough now.
	KueueAdmissionInsufficientQuota KueueAdmissionCode = "insufficient_quota"
	// KueueAdmissionExceedsQuota means every quota attempt exceeded the most
	// the queue or cohort could ever provide.
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
	// KueueAdmissionLocalQueueStopped means the LocalQueue's stop policy holds it.
	KueueAdmissionLocalQueueStopped KueueAdmissionCode = "local_queue_stopped"
	// KueueAdmissionLocalQueueInactive means the LocalQueue is inactive for a
	// reason the Workload does not report.
	KueueAdmissionLocalQueueInactive KueueAdmissionCode = "local_queue_inactive"
	// KueueAdmissionClusterQueueMissing means the ClusterQueue does not exist
	// or is being deleted.
	KueueAdmissionClusterQueueMissing KueueAdmissionCode = "cluster_queue_missing"
	// KueueAdmissionClusterQueueStopped means the ClusterQueue's stop policy
	// holds it.
	KueueAdmissionClusterQueueStopped KueueAdmissionCode = "cluster_queue_stopped"
	// KueueAdmissionClusterQueueInactive means the ClusterQueue is inactive.
	// That can be a stop policy or broken configuration; the Workload alone
	// does not say which.
	KueueAdmissionClusterQueueInactive KueueAdmissionCode = "cluster_queue_inactive"
	// KueueAdmissionCheckRejected means an AdmissionCheck rejected the
	// Workload. By itself it does not mean the Workload was deactivated.
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
	// KueueAdmissionInvalidWorkload means Kueue reports a problem with the
	// Workload's own configuration: resources, LimitRange, DRA claims or
	// other misconfiguration.
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
