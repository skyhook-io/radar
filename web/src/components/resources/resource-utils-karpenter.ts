// Karpenter CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// KARPENTER NODEPOOL UTILITIES
// ============================================================================

export function getNodePoolStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getNodePoolNodeClassRef(resource: any): string {
  const ref = resource.spec?.template?.spec?.nodeClassRef
  if (!ref) return '-'
  return ref.name || `${ref.group}/${ref.kind}`
}

export function getNodePoolLimits(resource: any): string {
  const limits = resource.spec?.limits || {}
  const parts: string[] = []
  if (limits.cpu) parts.push(`CPU: ${limits.cpu}`)
  if (limits.memory) parts.push(`Mem: ${limits.memory}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

export function getNodePoolDisruptionPolicy(resource: any): string {
  return resource.spec?.disruption?.consolidationPolicy || 'WhenEmptyOrUnderutilized'
}

export function getNodePoolRequirements(resource: any): Array<{ key: string; operator: string; values: string[] }> {
  return resource.spec?.template?.spec?.requirements || []
}

export function getNodePoolWeight(resource: any): number | undefined {
  return resource.spec?.weight
}

// ============================================================================
// KARPENTER NODECLAIM UTILITIES
// ============================================================================

export function getNodeClaimStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  // Check conditions in priority order: Ready > Launched > Initialized
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  const launchedCond = conditions.find((c: any) => c.type === 'Launched')
  if (launchedCond?.status === 'True') {
    const registeredCond = conditions.find((c: any) => c.type === 'Registered')
    if (registeredCond?.status === 'True') {
      return { text: 'Registered', color: healthColors.degraded, level: 'degraded' }
    }
    return { text: 'Launched', color: healthColors.degraded, level: 'degraded' }
  }

  const initializedCond = conditions.find((c: any) => c.type === 'Initialized')
  if (initializedCond?.status === 'True') {
    return { text: 'Initialized', color: healthColors.degraded, level: 'degraded' }
  }

  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Pending', color: healthColors.unknown, level: 'unknown' }
}

export function getNodeClaimInstanceType(resource: any): string {
  return resource.status?.instanceType || resource.spec?.instanceType || '-'
}

export function getNodeClaimNodeName(resource: any): string {
  return resource.status?.nodeName || '-'
}

export function getNodeClaimCapacity(resource: any): Record<string, string> {
  return resource.status?.capacity || {}
}

export function getNodeClaimNodePoolRef(resource: any): string {
  return resource.metadata?.labels?.['karpenter.sh/nodepool'] || '-'
}
