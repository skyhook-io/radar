import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import fixtures from '../../../../../pkg/rook/testdata/status.json'
import { getGenericResourceStatus } from './generic-status'
import { getCellFilterValue } from './resource-utils'
import {
  getCephClusterStatus,
  getCephClusterStatusReason,
  getCephBlockPoolStatus,
  getCephBlockPoolStatusReason,
  getCephHealth,
  getCephStaleAfterMs,
  getRookColumnValue,
  getRookSortValue,
  getCephBlockPoolProtection,
  formatCephBytes,
  parseGoDuration,
} from './resource-utils-rook'
import { CephClusterRenderer, CephBlockPoolRenderer } from './renderers/RookCephRenderers'

const NOW = Date.parse(fixtures.now)

function fixtureObject(kind: string, c: { name: string; object: any }): any {
  const object = structuredClone(c.object)
  return { apiVersion: 'ceph.rook.io/v1', kind, ...object, metadata: { ...object.metadata, name: c.name, namespace: 'rook-ceph' } }
}

const cluster = (name: string) => fixtureObject('CephCluster', fixtures.clusters.find(c => c.name === name)!)
const pool = (name: string) => fixtureObject('CephBlockPool', fixtures.pools.find(c => c.name === name)!)

// Renderers and generic status read the clock, so give them a report checked just now.
function freshCluster(name: string): any {
  const d = cluster(name)
  if (d.status?.ceph?.lastChecked) d.status.ceph.lastChecked = new Date(Date.now() - 30_000).toISOString()
  return d
}

describe('getCephClusterStatus', () => {
  it.each(fixtures.clusters)('$name → $want', (c: any) => {
    expect(getCephClusterStatus(fixtureObject('CephCluster', c), NOW).text).toBe(c.want)
  })
  it('colors health by severity and never shows unknown reports as healthy', () => {
    expect(getCephClusterStatus(cluster('healthy'), NOW).level).toBe('healthy')
    expect(getCephClusterStatus(cluster('warning'), NOW).level).toBe('degraded')
    expect(getCephClusterStatus(cluster('error'), NOW).level).toBe('unhealthy')
    expect(getCephClusterStatus(cluster('reconcile-failed-health-ok'), NOW).level).toBe('degraded')
    for (const name of ['stale', 'stale-error', 'checks-disabled', 'no-check-time', 'external-connected', 'no-status']) {
      expect(getCephClusterStatus(cluster(name), NOW).level, name).toBe('unknown')
    }
    expect(getCephClusterStatus(cluster('terminating'), NOW).level).toBe('neutral')
  })
  it('explains the status', () => {
    expect(getCephClusterStatusReason(cluster('error'), NOW)).toBe('OSD_FULL: 1 full osd(s)\nPOOL_FULL: 1 pool(s) full')
    expect(getCephClusterStatusReason(cluster('reconcile-failed-health-ok'), NOW)).toBe('failed to create cluster: failed to start ceph monitors')
    expect(getCephClusterStatusReason(cluster('stale-error'), NOW)).toMatch(/^Rook last reported HEALTH_ERR .*; the current health is unknown\.$/)
    expect(getCephClusterStatusReason(cluster('healthy'), NOW)).toBeUndefined()
  })
  it('feeds the generic status with its reason', () => {
    const d = freshCluster('warning')
    expect(getGenericResourceStatus(d)).toMatchObject({ text: 'HEALTH_WARN', tone: 'degraded', reason: 'MON_DOWN: 1/3 mons down, quorum a,b' })
  })
})

