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
  /** The Workload's shared admission classification (schedulinginsight.ClassifyKueueWorkload); absent when Radar doesn't classify it. */
  phase?: 'pending' | 'quota_reserved' | 'admitted' | 'finished' | (string & {})
  admission?: KueueAdmissionState
  /** Why the Workload is in this queue. */
  membership: KueueQueueMembership[]
  queueName?: string
  /**
   * Where the Workload's LocalQueue points now, when Radar read it; not where it
   * pointed at submission. Absent when there is no queueName, the LocalQueue
   * doesn't exist, or it wasn't read (then `localQueueUnread` is true).
   */
  submission?: { localQueue: string; localQueueUID: string; clusterQueue: string }
  /** Radar didn't read the LocalQueues in this Workload's namespace, so whether it was submitted here is unknown. Absent means false. */
  localQueueUnread?: boolean
  /** The ClusterQueue the Workload reports its admission in. */
  reservation?: { clusterQueue: string }
}

/** One page of a queue's Workloads. Every count describes all members Radar read for you (not just this page), as of this page's read. */
export interface KueueQueueMembers {
  items: KueueQueueWorkload[]
  /** Members the filters keep, across all pages. */
  matching: number
  /**
   * Over every member Radar read for you; submission and reservation overlap
   * (ClusterQueue only: a LocalQueue's members all belong by submission, so its
   * reservation count is 0). `states` counts, by admission state, the members
   * the membership filter keeps.
   */
  counts: { members: number; submission: number; reservation: number; states: Record<string, number> }
  next?: string
  /**
   * The Workload read alone. `complete` doesn't make the membership complete:
   * see `submissionUnresolved`.
   */
  coverage: ResourceReadCoverage
  /**
   * ClusterQueue only. Namespaces whose Workloads Radar read but whose
   * LocalQueues it didn't: only reservations place Workloads there, so while
   * set, `counts.members`, `counts.submission` and `matching` (unless filtered
   * to reservation) are lower bounds. Absent: no such namespace.
   */
  submissionUnresolved?: ReadGapSet<string>
}

export interface KueueQueueLocalQueue {
  namespace: string
  name: string
  /** The LocalQueue's own counts as its controller reports them; null when not reported. */
  reported: { pending: number | null; reserving: number | null; admitted: number | null }
  /** Radar read Workloads in this LocalQueue's namespace for you. When false, `workloadsGap` is the pkg/readreason code, when Radar knows it. */
  workloadsRead: boolean
  workloadsGap?: string
}

/**
 * Each part is present or its error is: `workloads` or `workloadsError`;
 * `localQueues` or `localQueuesError` for a ClusterQueue's first page only
 * (later pages and LocalQueue responses carry neither, except an error).
 */
export interface KueueQueueWorkloadsResponse {
  queue: { kind: 'ClusterQueue' | 'LocalQueue'; namespace?: string; name: string; uid: string; clusterQueue?: string }
  workloads?: KueueQueueMembers
  workloadsError?: ResourceReadErrorBody
  /** At most 500 items; `total` counts every LocalQueue found. Not paged. */
  localQueues?: { items: KueueQueueLocalQueue[]; total: number; coverage: ResourceReadCoverage }
  localQueuesError?: ResourceReadErrorBody
}
