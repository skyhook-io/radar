import { describe, expect, it } from 'vitest'
import type { CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeResponse } from './cnpg'
import { cnpgInstanceLive, cnpgLiveStandbyGaps, cnpgReplicationLive, withLiveReplication } from './cnpg-ha'

const rt = (status: any): CNPGRuntimeResponse => ({
  cluster: { namespace: 'db', name: 'pg', uid: 'u' },
  sampledAt: '2026-09-30T10:00:00Z',
  permission: { proxy: 'allowed' },
  instances: [
    { pod: 'pg-1', role: 'primary', status, metrics: { state: 'ok' } },
    { pod: 'pg-2', role: 'replica', status: { state: 'ok', replication: [] }, metrics: { state: 'ok' } },
  ],
})

describe('live replication from an incomplete primary report', () => {
  it('is unknown, never "0 streaming"', () => {
    expect(cnpgReplicationLive(rt({ state: 'partial', incomplete: true, replication: null }))).toBeUndefined()
    expect(cnpgReplicationLive(rt({ state: 'ok', replication: [] }))?.streaming).toBe(0)
  })
  it('carries the incomplete flag to the instance list', () => {
    expect(cnpgInstanceLive(rt({ state: 'partial', incomplete: true }))?.[0].incomplete).toBe(true)
  })
})

describe('standbys and slots from the live read', () => {
  const row = (over: Partial<CNPGFleetRow> = {}): CNPGFleetRow =>
    ({
      key: 'db/pg',
      namespace: 'db',
      name: 'pg',
      cluster: { metadata: { annotations: {} }, status: { currentPrimary: 'pg-6' } },
      instances: { ready: 3, desired: 3 },
      pods: [],
      replication: { text: '2/2 Pods ready · lag unknown', tone: 'unknown' },
      problems: [],
      attention: false,
      categories: new Set(),
      hibernated: false,
      ...over,
    }) as unknown as CNPGFleetRow
  const live = (standby1: any): CNPGRuntimeResponse => ({
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-10-04T10:00:00Z',
    permission: { proxy: 'allowed' },
    instances: [
      {
        pod: 'pg-6',
        role: 'primary',
        status: {
          state: 'ok',
          timeline: 7,
          currentLsn: '3/BC06F9E8',
          replication: [{ applicationName: 'pg-2', state: 'streaming', replayLsn: '3/BC06F9E8' }],
          slots: [
            { name: '_cnpg_pg_1', type: 'physical', active: false, retainedBytes: 4.7e9 },
            { name: '_cnpg_pg_2', type: 'physical', active: true, retainedBytes: 0 },
          ],
        },
        metrics: { state: 'ok' },
      },
      { pod: 'pg-1', role: 'replica', status: standby1, metrics: { state: 'ok' } },
      { pod: 'pg-2', role: 'replica', status: { state: 'ok', timeline: 7, roleDetail: 'streaming' }, metrics: { state: 'ok' } },
    ],
  })

  it('a standby with no row on the primary is a gap, with what it reports about itself', () => {
    const gaps = cnpgLiveStandbyGaps(row(), live({ state: 'ok', timeline: 4, replayPaused: true, roleDetail: 'replayPaused' }))
    expect(gaps.map((g) => g.pod)).toEqual(['pg-1'])
    expect(gaps[0].evidence).toEqual([
      'it has no row in the primary’s pg_stat_replication',
      'its WAL replay is paused',
      'it is on timeline 4 while the primary is on 7',
    ])
  })
  it('a fenced standby, or one running pg_rewind, is not a gap', () => {
    const fenced = row({ cluster: { metadata: { annotations: { 'cnpg.io/fencedInstances': '["pg-1"]' } }, status: {} } } as any)
    expect(cnpgLiveStandbyGaps(fenced, live({ state: 'ok', timeline: 7 }))).toEqual([])
    expect(cnpgLiveStandbyGaps(row(), live({ state: 'ok', timeline: 7, roleDetail: 'pgRewind' }))).toEqual([])
  })
  it('raises the gap and the inactive slot as problems, and names the standby in the replication fact', () => {
    const r = withLiveReplication(row(), live({ state: 'ok', timeline: 4, replayPaused: true }))
    expect(r.replication.text).toBe('1 of 2 expected standbys streaming · pg-1 not connected')
    expect(r.attention).toBe(true)
    expect(r.problems.map((p) => p.id).sort()).toEqual(['slot:db/pg:_cnpg_pg_1', 'standby:db/pg:pg-1'])
    expect(r.problems.find((p) => p.id === 'slot:db/pg:_cnpg_pg_1')!.title).toBe('Inactive slot _cnpg_pg_1 holds 4.4 GiB of WAL on pg-6 for pg-1')
  })
})
