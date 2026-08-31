// Velero CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge, formatDuration, cronToHuman } from './resource-utils'

// Several Velero plurals are shared with other operators —
// rancher/backup-restore-operator ships backups/restores.resources.cattle.io,
// CNPG ships backups.postgresql.cnpg.io, and `schedules` is a common CRD name.
// Renderers, status readers and cells must therefore select on the API group,
// not on the plural alone, or a foreign resource renders through Velero's UI.
export function isVeleroResource(resource: any): boolean {
  return typeof resource?.apiVersion === 'string' && resource.apiVersion.startsWith('velero.io/')
}

// ============================================================================
// TABLE LABELS
// ============================================================================

// The rule: sentence-case the upstream phase; diverge only where it does not
// fit. Most of the status column is therefore Velero's own vocabulary as
// typography, not a rename — which is what keeps it recognisable next to
// `velero backup describe`. The exact phase stays reachable in the detail
// drawer and in the badge tooltip.
//
// The budget is 96px: a w-36 column less 32px of cell padding and 16px of badge
// padding. Labels are held to at least 8px of headroom — sub-pixel shortfalls
// still render an ellipsis, and the ellipsis glyph costs two more characters.
const VELERO_LABEL_EXCEPTIONS: Record<string, string> = {
  // 175px, so no defensible column width. "Finishing up" was the obvious
  // alternative and is worse: a reader cannot tell it apart from the real
  // `Finalizing` phase, and two labels that look like the same state fail the
  // way an exact duplicate does, only more slowly.
  WaitingForPluginOperations: 'Plugin work',
  // "Validation failed" measures 91px — inside the noise floor at a 96px
  // budget — and the Schedule cell needs room for a paused indicator beside it.
  // "Rejected" also states the actionable fact: Velero refused this before it
  // ran, so nothing happened at all.
  FailedValidation: 'Rejected',
  // 237px and 147px. Both collapse onto the plain partial-failure label, which
  // makes the column non-injective: from the table you cannot tell whether a
  // partially-failed run has finished. That is acceptable only because the
  // drawer prints the raw `.status.phase` next to the badge — see
  // `VeleroPhaseValue`. Remove that and this stops being a display choice and
  // becomes lost information.
  WaitingForPluginOperationsPartiallyFailed: 'Partially failed',
  FinalizingPartiallyFailed: 'Partially failed',
}

// PartiallyFailed -> "Partially failed", ReadyToStart -> "Ready to start".
function sentenceCasePhase(phase: string): string {
  return phase
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .split(' ')
    .map((word, i) => (i === 0 ? word : word.toLowerCase()))
    .join(' ')
}

