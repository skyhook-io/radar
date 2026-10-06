import { describe, expect, it } from 'vitest'
import {
  cnpgCertificateViews,
  cnpgCertificatesSummary,
  cnpgHASummary,
  cnpgImageDrift,
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
    ...over,
  }
}

it('does not call successful snapshot protection WAL archiving when no archive is configured', () => {
  const r = row()
  r.protection.destination = { text: 'Volume snapshots', tone: 'neutral', method: 'volumeSnapshot' }
  r.protection.walArchiving = { text: 'No archive destination configured', tone: 'neutral', source: 'Cluster spec' }
  r.protection.lastSuccessfulBackup = { text: 'Completed', tone: 'healthy', source: 'Backup snapshot' }
  expect(cnpgDimensions({ row: r }).find((d) => d.id === 'protection')).toMatchObject({ text: 'Backup completed', tone: 'healthy', source: 'Backup snapshot' })
})

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
    const s = cnpgZoneSpread(ha({ nodes: { state: 'denied', grant: { verb: 'get', resource: 'nodes' } } }))
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
    expect(cnpgQuorumFact(q({ object: { state: 'denied', grant: { verb: 'get', group: 'postgresql.cnpg.io', resource: 'failoverquorums', namespace: 'db' } } })).tone).toBe('unknown')
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
      ['storage', 'unknown'],
      ['protection', 'healthy'],
    ])
    expect(d.find((x) => x.id === 'protection')?.label).toBe('Backups')
  })
  it('storage follows the measured disk fact, and stays unassessed without a measurement', () => {
    const measured = cnpgDimensions({ row: row({ disk: { text: '91% used', tone: 'unhealthy', source: 'Fullest: data of pg-1' } }) })
    expect(measured[2]).toMatchObject({ id: 'storage', tone: 'unhealthy', text: '91% used' })
    const unmeasured = cnpgDimensions({ row: row({ disk: { text: 'No usage metrics', tone: 'unknown', source: 'needs Prometheus' } }) })
    expect(unmeasured[2]).toMatchObject({ tone: 'unknown', text: 'No usage metrics: needs Prometheus', source: '' })
  })
  it('names WAL an inactive slot holds even while volume usage is unassessed', () => {
    const slot = { id: 'slot:pg/pg:_cnpg_pg_2', reason: 'CNPGInactiveSlot', slot: '_cnpg_pg_2', severity: 'warning', category: 'availability', title: 'Inactive slot _cnpg_pg_2 holds 4.5 GiB of WAL on pg-1 for pg-2', subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'pg' }, source: 'measurement' } as const
    const r = row({ key: 'pg/pg', problems: [slot], disk: { text: 'No usage metrics', tone: 'unknown', source: 'no series' } })
    const d = cnpgDimensions({ row: r })
    expect(d[2]).toMatchObject({ id: 'storage', tone: 'degraded', text: 'WAL held by an inactive slot' })
    expect(d[2].source).toContain('4.5 GiB')
    expect(d[2].source).toContain('No usage metrics: no series')
    const measured = cnpgDimensions({ row: { ...r, disk: { text: '40% used', tone: 'healthy', source: 'Fullest' } } })
    expect(measured[2]).toMatchObject({ tone: 'degraded', text: '40% used · WAL held by an inactive slot' })
  })
  it('a standby another source saw receiving nothing keeps Replication from reading unassessed or calm', () => {
    const gap = { id: 'standby:pg/pg:pg-2', reason: 'CNPGStandbyNotReceiving', severity: 'warning', category: 'availability', title: 'pg-2 is not receiving WAL from the primary', subject: { kind: 'Pod', group: '', namespace: 'pg', name: 'pg-2' }, source: 'measurement' } as const
    const r = row({ key: 'pg/pg', problems: [gap] })
    expect(cnpgDimensions({ row: r })[1]).toMatchObject({ tone: 'degraded', text: 'pg-2 not receiving WAL' })
    const live = cnpgDimensions({ row: r, replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 0 } })[1]
    expect(live.tone).toBe('degraded')
    expect(live.text).toContain('pg-2 not receiving WAL')
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
    expect(at(8)).toMatchObject({ tone: 'degraded', text: '1 of 1 streaming · replay delay 8.0 s' })
    expect(at(23.6)).toMatchObject({ tone: 'degraded', text: '1 of 1 streaming · replay delay 23 s' })
    expect(at(72)).toMatchObject({ tone: 'unhealthy', text: '1 of 1 streaming · replay delay 72 s' })
    expect(at(72).tone).toBe(cnpgLagTone(72))
  })
  it('a missing standby does not hide a severe lag on the one that streams', () => {
    const r = row({ instances: { ready: 2, desired: 3 } })
    const d = cnpgDimensions({ row: r, replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 72 } })[1]
    expect(d).toMatchObject({ tone: 'unhealthy', text: '1 of 2 expected standbys streaming · replay delay 72 s' })
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

describe('folded HA and certificates summaries', () => {
  const pdbs = { state: 'ok' as const, enabled: true, items: [{ name: 'pg', role: 'replicas', disruptionsAllowed: 1, currentHealthy: 1, expectedPods: 1, observed: true }] } as unknown as CNPGClusterHA['pdbs']

  it('stays folded with the known facts when nothing is out of line', () => {
    const live = [{ pod: 'pg-1', state: 'ok' }, { pod: 'pg-2', state: 'ok' }] as never
    expect(cnpgHASummary(ha({ pdbs, operatorLease: { state: 'ok', holder: 'op_1' } }), live)).toEqual({ text: '2/2 observed instances ready · 2 zones · images match', attention: false })
  })

  it('claims neither readiness nor matching images without instances', () => {
    expect(cnpgHASummary(ha({ pdbs, instances: [], operatorLease: { state: 'ok' } }), [] as never)).toEqual({ text: 'no instance Pods', attention: true })
    const unset = ha({ pdbs, operatorLease: { state: 'ok' }, instances: [{ pod: 'pg-1', podUID: 'a', role: 'primary', ready: true, node: 'n1', zone: 'z1', restartCount: 0 }] })
    expect(cnpgHASummary(unset, [{ pod: 'pg-1', state: 'ok' }] as never).text).toBe('1/1 observed instances ready')
  })

  it('names what it could not read instead of reading calm', () => {
    const denied = { state: 'denied' as const, grant: { verb: 'list', resource: 'pods', namespace: 'db' } }
    const unread = ha({ pods: denied, nodes: denied, pdbs: { ...denied, enabled: true, items: [] }, primaryLease: denied, operatorLease: denied, jobs: { ...denied, items: [] } } as never)
    expect(cnpgHASummary(unread, undefined)).toEqual({
      text: 'Not read: Pods, zones, disruption budgets, primary lease, operator lease, Jobs, pending restarts',
      attention: false,
    })
    expect(cnpgHASummary(ha({ pdbs }), undefined).text).toBe('2/2 observed instances ready · 2 zones · images match · not read: operator lease, pending restarts')
  })

  it('opens and leads with what is wrong', () => {
    const shared = ha({
      pdbs,
      instances: [
        { pod: 'pg-1', podUID: 'a', role: 'primary', ready: true, node: 'n1', zone: 'z1', restartCount: 0, image: 'pg:17', imageMatches: true },
        { pod: 'pg-2', podUID: 'b', role: 'replica', ready: false, node: 'n1', zone: 'z1', restartCount: 0, image: 'pg:16', imageMatches: false },
      ],
    })
    const s = cnpgHASummary(shared, [{ pod: 'pg-2', state: 'ok', pendingRestart: true } as never])
    expect(s.attention).toBe(true)
    expect(s.text).toBe('1 of 2 instances not ready · every instance in one zone · instances share a Node · restart pending on pg-2 · an instance Pod has a different image · not read: operator lease')
  })

  it('names the nearest certificate expiry and who renews them', () => {
    const now = Date.parse('2026-09-30T00:00:00Z')
    const certs = [
      { secret: 'pg-ca', expiresAt: '2026-12-29T00:00:00Z', renewal: 'operator' },
      { secret: 'pg-server', expiresAt: '2026-10-20T00:00:00Z', renewal: 'operator' },
    ] as never
    expect(cnpgCertificatesSummary(certs, now)).toEqual({ text: '2 certificates · nearest reported expiry in 20 d (pg-server) · CloudNativePG renews them', attention: false })
    const oneUnread = [{ secret: 'pg-ca', expiresAt: '2026-12-29T00:00:00Z', renewal: 'operator' }, { secret: 'pg-server', raw: 'garbage', renewal: 'operator' }] as never
    expect(cnpgCertificatesSummary(oneUnread, now)).toEqual({ text: '2 certificates · nearest reported expiry in 90 d (pg-ca) · 1 expiry unreadable · CloudNativePG renews them', attention: true })
    const userSoon = [{ secret: 'app-tls', expiresAt: '2026-10-05T00:00:00Z', renewal: 'user' }] as never
    expect(cnpgCertificatesSummary(userSoon, now).attention).toBe(true)
    expect(cnpgCertificatesSummary([], now)).toEqual({ text: 'No expiry reported by the operator', attention: false })
  })
})

describe('replication chip and the sustained-lag finding', () => {
  it('reads no calmer than a standby measured far behind for the whole window', () => {
    const problem = { id: 'lag:db/pg', reason: 'CNPGSustainedLag', severity: 'critical', category: 'availability', title: 'pg-2 ≥ 24 h behind in every sample for 10 min', subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }, source: 'measurement' } as never
    const dims = cnpgDimensions({ row: row({ problems: [problem] }), replication: { streaming: 1, standbys: 1, maxReplayLagSeconds: 0 } })
    const rep = dims.find((d) => d.id === 'replication')!
    expect(rep.tone).toBe('unhealthy')
    expect(rep.text).toBe('1 of 1 standbys streaming · sustained lag')
    expect(rep.source).toBe('pg-2 ≥ 24 h behind in every sample for 10 min')
    const unread = cnpgDimensions({ row: row({ problems: [problem] }) }).find((d) => d.id === 'replication')!
    expect(unread).toMatchObject({ tone: 'unhealthy', text: 'sustained lag' })
  })
})

