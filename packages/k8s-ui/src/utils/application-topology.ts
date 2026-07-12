import type { GitOpsResourceTree, GitOpsTreeNode, Topology, TopologyNode } from '../types'
import { apiVersionToGroup, groupQualifiesLaneId } from './navigation'

export type DeploymentMembership = 'managed-runtime' | 'runtime-only'

export interface DeploymentTopologyLayer {
  topology: Topology
  managedRuntimeCount: number
  runtimeOnlyCount: number
  managedOnly: GitOpsTreeNode[]
  inventoryComplete: boolean
}

function identityKey(kind: string, group: string | undefined, namespace: string, name: string): string {
  const identityGroup = groupQualifiesLaneId(group) ? group!.toLowerCase() : ''
  return `${identityGroup}/${kind.toLowerCase()}/${namespace}/${name}`
}

function topologyGroup(node: TopologyNode): string | undefined {
  return apiVersionToGroup(node.data?.apiVersion as string | undefined)
}

export function layerDeploymentInventory(
  topology: Topology,
  inventory: GitOpsResourceTree,
  sourceLabel: string,
): DeploymentTopologyLayer {
  const inventoryNodes = inventory.nodes.filter((node) => node.role !== 'root' && node.role !== 'group')
  const inventoryByExact = new Map(inventoryNodes.map((node) => [
    identityKey(node.ref.kind, node.ref.group, node.ref.namespace, node.ref.name),
    node,
  ]))
  const matchedInventoryIds = new Set<string>()
  let managedRuntimeCount = 0
  let runtimeOnlyCount = 0
  const inventoryComplete = (inventory.warnings?.length ?? 0) === 0

  const nodes = topology.nodes.map((node) => {
    if (node.kind === 'PodGroup' || node.kind === 'Internet') return node
    const namespace = (node.data?.namespace as string) || ''
    const match = inventoryByExact.get(identityKey(node.kind, topologyGroup(node), namespace, node.name))
    let membership: DeploymentMembership | undefined
    if (match) {
      matchedInventoryIds.add(match.id)
      managedRuntimeCount += 1
      membership = 'managed-runtime'
    } else if (inventoryComplete) {
      runtimeOnlyCount += 1
      membership = 'runtime-only'
    }
    if (!membership) return node
    return {
      ...node,
      data: {
        ...node.data,
        deploymentMembership: membership,
        deploymentSourceLabel: sourceLabel,
      },
    }
  })

  return {
    topology: { ...topology, nodes },
    managedRuntimeCount,
    runtimeOnlyCount,
    managedOnly: inventoryNodes.filter((node) => !matchedInventoryIds.has(node.id)),
    inventoryComplete,
  }
}
