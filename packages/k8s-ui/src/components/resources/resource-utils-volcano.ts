// Volcano CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function summarizeResourceList(list: any): string {
  if (!list || typeof list !== 'object') return '-'
  const parts: string[] = []
  if (list.cpu !== undefined) parts.push(`CPU: ${list.cpu}`)
  if (list.memory !== undefined) parts.push(`Mem: ${list.memory}`)
  if (list['nvidia.com/gpu'] !== undefined) parts.push(`GPU: ${list['nvidia.com/gpu']}`)
  const rest = Object.keys(list).filter((k) => k !== 'cpu' && k !== 'memory' && k !== 'nvidia.com/gpu')
  if (rest.length > 0) parts.push(`+${rest.length} more`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

function summarizeCounts(entries: Array<[string, number]>): string {
  const parts = entries.filter(([, count]) => count > 0).map(([label, count]) => `${count} ${label}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

// ============================================================================
// VOLCANO JOB UTILITIES (batch.volcano.sh/v1alpha1)
// ============================================================================

export function getVolcanoJobStatus(resource: any): StatusBadge {
  const phase = resource.status?.state?.phase
  switch (phase) {
    case 'Running':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'Pending':
    case 'Completing':
    case 'Completed':
      return { text: phase, color: healthColors.neutral, level: 'neutral' }
    case 'Failed':
    case 'Aborted':
    case 'Terminated':
      return { text: phase, color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Aborting':
    case 'Restarting':
    case 'Terminating':
      return { text: phase, color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getVolcanoJobQueue(resource: any): string {
  return resource.spec?.queue || 'default'
}

export function getVolcanoJobMinAvailable(resource: any): string {
  const min = resource.spec?.minAvailable ?? resource.status?.minAvailable
  return min !== undefined && min !== null ? String(min) : '-'
}

export function getVolcanoJobPodCounts(resource: any): string {
  const status = resource.status || {}
  return summarizeCounts([
    ['running', status.running || 0],
    ['succeeded', status.succeeded || 0],
    ['failed', status.failed || 0],
  ])
}

// ============================================================================
// VOLCANO QUEUE UTILITIES (scheduling.volcano.sh/v1beta1, cluster-scoped)
// ============================================================================

export function getVolcanoQueueStatus(resource: any): StatusBadge {
  const state = resource.status?.state
  switch (state) {
    case 'Open':
      return { text: 'Open', color: healthColors.healthy, level: 'healthy' }
    case 'Closed':
      return { text: 'Closed', color: healthColors.neutral, level: 'neutral' }
    case 'Closing':
      return { text: 'Closing', color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: state || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getVolcanoQueueWeight(resource: any): string {
  const weight = resource.spec?.weight
  return weight !== undefined && weight !== null ? String(weight) : '1'
}

export function getVolcanoQueueCapability(resource: any): string {
  return summarizeResourceList(resource.spec?.capability)
}

export function getVolcanoQueueAllocated(resource: any): string {
  return summarizeResourceList(resource.status?.allocated)
}

export function getVolcanoQueuePodGroupCounts(resource: any): string {
  const status = resource.status || {}
  return summarizeCounts([
    ['running', status.running || 0],
    ['inqueue', status.inqueue || 0],
    ['pending', status.pending || 0],
    ['completed', status.completed || 0],
    ['unknown', status.unknown || 0],
  ])
}

// ============================================================================
// VOLCANO PODGROUP UTILITIES (scheduling.volcano.sh/v1beta1)
// ============================================================================

export function getVolcanoPodGroupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase
  if (phase === 'Running') {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  const conditions = resource.status?.conditions || []
  const unschedulable = conditions.some((c: any) => c.type === 'Unschedulable' && c.status === 'True')
  if (unschedulable) {
    return { text: 'Unschedulable', color: healthColors.alert, level: 'alert' }
  }
  if (phase === 'Pending' || phase === 'Inqueue' || phase === 'Completed') {
    return { text: phase, color: healthColors.neutral, level: 'neutral' }
  }
  return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getVolcanoPodGroupQueue(resource: any): string {
  return resource.spec?.queue || 'default'
}

export function getVolcanoPodGroupMinMember(resource: any): string {
  const min = resource.spec?.minMember
  return min !== undefined && min !== null ? String(min) : '-'
}

// ============================================================================
// VOLCANO JOBFLOW UTILITIES (flow.volcano.sh/v1alpha1)
// ============================================================================

export function getJobFlowStatus(resource: any): StatusBadge {
  const phase = resource.status?.state?.phase
  switch (phase) {
    case 'Running':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'Succeed':
    case 'Pending':
      return { text: phase, color: healthColors.neutral, level: 'neutral' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Terminating':
      return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getJobFlowFlowCount(resource: any): number {
  return (resource.spec?.flows || []).length
}

// ============================================================================
// VOLCANO JOBTEMPLATE UTILITIES (flow.volcano.sh/v1alpha1)
// ============================================================================

export function getJobTemplateStatus(_resource: any): StatusBadge {
  return { text: 'Template', color: healthColors.neutral, level: 'neutral' }
}

export function getJobTemplateTaskCount(resource: any): number {
  return (resource.spec?.tasks || []).length
}
