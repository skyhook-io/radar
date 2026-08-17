import type { SelectedResource, Topology, TopologyNode } from '@skyhook-io/k8s-ui/types/core'
import { apiVersionToGroup, kindToPlural } from '@skyhook-io/k8s-ui/utils/navigation'

function topologyNodeGroup(node: TopologyNode): string | undefined {
  const apiVersionGroup = apiVersionToGroup(node.data.apiVersion as string | undefined)
  if (apiVersionGroup) return apiVersionGroup

  const sourceGroup = node.data.sourceGroup
  if (typeof sourceGroup === 'string' && sourceGroup) return sourceGroup

  // Native NetworkPolicy nodes predate apiVersion in the topology payload.
  if (node.kind === 'NetworkPolicy') return 'networking.k8s.io'
  return undefined
}

export function findSelectedTopologyNode(
  topology: Topology | null,
  selectedResource: SelectedResource,
): TopologyNode | undefined {
  const namespace = selectedResource.namespace || ''
  const candidates = topology?.nodes.filter(node =>
    ((node.data.namespace as string) || '') === namespace &&
    node.name === selectedResource.name &&
    (kindToPlural(node.kind) === selectedResource.kind || node.kind === selectedResource.kind),
  ) ?? []

  if (candidates.length === 0) return undefined

  const selectedGroup = selectedResource.group || undefined
  if (!selectedGroup) return candidates[0]

  const groupMatch = candidates.find(node => topologyNodeGroup(node) === selectedGroup)
  if (groupMatch) return groupMatch

  // Preserve the old name/kind/ns behavior only when the topology has no group
  // information to compare. If it does, returning no match is safer than
  // highlighting a resource from another API group.
  return candidates.some(node => topologyNodeGroup(node) !== undefined) ? undefined : candidates[0]
}
