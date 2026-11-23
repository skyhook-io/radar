import { describe, expect, it } from 'vitest'
import { describeTopologyNode } from './topology-export'

describe('describeTopologyNode', () => {
  it('describes a resource with its kind label, subtitle and status', () => {
    const box = describeTopologyNode({
      id: 'sa', type: 'k8sResource',
      data: { kind: 'ServiceAccount', name: 'default', status: 'healthy', nodeData: {} },
    })
    expect(box).toMatchObject({ shape: 'resource', kind: 'ServiceAccount', name: 'default', status: 'healthy' })
    expect(box?.kindLabel).not.toBe('ServiceAccount')
    expect(box?.detail).toBeTruthy()
  })

  it('keeps an expanded group as a container and a collapsed one as a single box', () => {
    const base = { type: 'namespace', name: 'shop', nodeCount: 5, kindCounts: { Pod: 3, Service: 2 } }
    expect(describeTopologyNode({ id: 'g', type: 'group', data: { ...base, displayLevel: 'topology' } })?.shape).toBe('group')

    const chip = describeTopologyNode({ id: 'g', type: 'group', data: { ...base, displayLevel: 'chip' } })
    expect(chip).toMatchObject({ shape: 'collapsed-group', name: 'shop', count: 5 })
    expect(chip?.detail).toMatch(/3 Pods/)
  })

  it('lists as many kinds in a chip as the chip shows for its size', () => {
    const kindCounts = { Pod: 9, Service: 8, ConfigMap: 7, Secret: 6, Job: 5, CronJob: 4 }
    const chip = (nodeCount: number) => describeTopologyNode({ id: 'g', type: 'group', data: { type: 'namespace', name: 'shop', displayLevel: 'chip', nodeCount, kindCounts } })?.detail
    expect(chip(9)).toBe('9 Pods, 8 Services +4 more')
    expect(chip(39)).toBe('9 Pods, 8 Services, 7 ConfigMaps, 6 Secrets, 5 Jobs +1 more')
  })

  it('draws the workload cards of a card-grid group in their grid', () => {
    const cards = describeTopologyNode({
      id: 'g', type: 'group',
      data: {
        type: 'namespace', name: 'shop', nodeCount: 5, displayLevel: 'cardGrid', gridColumns: 2,
        workloadCards: [
          { id: 'a', kind: 'Deployment', name: 'api', status: 'healthy', subtitle: '2/2 ready', resourceCount: 4 },
          { id: 'b', kind: 'StatefulSet', name: 'db', status: 'unhealthy', subtitle: '0/1 ready', resourceCount: 2 },
          { id: 'c', kind: 'Deployment', name: 'web', status: 'healthy', subtitle: '1/1 ready', resourceCount: 3 },
        ],
      },
    })
    expect(cards).toMatchObject({ shape: 'collapsed-group', count: 3 })
    const inner = cards?.inner ?? []
    expect(inner.map(c => c.name)).toEqual(['api', 'db', 'web'])
    expect(inner[1].x).toBeGreaterThan(inner[0].x)
    expect(inner[2]).toMatchObject({ x: inner[0].x })
    expect(inner[2].y).toBeGreaterThan(inner[0].y)
    expect(inner[1].status).toBe('unhealthy')
    expect(inner.map(c => c.count)).toEqual([4, 2, 3])
  })

  it('draws the Internet node as its own shape', () => {
    expect(describeTopologyNode({ id: 'internet', type: 'k8sResource', data: { kind: 'Internet', name: 'internet', nodeData: {} } })?.shape).toBe('internet')
  })
})
