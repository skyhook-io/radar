import { describe, expect, it } from 'vitest'
import { cnpgLogicalPaths, cnpgLogicalSlotFact, cnpgResolvePublisher } from './logicalReplication'

const G = 'postgresql.cnpg.io/v1'
const cluster = (name: string, ns: string, spec: any = {}, status: any = {}) => ({ apiVersion: G, kind: 'Cluster', metadata: { name, namespace: ns }, spec: { instances: 3, ...spec }, status })
const sub = (spec: any, status: any = {}) => ({ apiVersion: G, kind: 'Subscription', metadata: { name: 'orders-sub', namespace: 'dst' }, spec: { cluster: { name: 'dst' }, name: 'orders_sub', dbname: 'app', publicationName: 'orders_pub', externalClusterName: 'src', ...spec }, status })
const pub = { apiVersion: G, kind: 'Publication', metadata: { name: 'orders-pub', namespace: 'src' }, spec: { cluster: { name: 'src' }, name: 'orders_pub', dbname: 'app', target: { allTables: true } }, status: { applied: true } }
const subscriber = (host: string) => cluster('dst', 'dst', { externalClusters: [{ name: 'src', connectionParameters: { host, dbname: 'app', user: 'app' } }] })
const synced = (major: number) => cluster('src', 'src', { replicationSlots: { highAvailability: { synchronizeLogicalDecoding: true } } }, { pgDataImageInfo: { majorVersion: major } })

describe('cnpgResolvePublisher', () => {
  const clusters = [cluster('src', 'src'), cluster('dst', 'dst')]
  it('resolves -rw/-ro/-r Service hosts in any spelling to the visible Cluster', () => {
    for (const host of ['src-rw.src.svc', 'src-rw.src.svc.cluster.local', 'src-ro.src', 'SRC-R.src.svc']) {
      const p = cnpgResolvePublisher(host, 'dst', clusters, [])
      expect(p.kind === 'cluster' && p.name).toBe('src')
    }
  })
  it('resolves through a Pooler Service, and a bare name in the subscriber namespace', () => {
    const p = cnpgResolvePublisher('src-pooler.src.svc', 'dst', clusters, [{ metadata: { name: 'src-pooler', namespace: 'src' }, spec: { cluster: { name: 'src' } } }])
    expect(p.kind === 'cluster' && p.via).toBe('Pooler src-pooler')
    expect(cnpgResolvePublisher('dst-rw', 'dst', clusters, []).kind).toBe('cluster')
  })
  it('never guesses a cluster from an external host', () => {
    expect(cnpgResolvePublisher('src-rw.example.com', 'dst', clusters, []).kind).toBe('external')
    expect(cnpgResolvePublisher('other-rw.src.svc', 'dst', clusters, []).kind).toBe('external')
    expect(cnpgResolvePublisher(undefined, 'dst', clusters, []).kind).toBe('external')
  })
})

describe('cnpgLogicalPaths', () => {
  it('walks subscription → external cluster → publisher → publication object → slot', () => {
    const [p] = cnpgLogicalPaths([sub({})], [synced(17), subscriber('src-rw.src.svc')], [pub], [])
    expect(p.publisher.kind === 'cluster' && `${p.publisher.namespace}/${p.publisher.name}`).toBe('src/src')
    expect(p.publication.object?.name).toBe('orders-pub')
    expect(p.publication.dbname).toBe('app')
    expect(p.slot.name).toBe('orders_sub')
  })
  it('says when the external cluster is not declared or the publication is not a CNPG object', () => {
    const [missing] = cnpgLogicalPaths([sub({ externalClusterName: 'nope' })], [synced(17), subscriber('src-rw.src.svc')], [pub], [])
    expect(missing.publisher.kind).toBe('external')
    expect(missing.externalCluster.declared).toBe(false)
    const [sqlOnly] = cnpgLogicalPaths([sub({ publicationName: 'made_in_sql' })], [synced(17), subscriber('src-rw.src.svc')], [pub], [])
    expect(sqlOnly.publication.object).toBeUndefined()
  })
  it('says a Publication is unknown when the publisher namespace\'s Publications are unreadable', () => {
    const [p] = cnpgLogicalPaths([sub({})], [synced(17), subscriber('src-rw.src.svc')], [], [], (ns) => (ns === 'src' ? 'No access to Publications' : null))
    expect(p.publication.object).toBeUndefined()
    expect(p.publication.unavailable).toBe('No access to Publications')
    const [readable] = cnpgLogicalPaths([sub({})], [synced(17), subscriber('src-rw.src.svc')], [], [], () => null)
    expect(readable.publication.unavailable).toBeUndefined()
  })
  it('names the slot from slot_name, and none for slot_name = NONE', () => {
    expect(cnpgLogicalPaths([sub({ parameters: { slot_name: 'custom' } })], [], [], [])[0].slot.name).toBe('custom')
    expect(cnpgLogicalPaths([sub({ parameters: { slot_name: 'NONE' } })], [], [], [])[0].slot.name).toBeUndefined()
  })
})