describe('backup dimension certainty', () => {
  it('never treats archiving alone as restorable', () => {
    const r = row()
    r.protection.lastSuccessfulBackup = { text: 'No successful backup yet', tone: 'degraded', source: 'Backups read in this namespace; none completed' }
    expect(cnpgDimensions({ row: r }).find((d) => d.id === 'protection')).toMatchObject({ text: 'CNPG reports archiving · no successful backup yet', tone: 'degraded', source: 'Backups read in this namespace; none completed' })
    r.protection.lastSuccessfulBackup = { text: 'No access to Backups', tone: 'unknown', source: 'Backups not read' }
    expect(cnpgDimensions({ row: r }).find((d) => d.id === 'protection')).toMatchObject({ text: 'unassessed', tone: 'unknown', source: 'Backups not read' })
  })
})

it('uses declared instances for readiness and opens for an instance Pod not observed', () => {
  const base = ha({ declaredInstances: 2, expectedInstances: ['pg-1', 'pg-2'] })
  base.instances = base.instances.slice(0, 1)
  const summary = cnpgHASummary(base, [{ pod: 'pg-1', state: 'ok' }])
  expect(summary.attention).toBe(true)
  expect(summary.text).toContain('1 of 2 declared instances ready; no instance Pod observed for pg-2')
  expect(summary.text).not.toContain('1/1')
})

