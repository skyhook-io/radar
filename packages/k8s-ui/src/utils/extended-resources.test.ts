import { describe, it, expect } from 'vitest'
import {
  isGpuResourceKey,
  isGpuCountKey,
  getEffectiveResourceValue,
  getEffectiveResources,
  getPodGpuCount,
  getNodeGpuCount,
  getExtendedCapacityRows,
} from './extended-resources'

describe('isGpuResourceKey', () => {
  it('matches vendor whole-GPU keys', () => {
    expect(isGpuResourceKey('nvidia.com/gpu')).toBe(true)
    expect(isGpuResourceKey('amd.com/gpu')).toBe(true)
  })
  it('matches NVIDIA MIG slices', () => {
    expect(isGpuResourceKey('nvidia.com/mig-1g.5gb')).toBe(true)
  })
  it('matches the Intel gpu.intel.com family', () => {
    expect(isGpuResourceKey('gpu.intel.com/i915')).toBe(true)
    expect(isGpuResourceKey('gpu.intel.com/millicores')).toBe(true)
  })
  it('rejects non-GPU keys', () => {
    expect(isGpuResourceKey('cpu')).toBe(false)
    expect(isGpuResourceKey('hugepages-2Mi')).toBe(false)
    expect(isGpuResourceKey('example.com/fpga')).toBe(false)
  })
})

describe('isGpuCountKey', () => {
  it('excludes fractional Intel dimensions', () => {
    expect(isGpuCountKey('gpu.intel.com/millicores')).toBe(false)
    expect(isGpuCountKey('gpu.intel.com/memory.max')).toBe(false)
    expect(isGpuCountKey('gpu.intel.com/tiles')).toBe(false)
  })
  it('includes count-like keys', () => {
    expect(isGpuCountKey('nvidia.com/gpu')).toBe(true)
    expect(isGpuCountKey('nvidia.com/mig-3g.20gb')).toBe(true)
    expect(isGpuCountKey('gpu.intel.com/i915')).toBe(true)
  })
})

describe('getEffectiveResourceValue', () => {
  it('falls back to limits when requests omit the key (canonical GPU form)', () => {
    const resources = { limits: { 'nvidia.com/gpu': '2' } }
    expect(getEffectiveResourceValue(resources, 'nvidia.com/gpu')).toBe('2')
  })
  it('prefers an explicit request', () => {
    const resources = { requests: { 'nvidia.com/gpu': '1' }, limits: { 'nvidia.com/gpu': '1' } }
    expect(getEffectiveResourceValue(resources, 'nvidia.com/gpu')).toBe('1')
  })
  it('returns undefined when absent', () => {
    expect(getEffectiveResourceValue({}, 'nvidia.com/gpu')).toBeUndefined()
    expect(getEffectiveResourceValue(undefined, 'nvidia.com/gpu')).toBeUndefined()
  })
})

describe('getPodGpuCount', () => {
  it('sums limits-only GPU containers', () => {
    const pod = {
      spec: {
        containers: [
          { resources: { limits: { 'nvidia.com/gpu': '2' } } },
          { resources: { limits: { 'nvidia.com/gpu': '1' } } },
        ],
      },
    }
    expect(getPodGpuCount(pod)).toBe(3)
  })
  it('applies max(sum(containers), max(initContainers))', () => {
    const pod = {
      spec: {
        containers: [{ resources: { limits: { 'nvidia.com/gpu': '1' } } }],
        initContainers: [{ resources: { limits: { 'nvidia.com/gpu': '4' } } }],
      },
    }
    expect(getPodGpuCount(pod)).toBe(4)
  })
  it('counts MIG slices but not fractional Intel dimensions', () => {
    const pod = {
      spec: {
        containers: [
          { resources: { limits: { 'nvidia.com/mig-1g.5gb': '2', 'gpu.intel.com/millicores': '500' } } },
        ],
      },
    }
    expect(getPodGpuCount(pod)).toBe(2)
  })
  it('returns 0 for GPU-free pods and malformed values', () => {
    expect(getPodGpuCount({ spec: { containers: [{ resources: { limits: { cpu: '1' } } }] } })).toBe(0)
    expect(getPodGpuCount({ spec: { containers: [{ resources: { limits: { 'nvidia.com/gpu': 'x' } } }] } })).toBe(0)
    expect(getPodGpuCount({})).toBe(0)
  })
  it('parses K8s quantity suffixes (3000m = 3, 3k = 3000)', () => {
    expect(getPodGpuCount({ spec: { containers: [{ resources: { limits: { 'nvidia.com/gpu': '3000m' } } }] } })).toBe(3)
    expect(getPodGpuCount({ spec: { containers: [{ resources: { limits: { 'nvidia.com/gpu': '3k' } } }] } })).toBe(3000)
  })
})

describe('getEffectiveResources', () => {
  it('merges limits-only keys with explicit requests', () => {
    const resources = {
      requests: { cpu: '100m' },
      limits: { cpu: '200m', 'nvidia.com/gpu': '1' },
    }
    expect(getEffectiveResources(resources)).toEqual({ cpu: '100m', 'nvidia.com/gpu': '1' })
  })
  it('returns empty for missing blocks', () => {
    expect(getEffectiveResources(undefined)).toEqual({})
    expect(getEffectiveResources({})).toEqual({})
  })
})

describe('getNodeGpuCount', () => {
  it('sums allocatable count-like GPU keys', () => {
    const node = { status: { allocatable: { 'nvidia.com/gpu': '8', cpu: '64' } } }
    expect(getNodeGpuCount(node)).toBe(8)
  })
  it('returns 0 without GPU keys', () => {
    expect(getNodeGpuCount({ status: { allocatable: { cpu: '64' } } })).toBe(0)
    expect(getNodeGpuCount({})).toBe(0)
  })
})

describe('getExtendedCapacityRows', () => {
  it('excludes the curated standard keys and sorts GPU rows first', () => {
    const capacity = {
      cpu: '64',
      memory: '256Gi',
      pods: '110',
      'ephemeral-storage': '1Ti',
      'hugepages-1Gi': '2Gi',
      'nvidia.com/gpu': '8',
    }
    const allocatable = { ...capacity, 'nvidia.com/gpu': '7' }
    const rows = getExtendedCapacityRows(capacity, allocatable)
    expect(rows.map(r => r.key)).toEqual(['nvidia.com/gpu', 'hugepages-1Gi'])
    expect(rows[0]).toMatchObject({ capacity: '8', allocatable: '7', isGpu: true })
  })
  it('drops all-zero rows (every node advertises hugepages-* as 0)', () => {
    const capacity = { cpu: '4', 'hugepages-2Mi': '0', 'hugepages-1Gi': '0' }
    expect(getExtendedCapacityRows(capacity, capacity)).toEqual([])
  })
  it('handles keys present on only one side', () => {
    const rows = getExtendedCapacityRows({ 'amd.com/gpu': '4' }, {})
    expect(rows).toEqual([{ key: 'amd.com/gpu', capacity: '4', allocatable: undefined, isGpu: true }])
  })
  it('returns empty for nodes with only standard resources', () => {
    expect(getExtendedCapacityRows({ cpu: '4', memory: '8Gi' }, { cpu: '4' })).toEqual([])
  })
})
