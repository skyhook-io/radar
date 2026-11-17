import { describe, expect, it } from 'vitest'
import type { VeleroIssue } from '@skyhook-io/k8s-ui'
import { veleroAttention, veleroAttentionCount, veleroScreenCounts } from './attention'
import { veleroKindListMode } from './routes'
import { location, schedule, workspace } from './velero.fixtures'

const issue = (kind: string, name: string): VeleroIssue => ({
  id: `${kind}/${name}`, severity: 'critical', category: 'backup_target_unavailable', kind, group: 'velero.io', namespace: 'velero', name, reason: 'BackupStorageLocationUnavailable',
})

// The sidebar badge counts what the Overview lists, so it never promises a
// problem the screen does not show, or hides one it does.
describe('the Velero Overview badge', () => {
  it('counts what Needs attention lists', () => {
    const data = workspace({
      schedules: [schedule({ name: 'late', state: 'late', attention: true }), schedule()],
      locations: [location({ name: 'dr', attention: true })],
      issues: [issue('BackupStorageLocation', 'dr'), issue('Backup', 'nightly-1')],
      issueTotals: { velero: 2 },
    })
    expect(veleroAttention(data, null)).toHaveLength(4)
    expect(veleroAttentionCount(data)).toMatchObject({ count: 4, lowerBound: false })
  })

  it('reads zero as none only when everything was read', () => {
    expect(veleroAttentionCount(workspace())).toMatchObject({ count: 0, lowerBound: false })
    const partial = workspace({ coverage: { ...workspace().coverage, backups: { state: 'partial' } as any } })
    expect(veleroAttentionCount(partial)).toMatchObject({ count: null, lowerBound: true })
  })

  it('gives each list the rows about its kind, so the lists add up to the total', () => {
    const data = workspace({
      schedules: [schedule({ name: 'late', state: 'late', attention: true }), schedule()],
      locations: [location({ name: 'dr', attention: true })],
      issues: [issue('BackupStorageLocation', 'dr'), issue('Backup', 'nightly-1')],
      issueTotals: { velero: 2 },
    })
    const screens = veleroScreenCounts(data)
    expect(Object.fromEntries(Object.entries(screens).map(([k, v]) => [k, v.count]))).toEqual({ backups: 1, schedules: 1, restores: 0, backupstoragelocations: 2 })
    expect(screens.schedules.title).toBe('1 schedule needs attention.')
    const partial = veleroScreenCounts(workspace({ coverage: { ...workspace().coverage, restores: { state: 'partial' } as any } }))
    expect(partial.restores).toMatchObject({ count: null, lowerBound: true })
    expect(partial.backups).toMatchObject({ count: 0, lowerBound: false })
  })

  it('counts issues the server capped from the total it sends', () => {
    const data = workspace({ issues: [issue('Backup', 'a')], issueTotals: { velero: 7 } })
    expect(veleroAttentionCount(data)).toMatchObject({ count: 7, lowerBound: false })
  })
})

// One fault is one row, as on the Issues page: the stopped server's row names
// the schedules it left late, and storage it left unchecked is not listed.
describe('a stopped Velero server on the Overview', () => {
  const stopped = { ...workspace().installations[0], server: { ...workspace().installations[0].server, replicas: 0, deployment: 'velero' } }
  const late = (name: string) => schedule({ name, state: 'late', attention: true, reason: 'serverDown' })

  it('is one row naming the schedules it left late, without the storage it left unchecked', () => {
    const rows = veleroAttention(workspace({
      installations: [stopped],
      schedules: [late('late')],
      locations: [location({ attention: true, validationOverdue: true, reason: 'serverDown' })],
    }), null)
    expect(rows.map((r) => r.category)).toEqual(['server'])
    expect(rows[0].detail).toContain('1 schedule has missed runs: late.')
  })

  it('stays one row for a hundred schedules', () => {
    const rows = veleroAttention(workspace({ installations: [stopped], schedules: Array.from({ length: 100 }, (_, i) => late(`s${i}`)) }), null)
    expect(rows).toHaveLength(1)
    expect(rows[0].detail).toContain('100 schedules have missed runs: s0, s1, s2 and 97 more.')
  })

  it('keeps a schedule wrong for another reason as its own row', () => {
    const rows = veleroAttention(workspace({ installations: [stopped], schedules: [late('late'), schedule({ name: 'long', state: 'cannotRun', attention: true })] }), null)
    expect(rows.map((r) => r.subject?.name)).toEqual(['velero', 'long'])
  })
})

describe('an unavailable storage location on the Overview', () => {
  it('names the runs Velero rejected because of it, each one a link', () => {
    const data = workspace({
      issues: [{ ...issue('BackupStorageLocation', 'dr'), message: 'bucket not found', affected: [{ kind: 'Backup', name: 'a-1' }, { kind: 'Backup', name: 'b-1' }] }],
      issueTotals: { velero: 1 },
    })
    const [row] = veleroAttention(data, null)
    expect(row.detail).toBe('bucket not found.')
    expect(row.related?.lead).toBe('Velero rejected 2 runs because of it:')
    expect(row.related?.refs).toEqual([
      { kind: 'Backup', group: 'velero.io', namespace: 'velero', name: 'a-1' },
      { kind: 'Backup', group: 'velero.io', namespace: 'velero', name: 'b-1' },
    ])
  })
})

describe('the Velero Schedule and BackupStorageLocation kind lists', () => {
  it("are Radar's own tables when this Radar serves them, and the generic table otherwise", () => {
    expect(veleroKindListMode('supported', false)).toBe('view')
    expect(veleroKindListMode('unknown', true)).toBe('wait')
    expect(veleroKindListMode('unknown', false)).toBe('table')
    expect(veleroKindListMode('unsupported', false)).toBe('table')
  })
})

describe('Velero next steps', () => {
  it('says what to do about every Issue reason and every attention state', async () => {
    const { veleroIssueNextStep } = await import('./shared')
    const { veleroScheduleNextStep } = await import('./attention')
    for (const reason of ['BackupFailed', 'BackupPartiallyFailed', 'BackupValidationFailed', 'RestoreFailed', 'RestorePartiallyFailed', 'RestoreValidationFailed', 'ScheduleValidationFailed', 'BackupStorageLocationUnavailable', 'BackupRepositoryNotReady', 'BackupRepositoryMaintenanceFailed', 'VeleroRunStalled', 'VeleroServerNotRunning', 'VeleroScheduleLate', 'VeleroScheduleCannotRun', 'VeleroScheduleHeld', 'VeleroScheduleNotValidated', 'VeleroLocationValidationOverdue']) {
      expect(veleroIssueNextStep({ reason }), reason).toBeTruthy()
    }
    expect(veleroIssueNextStep({ reason: 'BackupFailed', message: 'found a backup with status "InProgress" during the server starting, mark it as "Failed"' })).toBe('Run the backup again')
    expect(veleroIssueNextStep({ reason: 'BackupFailed', action: 'Do the thing' })).toBe('Do the thing')
    expect(veleroIssueNextStep({ reason: 'VeleroScheduleCannotRun', message: "This schedule's name is longer than 63 characters" })).toBe('Recreate the schedule with a name of 63 characters or fewer')
    for (const state of ['cannotRun', 'alwaysDue', 'rejected', 'held', 'late', 'probablyLate', 'lateOrHeld', 'notValidated'] as const) {
      expect(veleroScheduleNextStep({ state }), state).toBeTruthy()
    }
    expect(veleroScheduleNextStep({ state: 'late', reason: 'serverDown' })).toContain('Start the Velero server')
  })
})
