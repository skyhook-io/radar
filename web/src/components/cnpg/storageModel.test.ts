import { describe, expect, it } from 'vitest'
import { buildResizeManifest, cnpgFloorTone, cnpgInstanceDiskTone, cnpgSharedExpansionGap, cnpgSlotRetentionText, cnpgWALUsageFloor } from './storageModel'
import type { CNPGStorageVolume, CNPGStorageWAL } from '../../api/cnpg-storage'

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
    expect(cnpgSlotRetentionText([{ name: 'a', retainedBytes: 0 }])).toBe('0 B')
    expect(cnpgSlotRetentionText([{ name: 'a', retainedBytes: 1024 }])).toBe('1.0 KiB')
    const two = cnpgSlotRetentionText([{ name: 'a', retainedBytes: 10 * 1024 * 1024 }, { name: 'b', retainedBytes: 2 * 1024 * 1024 }])
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

describe('cnpgWALUsageFloor', () => {
  const vol = (state: string, claim = 'pg-1') => ({ claim, role: 'PG_DATA', capacity: '1Gi', capacityBytes: 1024 ** 3, storageClass: {}, resize: {}, usage: { state } }) as CNPGStorageVolume
  const wal = (sizeBytes: number, volume = 'pg-1') => ({ status: { state: 'ok' }, metrics: { state: 'ok' }, volume, sizeBytes }) as CNPGStorageWAL
  it('is the WAL size on the claim that holds it, only when kubelet did not measure', () => {
    expect(cnpgWALUsageFloor(vol('noSeries'), wal(512 * 1024 ** 2))).toEqual({ bytes: 512 * 1024 ** 2, ratio: 0.5 })
    expect(cnpgWALUsageFloor(vol('ok'), wal(512 * 1024 ** 2))).toBeUndefined()
    expect(cnpgWALUsageFloor(vol('noSeries', 'pg-1-wal'), wal(512 * 1024 ** 2))).toBeUndefined()
    expect(cnpgWALUsageFloor(vol('noSeries'), { ...wal(1), metrics: { state: 'denied' } } as CNPGStorageWAL)).toBeUndefined()
  })
})

describe('cnpgFloorTone', () => {
  it('never reads a lower bound below the warning line as healthy', () => {
    expect(cnpgFloorTone(0.5)).toBe('unknown')
    expect(cnpgFloorTone(0.85)).toBe('degraded')
    expect(cnpgFloorTone(3.3)).toBe('unhealthy')
    expect(cnpgFloorTone(undefined)).toBe('unknown')
  })
})

describe('cnpgInstanceDiskTone', () => {
  const measured = (ratio: number, claim = 'pg-1') => ({ claim, role: 'PG_DATA', capacity: '1Gi', capacityBytes: 1024 ** 3, storageClass: {}, resize: {}, usage: { state: 'ok', ratio } }) as CNPGStorageVolume
  const unmeasured = (claim: string) => ({ claim, role: 'PG_WAL', capacity: '1Gi', capacityBytes: 1024 ** 3, storageClass: {}, resize: {}, usage: { state: 'noSeries' } }) as CNPGStorageVolume
  const wal = (sizeBytes: number, volume: string) => ({ status: { state: 'ok' }, metrics: { state: 'ok' }, volume, sizeBytes }) as CNPGStorageWAL
  it('lets a dangerous WAL bound on one volume outrank a calm measurement on another', () => {
    expect(cnpgInstanceDiskTone([measured(0.2), unmeasured('pg-1-wal')], wal(0.95 * 1024 ** 3, 'pg-1-wal'))).toBe('unhealthy')
  })
  it('stays unknown, not healthy, while any volume is unmeasured', () => {
    expect(cnpgInstanceDiskTone([measured(0.2), unmeasured('pg-1-wal')], wal(0.1 * 1024 ** 3, 'pg-1-wal'))).toBe('unknown')
    expect(cnpgInstanceDiskTone([measured(0.2)], undefined)).toBe('healthy')
  })
})

it('does not infer slot absence from absent byte readings', () => {
  expect(cnpgSlotRetentionText(undefined)).toBe('Slot inventory not read')
  expect(cnpgSlotRetentionText(null)).toBe('Slot inventory not read')
  expect(cnpgSlotRetentionText([], false)).toBe('Slot inventory partly read')
  expect(cnpgSlotRetentionText([{ name: 'a', active: false }], false)).toBe('≥1 inactive slot; retained WAL not reported')
  expect(cnpgSlotRetentionText([{ name: '_cnpg_orders_2', active: false }])).toBe('1 inactive slot; retained WAL not reported')
  expect(cnpgSlotRetentionText([{ name: 'a', retainedBytes: 10 }, { name: 'b' }])).toContain('≥10 B reported; some slots unmeasured')
})

it('keeps measured retained WAL when slot inventory is unavailable', () => {
  expect(cnpgSlotRetentionText(null, true, [{ slot: 'a', bytes: 2048 }])).toBe('≥2.0 KiB retained WAL reported; slot inventory not read')
})

it('can separate an unmeasured retention note from slot counts without changing the default', () => {
  const slots = [{ name: '_cnpg_orders_2', active: false }]
  expect(cnpgSlotRetentionText(slots, true, [], { separateMissingBytes: true })).toBe('1 inactive slot')
  expect(cnpgSlotRetentionText(slots, false, [], { separateMissingBytes: true })).toBe('≥1 inactive slot')
  expect(cnpgSlotRetentionText(slots)).toBe('1 inactive slot; retained WAL not reported')
})
