import { describe, it, expect, beforeAll } from 'vitest'
import { buildHierarchicalElkGraph, applyHierarchicalLayout, buildInterGroupEdges, setLayoutEngine, type GroupDisplayLevel } from './layout'
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

    // smartDefaultActive=true: the large-cluster chip pass ran, so the
    // late-arriving skyhook-gateway defaults to collapsed.
    const { elkGraph } = buildHierarchicalElkGraph(nodes, edges, 'namespace', collapsedGroups, groupLevels, true)

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

  // Without smart-default (small clusters, manual toggles), collapsing one group
  // must NOT cascade-collapse untouched no-entry groups. app2 has no level entry;
  // since smartDefaultActive is false it stays expanded and its member renders.
  it('does not cascade-collapse no-entry groups when smart-default is inactive', () => {
    const nodes: TopologyNode[] = [
      deployment('app1', 'web'),
      deployment('app2', 'api'),
    ]
    const edges: TopologyEdge[] = [
      { id: 'e1', source: 'deployment/app1/web', target: 'deployment/app2/api', type: 'routes-to' },
    ]
    // User collapsed only app1; app2 untouched (no entry).
    const groupLevels = new Map<string, GroupDisplayLevel>([['group-namespace-app1', 'chip']])
    const collapsedGroups = new Set<string>(['group-namespace-app1'])

    const { elkGraph } = buildHierarchicalElkGraph(nodes, edges, 'namespace', collapsedGroups, groupLevels, false)

    // app2 stays expanded with its member as a child.
    const app2 = elkGraph.children.find(c => c.id === 'group-namespace-app2')
    expect(app2?.children?.some(c => c.id === 'deployment/app2/api')).toBe(true)

    // Edge stays valid: app1 redirected to its chip, app2 member kept (it exists).
    const valid = validEndpointIds(elkGraph)
    for (const edge of elkGraph.edges) {
      expect(valid.has(edge.sources[0])).toBe(true)
      expect(valid.has(edge.targets[0])).toBe(true)
    }
    const endpoints = elkGraph.edges.flatMap(e => [...e.sources, ...e.targets])
    expect(endpoints).toContain('group-namespace-app1')
    expect(endpoints).toContain('deployment/app2/api')
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

// Phase-2 meta layout: edges that position groups relative to each other must
// survive even when both endpoints are already meta nodes (two collapsed chips, or
// a chip and an ungrouped node). The old branching dropped those, so connected
// chips weren't pulled together.
describe('buildInterGroupEdges — meta-graph connectivity', () => {
  const edge = (id: string, source: string, target: string) => ({ id, sources: [source], targets: [target] })

  it('keeps chip↔chip edges (both endpoints already meta nodes)', () => {
    const metaIds = new Set(['group-namespace-app1', 'group-namespace-app2'])
    // Neither endpoint is an expanded-group member, so nodeToGroup is empty.
    const out = buildInterGroupEdges([edge('e1', 'group-namespace-app1', 'group-namespace-app2')], new Map(), metaIds)
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ sources: ['group-namespace-app1'], targets: ['group-namespace-app2'] })
  })

  it('keeps chip↔ungrouped edges', () => {
    const metaIds = new Set(['group-namespace-app1', 'orphan-node'])
    const out = buildInterGroupEdges([edge('e1', 'group-namespace-app1', 'orphan-node')], new Map(), metaIds)
    expect(out).toHaveLength(1)
  })

  it('normalizes expanded-group members to their group', () => {
    const nodeToGroup = new Map([
      ['deployment/app1/web', 'group-namespace-app1'],
      ['deployment/app2/api', 'group-namespace-app2'],
    ])
    const metaIds = new Set(['group-namespace-app1', 'group-namespace-app2'])
    const out = buildInterGroupEdges([edge('e1', 'deployment/app1/web', 'deployment/app2/api')], nodeToGroup, metaIds)
    expect(out[0]).toMatchObject({ sources: ['group-namespace-app1'], targets: ['group-namespace-app2'] })
  })

  it('drops intra-group edges and endpoints absent from the meta graph', () => {
    const nodeToGroup = new Map([
      ['deployment/app1/web', 'group-namespace-app1'],
      ['deployment/app1/api', 'group-namespace-app1'],
    ])
    const metaIds = new Set(['group-namespace-app1'])
    // Same group on both ends → intra, skip. And an edge to a non-meta id → skip.
    const out = buildInterGroupEdges([
      edge('e1', 'deployment/app1/web', 'deployment/app1/api'),
      edge('e2', 'group-namespace-app1', 'ghost-node'),
    ], nodeToGroup, metaIds)
    expect(out).toHaveLength(0)
  })

  it('dedupes edges collapsing to the same meta pair', () => {
    const nodeToGroup = new Map([
      ['deployment/app1/web', 'group-namespace-app1'],
      ['deployment/app1/api', 'group-namespace-app1'],
      ['deployment/app2/x', 'group-namespace-app2'],
    ])
    const metaIds = new Set(['group-namespace-app1', 'group-namespace-app2'])
    const out = buildInterGroupEdges([
      edge('e1', 'deployment/app1/web', 'deployment/app2/x'),
      edge('e2', 'deployment/app1/api', 'deployment/app2/x'),
    ], nodeToGroup, metaIds)
    expect(out).toHaveLength(1)
  })
})

// Render layer: the GroupNode's displayLevel must agree with ELK placement, or a
// chip renders over its own laid-out children. group.isCollapsed (from the layout
// engine) is the single source of truth.
describe('applyHierarchicalLayout — rendered displayLevel matches placement', () => {
  beforeAll(() => setLayoutEngine('main-thread'))

  const noop = () => {}
  const callbacks = { onSetLevel: noop, onCardClick: noop }

  it('renders an untouched no-entry group as topology (not chip) on manual collapse', async () => {
    const nodes: TopologyNode[] = [deployment('app1', 'web'), deployment('app2', 'api')]
    const edges: TopologyEdge[] = [
      { id: 'e1', source: 'deployment/app1/web', target: 'deployment/app2/api', type: 'routes-to' },
    ]
    // Small cluster, smart-default inactive: user collapsed only app1.
    const groupLevels = new Map<string, GroupDisplayLevel>([['group-namespace-app1', 'chip']])
    const collapsedGroups = new Set<string>(['group-namespace-app1'])

    const { elkGraph, groupMap } = buildHierarchicalElkGraph(nodes, edges, 'namespace', collapsedGroups, groupLevels, false)
    const { nodes: rendered } = await applyHierarchicalLayout(
      elkGraph, nodes, edges, groupMap, 'namespace', collapsedGroups, callbacks, false, groupLevels,
    )

    const groupNode = (id: string) => rendered.find(n => n.id === id && n.type === 'group')
    expect(groupNode('group-namespace-app1')?.data.displayLevel).toBe('chip')
    expect(groupNode('group-namespace-app2')?.data.displayLevel).toBe('topology')
  })
})
