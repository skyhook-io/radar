import { describe, it, expect } from 'vitest'

import type { TimelineEvent, Topology } from '../types/core'

import { buildResourceHierarchy } from './resource-hierarchy'

function svcEvent(namespace: string, name: string): TimelineEvent {
  return {
    id: `${namespace}/${name}`,
    timestamp: '2024-01-01T00:00:00.000Z',
    source: 'informer',
    kind: 'Service',
    namespace,
    name,
    eventType: 'update',
  }
}

function svcNode(namespace: string, name: string, app: string) {
  return {
    id: `service/${namespace}/${name}`,
    data: { apiVersion: 'v1', labels: { 'app.kubernetes.io/name': app } },
  }
}

function topo(nodes: ReturnType<typeof svcNode>[]): Topology {
  return { nodes, edges: [] } as unknown as Topology
}

describe('buildResourceHierarchy app-label grouping', () => {
  it('does not merge the same app label across namespaces', () => {
    const events = [svcEvent('team-a', 'web'), svcEvent('team-b', 'web')]
    const topology = topo([svcNode('team-a', 'web', 'web'), svcNode('team-b', 'web', 'web')])

    const lanes = buildResourceHierarchy({ events, topology, groupByApp: true })

    expect(lanes).toHaveLength(2)
    expect(lanes.every((l) => (l.children ?? []).length === 0)).toBe(true)
    expect(new Set(lanes.map((l) => l.namespace))).toEqual(new Set(['team-a', 'team-b']))
  })

  it('groups distinct resources sharing an app label within one namespace', () => {
    const events = [svcEvent('team-a', 'web'), svcEvent('team-a', 'web-edge')]
    const topology = topo([svcNode('team-a', 'web', 'web'), svcNode('team-a', 'web-edge', 'web')])

    const lanes = buildResourceHierarchy({ events, topology, groupByApp: true })

    expect(lanes).toHaveLength(1)
    expect(lanes[0].children).toHaveLength(1)
  })
})