// The table label for a Velero phase. Display only — the issues engine and MCP
// keep the raw phase, because those are API surfaces read by agents rather than
// a column read by a human.
export function veleroPhaseLabel(phase: string): string {
  if (!phase) return 'Unknown'
  return VELERO_LABEL_EXCEPTIONS[phase] ?? sentenceCasePhase(phase)
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
      return { text: veleroPhaseLabel('Completed'), color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
    case 'WaitingForPluginOperations':
    case 'Finalizing':
    case 'Queued':
    case 'ReadyToStart':
      return { text: veleroPhaseLabel(phase), color: healthColors.neutral, level: 'neutral' }
    // Partial failure is worse than "degraded" (some data is already lost) but
    // not a total loss — the orange `alert` tier separates the two.
    case 'PartiallyFailed':
    case 'FinalizingPartiallyFailed':
    case 'WaitingForPluginOperationsPartiallyFailed':
      return { text: veleroPhaseLabel(phase), color: healthColors.alert, level: 'alert' }
    case 'Failed':
    case 'FailedValidation':
      return { text: veleroPhaseLabel(phase), color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Deleting':
      return { text: veleroPhaseLabel('Deleting'), color: healthColors.degraded, level: 'degraded' }
    case 'New':
      return { text: veleroPhaseLabel('New'), color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: veleroPhaseLabel(phase), color: healthColors.unknown, level: 'unknown' }
  }
}

// Backups queued behind the concurrency limit (v1.18+) report their position.
export function getBackupQueuePosition(resource: any): number | null {
  const pos = resource.status?.queuePosition
  return typeof pos === 'number' ? pos : null
}

// The name a Backup carries, applying Velero's fallback by name only. Callers
// that hold the location list should prefer resolveBackupStorageLocation, which
// applies Velero's actual rule.
export function getBackupStorageLocation(resource: any): string {
  return resource.spec?.storageLocation || 'default'
}

// Velero resolves an unset spec.storageLocation to whichever location carries
// spec.default — a flag, not a name. The literal name "default" is only the
// fallback when nothing claims the flag, and an install that renamed its
// default location has no such object at all: the health lookup finds nothing
// and the link points at a name that does not exist.
//
// Mirrors the server-side resolution behind the stored-backups endpoint so the
// two cannot disagree about which location holds a backup.
export function getVeleroDefaultLocationName(locations: any[] | undefined): string {
  for (const l of locations ?? []) {
    if (l?.spec?.default === true && l?.metadata?.name) return l.metadata.name
  }
  return ''
}

export function resolveBackupStorageLocation(resource: any, locations: any[] | undefined): string {
  const declared = resource?.spec?.storageLocation
  if (declared) return declared
  return getVeleroDefaultLocationName(locations) || 'default'
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

/**
 * How long this backup allows a single operation before it has outlived its own
 * budget. Shown because the stalled-run issue measures against this field and
 * quotes it — without it on the page, the message cites a number from a spec
 * option the page lists every sibling of.
 *
 * Unset renders as "default", the same word getBackupSnapshotVolumes uses for
 * the same situation — not as a number. Velero's built-in is four hours, but the
 * controller takes `--default-item-operation-timeout` and distributions ship
 * their own, so printing 4h here would state an install setting nothing on this
 * page can read.
 */
export function getBackupItemOperationTimeout(resource: any): string {
  return resource.spec?.itemOperationTimeout || 'default'
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
      return { text: veleroPhaseLabel('Completed'), color: healthColors.healthy, level: 'healthy' }
    case 'InProgress':
    case 'WaitingForPluginOperations':
    case 'Finalizing':
      return { text: veleroPhaseLabel(phase), color: healthColors.neutral, level: 'neutral' }
    case 'PartiallyFailed':
    case 'FinalizingPartiallyFailed':
    case 'WaitingForPluginOperationsPartiallyFailed':
      return { text: veleroPhaseLabel(phase), color: healthColors.alert, level: 'alert' }
    case 'Failed':
    case 'FailedValidation':
      return { text: veleroPhaseLabel(phase), color: healthColors.unhealthy, level: 'unhealthy' }
    case 'New':
      return { text: veleroPhaseLabel('New'), color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: veleroPhaseLabel(phase), color: healthColors.unknown, level: 'unknown' }
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

  // Velero leaves the phase empty on some validation failures and only records
  // the reason in status.validationErrors, so the array is the authoritative
  // signal — a schedule with errors is not producing backups whatever the phase.
  const isRejected = phase === 'FailedValidation' || getScheduleValidationErrors(resource).length > 0

  // A real failure outranks paused. Pausing is a state the operator chose; being
  // rejected is one they need to fix, and it survives resuming. When both are
  // true the cell renders this badge plus a separate paused indicator, so
  // neither fact is lost — previously paused won outright and the table showed
  // no sign the schedule was also invalid.
  if (isRejected) {
    return { text: veleroPhaseLabel('FailedValidation'), color: healthColors.unhealthy, level: 'unhealthy' }
  }

  if (isPaused) {
    return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
  }

  switch (phase) {
    case 'Enabled':
      return { text: veleroPhaseLabel('Enabled'), color: healthColors.healthy, level: 'healthy' }
    case 'FailedValidation':
      return { text: veleroPhaseLabel('FailedValidation'), color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: veleroPhaseLabel(phase), color: healthColors.unknown, level: 'unknown' }
  }
}

export function getScheduleValidationErrors(resource: any): string[] {
  return resource.status?.validationErrors || []
}

export function getScheduleCron(resource: any): string {
  return resource.spec?.schedule || '-'
}

// The schedule cell shows the raw cron with its plain-English reading beneath,
// matching CronJob. Two departures from that cell, both deliberate:
//
// `readable` is empty when cronToHuman gives the expression back unchanged —
// it recognises a handful of shapes and returns the input for the rest, so
// rendering unconditionally prints the same string twice (which CronJob does).
//
// `malformed` is a field-count check, the same thing Velero's own parser
// complains about first ("expected exactly 5 fields"). It is deliberately not
// derived from FailedValidation: a schedule can be rejected for a missing
// storage location while its cron is perfectly good, and reddening the cron
// there would accuse the wrong field. `@`-descriptors pass unflagged, and a
// 5-field expression with an out-of-range number is not caught — the status
// badge carries Velero's verdict, so under-marking beats a false accusation.
export function getScheduleCronInfo(resource: any): {
  cron: string
  readable: string
  malformed: boolean
} {
  const cron = (resource.spec?.schedule || '').trim()
  if (!cron) return { cron: '-', readable: '', malformed: false }

  // Velero documents `CRON_TZ=America/New_York 0 3 * * *`, and robfig/cron also
  // accepts the legacy `TZ=` spelling. Both put a sixth token in front of the
  // expression, so the prefix comes off before anything is counted.
  const zoned = /^(CRON_TZ|TZ)=(\S+)\s+/i.exec(cron)
  const bare = zoned ? cron.slice(zoned[0].length) : cron

  const malformed = !bare.startsWith('@') && bare.split(/\s+/).length !== 5
  // No plain-English line for a zoned expression: "Daily at 3:00" reads as
  // cluster time, and the whole point of the prefix is that it isn't. The raw
  // value already names the zone.
  const readable = malformed || zoned ? '' : cronToHuman(bare)
  return { cron, readable: readable === cron ? '' : readable, malformed }
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
      return { text: veleroPhaseLabel('Available'), color: healthColors.healthy, level: 'healthy' }
    case 'Unavailable':
      return { text: veleroPhaseLabel('Unavailable'), color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: veleroPhaseLabel(phase), color: healthColors.unknown, level: 'unknown' }
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

// Why the location is unavailable, in the validation controller's own words.
// A BackupStorageLocation carries no conditions, so status.message is the only
// place the reason exists — without it the phase is the whole story a reader
// gets.
export function getBSLMessage(resource: any): string {
  return resource.status?.message || ''
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

// ============================================================================
// BACKUP REPOSITORY UTILITIES
// ============================================================================

export function getBackupRepositoryStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (phase) {
    case 'Ready':
      return { text: veleroPhaseLabel('Ready'), color: healthColors.healthy, level: 'healthy' }
    case 'NotReady':
      return { text: veleroPhaseLabel('NotReady'), color: healthColors.unhealthy, level: 'unhealthy' }
    case 'New':
      return { text: veleroPhaseLabel('New'), color: healthColors.unknown, level: 'unknown' }
    default:
      return { text: veleroPhaseLabel(phase), color: healthColors.unknown, level: 'unknown' }
  }
}

// kopia | restic. Worth scanning for: restic is being retired — v1.17 stopped
// creating new restic backups and v1.19 drops restic restore — so a restic
// repository is a migration liability, not just a configuration detail.
export function getBackupRepositoryType(resource: any): string {
  return resource.spec?.repositoryType || '-'
}

// The namespace whose volumes this repository holds. One repository per
// (namespace, location, type) is how Velero partitions them, so this is what
// the repository is *for* rather than where the object lives.
export function getBackupRepositoryVolumeNamespace(resource: any): string {
  return resource.spec?.volumeNamespace || '-'
}

export function getBackupRepositoryLocation(resource: any): string {
  return resource.spec?.backupStorageLocation || '-'
}

export function getBackupRepositoryMaintenanceFrequency(resource: any): string {
  return resource.spec?.maintenanceFrequency || '-'
}

export function getBackupRepositoryLastMaintenance(resource: any): string {
  const at = resource.status?.lastMaintenanceTime
  if (!at) return '-'
  return formatAge(at)
}

// Why the repository is not ready, in the controller's own words. Velero writes
// it on the object and nowhere else, so without it the phase is the whole story
// a reader gets.
export function getBackupRepositoryMessage(resource: any): string {
  return resource.status?.message || ''
}
