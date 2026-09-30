import { describe, expect, it } from 'vitest'
import {
  cnpgCertificateViews,
  cnpgDimensions,
  cnpgPDBFact,
  cnpgLeaseHolderPod,
  cnpgLiveGap,
  cnpgPendingRestart,
  cnpgQuorumFact,
  cnpgZoneSpread,
  type CNPGClusterHA,
  type CNPGHAQuorum,
} from './ha'
import { cnpgLagTone, type CNPGFleetRow } from './workspace'

function ha(over: Partial<CNPGClusterHA> = {}): CNPGClusterHA {
  return {
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-09-30T12:00:00Z',
    desiredImage: 'pg:17',
    instances: [
      { pod: 'pg-1', podUID: 'a', role: 'primary', ready: true, node: 'n1', zone: 'z1', restartCount: 0, image: 'pg:17', imageMatches: true },
      { pod: 'pg-2', podUID: 'b', role: 'replica', ready: true, node: 'n2', zone: 'z2', restartCount: 0, image: 'pg:17', imageMatches: true },
    ],
    pods: { state: 'ok' },
    nodes: { state: 'ok' },
    quorum: { enabled: false, object: { state: 'notFound' } },
    pdbs: { state: 'ok', enabled: true, items: [] },
    primaryLease: { state: 'notFound' },
    operatorLease: { state: 'unavailable' },
    jobs: { state: 'ok', items: [] },
    rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-1'] },
    certificates: [],
    maintenance: { declared: false, inProgress: false, reusePVC: true },
    ...over,
  }
}

function row(over: Partial<CNPGFleetRow> = {}): CNPGFleetRow {
  return {
    key: 'db/pg',
    namespace: 'db',
    name: 'pg',
    cluster: { status: { currentPrimary: 'pg-1' } },
    controllerStatus: { text: 'Healthy', level: 'healthy' },
    instances: { ready: 2, desired: 2 },
    pods: [
      { name: 'pg-1', role: 'primary', ready: true },
      { name: 'pg-2', role: 'replica', ready: true },
    ],
    replicaCluster: null,
    hibernated: false,
    pgVersion: '17',
    catalog: null,
    replication: { text: 'Lag unknown', tone: 'unknown' },
    protection: {
      schedule: { text: 'Active', tone: 'healthy', names: [] },
      destination: { text: 'ObjectStore', tone: 'healthy', method: 'plugin' },
      lastSuccessfulBackup: { text: '1 h ago', tone: 'healthy' },
      walArchiving: { text: 'Archiving', tone: 'healthy' },
      recoveryWindow: { text: 'x', tone: 'healthy' },
      restoreValidation: { text: 'None recorded', tone: 'unknown' },
      summary: { text: 'ok', tone: 'healthy' },
    },
    declarations: { summary: { text: 'None', tone: 'neutral' }, total: 0, failed: 0, pending: 0 },
    poolers: [],
    poolersKnown: true,
    problems: [],
    attention: false,
    categories: new Set(),
    gitops: null,
    ...over,
  }
}

describe('cnpgZoneSpread', () => {
  it('groups instances by zone and names the primary’s', () => {
    const s = cnpgZoneSpread(ha())
    expect(s.known).toBe(true)
    expect(s.zones.map((z) => z.zone)).toEqual(['z1', 'z2'])
    expect(s.primaryZone).toBe('z1')
    expect(s.singleZone).toBe(false)
  })
  it('flags every instance in one zone and a shared node', () => {
    const s = cnpgZoneSpread(
      ha({
        instances: [
          { pod: 'pg-1', podUID: 'a', role: 'primary', ready: true, node: 'n1', zone: 'z1', restartCount: 0 },
          { pod: 'pg-2', podUID: 'b', role: 'replica', ready: true, node: 'n1', zone: 'z1', restartCount: 0 },
        ],
      }),
    )
    expect(s.singleZone).toBe(true)
    expect(s.sharedNode).toBe(true)
  })
  it('is unknown, not single-zone, when Nodes are not readable', () => {
    const s = cnpgZoneSpread(ha({ nodes: { state: 'denied', grant: 'get nodes' } }))
    expect(s.known).toBe(false)
    expect(s.singleZone).toBe(false)
  })
})

