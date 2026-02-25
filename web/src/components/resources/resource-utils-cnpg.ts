// CloudNativePG CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge, formatDuration } from './resource-utils'

// ============================================================================
// CNPG CLUSTER UTILITIES
// ============================================================================

export function getCNPGClusterStatus(resource: any): StatusBadge {
  const status = resource.status || {}
  const phase = status.phase || ''
  const instances = resource.spec?.instances ?? 0
  const readyInstances = status.readyInstances ?? 0

  // Check for degraded instances first
  if (instances > 0 && readyInstances < instances) {
    // Distinguish between total failure and partial degradation
    if (readyInstances === 0) {
      return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
    }
    return { text: 'Degraded', color: healthColors.degraded, level: 'degraded' }
  }

  // Phase-based status
  const healthyPhases = ['Cluster in healthy state', 'Healthy']
  const transientPhases = [
    'Setting up primary',
    'Creating replica',
    'Switchover in progress',
    'Upgrading cluster',
    'Online upgrade in progress',
  ]
  const unhealthyPhases = [
    'Failing over',
    'Failing over (streaming)',
    'Failing over (designated primary)',
  ]

  if (healthyPhases.some(p => phase.includes(p))) {
    return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
  }
  if (unhealthyPhases.some(p => phase.includes(p))) {
    return { text: 'Failing Over', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (transientPhases.some(p => phase.includes(p))) {
    return { text: phase, color: healthColors.degraded, level: 'degraded' }
  }

  // Conditions fallback
  const conditions = status.conditions || []
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  if (phase) {
    return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getCNPGClusterInstances(resource: any): string {
  const desired = resource.spec?.instances ?? 0
  const ready = resource.status?.readyInstances ?? 0
  return `${ready}/${desired}`
}

export function getCNPGClusterPrimary(resource: any): string {
  return resource.status?.currentPrimary || resource.status?.targetPrimary || '-'
}

export function getCNPGClusterPhase(resource: any): string {
  return resource.status?.phase || '-'
}

export function getCNPGClusterImage(resource: any): string {
  return resource.spec?.imageName || '-'
}

export function getCNPGClusterImageTag(resource: any): string {
  const image = resource.spec?.imageName || ''
  if (!image) return '-'
  const parts = image.split(':')
  if (parts.length > 1) return parts[parts.length - 1]
  return image.split('/').pop() || '-'
}

export function getCNPGClusterStorage(resource: any): string {
  return resource.spec?.storage?.size || '-'
}

export function getCNPGClusterStorageClass(resource: any): string {
  return resource.spec?.storage?.storageClass || '-'
}

export function getCNPGClusterWALStorage(resource: any): { size?: string; storageClass?: string } | null {
  const wal = resource.spec?.walStorage
  if (!wal) return null
  return { size: wal.size, storageClass: wal.storageClass }
}

export function getCNPGClusterBootstrapMethod(resource: any): string {
  const bootstrap = resource.spec?.bootstrap
  if (!bootstrap) return '-'
  if (bootstrap.initdb) return 'initdb'
  if (bootstrap.recovery) return 'recovery'
  if (bootstrap.pg_basebackup) return 'pg_basebackup'
  return '-'
}

export function getCNPGClusterUpdateStrategy(resource: any): string {
  return resource.spec?.primaryUpdateStrategy || 'unsupervised'
}

export function getCNPGClusterBackupConfig(resource: any): {
  configured: boolean
  destinationPath?: string
  retentionPolicy?: string
  lastSuccessfulBackup?: string
  firstRecoverabilityPoint?: string
} {
  const backup = resource.spec?.backup
  const barman = backup?.barmanObjectStore
  return {
    configured: !!barman,
    destinationPath: barman?.destinationPath,
    retentionPolicy: backup?.retentionPolicy,
    lastSuccessfulBackup: resource.status?.lastSuccessfulBackup,
    firstRecoverabilityPoint: resource.status?.firstRecoverabilityPoint,
  }
}

export function getCNPGClusterMonitoring(resource: any): {
  podMonitorEnabled: boolean
  customQueriesConfigMap?: string[]
} {
  const monitoring = resource.spec?.monitoring
  return {
    podMonitorEnabled: monitoring?.enablePodMonitor === true,
    customQueriesConfigMap: monitoring?.customQueriesConfigMap?.map((ref: any) =>
      typeof ref === 'string' ? ref : ref.name
    ),
  }
}

export function getCNPGClusterIsReplica(resource: any): boolean {
  return !!resource.spec?.replicaCluster
}

export function getCNPGClusterReplicaSource(resource: any): string {
  const replica = resource.spec?.replicaCluster
  if (!replica) return '-'
  return replica.source || replica.primary || '-'
}

export function getCNPGClusterInstanceNames(resource: any): string[] {
  return resource.status?.instanceNames || []
}

export function getCNPGClusterPostgresParams(resource: any): Record<string, string> {
  return resource.spec?.postgresql?.parameters || {}
}

// ============================================================================
// CNPG BACKUP UTILITIES
// ============================================================================

export function getCNPGBackupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase.toLowerCase()) {
    case 'completed':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'running':
      return { text: 'Running', color: healthColors.degraded, level: 'degraded' }
    case 'pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    case 'failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      if (phase) return { text: phase, color: healthColors.unknown, level: 'unknown' }
      return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getCNPGBackupCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGBackupMethod(resource: any): string {
  return resource.status?.method || resource.spec?.method || '-'
}

export function getCNPGBackupPhase(resource: any): string {
  return resource.status?.phase || '-'
}

export function getCNPGBackupDuration(resource: any): string {
  const started = resource.status?.startedAt
  const stopped = resource.status?.stoppedAt
  if (!started) return '-'
  const startTime = new Date(started).getTime()
  const endTime = stopped ? new Date(stopped).getTime() : Date.now()
  return formatDuration(endTime - startTime, true)
}

export function getCNPGBackupStartedAt(resource: any): string {
  return resource.status?.startedAt ? formatAge(resource.status.startedAt) : '-'
}

export function getCNPGBackupStoppedAt(resource: any): string {
  return resource.status?.stoppedAt ? formatAge(resource.status.stoppedAt) : '-'
}

export function getCNPGBackupName(resource: any): string {
  return resource.status?.backupName || '-'
}

export function getCNPGBackupDestinationPath(resource: any): string {
  return resource.status?.destinationPath || '-'
}

export function getCNPGBackupServerName(resource: any): string {
  return resource.status?.serverName || '-'
}

export function getCNPGBackupError(resource: any): string {
  return resource.status?.error || ''
}

export function getCNPGBackupTarget(resource: any): string {
  return resource.spec?.target || '-'
}

// ============================================================================
// CNPG SCHEDULED BACKUP UTILITIES
// ============================================================================

export function getCNPGScheduledBackupStatus(resource: any): StatusBadge {
  const isSuspended = resource.spec?.suspend === true

  if (isSuspended) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }

  // If we have a last schedule time, it's active
  if (resource.status?.lastScheduleTime) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }

  // Check if immediate flag is set and no schedule has run yet
  if (resource.spec?.immediate) {
    return { text: 'Immediate', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Scheduled', color: healthColors.healthy, level: 'healthy' }
}

export function getCNPGScheduledBackupCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGScheduleCron(resource: any): string {
  return resource.spec?.schedule || '-'
}

export function getCNPGScheduledBackupMethod(resource: any): string {
  return resource.spec?.method || 'barmanObjectStore'
}

export function getCNPGScheduledBackupLastSchedule(resource: any): string {
  return resource.status?.lastScheduleTime ? formatAge(resource.status.lastScheduleTime) : '-'
}

export function getCNPGScheduledBackupNextSchedule(resource: any): string {
  const next = resource.status?.nextScheduleTime
  if (!next) return '-'
  const nextDate = new Date(next)
  const now = new Date()
  const diffMs = nextDate.getTime() - now.getTime()
  if (diffMs <= 0) return 'overdue'
  return `in ${formatDuration(diffMs)}`
}

export function getCNPGScheduledBackupIsSuspended(resource: any): boolean {
  return resource.spec?.suspend === true
}

export function getCNPGScheduledBackupIsImmediate(resource: any): boolean {
  return resource.spec?.immediate === true
}

export function getCNPGScheduledBackupOwnerRef(resource: any): string {
  return resource.spec?.backupOwnerReference || 'none'
}

// ============================================================================
// CNPG POOLER UTILITIES
// ============================================================================

export function getCNPGPoolerStatus(resource: any): StatusBadge {
  const desired = resource.spec?.instances ?? 0
  const ready = resource.status?.instances ?? 0

  if (desired > 0 && ready < desired) {
    if (ready === 0) {
      return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
    }
    return { text: 'Degraded', color: healthColors.degraded, level: 'degraded' }
  }

  if (desired > 0 && ready >= desired) {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getCNPGPoolerCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGPoolerType(resource: any): string {
  return resource.spec?.type || '-'
}

export function getCNPGPoolerMode(resource: any): string {
  return resource.spec?.pgbouncer?.poolMode || 'session'
}

export function getCNPGPoolerInstances(resource: any): string {
  const desired = resource.spec?.instances ?? 0
  const ready = resource.status?.instances ?? 0
  return `${ready}/${desired}`
}

export function getCNPGPoolerParameters(resource: any): Record<string, string> {
  return resource.spec?.pgbouncer?.parameters || {}
}
