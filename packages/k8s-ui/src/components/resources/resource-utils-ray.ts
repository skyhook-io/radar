// KubeRay CRD utility functions (ray.io/v1)

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge } from './resource-utils'

// ============================================================================
// RAYCLUSTER UTILITIES
// ============================================================================

export function getRayClusterStatus(resource: any): StatusBadge {
  const state = resource.status?.state
  const conditions = resource.status?.conditions || []

  const suspendingCond = conditions.find((c: any) => c.type === 'RayClusterSuspending')
  if (suspendingCond?.status === 'True') {
    return { text: 'Suspending', color: healthColors.degraded, level: 'degraded' }
  }

  const suspendedCond = conditions.find((c: any) => c.type === 'RayClusterSuspended')
  if (suspendedCond?.status === 'True') {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  if (resource.spec?.suspend === true || state === 'suspended') {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  const replicaFailureCond = conditions.find((c: any) => c.type === 'RayClusterReplicaFailure')
  if (replicaFailureCond?.status === 'True') {
    return { text: replicaFailureCond.reason || 'ReplicaFailure', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  if (state === 'failed') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const headReadyCond = conditions.find((c: any) => c.type === 'HeadPodReady')
  if (headReadyCond?.status === 'False') {
    return { text: headReadyCond.reason || 'HeadNotReady', color: healthColors.degraded, level: 'degraded' }
  }
  if (headReadyCond?.status === 'True') {
    const availableWorkers = resource.status?.availableWorkerReplicas ?? 0
    const desiredWorkers = resource.status?.desiredWorkerReplicas ?? 0
    if (availableWorkers >= desiredWorkers) {
      return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
    }
    return { text: `Workers ${availableWorkers}/${desiredWorkers}`, color: healthColors.degraded, level: 'degraded' }
  }

  if (conditions.length === 0 && state === 'ready') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  const provisionedCond = conditions.find((c: any) => c.type === 'RayClusterProvisioned')
  if (provisionedCond?.status === 'True') {
    return { text: 'Provisioned', color: healthColors.neutral, level: 'neutral' }
  }

  if (resource.status) {
    return { text: 'Provisioning', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRayClusterVersion(resource: any): string {
  return resource.spec?.rayVersion || '-'
}

export function getRayClusterWorkers(resource: any): string {
  const status = resource.status
  if (!status) return '-'
  const available = status.availableWorkerReplicas ?? 0
  const desired = status.desiredWorkerReplicas ?? 0
  return `${available}/${desired}`
}

export function getRayClusterHeadService(resource: any): string {
  return resource.status?.head?.serviceName || '-'
}

// ============================================================================
// RAYJOB UTILITIES
// ============================================================================

export function getRayJobStatus(resource: any): StatusBadge {
  const jobStatus = resource.status?.jobStatus
  const deploymentStatus = resource.status?.jobDeploymentStatus

  if (deploymentStatus === 'Failed' || deploymentStatus === 'ValidationFailed') {
    return { text: deploymentStatus, color: healthColors.unhealthy, level: 'unhealthy' }
  }

  switch (jobStatus) {
    case 'SUCCEEDED':
      return { text: 'Succeeded', color: healthColors.neutral, level: 'neutral' }
    case 'RUNNING':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'FAILED':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'STOPPED':
      return { text: 'Stopped', color: healthColors.neutral, level: 'neutral' }
    case 'PENDING':
      return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }

  switch (deploymentStatus) {
    case 'Suspended':
      return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
    case 'Complete':
      return { text: 'Complete', color: healthColors.neutral, level: 'neutral' }
    case 'Running':
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    case 'Initializing':
    case 'Suspending':
    case 'Retrying':
    case 'Waiting':
      return { text: deploymentStatus, color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRayJobJobStatus(resource: any): string {
  return resource.status?.jobStatus || '-'
}

export function getRayJobDeploymentStatus(resource: any): string {
  return resource.status?.jobDeploymentStatus || '-'
}

export function getRayJobClusterName(resource: any): string {
  return resource.status?.rayClusterName || '-'
}

// ============================================================================
// RAYSERVICE UTILITIES
// ============================================================================

export function getRayServiceStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const suspendingCond = conditions.find((c: any) => c.type === 'Suspending')
  if (suspendingCond?.status === 'True') {
    return { text: 'Suspending', color: healthColors.degraded, level: 'degraded' }
  }

  const suspendedCond = conditions.find((c: any) => c.type === 'Suspended')
  if (suspendedCond?.status === 'True' || resource.spec?.suspend === true) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  const upgradeCond = conditions.find((c: any) => c.type === 'UpgradeInProgress')
  if (upgradeCond?.status === 'True') {
    return { text: 'Upgrading', color: healthColors.degraded, level: 'degraded' }
  }

  const rollbackCond = conditions.find((c: any) => c.type === 'RollbackInProgress')
  if (rollbackCond?.status === 'True') {
    return { text: 'RollingBack', color: healthColors.degraded, level: 'degraded' }
  }

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.degraded, level: 'degraded' }
  }

  if (resource.status?.serviceStatus === 'Running') {
    return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRayServiceServiceStatus(resource: any): string {
  return resource.status?.serviceStatus || '-'
}

export function getRayServiceClusters(resource: any): string {
  const active = resource.status?.activeServiceStatus?.rayClusterName
  const pending = resource.status?.pendingServiceStatus?.rayClusterName
  const parts: string[] = []
  if (active) parts.push(active)
  if (pending) parts.push(`pending: ${pending}`)
  return parts.join(' ') || '-'
}

// ============================================================================
// RAYCRONJOB UTILITIES
// ============================================================================

export function getRayCronJobStatus(resource: any): StatusBadge {
  if (resource.spec?.suspend === true) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Active', color: healthColors.neutral, level: 'neutral' }
}

export function getRayCronJobSchedule(resource: any): string {
  return resource.spec?.schedule || '-'
}

export function getRayCronJobSuspend(resource: any): boolean {
  return resource.spec?.suspend === true
}

export function getRayCronJobLastSchedule(resource: any): string {
  const lastSchedule = resource.status?.lastScheduleTime
  if (!lastSchedule) return '-'
  return formatAge(lastSchedule)
}
