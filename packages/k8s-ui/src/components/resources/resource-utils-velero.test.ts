import { describe, it, expect } from 'vitest'
import { getCellFilterValue } from './resource-utils'
import {
  getBackupStatus,
  getRestoreStatus,
  getScheduleStatus,
  isBackupActivePhase,
  isBackupPartialFailurePhase,
  isVeleroResource,
} from './resource-utils-velero'

const backup = (phase: string, extra: Record<string, unknown> = {}) => ({
  apiVersion: 'velero.io/v1',
  kind: 'Backup',
  status: { phase, ...extra },
})

const schedule = (spec: Record<string, unknown>, status: Record<string, unknown> = {}) => ({
  apiVersion: 'velero.io/v1',
  kind: 'Schedule',
  spec,
  status,
})

// The status column's filter dropdown reads getCellFilterValue, which is keyed
// on normalizeKindToPlural's output. Velero's Restore/Schedule keys are
// group-qualified, so the unqualified plurals no longer reach it — if these
// branches aren't kept in step, the filter silently falls through to raw
// status.phase while the cell still renders the curated badge, and the two
// disagree.
describe('getCellFilterValue — Velero status column', () => {
  it('filters a paused Schedule as Paused, matching what the cell renders', () => {
    const paused = schedule({ paused: true }, { phase: 'Enabled' })
    expect(getScheduleStatus(paused).text).toBe('Paused')
    expect(getCellFilterValue(paused, 'status', 'veleroschedules')).toBe('Paused')
  })

  it('filters a validation-error-only Schedule as FailedValidation', () => {
    // Velero leaves the phase empty on some validation failures. Falling
    // through to status.phase would make the row unfilterable — the exact
    // failure this integration exists to surface.
    const invalid = schedule({ schedule: 'not-a-cron' }, { validationErrors: ['invalid schedule'] })
    expect(getScheduleStatus(invalid).text).toBe('FailedValidation')
    expect(getCellFilterValue(invalid, 'status', 'veleroschedules')).toBe('FailedValidation')
  })

  it('filters Restore phases through the curated reader', () => {
    const restore = { apiVersion: 'velero.io/v1', kind: 'Restore', status: { phase: 'PartiallyFailed' } }
    expect(getCellFilterValue(restore, 'status', 'velerorestores')).toBe('PartiallyFailed')
  })

  it('leaves a foreign CRD sharing the plural on the generic reader', () => {
    // rancher/backup-restore-operator ships restores.resources.cattle.io. It
    // resolves to the unqualified key, which must NOT hit Velero's reader.
    const rancher = { apiVersion: 'resources.cattle.io/v1', kind: 'Restore', status: { phase: 'Completed' } }
    expect(getCellFilterValue(rancher, 'status', 'restores')).toBe('Completed')
    const rancherPaused = { apiVersion: 'resources.cattle.io/v1', kind: 'Restore', spec: { paused: true }, status: {} }
    expect(getCellFilterValue(rancherPaused, 'status', 'restores')).toBe('')
  })

  it('still filters BackupStorageLocation, whose key is not group-qualified', () => {
    const bsl = { apiVersion: 'velero.io/v1', kind: 'BackupStorageLocation', status: { phase: 'Unavailable' } }
    expect(getCellFilterValue(bsl, 'status', 'backupstoragelocations')).toBe('Unavailable')
  })
})

describe('Velero Backup phase vocabulary', () => {
  it.each([
    ['Completed', 'healthy'],
    ['InProgress', 'neutral'],
    ['Queued', 'neutral'],
    ['ReadyToStart', 'neutral'],
    ['WaitingForPluginOperations', 'neutral'],
    ['Finalizing', 'neutral'],
    ['PartiallyFailed', 'alert'],
    ['FinalizingPartiallyFailed', 'alert'],
    ['WaitingForPluginOperationsPartiallyFailed', 'alert'],
    ['Failed', 'unhealthy'],
    ['FailedValidation', 'unhealthy'],
  ])('maps %s to %s', (phase, level) => {
    const status = getBackupStatus(backup(phase))
    expect(status.text).toBe(phase)
    expect(status.level).toBe(level)
  })

  it('no longer knows the fictional Uploading phase', () => {
    // "Uploading" is not in Velero's enum and never has been; it must not be
    // special-cased back in.
    expect(getBackupStatus(backup('Uploading')).level).toBe('unknown')
  })

  it('treats the partial phases as both active and partially failed', () => {
    // Both are true at once: plugin operations are still running AND some items
    // already failed. The renderer shows a progress bar and a warning banner.
    expect(isBackupActivePhase('WaitingForPluginOperationsPartiallyFailed')).toBe(true)
    expect(isBackupPartialFailurePhase('WaitingForPluginOperationsPartiallyFailed')).toBe(true)
    expect(isBackupActivePhase('Failed')).toBe(false)
    expect(isBackupPartialFailurePhase('Failed')).toBe(false)
  })

  it('gives Restore the same partial/validation vocabulary', () => {
    expect(getRestoreStatus({ status: { phase: 'FailedValidation' } }).level).toBe('unhealthy')
    expect(getRestoreStatus({ status: { phase: 'FinalizingPartiallyFailed' } }).level).toBe('alert')
  })
})

describe('isVeleroResource', () => {
  it('selects only the velero.io group', () => {
    expect(isVeleroResource({ apiVersion: 'velero.io/v1' })).toBe(true)
    expect(isVeleroResource({ apiVersion: 'resources.cattle.io/v1' })).toBe(false)
    expect(isVeleroResource({ apiVersion: 'postgresql.cnpg.io/v1' })).toBe(false)
    // A group that merely ends in velero.io must not match.
    expect(isVeleroResource({ apiVersion: 'notvelero.io/v1' })).toBe(false)
    expect(isVeleroResource({})).toBe(false)
    expect(isVeleroResource(null)).toBe(false)
  })
})
