// KEDA CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function extractTriggerTypes(resource: any): string {
  const triggers = resource.spec?.triggers || []
  if (triggers.length === 0) return '-'
  const types = [...new Set(triggers.map((t: any) => t.type).filter(Boolean))] as string[]
  if (types.length > 3) return `${types.slice(0, 3).join(', ')} +${types.length - 3}`
  return types.join(', ') || '-'
}

// ============================================================================
// KEDA SCALEDOBJECT UTILITIES
// ============================================================================

export function getScaledObjectStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  // Check for paused state (3 annotation variants)
  const annotations = resource.metadata?.annotations || {}
  const isPaused = annotations['autoscaling.keda.sh/paused'] === 'true' ||
    annotations['autoscaling.keda.sh/paused-replicas'] !== undefined ||
    conditions.some((c: any) => c.type === 'Paused' && c.status === 'True')

  if (isPaused) {
    return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
  }

  // Check Fallback condition
  const fallbackCond = conditions.find((c: any) => c.type === 'Fallback')
  if (fallbackCond?.status === 'True') {
    return { text: 'Fallback', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Check Ready condition
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Check Active condition
  const activeCond = conditions.find((c: any) => c.type === 'Active')
  if (activeCond?.status === 'True') {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }
  if (activeCond?.status === 'False') {
    return { text: 'Idle', color: healthColors.degraded, level: 'degraded' }
  }

  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getScaledObjectTarget(resource: any): string {
  const ref = resource.spec?.scaleTargetRef
  if (!ref) return '-'
  return `${ref.kind || 'Deployment'}/${ref.name}`
}

export function getScaledObjectTargetKind(resource: any): string {
  return resource.spec?.scaleTargetRef?.kind || 'Deployment'
}

export function getScaledObjectTargetName(resource: any): string {
  return resource.spec?.scaleTargetRef?.name || '-'
}

export function getScaledObjectReplicas(resource: any): string {
  const min = resource.spec?.minReplicaCount ?? 0
  const max = resource.spec?.maxReplicaCount ?? '-'
  const idle = resource.spec?.idleReplicaCount
  const parts = [`${min}-${max}`]
  if (idle !== undefined) parts.push(`idle: ${idle}`)
  return parts.join(' ')
}

export function getScaledObjectTriggers(resource: any): Array<{ type: string; name?: string; metadata?: Record<string, string>; authenticationRef?: { name: string; kind?: string } }> {
  return (resource.spec?.triggers || []).map((t: any) => ({
    type: t.type,
    name: t.name,
    metadata: t.metadata,
    ...(t.authenticationRef ? { authenticationRef: { name: t.authenticationRef.name, kind: t.authenticationRef.kind } } : {}),
  }))
}

export function getScaledObjectTriggerTypes(resource: any): string {
  return extractTriggerTypes(resource)
}

export function getScaledObjectTriggerCount(resource: any): number {
  return (resource.spec?.triggers || []).length
}

export function getScaledObjectHpaName(resource: any): string {
  return resource.status?.hpaName || '-'
}

export function getScaledObjectLastActiveTime(resource: any): string {
  const lastActive = resource.status?.lastActiveTime
  if (!lastActive) return '-'
  return formatAge(lastActive)
}

export function getScaledObjectPollingInterval(resource: any): number {
  return resource.spec?.pollingInterval ?? 30
}

export function getScaledObjectCooldownPeriod(resource: any): number {
  return resource.spec?.cooldownPeriod ?? 300
}

// ============================================================================
// KEDA SCALEDJOB UTILITIES
// ============================================================================

export function getScaledJobStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  const activeCond = conditions.find((c: any) => c.type === 'Active')
  if (activeCond?.status === 'True') {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }
  if (activeCond?.status === 'False') {
    return { text: 'Idle', color: healthColors.degraded, level: 'degraded' }
  }

  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getScaledJobTarget(resource: any): string {
  return resource.spec?.jobTargetRef?.name || '-'
}

export function getScaledJobStrategy(resource: any): string {
  return resource.spec?.scalingStrategy?.strategy || 'default'
}

export function getScaledJobTriggerCount(resource: any): number {
  return (resource.spec?.triggers || []).length
}

export function getScaledJobTriggers(resource: any): Array<{ type: string; name?: string; metadata?: Record<string, string>; authenticationRef?: { name: string; kind?: string } }> {
  return (resource.spec?.triggers || []).map((t: any) => ({
    type: t.type,
    name: t.name,
    metadata: t.metadata,
    ...(t.authenticationRef ? { authenticationRef: { name: t.authenticationRef.name, kind: t.authenticationRef.kind } } : {}),
  }))
}

export function getScaledJobTriggerTypes(resource: any): string {
  return extractTriggerTypes(resource)
}

// ============================================================================
// KEDA TRIGGERAUTHENTICATION UTILITIES
// ============================================================================

export function getTriggerAuthSecretRefCount(resource: any): number {
  return (resource.spec?.secretTargetRef || []).length
}

export function getTriggerAuthEnvCount(resource: any): number {
  return (resource.spec?.env || []).length
}

export function getTriggerAuthHasVault(resource: any): boolean {
  return !!resource.spec?.hashiCorpVault
}

export function getTriggerAuthSecretRefs(resource: any): Array<{ parameter: string; name: string; key: string }> {
  return (resource.spec?.secretTargetRef || []).map((r: any) => ({
    parameter: r.parameter || '',
    name: r.name || '',
    key: r.key || '',
  }))
}

export function getTriggerAuthEnvVars(resource: any): Array<{ parameter: string; name: string; containerName?: string }> {
  return (resource.spec?.env || []).map((e: any) => ({
    parameter: e.parameter || '',
    name: e.name || '',
    containerName: e.containerName,
  }))
}
