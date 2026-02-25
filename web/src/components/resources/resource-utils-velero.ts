// Velero CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge, formatDuration } from './resource-utils'

// ============================================================================
// BACKUP UTILITIES
// ============================================================================

export function getBackupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Completed':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
      return { text: 'InProgress', color: healthColors.neutral, level: 'neutral' }
    case 'Uploading':
      return { text: 'Uploading', color: healthColors.neutral, level: 'neutral' }
    case 'PartiallyFailed':
      return { text: 'PartiallyFailed', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Deleting':
      return { text: 'Deleting', color: healthColors.degraded, level: 'degraded' }
    case 'New':
      return { text: 'New', color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
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

export function getRestoreStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Completed':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
      return { text: 'InProgress', color: healthColors.neutral, level: 'neutral' }
    case 'PartiallyFailed':
      return { text: 'PartiallyFailed', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'New':
      return { text: 'New', color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
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

  switch (phase) {
    case 'Enabled':
      return { text: 'Enabled', color: healthColors.healthy, level: 'healthy' }
    case 'FailedValidation':
      return { text: 'FailedValidation', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
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
