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

describe('a live read replaces what it disproves', () => {
  const base = (problems: any[]): CNPGFleetRow =>
    ({
      key: 'db/pg',
      namespace: 'db',
      name: 'pg',
      cluster: { metadata: {}, status: {} },
      instances: { ready: 2, desired: 2 },
      pods: [],
      replication: { text: '2/2 Pods ready · pg-2 not receiving WAL', tone: 'degraded' },
      problems,
      attention: true,
      categories: new Set(['availability']),
      hibernated: false,
    }) as unknown as CNPGFleetRow
  const fleetStandby = { id: 'standby:db/pg:pg-2', severity: 'warning', category: 'availability', title: 'pg-2 is not receiving WAL from the primary', subject: { kind: 'Pod', group: '', namespace: 'db', name: 'pg-2' }, source: 'measurement' }
  const fleetSlot = { id: 'slot:db/pg:_cnpg_pg_2', severity: 'warning', category: 'availability', title: 'Inactive slot _cnpg_pg_2 holds 2 GiB', subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }, source: 'measurement' }
  const rt = (primaryStatus: any): CNPGRuntimeResponse => ({
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-10-04T10:00:00Z',
    permission: { proxy: 'allowed' },
    instances: [
      { pod: 'pg-1', role: 'primary', status: primaryStatus, metrics: { state: 'ok' } },
      { pod: 'pg-2', role: 'replica', status: { state: 'ok', timeline: 1 }, metrics: { state: 'ok' } },
    ],
  })
  it('clears the fleet’s standby and slot problems when the primary streams to it and the slot is active', () => {
    const r = withLiveReplication(
      base([fleetStandby, fleetSlot]),
      rt({ state: 'ok', timeline: 1, replication: [{ applicationName: 'pg-2', state: 'streaming' }], slots: [{ name: '_cnpg_pg_2', type: 'physical', active: true, retainedBytes: 0 }] }),
    )
    expect(r.problems).toEqual([])
    expect(r.attention).toBe(false)
    expect(r.replication.text).toBe('1/1 streaming')
  })
  it('judges slots from a partial primary report the replication rows cannot use', () => {
    const r = withLiveReplication(base([]), rt({ state: 'partial', incomplete: true, replication: null, slots: [{ name: '_cnpg_pg_2', type: 'physical', active: false, retainedBytes: 3 * 1024 ** 3 }] }))
    expect(r.problems.map((p) => p.id)).toEqual(['slot:db/pg:_cnpg_pg_2'])
  })
})