describe('getCephHealth', () => {
  it('reads times and orders errors first', () => {
    const h = getCephHealth(cluster('error'), NOW)
    expect(h.state).toBe('fresh')
    expect(h.checks.map(c => c.code)).toEqual(['OSD_FULL', 'POOL_FULL'])
    const w = getCephHealth(cluster('warning'), NOW)
    expect([w.lastChanged, w.previous]).toEqual(['2026-11-20T11:40:00Z', 'HEALTH_OK'])
  })
  it('derives the stale threshold from the configured interval', () => {
    expect(getCephStaleAfterMs(cluster('healthy'))).toBe(10 * 60_000)
    expect(getCephStaleAfterMs(cluster('long-interval-not-stale'))).toBe(90 * 60_000)
    expect(parseGoDuration('1m30s')).toBe(90_000)
    expect(parseGoDuration('500ms')).toBe(500)
    expect(parseGoDuration('soon')).toBeUndefined()
  })
})

describe('getCephBlockPoolStatus', () => {
  it.each(fixtures.pools)('$name → $want', (c: any) => {
    expect(getCephBlockPoolStatus(fixtureObject('CephBlockPool', c)).text).toBe(c.want)
  })
  it('explains failures and blocked deletion', () => {
    expect(getCephBlockPoolStatusReason(pool('failure'))).toBe('Failed to create the block pool. See the operator log for details.')
    expect(getCephBlockPoolStatusReason(pool('deletion-blocked'))).toBe('pool "replicapool" contains images and/or snapshots')
    expect(getGenericResourceStatus(pool('ready'))).toMatchObject({ text: 'Ready', tone: 'healthy' })
  })
  it('reads data protection', () => {
    expect(getCephBlockPoolProtection(pool('ready'))?.text).toBe('Replicated, size 3')
    expect(getCephBlockPoolProtection({ spec: { replicated: { size: 0 }, erasureCoded: { dataChunks: 4, codingChunks: 2 } } })?.text).toBe('Erasure coded, 4+2')
    expect(getCephBlockPoolProtection({ spec: {} })).toBeUndefined()
  })
})

describe('columns', () => {
  it('fills the cluster columns', () => {
    const d = freshCluster('error')
    d.spec = { mon: { count: 3 }, external: { enable: false } }
    d.status.version = { version: '19.2.3-0' }
    d.status.ceph.capacity = { bytesTotal: 3 * 1024 ** 4, bytesUsed: 1024 ** 4 }
    expect(getRookColumnValue(d, 'status')).toBe('HEALTH_ERR')
    expect(getRookColumnValue(d, 'cephHealthChecks')).toBe('OSD_FULL, POOL_FULL')
    expect(getRookColumnValue(d, 'cephCapacity')).toBe('1.0 TiB / 3.0 TiB')
    expect(getRookSortValue(d, 'cephCapacity')).toBeCloseTo(1 / 3)
    expect(getRookColumnValue(d, 'cephMons')).toBe('3')
    expect(getRookSortValue(d, 'cephMons')).toBe(3)
    expect(getRookColumnValue(d, 'cephExternal')).toBe('No')
    expect(getRookColumnValue(d, 'cephVersion')).toBe('19.2.3-0')
    expect(getRookColumnValue(d, 'cephPhase')).toBe('Ready')
    expect(getCellFilterValue(d, 'cephHealthChecks', 'cephclusters')).toBe('OSD_FULL, POOL_FULL')
  })
  it('hides health checks from a stale report and sorts missing values first', () => {
    const d = cluster('stale-error')
    d.status.ceph.details = { OSD_FULL: { severity: 'HEALTH_ERR', message: '1 full osd(s)' } }
    expect(getRookColumnValue(d, 'cephHealthChecks')).toBe('')
    expect(getRookSortValue(cluster('no-status'), 'cephCapacity')).toBe(-1)
    expect(getRookSortValue(cluster('no-status'), 'cephLastChecked')).toBe(-1)
  })
  it('fills the pool columns', () => {
    const d = pool('ready')
    expect(getRookColumnValue(d, 'cephPoolProtection')).toBe('Replicated, size 3')
    expect(getRookColumnValue(d, 'cephFailureDomain')).toBe('host')
    expect(getRookColumnValue(d, 'cephMirroring')).toBe('Off')
    d.spec.mirroring = { enabled: true, mode: 'image' }
    expect(getRookColumnValue(d, 'cephMirroring')).toBe('image')
    expect(getRookColumnValue({ ...pool('progressing'), status: { info: { failureDomain: 'host' } } }, 'cephFailureDomain')).toBe('host')
  })
  it('claims only the exact API group', () => {
    for (const apiVersion of ['fake.ceph.rook.io/v1', 'ceph.example.com/v1']) {
      expect(getRookColumnValue({ ...cluster('healthy'), apiVersion }, 'cephPhase')).toBeUndefined()
      expect(getRookColumnValue({ ...pool('ready'), apiVersion }, 'cephPoolProtection')).toBeUndefined()
    }
  })
  it('formats byte counts', () => {
    expect(formatCephBytes(512)).toBe('512 B')
    expect(formatCephBytes(1536)).toBe('1.5 KiB')
    expect(formatCephBytes(5 * 1024 ** 5)).toBe('5.0 PiB')
  })
})

