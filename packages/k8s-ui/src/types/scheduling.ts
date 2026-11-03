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
    /** Kueue's reported pending position; absent when Radar didn't ask. */
    pendingPosition?: KueuePendingPosition
  }
}

/**
 * Where Kueue's visibility API placed one pending Workload when Radar read it,
 * as the viewer (pkg/resourcecontext/kueue_position.go). Never derived by Radar.
 */
export type KueuePendingPositionStatus =
  | 'listed'
  | 'not_listed'
  | 'limit_reached'
  | 'queue_unknown'
  | 'no_local_queue'
  | 'not_served'
  | 'unsupported'
  | 'unavailable'

export interface KueuePendingPosition {
  status: KueuePendingPositionStatus | (string & {})
  /** listed only: Workloads Kueue orders before this one (Kueue's 0-based position). */
  aheadInLocalQueue?: number
  aheadInClusterQueue?: number
  localQueue?: string
  /** Rows Kueue returned; set whenever a read succeeded. */
  rows?: number
  rowLimit?: number
  apiVersion?: string
  /** When Radar made the read (RFC 3339). */
  readAt?: string
  /** pkg/readreason code; unavailable only. */
  code?: string
  message?: string
}

/** GET /api/kueue/position/{namespace}/{name} */
export interface KueuePositionResponse {
  uid: string
  /** Kueue holds the Workload in its pending queues, judged from Radar's copy. */
  eligible: boolean
  position?: KueuePendingPosition
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
