// Which Workloads belong to one Kueue queue, as Radar's
// GET /api/kueue/{clusterqueues/{name},localqueues/{namespace}/{name}}/workloads
// serializes it (internal/server/kueue_queue_workloads.go).
import type { ReadGapSet } from './read-coverage'
import type { KueueAdmissionState } from './scheduling'
import type { ResourceReadCoverage, ResourceReadErrorBody } from './resource-coverage'

export type KueueQueueMembership = 'submission' | 'reservation'

export interface KueueQueueWorkload {
  namespace: string
  name: string
  uid: string
  apiVersion: string
  createdAt: string | null
  deleting: boolean
  /** The shared classification of the Workload (RAD-811); absent when Radar doesn't classify it. */
  phase?: 'pending' | 'quota_reserved' | 'admitted' | 'finished' | (string & {})
  admission?: KueueAdmissionState
  /** Why the Workload is in this queue. */
  membership: KueueQueueMembership[]
  queueName?: string
  /** Where the Workload's LocalQueue points now, when Radar read it. Not where it pointed at submission. */
  submission?: { localQueue: string; localQueueUID: string; clusterQueue: string }
  /** Radar didn't read the LocalQueues in this Workload's namespace, so whether it was submitted here is unknown. */
  localQueueUnread?: boolean
  /** The ClusterQueue the Workload reports its admission in. */
  reservation?: { clusterQueue: string }
}

export interface KueueQueueMembers {
  items: KueueQueueWorkload[]
  /** Members the filters keep, across all pages. */
  matching: number
  /** Over every member Radar read for you; submission and reservation overlap. `states` counts, by admission state, the members the membership filter keeps. */
  counts: { members: number; submission: number; reservation: number; states: Record<string, number> }
  next?: string
  coverage: ResourceReadCoverage
  /** Namespaces whose Workloads Radar read but whose LocalQueues it didn't: only reservations place Workloads there. */
  submissionUnresolved?: ReadGapSet<string>
}

export interface KueueQueueLocalQueue {
  namespace: string
  name: string
  /** The LocalQueue's own counts as its controller reports them; null when not reported. */
  reported: { pending: number | null; reserving: number | null; admitted: number | null }
  workloadsRead: boolean
  workloadsGap?: string
}

export interface KueueQueueWorkloadsResponse {
  queue: { kind: 'ClusterQueue' | 'LocalQueue'; namespace?: string; name: string; uid: string; clusterQueue?: string }
  workloads?: KueueQueueMembers
  workloadsError?: ResourceReadErrorBody
  localQueues?: { items: KueueQueueLocalQueue[]; total: number; coverage: ResourceReadCoverage }
  localQueuesError?: ResourceReadErrorBody
}