describe('cnpgQuorumFact', () => {
  const q = (over: Partial<CNPGHAQuorum>): CNPGHAQuorum => ({ enabled: true, object: { state: 'ok' }, ...over })
  it('states R + W > N when it holds', () => {
    const f = cnpgQuorumFact(q({ n: 2, w: 1, r: 2, holds: true, status: { standbyNames: ['a', 'b'], standbyNumber: 1 } }))
    expect(f.text).toContain('R + W > N')
    expect(f.tone).toBe('healthy')
  })
  it('states when it does not', () => {
    const f = cnpgQuorumFact(q({ n: 2, w: 1, r: 1, holds: false }))
    expect(f.text).toContain('R + W ≤ N')
    expect(f.tone).toBe('degraded')
  })
  it('a reset object is no configuration, not a pass', () => {
    expect(cnpgQuorumFact(q({ status: { standbyNames: [], standbyNumber: 0 } })).text).toContain('no synchronous configuration recorded')
  })
  it('an unreadable object is unknown', () => {
    expect(cnpgQuorumFact(q({ object: { state: 'denied', grant: 'get failoverquorums' } })).tone).toBe('unknown')
  })
})

describe('cnpgPDBFact', () => {
  it('distinguishes disabled from missing and unreadable', () => {
    expect(cnpgPDBFact({ state: 'ok', enabled: false, items: [] }).tone).toBe('neutral')
    expect(cnpgPDBFact({ state: 'ok', enabled: true, items: [] }).tone).toBe('degraded')
    expect(cnpgPDBFact({ state: 'denied', enabled: true, items: [] }).tone).toBe('unknown')
  })
})

describe('cnpgCertificateViews', () => {
  const now = Date.parse('2026-09-30T00:00:00Z')
  const at = (days: number) => new Date(now + days * 86_400_000).toISOString()
  it('uses the issues-engine thresholds and owner', () => {
    const v = cnpgCertificateViews(
      [
        { secret: 'u-20', raw: '', expiresAt: at(20), renewal: 'user' },
        { secret: 'u-3', raw: '', expiresAt: at(3), renewal: 'user' },
        { secret: 'o-3', raw: '', expiresAt: at(3), renewal: 'operator' },
        { secret: 'x', raw: 'garbage', renewal: 'operator' },
      ],
      now,
    )
    expect(v.map((c) => c.tone)).toEqual(['degraded', 'unhealthy', 'healthy', 'unknown'])
    expect(v[0].daysLeft).toBe(20)
  })
})

describe('cnpgPendingRestart', () => {
  it('is unknown without live data and names the instances otherwise', () => {
    expect(cnpgPendingRestart(undefined).known).toBe(false)
    const p = cnpgPendingRestart([
      { pod: 'pg-1', state: 'ok', pendingRestart: true, pendingRestartForDecrease: true },
      { pod: 'pg-2', state: 'unreachable' },
    ])
    expect(p.pods).toEqual(['pg-1'])
    expect(p.forDecrease).toBe(true)
    expect(p.known).toBe(false)
  })
  it('does not count an incomplete report as "no restart pending"', () => {
    const p = cnpgPendingRestart([
      { pod: 'pg-1', state: 'ok' },
      { pod: 'pg-2', state: 'partial', incomplete: true },
    ])
    expect(p.known).toBe(false)
    expect(cnpgPendingRestart([{ pod: 'pg-2', state: 'partial', incomplete: true }]).known).toBe(false)
  })
})

describe('cnpgLiveGap', () => {
  it('names the missing grant when there is no runtime read', () => {
    expect(cnpgLiveGap(undefined, 'needs get pods/proxy in db')).toBe('needs get pods/proxy in db')
  })
  it('names each instance that did not report, and why, when runtime access exists', () => {
    const gap = cnpgLiveGap([
      { pod: 'pg-1', state: 'ok' },
      { pod: 'pg-2', state: 'unreachable', reason: 'PostgreSQL is not running on this instance' },
      { pod: 'pg-3', state: 'partial', incomplete: true, reason: 'pg_rewind is running' },
    ])
    expect(gap).toBe('pg-2 did not report (PostgreSQL is not running on this instance); pg-3 reported incompletely (pg_rewind is running)')
    expect(gap).not.toContain('runtime access')
    expect(cnpgLiveGap([{ pod: 'pg-1', state: 'ok' }])).toBe('')
  })
})

