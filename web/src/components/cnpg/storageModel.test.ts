import { describe, expect, it } from 'vitest'
import { buildResizeManifest, cnpgSharedExpansionGap, cnpgSlotRetentionText } from './storageModel'

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

describe('cnpgSlotRetentionText', () => {
  it('shows the largest slot, never a sum, and 0 B when nothing is held', () => {
    expect(cnpgSlotRetentionText([])).toBe('No slots')
    expect(cnpgSlotRetentionText([{ slot: 'a', bytes: 0 }])).toBe('0 B')
    expect(cnpgSlotRetentionText([{ slot: 'a', bytes: 1024 }])).toBe('1.0 KiB')
    const two = cnpgSlotRetentionText([{ slot: 'a', bytes: 10 * 1024 * 1024 }, { slot: 'b', bytes: 2 * 1024 * 1024 }])
    expect(two).toBe('10 MiB (largest slot)')
    expect(two).not.toContain('up to')
  })
})

describe('cnpgSharedExpansionGap', () => {
  const vol = (reason?: string, allow?: boolean) => ({ storageClass: { name: 'standard', allowVolumeExpansion: allow, reason } })
  it('names one shared reason for every unknown volume, so the page says it once', () => {
    expect(cnpgSharedExpansionGap([vol('cannot list StorageClasses'), vol('cannot list StorageClasses')])).toBe('cannot list StorageClasses')
    expect(cnpgSharedExpansionGap([vol('a'), vol('b')])).toBeUndefined()
    expect(cnpgSharedExpansionGap([vol('a')])).toBeUndefined()
    expect(cnpgSharedExpansionGap([vol(undefined, true), vol(undefined, true)])).toBeUndefined()
  })
})
