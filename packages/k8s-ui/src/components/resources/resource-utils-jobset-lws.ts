// LeaderWorkerSet (leaderworkerset.x-k8s.io/v1) and JobSet (jobset.x-k8s.io/v1alpha2) utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// LEADERWORKERSET UTILITIES
// ============================================================================

export function getLeaderWorkerSetStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const availableCond = conditions.find((c: any) => c.type === 'Available')
  if (availableCond?.status === 'True') {
    return { text: 'Available', color: healthColors.healthy, level: 'healthy' }
  }

  const updateCond = conditions.find((c: any) => c.type === 'UpdateInProgress')
  if (updateCond?.status === 'True') {
    return { text: 'Updating', color: healthColors.degraded, level: 'degraded' }
  }

  const progressingCond = conditions.find((c: any) => c.type === 'Progressing')
  if (progressingCond?.status === 'True') {
    return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
  }

  if (availableCond?.status === 'False') {
    return { text: availableCond.reason || 'Unavailable', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getLeaderWorkerSetReplicas(resource: any): string {
  const replicas = resource.spec?.replicas
  return replicas !== undefined && replicas !== null ? String(replicas) : '1'
}

export function getLeaderWorkerSetSize(resource: any): string {
  const size = resource.spec?.leaderWorkerTemplate?.size
  return size !== undefined && size !== null ? String(size) : '1'
}

export function getLeaderWorkerSetReady(resource: any): string {
  const ready = resource.status?.readyReplicas ?? 0
  const desired = resource.spec?.replicas ?? 1
  return `${ready}/${desired}`
}

export function getLeaderWorkerSetUpdated(resource: any): string {
  const updated = resource.status?.updatedReplicas
  return updated !== undefined && updated !== null ? String(updated) : '0'
}

// ============================================================================
// JOBSET UTILITIES
// ============================================================================

export function getJobSetStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const failedCond = conditions.find((c: any) => c.type === 'Failed')
  if (failedCond?.status === 'True') {
    return { text: failedCond.reason || 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const completedCond = conditions.find((c: any) => c.type === 'Completed')
  if (completedCond?.status === 'True') {
    return { text: 'Completed', color: healthColors.neutral, level: 'neutral' }
  }

  const suspendedCond = conditions.find((c: any) => c.type === 'Suspended')
  if (suspendedCond?.status === 'True' || resource.spec?.suspend === true) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  // Running only when child jobs are actually live — a fresh JobSet carries a
  // minimal status (no conditions, zeroed counts) before reconciliation.
  const live = sumReplicatedJobsField(resource, 'active') + sumReplicatedJobsField(resource, 'ready')
  if (live > 0) {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  if (resource.status) {
    return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getJobSetReplicatedJobs(resource: any): string {
  const jobs = resource.spec?.replicatedJobs
  if (!Array.isArray(jobs) || jobs.length === 0) return '-'
  return String(jobs.length)
}

function sumReplicatedJobsField(resource: any, field: string): number {
  const statuses = resource.status?.replicatedJobsStatus
  if (!Array.isArray(statuses)) return 0
  return statuses.reduce((sum: number, s: any) => sum + (s?.[field] ?? 0), 0)
}

export function getJobSetReadyJobs(resource: any): number {
  return sumReplicatedJobsField(resource, 'ready')
}

export function getJobSetSucceededJobs(resource: any): number {
  return sumReplicatedJobsField(resource, 'succeeded')
}

export function getJobSetFailedJobs(resource: any): number {
  return sumReplicatedJobsField(resource, 'failed')
}

export function getJobSetRestarts(resource: any): number {
  return resource.status?.restarts ?? 0
}