describe('cnpgDimensions', () => {
  it('reads each dimension from its own source and leaves storage unassessed', () => {
    const d = cnpgDimensions({ row: row(), ha: ha(), replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 0 } })
    expect(d.map((x) => [x.id, x.tone])).toEqual([
      ['serving', 'healthy'],
      ['replication', 'healthy'],
      ['protection', 'healthy'],
      ['storage', 'unknown'],
    ])
  })
  it('storage follows the measured disk fact, and stays unassessed without a measurement', () => {
    const measured = cnpgDimensions({ row: row({ disk: { text: '91% used', tone: 'unhealthy', source: 'Fullest: data of pg-1' } }) })
    expect(measured[3]).toMatchObject({ id: 'storage', tone: 'unhealthy', text: '91% used' })
    const unmeasured = cnpgDimensions({ row: row({ disk: { text: 'No usage metrics', tone: 'unknown', source: 'needs Prometheus' } }) })
    expect(unmeasured[3]).toMatchObject({ tone: 'unknown', text: 'unassessed', source: 'No usage metrics · needs Prometheus' })
  })
  it('counts expected standbys from spec.instances, not from the Pods still running', () => {
    // spec.instances 3, only the primary's Pod exists, nothing streams.
    const r = row({ instances: { ready: 1, desired: 3 }, pods: [{ name: 'pg-1', role: 'primary', ready: true }] })
    const d = cnpgDimensions({ row: r, replication: { streaming: 0, standbys: 0 } })
    expect(d[1]).toMatchObject({ tone: 'degraded', text: '0 of 2 expected standbys streaming' })
    const one = cnpgDimensions({ row: r, replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 0 } })
    expect(one[1]).toMatchObject({ tone: 'degraded', text: '1 of 2 expected standbys streaming' })
  })
  it('replication lag takes the same tone as the Replication fact', () => {
    const at = (lag: number) => cnpgDimensions({ row: row(), replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: lag } })[1]
    expect(at(2)).toMatchObject({ tone: 'healthy' })
    expect(at(8)).toMatchObject({ tone: 'degraded', text: '1 of 1 streaming · replay 8 s behind' })
    expect(at(23.6)).toMatchObject({ tone: 'degraded', text: '1 of 1 streaming · replay 24 s behind' })
    expect(at(72)).toMatchObject({ tone: 'unhealthy', text: '1 of 1 streaming · replay 72 s behind' })
    expect(at(72).tone).toBe(cnpgLagTone(72))
  })
  it('a missing standby does not hide a severe lag on the one that streams', () => {
    const r = row({ instances: { ready: 2, desired: 3 } })
    const d = cnpgDimensions({ row: r, replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 72 } })[1]
    expect(d).toMatchObject({ tone: 'unhealthy', text: '1 of 2 expected standbys streaming · replay 72 s behind' })
    expect(cnpgDimensions({ row: r, replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 0.2 } })[1]).toMatchObject({
      tone: 'degraded',
      text: '1 of 2 expected standbys streaming',
    })
  })
  it('replication is unknown when spec.instances is not reported', () => {
    const d = cnpgDimensions({ row: row({ instances: { ready: null, desired: null } }), replication: { streaming: 0, standbys: 0 } })
    expect(d[1].tone).toBe('unknown')
  })
  it('replication is unassessed without runtime, never healthy', () => {
    const d = cnpgDimensions({ row: row() })
    expect(d.find((x) => x.id === 'replication')?.text).toBe('unassessed')
  })
  it('serving fails when the rw endpoint is not on the primary', () => {
    const d = cnpgDimensions({ row: row(), ha: ha({ rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-2'] } }) })
    expect(d[0].tone).toBe('unhealthy')
  })
  it('serving is unassessed when instance Pods are not readable', () => {
    const d = cnpgDimensions({ row: row({ pods: [] }) })
    expect(d[0].text).toBe('unassessed')
  })
})

describe('cnpgLeaseHolderPod', () => {
  it('names the Pod of a controller-runtime holder identity', () => {
    expect(cnpgLeaseHolderPod('cnpg-controller-manager-5fbdd6bb78-jx82z_6ec0566c-6da6-47aa-9ab1-0e5d3b2c1f11')).toBe('cnpg-controller-manager-5fbdd6bb78-jx82z')
    expect(cnpgLeaseHolderPod('pg-1')).toBe('pg-1')
  })
})
