import type { TopologyNode } from '@skyhook-io/k8s-ui/types/core'

// A node with no namespace is cluster-scoped (Node, PersistentVolume,
// Namespace, ...) and stays visible in every scope.
export function isInNamespaceScope(node: TopologyNode, nsSet: Set<string> | null): boolean {
  if (!nsSet) return true
  const ns = node.data?.namespace as string | undefined
  return !ns || nsSet.has(ns)
}

// Shared by the topology graph and the filter sidebar so the two cannot
// disagree about which nodes are in scope. Namespace carve-out only — kind
// filtering stays on the graph.
export function scopeNodesToNamespaces(nodes: TopologyNode[], namespaces: string[]): TopologyNode[] {
  if (namespaces.length === 0) return nodes
  const nsSet = new Set(namespaces)
  return nodes.filter(node => isInNamespaceScope(node, nsSet))
}
