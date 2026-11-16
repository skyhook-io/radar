import { describe, expect, it } from 'vitest'
import type { VeleroIssue } from '@skyhook-io/k8s-ui'
import { veleroAttention, veleroDestinationCounts } from './attention'
import { veleroScheduleKindListMode } from './routes'
import { location, schedule, workspace } from './velero.fixtures'

const issue = (kind: string, name: string): VeleroIssue => ({
  id: `${kind}/${name}`, severity: 'critical', category: 'backup_target_unavailable', kind, group: 'velero.io', namespace: 'velero', name, reason: 'BackupStorageLocationUnavailable',
})

// The sidebar badges count what the Overview lists, so a badge never promises
// a problem the screen does not show, or hides one it does.
describe('the Velero sidebar counts', () => {
  it('counts what Needs attention lists, and each storage location once', () => {
    const data = workspace({
      schedules: [schedule({ name: 'late', state: 'late', attention: true }), schedule()],
      locations: [location({ name: 'dr', attention: true })],
      issues: [issue('BackupStorageLocation', 'dr'), issue('Backup', 'nightly-1')],
      issueTotals: { velero: 2 },
    })
    const counts = veleroDestinationCounts(data)
    expect(veleroAttention(data, null)).toHaveLength(4)
    expect(counts.overview).toMatchObject({ count: 4, lowerBound: false })
    expect(counts.locations).toMatchObject({ count: 1, lowerBound: false })
  })

  it('reads zero as none only when everything was read', () => {
    expect(veleroDestinationCounts(workspace()).overview).toMatchObject({ count: 0, lowerBound: false })
    const partial = workspace({ coverage: { ...workspace().coverage, backups: { state: 'partial' } as any } })
    expect(veleroDestinationCounts(partial).overview).toMatchObject({ count: null, lowerBound: true })
  })

  it('counts issues the server capped from the total it sends', () => {
    const data = workspace({ issues: [issue('Backup', 'a')], issueTotals: { velero: 7 } })
    expect(veleroDestinationCounts(data).overview).toMatchObject({ count: 7, lowerBound: false })
    // The capped six could be about locations, so that count is only a floor.
    expect(veleroDestinationCounts(data).locations).toMatchObject({ count: null, lowerBound: true })
  })
})

describe('the Velero Schedule kind list', () => {
  it('is the Overview when this Radar serves it, and the table otherwise', () => {
    expect(veleroScheduleKindListMode('supported', false)).toBe('view')
    expect(veleroScheduleKindListMode('unknown', true)).toBe('wait')
    expect(veleroScheduleKindListMode('unknown', false)).toBe('table')
    expect(veleroScheduleKindListMode('unsupported', false)).toBe('table')
  })
})
