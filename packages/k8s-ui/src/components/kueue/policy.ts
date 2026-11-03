// Plain-language meanings for Kueue queue policy values, one dictionary per
// served API version. Wording follows the CRD field descriptions; a value a
// dictionary doesn't list is shown raw, never mapped to a nearby meaning.

export type KueuePolicyField =
  | 'stopPolicy'
  | 'queueingStrategy'
  | 'preemption.withinClusterQueue'
  | 'preemption.reclaimWithinCohort'
  | 'preemption.borrowWithinCohort.policy'
  | 'flavorFungibility.whenCanBorrow'
  | 'flavorFungibility.whenCanPreempt'
  | 'flavorFungibility.preference'
  | 'admissionScope.admissionMode'

type Dictionary = Partial<Record<KueuePolicyField, Record<string, string>>>

const shared: Dictionary = {
  stopPolicy: {
    None: 'Not stopped: new reservations can be made.',
    Hold: 'Stopped: no new reservations; reservations are cancelled for Workloads not yet admitted, and admitted Workloads run to completion.',
    HoldAndDrain: 'Stopped and draining: no new reservations; reservations are cancelled and admitted Workloads are evicted.',
  },
  queueingStrategy: {
    StrictFIFO: 'Strictly by creation time: an older Workload that can’t be admitted blocks newer ones, even if they fit.',
    BestEffortFIFO: 'By creation time, but an older Workload that can’t be admitted doesn’t block newer ones that fit.',
  },
  'preemption.withinClusterQueue': {
    Never: 'A pending Workload never preempts Workloads in this ClusterQueue.',
    LowerPriority: 'A pending Workload that doesn’t fit nominal quota may preempt lower-priority Workloads in this ClusterQueue.',
    LowerOrNewerEqualPriority: 'A pending Workload may preempt Workloads in this ClusterQueue with lower priority, or equal priority and newer.',
  },
  'preemption.reclaimWithinCohort': {
    Never: 'Never preempts Workloads of other ClusterQueues in the cohort.',
    LowerPriority: 'May preempt lower-priority Workloads of cohort ClusterQueues that use more than their nominal quota.',
    Any: 'May preempt Workloads of cohort ClusterQueues that use more than their nominal quota, regardless of priority.',
  },
  'preemption.borrowWithinCohort.policy': {
    Never: 'A Workload that must borrow never preempts in other cohort ClusterQueues.',
    LowerPriority: 'A Workload that must borrow may preempt lower-priority Workloads in other cohort ClusterQueues.',
  },
  'flavorFungibility.preference': {
    BorrowingOverPreemption: 'When every flavor needs borrowing or preemption, prefer borrowing.',
    PreemptionOverBorrowing: 'When every flavor needs borrowing or preemption, prefer preemption.',
  },
}

const admissionMode = {
  UsageBasedAdmissionFairSharing: 'Admission fair sharing: Workloads are ordered by their LocalQueue’s recent resource usage.',
  NoAdmissionFairSharing: 'Admission fair sharing is off for this ClusterQueue.',
}

const mayStopSearchBorrow = 'Stop at the first flavor where the Workload fits or fits by borrowing.'
const mayStopSearchPreempt = 'Stop at the first flavor where the Workload fits or fits by preempting.'

const v1beta1: Dictionary = {
  ...shared,
  'admissionScope.admissionMode': admissionMode,
  'flavorFungibility.whenCanBorrow': {
    MayStopSearch: mayStopSearchBorrow,
    TryNextFlavor: 'Try the next flavor before borrowing in this one.',
    Borrow: `${mayStopSearchBorrow} (Deprecated spelling of MayStopSearch.)`,
  },
  'flavorFungibility.whenCanPreempt': {
    MayStopSearch: mayStopSearchPreempt,
    TryNextFlavor: 'Try the next flavor before preempting in this one.',
    Preempt: `${mayStopSearchPreempt} (Deprecated spelling of MayStopSearch.)`,
  },
}

const v1beta2: Dictionary = {
  ...shared,
  'flavorFungibility.whenCanBorrow': {
    MayStopSearch: mayStopSearchBorrow,
    TryNextFlavor: 'Try the next flavor before borrowing in this one.',
  },
  'flavorFungibility.whenCanPreempt': {
    MayStopSearch: mayStopSearchPreempt,
    TryNextFlavor: 'Try the next flavor before preempting in this one.',
  },
  'admissionScope.admissionMode': admissionMode,

}

const dictionaries: Record<string, Dictionary> = {
  'kueue.x-k8s.io/v1beta1': v1beta1,
  'kueue.x-k8s.io/v1beta2': v1beta2,
}

export interface KueuePolicyValue {
  raw: string
  /** Absent when this Radar has no meaning for the value at this API version. */
  meaning?: string
}

export function describeKueuePolicy(apiVersion: string | undefined, field: KueuePolicyField, value: unknown): KueuePolicyValue {
  const raw = String(value)
  const meaning = dictionaries[apiVersion ?? '']?.[field]?.[raw]
  return meaning ? { raw, meaning } : { raw }
}
