import type { SelectedResource, Topology, TopologyNode } from '@skyhook-io/k8s-ui/types/core'
import { builtinGroupForKind, canonicalResourceGroup } from '@skyhook-io/k8s-ui/utils/api-resources'
import { apiVersionToGroup, kindToPluralWithGroup } from '@skyhook-io/k8s-ui/utils/navigation'
import { topologyNodeResourceKind } from '@skyhook-io/k8s-ui/utils/topology-neighborhood'

function topologyNodeGroup(node: TopologyNode): string | undefined {
  const apiVersion = node.data.apiVersion
  if (typeof apiVersion === 'string' && apiVersion) return apiVersionToGroup(apiVersion)

  const sourceGroup = node.data.sourceGroup
  if (typeof sourceGroup === 'string') return sourceGroup

  return builtinGroupForKind(topologyNodeResourceKind(node))
}

export function findSelectedTopologyNode(
  topology: Topology | null,
  selectedResource: SelectedResource,
): TopologyNode | undefined {
  const namespace = selectedResource.namespace || ''
  const selectedKind = kindToPluralWithGroup(selectedResource.kind, selectedResource.group ?? '')
  const candidates = topology?.nodes.filter(node =>
    ((node.data.namespace as string) || '') === namespace &&
    node.name === selectedResource.name &&
    (kindToPluralWithGroup(topologyNodeResourceKind(node), topologyNodeGroup(node) ?? '') === selectedKind || topologyNodeResourceKind(node) === selectedResource.kind),
  ) ?? []

  if (candidates.length === 0) return undefined

  const selectedGroup = canonicalResourceGroup(selectedResource.kind, selectedResource.group)
  if (selectedGroup === undefined) return candidates.length === 1 ? candidates[0] : undefined

  const groupMatch = candidates.find(node => topologyNodeGroup(node) === selectedGroup)
  if (groupMatch) return groupMatch

  // Preserve the old name/kind/ns behavior only when the topology has no group
  // information to compare. If it does, returning no match is safer than
  // highlighting a resource from another API group.
  return candidates.some(node => topologyNodeGroup(node) !== undefined) ? undefined : candidates[0]
}
