import { describe, expect, it } from 'vitest'
import { backupNameFor, buildRestoreManifest, pickDefaultStandby, restoreSourcesFor } from './actionModel'

describe('CNPG action model', () => {
  it('names backups like kubectl-cnpg, in UTC', () => {
    expect(backupNameFor('pg-orders', new Date(Date.UTC(2026, 8, 28, 14, 5, 9)))).toBe('pg-orders-20260928140509')
  })

  it('prefers a synchronous standby, then the least lag, and never an ineligible one', () => {
    expect(
      pickDefaultStandby([
        { pod: 'a', podUID: '1', replayLagSeconds: 0.1, syncState: 'async' },
        { pod: 'b', podUID: '2', replayLagSeconds: 3, syncState: 'sync' },
        { pod: 'c', podUID: '3', ineligible: 'fenced', syncState: 'sync' },
      ])?.pod,
    ).toBe('b')
    expect(pickDefaultStandby([{ pod: 'a', podUID: '1', ineligible: 'not ready' }])).toBeUndefined()
  })
})

describe('restore manifests', () => {
  const cluster = {
    metadata: { name: 'pg-a', namespace: 'db' },
    spec: {
      imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'std', major: 17 },
      storage: { size: '10Gi' },
      plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store', serverName: 'pg-a-v2' } }],
    },
  }

  it('lists the object store first, then completed backups of this cluster only', () => {
    const sources = restoreSourcesFor(cluster, [
      { metadata: { name: 'b1' }, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'completed' } },
      { metadata: { name: 'b2' }, spec: { cluster: { name: 'other' } }, status: { phase: 'completed' } },
      { metadata: { name: 'b3' }, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'failed' } },
    ])
    expect(sources).toEqual([
      { kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' },
      { kind: 'backup', backup: 'b1' },
    ])
  })

  it('builds a plugin recovery that reads the source store and does not archive into it', () => {
    const m: any = buildRestoreManifest(cluster, { kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' }, 'pg-a-restore', '2026-09-28T12:00:00Z')
    expect(m.spec.bootstrap.recovery).toEqual({ source: 'origin', recoveryTarget: { targetTime: '2026-09-28T12:00:00Z' } })
    expect(m.spec.externalClusters[0].plugin.parameters).toEqual({ barmanObjectName: 'store', serverName: 'pg-a-v2' })
    expect(m.spec.plugins).toBeUndefined()
    expect(m.spec.imageCatalogRef.major).toBe(17)
  })

  it('restores from a named Backup without external clusters', () => {
    const m: any = buildRestoreManifest(cluster, { kind: 'backup', backup: 'b1' }, 'pg-a-restore')
    expect(m.spec.bootstrap.recovery).toEqual({ backup: { name: 'b1' } })
    expect(m.spec.externalClusters).toBeUndefined()
  })
})
