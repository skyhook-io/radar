export interface SchedulingRef {
  kind: string
  group?: string
  namespace?: string
  name: string
}

export interface SchedulingCondition {
  type: string
  status: string
  reason?: string
  message?: string
  observedGeneration?: number
  lastTransitionTime?: string
}

export type SchedulingDecision = 'satisfied' | 'unsatisfied' | 'held' | 'unknown'

// Matches the shared Go scheduling projection; admission UI consumes this subset.
export interface SchedulingObservation {
  source: string
  domain: string
  subject: SchedulingRef
  subjectGeneration?: number
  decision: SchedulingDecision
  primaryCondition?: SchedulingCondition
  queues?: { name: string; roles: string[]; ref?: SchedulingRef }[]
  gates?: {
    kind: string
    name: string
    ref?: SchedulingRef
    nativeState?: string
    decision: SchedulingDecision
    message?: string
    lastTransitionTime?: string
    requeueAfterSeconds?: number
    retryCount?: number
  }[]
  disruptions?: SchedulingCondition[]
  kueue?: {
    phase: 'pending' | 'quota_reserved' | 'admitted' | 'finished'
    outcome?: 'succeeded' | 'failed'
    active?: boolean
    podsReady?: SchedulingCondition
    waitingForReplacementPods?: SchedulingCondition
    requeueState?: { count?: number; requeueAt?: string }
    concurrentAdmission?: { parentName: string; parentRef?: SchedulingRef }
    admission?: KueueAdmissionState
  }
}

// Mirrors resourcecontext.KueueAdmissionFact (see its Go doc for the
// evidence contract: message is bounded display text, an absent stale means
// not known to be stale).
export interface KueueAdmissionFact {
  code: string
  // Open like code: a newer Radar may send a source this UI doesn't know.
  source?: 'condition' | 'admission_check' | 'spec' | (string & {})
  name?: string
  reason?: string
  message?: string
  since?: string
  stale?: boolean
}

// Mirrors resourcecontext.KueueAdmissionState. The server is the only place
// codes and attention are decided; a code this UI does not know still renders
// verbatim. A missing fact is not evidence on its own (see the Go doc for
// which facts each state reports), and a missing admission block means an
// older Radar, not a Workload with no blocker.
export interface KueueAdmissionState {
  state: string
  attention: string
  stale?: boolean
  inactive?: KueueAdmissionFact
  blocker?: KueueAdmissionFact
  disruption?: KueueAdmissionFact
}

export interface KueueAdmissionWorkload {
  apiVersion: string
  namespace: string
  name: string
  uid: string
  generation: number
  createdAt: string | null
  deleting: boolean
  ref?: SchedulingRef
  projection: 'available' | 'unsupported' | 'forbidden'
  scheduling?: { observations?: SchedulingObservation[] }
  linksLimited: boolean
}

export interface KueueAdmissionResponse {
  uid: string
  installed: boolean
  workloads: KueueAdmissionWorkload[]
  total: number
  truncated: boolean
}
