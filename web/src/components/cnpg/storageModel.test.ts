import { describe, expect, it } from 'vitest'
import { buildResizeManifest } from './storageModel'

const cluster = {
  metadata: { name: 'pg', namespace: 'db' },
  spec: {
    storage: { size: '10Gi', storageClass: 'fast' },
    tablespaces: [
      { name: 'hot', storage: { size: '5Gi' }, owner: { name: 'app' } },
      { name: 'cold', storage: { pvcTemplate: { resources: { requests: { storage: '50Gi' } }, storageClassName: 'slow' } } },
    ],
  },
}

describe('buildResizeManifest', () => {
  it('applies only the data size', () => {
    const m = buildResizeManifest(cluster, { role: 'PG_DATA', field: 'spec.storage.size', declared: '10Gi' }, '20Gi')
    expect(m).toEqual({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db' }, spec: { storage: { size: '20Gi' } } })
  })

  it('writes WAL sizes under walStorage, following a pvcTemplate', () => {
    const m = buildResizeManifest(cluster, { role: 'PG_WAL', field: 'spec.walStorage.pvcTemplate.resources.requests.storage' }, '4Gi')
    expect(m.spec).toEqual({ walStorage: { pvcTemplate: { resources: { requests: { storage: '4Gi' } } } } })
  })

  it('sends every tablespace, changing only the one named', () => {
    const m = buildResizeManifest(cluster, { role: 'PG_TABLESPACE', tablespace: 'cold', field: 'spec.tablespaces[name=cold].storage.pvcTemplate.resources.requests.storage' }, '80Gi')
    expect(m.spec.tablespaces[0]).toEqual(cluster.spec.tablespaces[0])
    expect(m.spec.tablespaces[1].storage).toEqual({ pvcTemplate: { resources: { requests: { storage: '80Gi' } }, storageClassName: 'slow' } })
    expect(cluster.spec.tablespaces[1]!.storage.pvcTemplate?.resources.requests.storage).toBe('50Gi')
  })
})