describe('slot failover', () => {
  const failoverOf = (publisher: any, params?: any) =>
    cnpgLogicalPaths([sub(params ? { parameters: params } : {})], [publisher, subscriber('src-rw.src.svc')], [], [])[0].failover
  it('is lost when the publisher does not synchronize logical slots', () => {
    const f = failoverOf(cluster('src', 'src'))
    expect(f.tone).toBe('degraded')
    expect(f.text).toContain('synchronizeLogicalDecoding is off')
  })
  it('on PostgreSQL 17 needs the subscription to request failover', () => {
    expect(failoverOf(synced(17)).tone).toBe('degraded')
    expect(failoverOf(synced(17), { failover: 'true' }).tone).toBe('healthy')
  })
  it('is lost when HA slots are disabled, whatever synchronizeLogicalDecoding says', () => {
    const off = cluster('src', 'src', { replicationSlots: { highAvailability: { enabled: false, synchronizeLogicalDecoding: true } } }, { pgDataImageInfo: { majorVersion: 17 } })
    const f = failoverOf(off, { failover: 'true' })
    expect(f.tone).toBe('degraded')
    expect(f.text).toContain('HA replication slots are disabled')
  })
  it('is unknown before 17 (pg_failover_slots), without a major, or outside Radar', () => {
    expect(failoverOf(synced(16)).tone).toBe('unknown')
    expect(failoverOf(cluster('src', 'src', { replicationSlots: { highAvailability: { synchronizeLogicalDecoding: true } } })).tone).toBe('unknown')
    expect(cnpgLogicalPaths([sub({})], [subscriber('db.example.com')], [], [])[0].failover.tone).toBe('unknown')
  })
})

describe('cnpgLogicalSlotFact', () => {
  const [path] = cnpgLogicalPaths([sub({}, { applied: true })], [synced(17), subscriber('src-rw.src.svc')], [pub], [])
  it('never reads a denied or unread runtime as a missing slot', () => {
    expect(cnpgLogicalSlotFact(path, { state: 'denied' }).tone).toBe('unknown')
    expect(cnpgLogicalSlotFact(path, { state: 'unavailable', reason: 'unreachable' }).text).toContain('not read (unreachable)')
  })
  it('reports the observed slot, and a missing one only from a readable report', () => {
    const ok = cnpgLogicalSlotFact(path, { state: 'ok', slots: [{ name: 'orders_sub', type: 'logical', active: true, retainedBytes: 2048, walStatus: 'reserved' }] })
    expect(ok.text).toBe('Slot orders_sub · logical · active · retains 2.0 KiB of WAL · WAL reserved')
    expect(ok.tone).toBe('healthy')
    expect(cnpgLogicalSlotFact(path, { state: 'ok', slots: [{ name: 'orders_sub', active: false }] }).tone).toBe('degraded')
    const capped = cnpgLogicalSlotFact(path, { state: 'partial', reason: '250 replication slots; the first 200 are shown', slots: [] })
    expect(capped.text).toContain('not in the reported slots (report incomplete: 250 replication slots')
    expect(capped.tone).toBe('unknown')
    const stale = cnpgLogicalSlotFact(path, { state: 'ok', stale: true, slots: [{ name: 'orders_sub', type: 'logical', active: true }] })
    expect(stale.tone).toBe('unknown')
    expect(stale.text).toContain('from an earlier read')
    const gone = cnpgLogicalSlotFact(path, { state: 'ok', slots: [] })
    expect(gone.text).toContain('not found')
    expect(gone.tone).toBe('degraded')
  })
})
