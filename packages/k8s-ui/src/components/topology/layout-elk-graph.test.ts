import { describe, it, expect } from 'vitest'
import { buildHierarchicalElkGraph, type GroupDisplayLevel } from './layout'
import type { TopologyNode, TopologyEdge } from '../../types'

// Collect every id ELK will see as a layoutable shape: top-level children plus
// the members of expanded groups. An edge endpoint outside this set is exactly
// what makes ELK throw "Referenced shape does not exist".
function validEndpointIds(elkGraph: { children: Array<{ id: string; children?: Array<{ id: string }> }> }): Set<string> {
  const ids = new Set<string>()
  for (const child of elkGraph.children) {
    ids.add(child.id)
    for (const c of child.children ?? []) ids.add(c.id)
  }
  return ids
}

function deployment(ns: string, name: string): TopologyNode {
  return {
    id: `deployment/${ns}/${name}`,
    kind: 'Deployment',
    name,
    status: 'healthy',
    data: { namespace: ns },
  }
}

describe('buildHierarchicalElkGraph — collapse predicate consistency', () => {
  // Reproduces the Resources→Traffic crash: smart-default chipped the namespaces
  // present at the time (app1), then a view switch surfaced a namespace
  // (skyhook-gateway) with no groupLevels entry. Node placement hid its members
  // (treated as collapsed) but the old edge-redirect only fired for groups in
  // collapsedGroups — leaving an edge pointed at a hidden plain node id.
  it('redirects edges into a late-arriving (no-level) group so no edge dangles', () => {
    const nodes: TopologyNode[] = [
      deployment('app1', 'web'),
      deployment('skyhook-gateway', 'skyhook-frpc'),
    ]
    const edges: TopologyEdge[] = [
      { id: 'e1', source: 'deployment/app1/web', target: 'deployment/skyhook-gateway/skyhook-frpc', type: 'routes-to' },
    ]

    // Smart default chipped app1 only; skyhook-gateway appeared later → no entry.
    const groupLevels = new Map<string, GroupDisplayLevel>([['group-namespace-app1', 'chip']])
    // collapsedGroups mirrors TopologyGraph: only explicit non-'topology' levels.
    const collapsedGroups = new Set<string>(['group-namespace-app1'])

    const { elkGraph } = buildHierarchicalElkGraph(nodes, edges, 'namespace', collapsedGroups, groupLevels)

    const valid = validEndpointIds(elkGraph)
    for (const edge of elkGraph.edges) {
      expect(valid.has(edge.sources[0]), `source ${edge.sources[0]} must exist`).toBe(true)
      expect(valid.has(edge.targets[0]), `target ${edge.targets[0]} must exist`).toBe(true)
    }

    // The plain hidden member id must never survive as an endpoint.
    const endpoints = elkGraph.edges.flatMap(e => [...e.sources, ...e.targets])
    expect(endpoints).not.toContain('deployment/skyhook-gateway/skyhook-frpc')
    expect(endpoints).toContain('group-namespace-skyhook-gateway')
  })

  it('keeps edges between expanded groups referencing plain member ids', () => {
    const nodes: TopologyNode[] = [
      deployment('app1', 'web'),
      deployment('app2', 'api'),
    ]
    const edges: TopologyEdge[] = [
      { id: 'e1', source: 'deployment/app1/web', target: 'deployment/app2/api', type: 'routes-to' },
    ]
    // Both groups explicitly expanded.
    const groupLevels = new Map<string, GroupDisplayLevel>([
      ['group-namespace-app1', 'topology'],
      ['group-namespace-app2', 'topology'],
    ])

    const { elkGraph } = buildHierarchicalElkGraph(nodes, edges, 'namespace', new Set(), groupLevels)

    const valid = validEndpointIds(elkGraph)
    for (const edge of elkGraph.edges) {
      expect(valid.has(edge.sources[0])).toBe(true)
      expect(valid.has(edge.targets[0])).toBe(true)
    }
    const endpoints = elkGraph.edges.flatMap(e => [...e.sources, ...e.targets])
    expect(endpoints).toContain('deployment/app1/web')
    expect(endpoints).toContain('deployment/app2/api')
  })

  it('produces no dangling endpoints with no grouping', () => {
    const nodes: TopologyNode[] = [deployment('app1', 'web'), deployment('app1', 'api')]
    const edges: TopologyEdge[] = [
      { id: 'e1', source: 'deployment/app1/web', target: 'deployment/app1/api', type: 'routes-to' },
    ]
    const { elkGraph } = buildHierarchicalElkGraph(nodes, edges, 'none', new Set(), new Map())
    const valid = validEndpointIds(elkGraph)
    for (const edge of elkGraph.edges) {
      expect(valid.has(edge.sources[0])).toBe(true)
      expect(valid.has(edge.targets[0])).toBe(true)
    }
  })
})
