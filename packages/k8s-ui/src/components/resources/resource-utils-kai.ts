// KAI Scheduler CRD utility functions

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

// KAI quota units: GPU in fractions, CPU in millicpus, memory in megabytes; -1 means unlimited
function formatKaiQuota(value: any, unit: string): string {
  if (typeof value !== 'number') return '-'
  if (value === -1) return 'unlimited'
  return `${value}${unit}`
}

// ============================================================================
// KAI QUEUE UTILITIES (scheduling.run.ai/v2, cluster-scoped)
// ============================================================================

export function getKaiQueueStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  const orphan = conditions.find((condition: any) => condition?.type === 'Orphan' && condition?.status === 'True')
  if (orphan) {
    return { text: orphan.reason || 'Orphan', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  const overQuota = conditions.find((condition: any) => condition?.type === 'OverQuota' && condition?.status === 'True')
  if (overQuota) {
    return { text: overQuota.reason || 'Over quota', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Configured', color: healthColors.neutral, level: 'neutral' }
}

export function getKaiQueueParent(resource: any): string {
  return resource.spec?.parentQueue || '-'
}

export function getKaiQueuePriority(resource: any): string {
  const priority = resource.spec?.priority
  return priority !== undefined && priority !== null ? String(priority) : '100'
}

export function getKaiQueueQuota(resource: any): string {
  const resources = resource.spec?.resources
  if (!resources) return '-'
  const parts: string[] = []
  if (typeof resources.gpu?.quota === 'number') parts.push(`GPU: ${formatKaiQuota(resources.gpu.quota, '')}`)
  if (typeof resources.cpu?.quota === 'number') parts.push(`CPU: ${formatKaiQuota(resources.cpu.quota, 'm')}`)
  if (typeof resources.memory?.quota === 'number') parts.push(`Mem: ${formatKaiQuota(resources.memory.quota, 'MB')}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

export function getKaiQueueAllocated(resource: any): string {
  return summarizeResourceList(resource.status?.allocated)
}

// ============================================================================
// KAI PODGROUP UTILITIES (scheduling.run.ai/v2alpha2)
// ============================================================================

export function getKaiPodGroupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase
  if (phase === 'Running') {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  const conditions = resource.status?.conditions || []
  const schedulingConditions = resource.status?.schedulingConditions || []
  const unschedulable =
    conditions.some((c: any) => c.type === 'Unschedulable' && c.status === 'True') ||
    schedulingConditions.some((c: any) => c.type === 'UnschedulableOnNodePool' && c.status === 'True')
  if (unschedulable) {
    return { text: 'Unschedulable', color: healthColors.alert, level: 'alert' }
  }
  if (phase === 'Pending' || phase === 'Inqueue' || phase === 'Completed' || phase === 'Succeeded') {
    return { text: phase, color: healthColors.neutral, level: 'neutral' }
  }
  if (phase === 'Failed') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  // OSS KAI components never write status.phase (Run:ai platform does); fall back to pod counts
  if (!phase && (resource.status?.running || 0) > 0) {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getKaiPodGroupQueue(resource: any): string {
  return resource.spec?.queue || '-'
}

export function getKaiPodGroupMinMember(resource: any): string {
  const min = resource.spec?.minMember
  return min !== undefined && min !== null ? String(min) : '-'
}
