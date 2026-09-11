import { describe, it, expect } from 'vitest'
import { getCellFilterValue } from './resource-utils'
import {
  getBackupStatus,
  getRestoreStatus,
  getScheduleStatus,
  isBackupActivePhase,
  isBackupPartialFailurePhase,
  isVeleroResource,
  getScheduleCronInfo,
  getBackupRepositoryStatus,
  resolveBackupStorageLocation,
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

  it('offers the BackupRepository dropdown the same string the badge shows', () => {
    // Asserted as literals rather than against the reader: comparing the filter
    // to getBackupRepositoryStatus().text passes even when both are wrong. The
    // contract is that the dropdown offers "Not ready" — the label — and never
    // the raw NotReady phase the generic fallback would have produced.
    const repo = {
      apiVersion: 'velero.io/v1',
      kind: 'BackupRepository',
      status: { phase: 'NotReady' },
    }
    expect(getBackupRepositoryStatus(repo).text).toBe('Not ready')
    expect(getCellFilterValue(repo, 'status', 'backuprepositories')).toBe('Not ready')
    expect(getCellFilterValue(repo, 'status', 'backuprepositories')).not.toBe('NotReady')
  })

  it('filters a validation-error-only Schedule as FailedValidation', () => {
    // Velero leaves the phase empty on some validation failures. Falling
    // through to status.phase would make the row unfilterable — the exact
    // failure this integration exists to surface.
    const invalid = schedule({ schedule: 'not-a-cron' }, { validationErrors: ['invalid schedule'] })
    expect(getScheduleStatus(invalid).text).toBe('Rejected')
    expect(getCellFilterValue(invalid, 'status', 'veleroschedules')).toBe('Rejected')
  })

  it('filters Restore phases through the curated reader', () => {
    const restore = { apiVersion: 'velero.io/v1', kind: 'Restore', status: { phase: 'PartiallyFailed' } }
    expect(getCellFilterValue(restore, 'status', 'velerorestores')).toBe('Partially failed')
  })

  it('lets a real failure outrank paused, so neither fact is lost', () => {
    // Previously paused won outright and a paused+invalid schedule showed only
    // "Paused" — the table gave no sign it was also broken. The badge now
    // carries the failure (the fact to act on) and the cell adds a paused
    // indicator alongside.
    const pausedOnly = schedule({ paused: true }, { phase: 'Enabled' })
    expect(getScheduleStatus(pausedOnly).text).toBe('Paused')

    const pausedAndInvalid = schedule({ paused: true }, { phase: 'FailedValidation', validationErrors: ['bad cron'] })
    expect(getScheduleStatus(pausedAndInvalid).text).toBe('Rejected')
    // and it filters as the failure, so it is reachable from the status filter
    expect(getCellFilterValue(pausedAndInvalid, 'status', 'veleroschedules')).toBe('Rejected')
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

  // The cell dispatch sends a non-Velero BackupStorageLocation/BackupRepository
  // to GenericCell. The filter dropdown and the sort key have to follow it
  // there, or the column offers and orders values that appear on no row.
  it('leaves a foreign BackupStorageLocation on the generic reader', () => {
    // Conditions, not a phase: Velero's reader keys on phase and has nothing to
    // say here, so this only passes if the generic derivation ran.
    const foreign = {
      apiVersion: 'other.io/v1',
      kind: 'BackupStorageLocation',
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    }
    expect(getCellFilterValue(foreign, 'status', 'backupstoragelocations')).toBe('Ready')
  })

  it('leaves a foreign BackupRepository on the generic reader', () => {
    const foreign = {
      apiVersion: 'other.io/v1',
      kind: 'BackupRepository',
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    }
    expect(getCellFilterValue(foreign, 'status', 'backuprepositories')).toBe('Ready')
  })

  // Velero's own readers speak a phase vocabulary a foreign CRD does not share,
  // so the two must not produce the same string for the same object.
  it('does not label a foreign resource with Velero vocabulary', () => {
    const foreign = { apiVersion: 'other.io/v1', kind: 'BackupRepository', status: { phase: 'NotReady' } }
    expect(getCellFilterValue(foreign, 'status', 'backuprepositories')).not.toBe('Not ready')
  })
})

describe('Velero Backup phase vocabulary', () => {
  // The table label rule: sentence-case the upstream phase, diverge only where
  // it does not fit. Each row below is (phase, label, level) — the label column
  // is the contract, and the three departures from upstream are called out.
  it.each([
    ['Completed', 'Completed', 'healthy'],
    ['New', 'New', 'unknown'],
    ['Queued', 'Queued', 'neutral'],
    ['Failed', 'Failed', 'unhealthy'],
    ['Deleting', 'Deleting', 'degraded'],
    ['Finalizing', 'Finalizing', 'neutral'],
    // sentence-cased, not renamed
    ['InProgress', 'In progress', 'neutral'],
    ['ReadyToStart', 'Ready to start', 'neutral'],
    ['PartiallyFailed', 'Partially failed', 'alert'],
    // the three genuine departures
    ['WaitingForPluginOperations', 'Plugin work', 'neutral'],
    ['FailedValidation', 'Rejected', 'unhealthy'],
    ['FinalizingPartiallyFailed', 'Partially failed', 'alert'],
    ['WaitingForPluginOperationsPartiallyFailed', 'Partially failed', 'alert'],
  ])('%s renders as "%s" (%s)', (phase, label, level) => {
    const status = getBackupStatus(backup(phase))
    expect(status.text).toBe(label)
    expect(status.level).toBe(level)
  })

  // The collapse is deliberate and lossy: three distinct phases share one
  // label, so the column cannot tell you whether a partially-failed run has
  // finished. Pinned so the loss is a decision rather than a drift.
  it('collapses all three partial-failure phases onto one label', () => {
    const labels = ['PartiallyFailed', 'FinalizingPartiallyFailed', 'WaitingForPluginOperationsPartiallyFailed']
      .map(p => getBackupStatus(backup(p)).text)
    expect(new Set(labels)).toEqual(new Set(['Partially failed']))
  })

  // Two labels a reader cannot tell apart fail the way an exact duplicate
  // does. Every other phase must stay distinguishable.
  it('gives no two non-partial phases the same label', () => {
    const phases = ['New','Queued','ReadyToStart','InProgress','WaitingForPluginOperations','Finalizing','Completed','Failed','FailedValidation','Deleting']
    const labels = phases.map(p => getBackupStatus(backup(p)).text)
    expect(new Set(labels).size).toBe(phases.length)
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

describe('getScheduleCronInfo', () => {
  const sched = (schedule: string, status?: any) => ({ spec: { schedule }, status })

  it('reads a recognised cron as raw plus plain English', () => {
    const r = getScheduleCronInfo(sched('0 1 * * *'))
    expect(r).toEqual({ cron: '0 1 * * *', readable: 'Daily at 1:00', malformed: false })
  })

  it('leaves readable empty when cronToHuman has no phrasing for the expression', () => {
    // cronToHuman returns its input unchanged for shapes it does not recognise.
    // Rendering that would print the same string on both lines.
    const r = getScheduleCronInfo(sched('0 3 * * 0'))
    expect(r.malformed).toBe(false)
    expect(r.readable).toBe('')
  })

  it.each(['not-a-cron', 'bad-cron', '0 1 * *', '0 1 * * * *'])('flags %s as malformed', (c) => {
    const r = getScheduleCronInfo(sched(c))
    expect(r.malformed).toBe(true)
    expect(r.readable).toBe('')
    expect(r.cron).toBe(c)
  })

  it('does not flag timezone-prefixed crons, which Velero documents', () => {
    // From Velero's own CLI docs:
    //   velero schedule create x --schedule="CRON_TZ=America/New_York 0 3 * * *"
    const r = getScheduleCronInfo(sched('CRON_TZ=America/New_York 0 3 * * *'))
    expect(r.malformed).toBe(false)
    expect(r.cron).toBe('CRON_TZ=America/New_York 0 3 * * *')
    // No phrasing: "Daily at 3:00" would read as cluster time, which is exactly
    // what the prefix says it is not.
    expect(r.readable).toBe('')
  })

  it('accepts the legacy TZ= spelling and still catches a bad zoned expression', () => {
    expect(getScheduleCronInfo(sched('TZ=UTC 0 3 * * *')).malformed).toBe(false)
    expect(getScheduleCronInfo(sched('CRON_TZ=Europe/Berlin not-a-cron')).malformed).toBe(true)
  })

  it('does not flag @-descriptors, which the field count would reject', () => {
    expect(getScheduleCronInfo(sched('@daily')).malformed).toBe(false)
    expect(getScheduleCronInfo(sched('@every 1h')).malformed).toBe(false)
  })

  it('does not call a valid cron malformed just because the Schedule was rejected', () => {
    // Observed live: a schedule can fail validation for a missing storage
    // location while its cron is fine. Reddening the cron there accuses the
    // wrong field, so malformed must not be derived from phase.
    const r = getScheduleCronInfo(
      sched('0 4 * * *', { phase: 'FailedValidation', validationErrors: ['storage location "gone" not found'] })
    )
    expect(r.malformed).toBe(false)
    expect(r.readable).toBe('Daily at 4:00')
  })

  it('tolerates padded whitespace and trims the raw value', () => {
    const r = getScheduleCronInfo(sched('  0   1   *   *   *  '))
    expect(r.malformed).toBe(false)
  })

  it('renders a missing schedule as a dash, not as malformed', () => {
    expect(getScheduleCronInfo({ spec: {} })).toEqual({ cron: '-', readable: '', malformed: false })
  })
})

describe('which location holds a backup', () => {
  const locations = [
    { metadata: { name: 'primary' }, spec: { default: true } },
    { metadata: { name: 'dr-replica' }, spec: { default: false } },
  ]

  it('uses the location the backup names', () => {
    expect(resolveBackupStorageLocation({ spec: { storageLocation: 'dr-replica' } }, locations)).toBe('dr-replica')
  })

  // The rule Velero actually applies: the flag, not the name.
  it('resolves an unset location through spec.default', () => {
    expect(resolveBackupStorageLocation({ spec: {} }, locations)).toBe('primary')
  })

  // Velero's own fallback when nothing claims the flag.
  it('falls back to the conventional name when no location is marked default', () => {
    expect(resolveBackupStorageLocation({ spec: {} }, [{ metadata: { name: 'only' }, spec: {} }])).toBe('default')
  })

  it('falls back when the list has not been read', () => {
    expect(resolveBackupStorageLocation({ spec: {} }, undefined)).toBe('default')
  })
})
