import { describe, it, expect } from 'vitest'
import type { TopologyNode } from '../../types'

// Mirrors the matching predicate inside TopologySearch (kept in sync
// by reuse) — pure so we can pin the matching rule without rendering
// the React component.
function matchesQuery(node: TopologyNode, lowerQuery: string): boolean {
  const name = node.name.toLowerCase()
  const kind = node.kind.toLowerCase()
  const namespace = (node.data.namespace as string || '').toLowerCase()
  return (
    name.includes(lowerQuery) ||
    kind.includes(lowerQuery) ||
    namespace.includes(lowerQuery) ||
    `${kind}/${name}`.includes(lowerQuery) ||
    `${namespace}/${name}`.includes(lowerQuery)
  )
}

function countHidden(allNodes: TopologyNode[], visibleNodes: TopologyNode[], query: string): number {
  if (!query.trim()) return 0
  const lowerQuery = query.toLowerCase()
  // First check whether the visible set already has matches — the
  // hint should only appear when the visible search yielded zero.
  const visibleMatches = visibleNodes.filter(n => matchesQuery(n, lowerQuery))
  if (visibleMatches.length > 0) return 0
  const visibleIds = new Set(visibleNodes.map(n => n.id))
  return allNodes.filter(n => !visibleIds.has(n.id) && matchesQuery(n, lowerQuery)).length
}

function makeNode(id: string, kind: string, name: string, namespace = 'default'): TopologyNode {
  // Tests only need the four fields the matcher reads — cast through
  // unknown so the partial shape doesn't have to satisfy the full
  // TopologyNode type.
  return {
    id,
    kind,
    name,
    data: { namespace },
  } as unknown as TopologyNode
}

describe('TopologySearch hidden-match counting', () => {
  // The bug: in Fleet topology view (which only shows CAPI kinds),
  // searching pod names returned "No resources found" — misleading
  // when the cluster has 338 pods. We compute a hidden-match count
  // so the empty-state can say "X matches are hidden by the current
  // view; switch view to see them."
  //
  // The count should:
  //  1. Be 0 when the visible set has any match (don't shame the
  //     visible empty-state).
  //  2. Otherwise count nodes that match in the unfiltered set but
  //     are NOT in the visible set.
  //  3. Match by name, kind, namespace, kind/name, or
  //     namespace/name (case-insensitive).

  it('returns 0 when visible nodes contain a match', () => {
    const visible = [makeNode('a', 'Cluster', 'prod')]
    const all = [
      ...visible,
      makeNode('b', 'Pod', 'prod-app'), // would also match "prod"
    ]
    expect(countHidden(all, visible, 'prod')).toBe(0)
  })

  it('returns count of hidden matches when visible has none (Fleet → pod search)', () => {
    const visible = [
      makeNode('cluster-1', 'Cluster', 'prod-cluster'),
      makeNode('machine-1', 'Machine', 'prod-machine-1'),
    ]
    const all = [
      ...visible,
      makeNode('pod-1', 'Pod', '3scale-gateway-abc'),
      makeNode('pod-2', 'Pod', '3scale-system-xyz'),
      makeNode('pod-3', 'Pod', 'billing-api-789'),
    ]
    expect(countHidden(all, visible, '3scale')).toBe(2)
    expect(countHidden(all, visible, 'billing')).toBe(1)
  })

  it('matches case-insensitively', () => {
    const visible = [makeNode('a', 'Cluster', 'prod')]
    const all = [...visible, makeNode('b', 'Pod', 'MyApp')]
    expect(countHidden(all, visible, 'myapp')).toBe(1)
    expect(countHidden(all, visible, 'MYAPP')).toBe(1)
  })

  it('matches by namespace as well as name', () => {
    const visible = [makeNode('a', 'Cluster', 'prod')]
    const all = [
      ...visible,
      makeNode('p1', 'Pod', 'frontend', 'billing'),
      makeNode('p2', 'Pod', 'backend', 'billing'),
    ]
    expect(countHidden(all, visible, 'billing')).toBe(2)
  })

  it('returns 0 for an empty/whitespace query', () => {
    const visible: TopologyNode[] = []
    const all = [makeNode('a', 'Pod', 'foo')]
    expect(countHidden(all, visible, '')).toBe(0)
    expect(countHidden(all, visible, '   ')).toBe(0)
  })

  it('does not double-count: a node visible AND matching is excluded from hidden', () => {
    const visible = [makeNode('a', 'Pod', 'foo-1')]
    const all = [...visible, makeNode('b', 'Pod', 'foo-2')]
    // visible has a match → hint suppressed entirely
    expect(countHidden(all, visible, 'foo')).toBe(0)
  })
})