describe('Rook renderers', () => {
  it('shows health checks and cluster facts', () => {
    const d = freshCluster('error')
    d.spec = { mon: { count: 3 }, cephVersion: { image: 'quay.io/ceph/ceph:v19.2.3' }, dataDirHostPath: '/var/lib/rook' }
    d.status.storage = { deviceClasses: [{ name: 'ssd' }], osd: { storeType: { bluestore: 3 } } }
    const html = renderToStaticMarkup(<CephClusterRenderer data={d} />)
    for (const text of ['OSD_FULL', '1 full osd(s)', 'POOL_FULL', 'quay.io/ceph/ceph:v19.2.3', '/var/lib/rook', '3 (3 bluestore)', 'ssd', 'they do not show that it does']) expect(html).toContain(text)
  })
  it('flags stale and disabled health and a failed reconcile', () => {
    expect(renderToStaticMarkup(<CephClusterRenderer data={cluster('stale-error')} />)).toContain('Ceph health is stale')
    expect(renderToStaticMarkup(<CephClusterRenderer data={cluster('checks-disabled')} />)).toContain('Ceph health checks are disabled')
    const failed = renderToStaticMarkup(<CephClusterRenderer data={freshCluster('reconcile-failed-health-ok')} />)
    expect(failed).toContain('last reconcile failed')
    expect(failed).toContain('failed to start ceph monitors')
  })
  it('reads missing status as not reported', () => {
    const html = renderToStaticMarkup(<CephClusterRenderer data={cluster('no-status')} />)
    expect(html).toContain('Not reported')
    expect(html).not.toContain('HEALTH_OK')
  })
  it('shows pool protection, failure and blocked deletion', () => {
    const ready = renderToStaticMarkup(<CephBlockPoolRenderer data={pool('ready')} />)
    for (const text of ['Replicated, size 3', 'host', 'placement groups are healthy']) expect(ready).toContain(text)
    expect(renderToStaticMarkup(<CephBlockPoolRenderer data={pool('failure')} />)).toContain('Rook failed to configure this pool')
    expect(renderToStaticMarkup(<CephBlockPoolRenderer data={pool('deletion-blocked')} />)).toContain('contains images and/or snapshots')
  })
  it('shows mirroring when enabled', () => {
    const d = pool('ready')
    d.spec.mirroring = { enabled: true, mode: 'image', snapshotSchedules: [{ interval: '24h' }] }
    d.status.mirroringStatus = { summary: { health: 'WARNING', daemon_health: 'OK', image_health: 'WARNING' }, lastChecked: '2026-11-20T11:59:00Z' }
    const html = renderToStaticMarkup(<CephBlockPoolRenderer data={d} />)
    for (const text of ['Mirroring', 'image', '24h', 'WARNING']) expect(html).toContain(text)
  })
})
