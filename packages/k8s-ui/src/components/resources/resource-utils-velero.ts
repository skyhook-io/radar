// Velero CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge, formatDuration } from './resource-utils'

// Several Velero plurals are shared with other operators —
// rancher/backup-restore-operator ships backups/restores.resources.cattle.io,
// CNPG ships backups.postgresql.cnpg.io, and `schedules` is a common CRD name.
// Renderers, status readers and cells must therefore select on the API group,
// not on the plural alone, or a foreign resource renders through Velero's UI.
export function isVeleroResource(resource: any): boolean {
  return typeof resource?.apiVersion === 'string' && resource.apiVersion.startsWith('velero.io/')
}

// ============================================================================
// BACKUP UTILITIES
// ============================================================================

// Phases in which a Backup or Restore is still executing. Velero has no
// single "running" flag — the set below is the v1.18 enum minus the terminal
// and pre-start phases. `Finalizing`/`WaitingForPluginOperations` and their
// *PartiallyFailed twins are still doing work, so the progress bar applies.
const BACKUP_ACTIVE_PHASES = new Set([
  'InProgress',
  'WaitingForPluginOperations',
  'WaitingForPluginOperationsPartiallyFailed',
  'Finalizing',
  'FinalizingPartiallyFailed',
])

// Phases meaning "some of the backup made it, some didn't". Distinct from a
// plain failure: the data is partially recoverable.
const BACKUP_PARTIAL_PHASES = new Set([
  'PartiallyFailed',
  'FinalizingPartiallyFailed',
  'WaitingForPluginOperationsPartiallyFailed',
])

export function isBackupActivePhase(phase: string): boolean {
  return BACKUP_ACTIVE_PHASES.has(phase)
}

export function isBackupPartialFailurePhase(phase: string): boolean {
  return BACKUP_PARTIAL_PHASES.has(phase)
}

