import { describe, expect, it } from 'vitest'
import type { VeleroIssue } from '@skyhook-io/k8s-ui'
import { veleroAttention, veleroAttentionCount } from './attention'
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

  it('counts issues the server capped from the total it sends', () => {
    const data = workspace({ issues: [issue('Backup', 'a')], issueTotals: { velero: 7 } })
    expect(veleroAttentionCount(data)).toMatchObject({ count: 7, lowerBound: false })
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
