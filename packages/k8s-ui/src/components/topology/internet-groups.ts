import type { GroupingMode, TopologyEdge, TopologyNode } from '../../types'
import { getGroupKey } from './layout'

/**
 * Splits the traffic view's single Internet node into one per group, so each
 * namespace or app shows its own entry from outside. Every original Internet
 * edge is kept, whatever kind its entrypoint is (Ingress, Gateway, Istio
 * Gateway, Knative Service, Traefik route, Contour HTTPProxy); an entrypoint
 * with no group stays connected to the original Internet node.
 */
export function splitInternetByGroup(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  groupMode: GroupingMode,
): { nodes: TopologyNode[]; edges: TopologyEdge[] } {
  if (groupMode === 'none') return { nodes, edges }
  const internetNode = nodes.find(n => n.kind === 'Internet')
  if (!internetNode) return { nodes, edges }

  const nodeById = new Map(nodes.map(n => [n.id, n]))
  const groups = new Map<string, { entry: TopologyNode; edges: TopologyEdge[] }>()
  const regrouped = new Set<TopologyEdge>()
  for (const edge of edges) {
    if (edge.source !== internetNode.id) continue
    const target = nodeById.get(edge.target)
    const groupKey = target ? getGroupKey(target, groupMode) : null
    if (!target || !groupKey) continue
    if (!groups.has(groupKey)) groups.set(groupKey, { entry: target, edges: [] })
    groups.get(groupKey)!.edges.push(edge)
    regrouped.add(edge)
  }
  if (groups.size === 0) return { nodes, edges }

  const keepOriginal = edges.some(e => e.source === internetNode.id && !regrouped.has(e))
  const newNodes = keepOriginal ? [...nodes] : nodes.filter(n => n.id !== internetNode.id)
  const newEdges = edges.filter(e => !regrouped.has(e))
  for (const [groupKey, group] of groups) {
    const internetId = `internet-${groupMode}-${groupKey}`
    newNodes.push({
      id: internetId,
      kind: 'Internet',
      name: 'Internet',
      status: 'healthy',
      data: {
        // Group metadata so the node lands inside its entrypoints' group.
        namespace: groupMode === 'namespace' ? groupKey : group.entry.data?.namespace,
        labels: groupMode === 'app' ? { 'app.kubernetes.io/name': groupKey } : {},
      },
    })
    for (const edge of group.edges) {
      newEdges.push({ ...edge, id: `${internetId}-to-${edge.target}`, source: internetId })
    }
  }
  return { nodes: newNodes, edges: newEdges }
}