it('names excess observed instances without putting them over a smaller denominator', () => {
  const summary = cnpgHASummary(ha({ declaredInstances: 1 }), undefined)
  expect(summary.text).toContain('2 observed instances ready; 1 instances declared')
  expect(summary.text).not.toContain('2 of 1')
})
it('does not claim matching images without observed image evidence', () => {
  expect(cnpgImageDrift(ha({ instances: [] })).known).toBe(false)
  const base = ha()
  base.instances[0].imageMatches = undefined
  expect(cnpgImageDrift(base).known).toBe(false)
})

it('uses a definitive non-serving verdict from an empty complete endpoint read without a primary', () => {
  const r = row({ cluster: { status: {} }, pods: [] })
  const empty = ha({ rwEndpoints: { state: 'ok', service: 'analytics-rw', pods: [] } })
  expect(cnpgDimensions({ row: r, ha: empty })[0]).toMatchObject({ tone: 'unhealthy', text: 'not serving: no ready read-write endpoint', source: 'EndpointSlices of Service analytics-rw' })
  expect(cnpgDimensions({ row: r, ha: empty })[0].tone).toBe('unhealthy')
  expect(cnpgDimensions({ row: r, ha: ha({ rwEndpoints: { state: 'denied', service: 'analytics-rw', pods: [] } }) })[0].tone).toBe('unknown')
  expect(cnpgDimensions({ row: { ...r, hibernated: true }, ha: empty })[0].text).toBe('hibernated')
})
it('uses storage reasons for loading, missing Prometheus and denied usage', () => {
  expect(cnpgDimensions({ row: row() })[2].text).toBe('Reading…')
  expect(cnpgDimensions({ row: row({ disk: { tone: 'unknown', text: 'No usage metrics', source: 'Prometheus not connected' } }) })[2].text).toBe('No usage metrics')
  expect(cnpgDimensions({ row: row({ disk: { tone: 'unknown', text: 'No access', source: 'Needs list persistentvolumeclaims in namespace db' } }) })[2].text).toContain('Needs list persistentvolumeclaims')
})

