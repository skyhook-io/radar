import type { GitOpsResourceTree, GitOpsTreeEdge } from '../../../types'

// Flux source, chart and dependsOn edges travel consumer → dependency on the
// wire. A dependency feeds its consumer, so the graph draws it upstream (left
// of the consumer) with the arrow pointing into the consumer, as Flux's own
// tooling does. An object the root also manages (a bootstrap Kustomization
// that owns its GitRepository) keeps its place among the managed resources:
// ownership wins.
export function isUpstreamEdgeType(type: GitOpsTreeEdge['type']): boolean {
  return type === 'source' || type === 'dependsOn'
}

export function upstreamEdges(tree: Pick<GitOpsResourceTree, 'edges'>): GitOpsTreeEdge[] {
  const owned = new Set(tree.edges.filter(edge => edge.type === 'owns').map(edge => edge.target))
  return tree.edges.filter(edge => isUpstreamEdgeType(edge.type) && !owned.has(edge.target))
}

// rankTree places the root at 0, managed resources at positive ranks by
// ownership depth, and dependencies at negative ranks by their longest
// dependency path from the root, so a repository that a release names
// directly and through its generated chart still sits left of the chart.
// Cycles are bounded: a rank never moves past the node count.
export function rankTree(tree: GitOpsResourceTree): Map<string, number> {
  const ids = new Set(tree.nodes.map(node => node.id))
  ids.add(tree.root.id)
  const limit = ids.size
  const upstream = upstreamEdges(tree).filter(edge => ids.has(edge.source) && ids.has(edge.target) && edge.target !== tree.root.id)

  const depth = new Map<string, number>([[tree.root.id, 0]])
  for (let pass = 0; pass < limit; pass++) {
    let changed = false
    for (const edge of upstream) {
      const from = depth.get(edge.source)
      if (from === undefined) continue
      const next = Math.min(from + 1, limit)
      if ((depth.get(edge.target) ?? 0) < next) {
        depth.set(edge.target, next)
        changed = true
      }
    }
    if (!changed) break
  }

  const rank = new Map<string, number>([[tree.root.id, 0]])
  for (const [id, d] of depth) {
    if (id !== tree.root.id) rank.set(id, -d)
  }

  const children = new Map<string, string[]>()
  for (const edge of tree.edges) {
    if (!ids.has(edge.source) || !ids.has(edge.target)) continue
    if (rank.has(edge.target) && rank.get(edge.target)! < 0) continue
    if (rank.has(edge.source) && rank.get(edge.source)! < 0) continue
    children.set(edge.source, [...(children.get(edge.source) ?? []), edge.target])
  }
  const downstream = new Map<string, number>()
  const queue = [{ id: tree.root.id, rank: 0 }]
  let iterations = 0
  const maxIterations = limit * 8 + 64
  while (queue.length > 0 && ++iterations <= maxIterations) {
    const current = queue.shift()!
    const previous = downstream.get(current.id)
    if (previous !== undefined && previous >= current.rank) continue
    downstream.set(current.id, current.rank)
    for (const child of children.get(current.id) ?? []) {
      queue.push({ id: child, rank: current.rank + 1 })
    }
  }
  for (const [id, r] of downstream) {
    if (id !== tree.root.id) rank.set(id, r)
  }

  const incoming = new Set(tree.edges.map(edge => edge.target))
  for (const id of ids) {
    if (!rank.has(id)) rank.set(id, incoming.has(id) ? 1 : 0)
  }
  return rank
}

// renderedEdge returns the edge as drawn: dependency edges point from the
// dependency into its consumer.
export function renderedEdge(edge: GitOpsTreeEdge, upstreamTargets: Set<string>): { source: string; target: string } {
  if (isUpstreamEdgeType(edge.type) && upstreamTargets.has(edge.target)) {
    return { source: edge.target, target: edge.source }
  }
  return { source: edge.source, target: edge.target }
}

// upstreamClosure returns every dependency reachable from the root, for
// presets that must keep "where does this come from" in view.
export function upstreamClosure(tree: GitOpsResourceTree): Set<string> {
  const edges = upstreamEdges(tree)
  const out = new Set<string>()
  const queue = [tree.root.id]
  while (queue.length > 0) {
    const id = queue.shift()!
    for (const edge of edges) {
      if (edge.source !== id || out.has(edge.target) || edge.target === tree.root.id) continue
      out.add(edge.target)
      queue.push(edge.target)
    }
  }
  return out
}

// shortcutEdges returns the dependency edges that a longer dependency path
// already covers: an inline HelmRelease names its repository directly and
// through its generated chart, and drawing both puts the shortcut behind the
// chart card. The data keeps the edge; the drawing doesn't need it.
export function shortcutEdges(tree: Pick<GitOpsResourceTree, 'edges'>): Set<GitOpsTreeEdge> {
  const edges = upstreamEdges(tree)
  const out = new Set<GitOpsTreeEdge>()
  for (const edge of edges) {
    const seen = new Set<string>([edge.source])
    const queue = edges.filter(e => e !== edge && e.source === edge.source).map(e => e.target)
    while (queue.length > 0) {
      const id = queue.shift()!
      if (id === edge.target) {
        out.add(edge)
        break
      }
      if (seen.has(id)) continue
      seen.add(id)
      for (const next of edges) if (next.source === id) queue.push(next.target)
    }
  }
  return out
}
