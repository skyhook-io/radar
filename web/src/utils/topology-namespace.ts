import type { TopologyNode } from '@skyhook-io/k8s-ui/types/core'

/**
 * Whether a topology node belongs to the given namespace scope.
 *
 * A node with no namespace is cluster-scoped (Node, PersistentVolume,
 * Namespace, ...) and stays visible in every scope, so it always passes.
 * An empty scope means "all namespaces".
 */
export function isInNamespaceScope(node: TopologyNode, nsSet: Set<string> | null): boolean {
  if (!nsSet) return true
  const ns = node.data?.namespace as string | undefined
  return !ns || nsSet.has(ns)
}

/**
 * Narrow a node list to the selected namespaces.
 *
 * Shared by the topology graph and the filter sidebar so the two cannot
 * disagree about which nodes are in scope. Note this applies the namespace
 * carve-out only: the sidebar needs the full scoped set to compute its
 * visible/hidden counts, so kind filtering is applied separately by the graph.
 */
export function scopeNodesToNamespaces(nodes: TopologyNode[], namespaces: string[]): TopologyNode[] {
  if (namespaces.length === 0) return nodes
  const nsSet = new Set(namespaces)
  return nodes.filter(node => isInNamespaceScope(node, nsSet))
}