it('retains the missing standby and failover consequence when streaming is denied', () => {
  const h = ha({ expectedInstances: ['pg-1', 'pg-2'], instances: [ha().instances[0]] })
  const d = cnpgDimensions({ row: row(), ha: h, replicationGap: 'needs get pods/proxy in namespace db' }).find((d) => d.id === 'replication')!
  expect(d.tone).toBe('degraded')
  expect(d.text).toBe('Expected standby pg-2 is not running')
  expect(d.source).toContain('streaming not measured: needs get pods/proxy in namespace db')
  expect(d.source).toContain('No ready standby to fail over to')
  const unread = cnpgDimensions({ row: row(), ha: { ...h, pods: { state: 'denied' } } }).find((d) => d.id === 'replication')!
  expect(unread.text).toBe('unassessed')
  expect(unread.source).not.toContain('No ready standby')
})

it('keeps the Storage verdict concise while the Storage notice owns discovery details', () => {
  const r = row({ disk: { tone: 'unknown', text: 'No usage metrics', source: 'Prometheus not connected', detail: 'No working endpoint. Candidate monitoring/prometheus.' } })
  expect(cnpgDimensions({ row: r }).find((d) => d.id === 'storage')).toMatchObject({ text: 'No usage metrics', source: '' })
})
it('adds the failover consequence to measured replication without duplicating streaming facts', () => {
  const h = ha({ instances: [ha().instances[0]], expectedInstances: ['pg-1', 'pg-2'] })
  const d = cnpgDimensions({ row: row(), ha: h, replication: { streaming: 0, standbys: 0 } }).find((d) => d.id === 'replication')!
  expect(d.text).toBe('0 of 1 expected standbys streaming')
  expect(d.source).toContain('No ready standby to fail over to')
  expect(d.tone).toBe('degraded')
})

it('preserves separately measured standby gaps beside missing Pods and avoids inventing a grant', () => {
  const h = ha({ expectedInstances: ['pg-1', 'pg-2', 'pg-3'], instances: [ha().instances[0], { ...ha().instances[1], pod: 'pg-3' }] })
  const r = row({ instances: { desired: 3, ready: 2 }, problems: [{ id: 'standby:db/pg:pg-3', reason: 'CNPGStandbyNotReceiving', severity: 'warning', category: 'replication', title: 'pg-3 receiver is down', subject: { kind: 'Pod', name: 'pg-3' }, source: 'measurement' } as any] })
  const d = cnpgDimensions({ row: r, ha: h }).find((d) => d.id === 'replication')!
  expect(d.text).toContain('Expected standby pg-2 is not running')
  expect(d.text).toContain('pg-3 not receiving WAL')
  expect(d.source).toContain('pg-3 receiver is down')
  expect(d.source).toContain('streaming not measured: not read')
  expect(d.source).not.toContain('needs get pods/proxy')
  h.instances = [h.instances[0]]
  expect(cnpgDimensions({ row: r, ha: h }).find((d) => d.id === 'replication')!.text).toContain('Expected standbys pg-2, pg-3 are not running')
})
