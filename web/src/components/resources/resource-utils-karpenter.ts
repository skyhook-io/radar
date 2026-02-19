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
  // v1beta1: status.instanceType; v1: label or spec.requirements fallback
  return resource.status?.instanceType
    || resource.metadata?.labels?.['node.kubernetes.io/instance-type']
    || resource.spec?.requirements?.find((r: any) => r.key === 'node.kubernetes.io/instance-type')?.values?.[0]
    || '-'
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

export function getNodeClaimRequirements(resource: any): Array<{ key: string; operator: string; values: string[] }> {
  return resource.spec?.requirements || []
}

export function getNodeClaimNodeClassRef(resource: any): { group?: string; kind?: string; name?: string } | null {
  return resource.spec?.nodeClassRef || null
}

export function getNodeClaimExpireAfter(resource: any): string | undefined {
  return resource.spec?.expireAfter
}

// ============================================================================
// KARPENTER EC2NODECLASS UTILITIES
// ============================================================================

export function getEC2NodeClassStatus(resource: any): StatusBadge {
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

export function getEC2NodeClassAMI(resource: any): string {
  const terms = resource.spec?.amiSelectorTerms || []
  if (terms.length === 0) return '-'
  // Show alias if available (e.g., "al2023@latest"), otherwise show ID
  const first = terms[0]
  return first.alias || first.id || first.name || '-'
}

export function getEC2NodeClassRole(resource: any): string {
  return resource.spec?.role || '-'
}

export function getEC2NodeClassVolumeSize(resource: any): string {
  const mappings = resource.spec?.blockDeviceMappings || []
  if (mappings.length === 0) return '-'
  const first = mappings[0]
  const size = first.ebs?.volumeSize
  return size || '-'
}
