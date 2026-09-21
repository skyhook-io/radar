import { describe, expect, it } from 'vitest'
import { podLimitRangeNames } from './quotas'

describe('Pod-relevant LimitRanges', () => {
  it('excludes PVC-only rules and retains Pod, Container and mixed rules', () => {
    const rules = [
      { metadata: { name: 'storage-only' }, spec: { limits: [{ type: 'PersistentVolumeClaim' }] } },
      { metadata: { name: 'container-defaults' }, spec: { limits: [{ type: 'Container' }] } },
      { metadata: { name: 'pod-total' }, spec: { limits: [{ type: 'Pod' }] } },
      { metadata: { name: 'mixed' }, spec: { limits: [{ type: 'PersistentVolumeClaim' }, { type: 'Container' }] } },
    ]
    expect(podLimitRangeNames(rules)).toEqual(['container-defaults', 'pod-total', 'mixed'])
  })

  it('ignores LimitRanges with no declared entries', () => {
    expect(podLimitRangeNames([
      { metadata: { name: 'null-entries' }, spec: { limits: null } },
      { metadata: { name: 'empty-entries' }, spec: { limits: [] } },
    ])).toEqual([])
  })

  it('has no contextual links before data is available or for an empty namespace', () => {
    expect(podLimitRangeNames(undefined)).toEqual([])
    expect(podLimitRangeNames([])).toEqual([])
  })
})