export function getBackupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Completed':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
    case 'WaitingForPluginOperations':
    case 'Finalizing':
    case 'Queued':
    case 'ReadyToStart':
      return { text: phase, color: healthColors.neutral, level: 'neutral' }
    // Partial failure is worse than "degraded" (some data is already lost) but
    // not a total loss — the orange `alert` tier separates the two.
    case 'PartiallyFailed':
    case 'FinalizingPartiallyFailed':
    case 'WaitingForPluginOperationsPartiallyFailed':
      return { text: phase, color: healthColors.alert, level: 'alert' }
    case 'Failed':
    case 'FailedValidation':
      return { text: phase, color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Deleting':
      return { text: 'Deleting', color: healthColors.degraded, level: 'degraded' }
    case 'New':
      return { text: 'New', color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

// Backups queued behind the concurrency limit (v1.18+) report their position.
export function getBackupQueuePosition(resource: any): number | null {
  const pos = resource.status?.queuePosition
  return typeof pos === 'number' ? pos : null
}

export function getBackupStorageLocation(resource: any): string {
  return resource.spec?.storageLocation || 'default'
}

export function getBackupIncludedNamespaces(resource: any): string[] {
  return resource.spec?.includedNamespaces || []
}

export function getBackupExcludedNamespaces(resource: any): string[] {
  return resource.spec?.excludedNamespaces || []
}

export function getBackupIncludedResources(resource: any): string[] {
  return resource.spec?.includedResources || []
}

export function getBackupExcludedResources(resource: any): string[] {
  return resource.spec?.excludedResources || []
}

export function getBackupDuration(resource: any): string {
  const start = resource.status?.startTimestamp
  const end = resource.status?.completionTimestamp
  if (!start) return '-'
  const startDate = new Date(start)
  const endDate = end ? new Date(end) : new Date()
  const diffMs = endDate.getTime() - startDate.getTime()
  if (diffMs < 0) return '-'
  return formatDuration(diffMs, true)
}

export function getBackupItemCount(resource: any): string {
  const progress = resource.status?.progress
  if (!progress) return '-'
  const backed = progress.itemsBackedUp ?? 0
  const total = progress.totalItems ?? 0
  return `${backed}/${total}`
}

export function getBackupExpiry(resource: any): string {
  const expiration = resource.status?.expiration
  if (!expiration) return '-'
  const expiryDate = new Date(expiration)
  const now = new Date()
  const diffMs = expiryDate.getTime() - now.getTime()
  if (diffMs <= 0) return 'Expired'
  return formatDuration(diffMs) + ' remaining'
}

export function getBackupErrors(resource: any): number {
  return resource.status?.errors ?? 0
}

export function getBackupWarnings(resource: any): number {
  return resource.status?.warnings ?? 0
}

export function getBackupValidationErrors(resource: any): string[] {
  return resource.status?.validationErrors || []
}

export function getBackupTTL(resource: any): string {
  return resource.spec?.ttl || '-'
}

export function getBackupSnapshotVolumes(resource: any): string {
  const val = resource.spec?.snapshotVolumes
  if (val === undefined || val === null) return 'default'
  return val ? 'Yes' : 'No'
}

export function getBackupDefaultVolumesToFsBackup(resource: any): string {
  const val = resource.spec?.defaultVolumesToFsBackup
  if (val === undefined || val === null) return 'No'
  return val ? 'Yes' : 'No'
}

export function getBackupVolumeSnapshotLocations(resource: any): string[] {
  return resource.spec?.volumeSnapshotLocations || []
}

// ============================================================================
// RESTORE UTILITIES
// ============================================================================

// Restore shares Backup's phase vocabulary except for the pre-start
// (`Queued`/`ReadyToStart`) and `Deleting` phases, which only Backup has.
export function getRestoreStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Completed':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
    case 'WaitingForPluginOperations':
    case 'Finalizing':
      return { text: phase, color: healthColors.neutral, level: 'neutral' }
    case 'PartiallyFailed':
    case 'FinalizingPartiallyFailed':
    case 'WaitingForPluginOperationsPartiallyFailed':
      return { text: phase, color: healthColors.alert, level: 'alert' }
    case 'Failed':
    case 'FailedValidation':
      return { text: phase, color: healthColors.unhealthy, level: 'unhealthy' }
    case 'New':
      return { text: 'New', color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getRestoreValidationErrors(resource: any): string[] {
  return resource.status?.validationErrors || []
}

export function getRestoreBackupName(resource: any): string {
  return resource.spec?.backupName || '-'
}

export function getRestoreIncludedNamespaces(resource: any): string[] {
  return resource.spec?.includedNamespaces || []
}

export function getRestoreExcludedNamespaces(resource: any): string[] {
  return resource.spec?.excludedNamespaces || []
}

export function getRestoreIncludedResources(resource: any): string[] {
  return resource.spec?.includedResources || []
}

export function getRestoreExcludedResources(resource: any): string[] {
  return resource.spec?.excludedResources || []
}

export function getRestoreDuration(resource: any): string {
  const start = resource.status?.startTimestamp
  const end = resource.status?.completionTimestamp
  if (!start) return '-'
  const startDate = new Date(start)
  const endDate = end ? new Date(end) : new Date()
  const diffMs = endDate.getTime() - startDate.getTime()
  if (diffMs < 0) return '-'
  return formatDuration(diffMs, true)
}

export function getRestoreErrors(resource: any): number {
  return resource.status?.errors ?? 0
}

export function getRestoreWarnings(resource: any): number {
  return resource.status?.warnings ?? 0
}

export function getRestorePVs(resource: any): string {
  const val = resource.spec?.restorePVs
  if (val === undefined || val === null) return 'default'
  return val ? 'Yes' : 'No'
}

export function getRestoreExistingResourcePolicy(resource: any): string {
  return resource.spec?.existingResourcePolicy || 'none'
}

// ============================================================================
// SCHEDULE UTILITIES
// ============================================================================

export function getScheduleStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''
  const isPaused = resource.spec?.paused === true

  if (isPaused) {
    return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
  }

  // Velero leaves the phase empty on some validation failures and only records
  // the reason in status.validationErrors, so the array is the authoritative
  // signal — a schedule with errors is not producing backups whatever the phase.
  if (getScheduleValidationErrors(resource).length > 0) {
    return { text: 'FailedValidation', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  switch (phase) {
    case 'Enabled':
      return { text: 'Enabled', color: healthColors.healthy, level: 'healthy' }
    case 'FailedValidation':
      return { text: 'FailedValidation', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getScheduleValidationErrors(resource: any): string[] {
  return resource.status?.validationErrors || []
}

export function getScheduleCron(resource: any): string {
  return resource.spec?.schedule || '-'
}

export function getScheduleLastBackup(resource: any): string {
  const lastBackup = resource.status?.lastBackup
  if (!lastBackup) return 'Never'
  return formatAge(lastBackup)
}

export function getSchedulePaused(resource: any): boolean {
  return resource.spec?.paused === true
}

export function getScheduleTemplate(resource: any): any {
  return resource.spec?.template || {}
}

export function getScheduleUseOwnerReferences(resource: any): boolean {
  return resource.spec?.useOwnerReferencesInBackup === true
}

// ============================================================================
// BACKUP STORAGE LOCATION UTILITIES
// ============================================================================

export function getBSLStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Available':
      return { text: 'Available', color: healthColors.healthy, level: 'healthy' }
    case 'Unavailable':
      return { text: 'Unavailable', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getBSLProvider(resource: any): string {
  return resource.spec?.provider || '-'
}

export function getBSLBucket(resource: any): string {
  return resource.spec?.objectStorage?.bucket || '-'
}

export function getBSLPrefix(resource: any): string {
  return resource.spec?.objectStorage?.prefix || '-'
}

export function getBSLRegion(resource: any): string {
  return resource.spec?.config?.region || '-'
}

export function getBSLDefault(resource: any): boolean {
  return resource.spec?.default === true
}

export function getBSLAccessMode(resource: any): string {
  return resource.spec?.accessMode || 'ReadWrite'
}

export function getBSLLastValidation(resource: any): string {
  const lastValidation = resource.status?.lastValidationTime
  if (!lastValidation) return '-'
  return formatAge(lastValidation)
}

export function getBSLLastSynced(resource: any): string {
  const lastSynced = resource.status?.lastSyncedTime
  if (!lastSynced) return '-'
  return formatAge(lastSynced)
}

// ============================================================================
// VOLUME SNAPSHOT LOCATION UTILITIES
// ============================================================================

export function getVSLProvider(resource: any): string {
  return resource.spec?.provider || '-'
}

export function getVSLConfig(resource: any): Record<string, string> {
  return resource.spec?.config || {}
}
