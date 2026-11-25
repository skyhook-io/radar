import { describe, expect, it } from 'vitest'
import fixtures from '../../../../../pkg/longhorn/testdata/volumes.json'
import { getLonghornVolumeStatus, longhornHealthyReplicas, longhornRebuilds, longhornVolumeHealth } from './resource-utils-longhorn'

describe('longhornVolumeHealth', () => {
  it.each(fixtures.cases)('$name → $want', (c: any) => {
    expect(longhornVolumeHealth({ status: c.status })).toBe(c.want)
  })
})

describe('getLonghornVolumeStatus', () => {
  it('keeps degraded amber and faulted red', () => {
    expect(getLonghornVolumeStatus({ status: { robustness: 'degraded' } })).toMatchObject({ text: 'Degraded', level: 'degraded' })
    expect(getLonghornVolumeStatus({ status: { robustness: 'faulted' } })).toMatchObject({ text: 'Faulted', level: 'unhealthy' })
    expect(getLonghornVolumeStatus({ status: { state: 'detached', robustness: 'unknown' } })).toMatchObject({ text: 'Detached', level: 'neutral' })
    expect(getLonghornVolumeStatus({ status: { robustness: 'healthy' } })).toMatchObject({ text: 'Healthy', level: 'healthy' })
    expect(getLonghornVolumeStatus({})).toMatchObject({ level: 'unknown' })
  })
})

describe('longhornHealthyReplicas', () => {
  it('counts RW replicas, and is unknown without an engine', () => {
    expect(longhornHealthyReplicas({ status: { replicaModeMap: { a: 'RW', b: 'WO', c: 'RW' } } })).toBe(2)
    expect(longhornHealthyReplicas(undefined)).toBeUndefined()
    expect(longhornHealthyReplicas({ status: {} })).toBeUndefined()
  })
})

describe('longhornRebuilds', () => {
  it('lists rebuilds in progress or failed, by replica', () => {
    expect(longhornRebuilds({ status: { rebuildStatus: {
      'tcp://b': { isRebuilding: true, progress: 40, state: 'in_progress' },
      'tcp://a': { isRebuilding: false, progress: 100, state: 'complete' },
      'tcp://c': { isRebuilding: false, state: 'error', error: 'disk full' },
    } } })).toEqual([
      { replica: 'tcp://b', progress: 40, state: 'in_progress', error: '' },
      { replica: 'tcp://c', progress: undefined, state: 'error', error: 'disk full' },
    ])
  })

  it('names the replica from currentReplicaAddressMap, which records addresses without tcp://', () => {
    expect(longhornRebuilds({ status: {
      currentReplicaAddressMap: { 'vol-r-1': '10.0.0.2:10352' },
      rebuildStatus: {
        'tcp://10.0.0.2:10352': { isRebuilding: true, progress: 40, state: 'in_progress' },
        'tcp://10.0.0.9:10352': { isRebuilding: true, progress: 5, state: 'in_progress' },
      },
    } })).toEqual([
      { replica: 'tcp://10.0.0.9:10352', progress: 5, state: 'in_progress', error: '' },
      { replica: 'vol-r-1', progress: 40, state: 'in_progress', error: '' },
    ])
  })
})

describe('longhornRebuilds progress', () => {
  it('keeps unreported progress unknown, not zero', () => {
    const [r] = longhornRebuilds({ status: { rebuildStatus: { 'tcp://a': { isRebuilding: true, state: 'in_progress' } } } })
    expect(r.progress).toBeUndefined()
  })
})
