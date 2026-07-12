import type { GitOpsResourceTree, HealthStatus, HelmOwnedResource, Topology, TopologyNode } from '../types'
import { apiVersionToGroup, groupQualifiesLaneId } from './navigation'
import { pluralize } from './pluralize'

export type DeploymentMembership = 'runtime-only' | 'source-only'

export interface DeploymentInventoryNode {
  id: string
  kind: string
  group?: string
  namespace: string
  name: string
  role: 'declared' | 'generated'
  status?: HealthStatus
}

export interface DeploymentInventory {
  nodes: DeploymentInventoryNode[]
  warnings?: string[]
  complete?: boolean
}

export interface DeploymentTopologyLayer {
  topology: Topology
  managedRuntimeCount: number
  runtimeOnlyCount: number
  managedOnly: DeploymentInventoryNode[]
  managedOnlySummary?: string
  inventoryComplete: boolean
}

export function collapseStableReplicaSets(topology: Topology, expandedOwners: ReadonlySet<string>): Topology {
  const nodesByID = new Map(topology.nodes.map((node) => [node.id, node]))
  const replicaSetsByOwner = new Map<string, string[]>()
  for (const edge of topology.edges) {
    if (edge.type !== 'manages' || nodesByID.get(edge.target)?.kind !== 'ReplicaSet') continue
    replicaSetsByOwner.set(edge.source, [...(replicaSetsByOwner.get(edge.source) ?? []), edge.target])
  }

  const collapsedReplicaSets = new Set<string>()
  const rewiredEdges = [] as Topology['edges']
  const parentData = new Map<string, Record<string, unknown>>()

  for (const [ownerID, replicaSetIDs] of replicaSetsByOwner) {
    const owner = nodesByID.get(ownerID)
    const replicaSet = replicaSetIDs.length === 1 ? nodesByID.get(replicaSetIDs[0]) : undefined
    const ready = Number(replicaSet?.data?.readyReplicas ?? 0)
    const total = Number(replicaSet?.data?.totalReplicas ?? 0)
    const incoming = topology.edges.filter((edge) => edge.target === replicaSet?.id)
    const stable = owner?.status === 'healthy'
      && replicaSet?.status === 'healthy'
      && total > 0
      && ready >= total
      && incoming.every((edge) => edge.source === ownerID && edge.type === 'manages')
    const shouldCollapse = replicaSetIDs.length === 1 && stable && !expandedOwners.has(ownerID)
    parentData.set(ownerID, {
      replicaSetCount: replicaSetIDs.length,
      replicaSetsCollapsed: shouldCollapse,
      replicaSetsExpandable: stable && replicaSetIDs.length === 1,
    })
    if (shouldCollapse && replicaSet) collapsedReplicaSets.add(replicaSet.id)
  }

  for (const edge of topology.edges) {
    if (collapsedReplicaSets.has(edge.target)) continue
    if (!collapsedReplicaSets.has(edge.source)) {
      rewiredEdges.push(edge)
      continue
    }
    const ownerEdge = topology.edges.find((candidate) => candidate.target === edge.source && candidate.type === 'manages')
    if (!ownerEdge) continue
    rewiredEdges.push({
      ...edge,
      id: `${ownerEdge.source}-to-${edge.target}`,
      source: ownerEdge.source,
      type: 'manages',
    })
  }

  return {
    ...topology,
    nodes: topology.nodes
      .filter((node) => !collapsedReplicaSets.has(node.id))
      .map((node) => parentData.has(node.id) ? { ...node, data: { ...node.data, ...parentData.get(node.id) } } : node),
    edges: rewiredEdges,
  }
}

const GENERATED_RUNTIME_KINDS = new Set(['pod', 'podgroup', 'replicaset'])
const MAX_INDIVIDUAL_SOURCE_ONLY_RESOURCES = 12

function identityKey(kind: string, group: string | undefined, namespace: string, name: string): string {
  const identityGroup = groupQualifiesLaneId(group) ? group!.toLowerCase() : ''
  return `${identityGroup}/${kind.toLowerCase()}/${namespace}/${name}`
}

export function topologyGroup(node: TopologyNode): string | undefined {
  return apiVersionToGroup(node.data?.apiVersion as string | undefined) || (node.data?.sourceGroup as string | undefined)
}

export function deploymentInventoryFromGitOps(tree: GitOpsResourceTree | undefined): DeploymentInventory | undefined {
  if (!tree) return undefined
  return {
    nodes: tree.nodes
      .filter((node) => node.role === 'declared' || node.role === 'generated')
      .map((node) => ({
        id: node.id,
        kind: node.ref.kind,
        group: node.ref.group,
        namespace: node.ref.namespace,
        name: node.ref.name,
        role: node.role as 'declared' | 'generated',
        status: node.topologyStatus,
      })),
    warnings: tree.warnings,
  }
}

export function deploymentInventoryFromHelm(resources: HelmOwnedResource[] | undefined): DeploymentInventory | undefined {
  if (!resources) return undefined
  return {
    complete: false,
    nodes: resources.map((resource) => ({
      id: `helm/${resource.kind.toLowerCase()}/${resource.namespace}/${resource.name}`,
      kind: resource.kind,
      group: apiVersionToGroup(resource.apiVersion),
      namespace: resource.namespace,
      name: resource.name,
      role: 'declared',
      status: helmResourceStatus(resource.status),
    })),
  }
}

function helmResourceStatus(status: string | undefined): HealthStatus {
  const normalized = status?.toLowerCase() ?? ''
  if (['running', 'active', 'ready', 'succeeded', 'completed', 'bound'].includes(normalized)) return 'healthy'
  if (['failed', 'error', 'unhealthy'].includes(normalized)) return 'unhealthy'
  if (['pending', 'degraded'].includes(normalized)) return 'degraded'
  return 'unknown'
}

export function layerDeploymentInventory(
  topology: Topology,
  inventory: DeploymentInventory,
  sourceLabel: string,
): DeploymentTopologyLayer {
  const inventoryByExact = new Map(inventory.nodes.map((node) => [
    identityKey(node.kind, node.group, node.namespace, node.name),
    node,
  ]))
  const matchedInventoryIds = new Set<string>()
  let managedRuntimeCount = 0
  let runtimeOnlyCount = 0
  const inventoryComplete = inventory.complete !== false && (inventory.warnings?.length ?? 0) === 0

  const runtimeNodes = topology.nodes.map((node) => {
    if (node.kind === 'PodGroup' || node.kind === 'Internet') return node
    const namespace = (node.data?.namespace as string) || ''
    const match = inventoryByExact.get(identityKey(node.kind, topologyGroup(node), namespace, node.name))
    if (match) {
      matchedInventoryIds.add(match.id)
      managedRuntimeCount += 1
      return node
    }
    if (!inventoryComplete || GENERATED_RUNTIME_KINDS.has(node.kind.toLowerCase())) return node
    runtimeOnlyCount += 1
    return {
      ...node,
      data: {
        ...node.data,
        deploymentMembership: 'runtime-only' satisfies DeploymentMembership,
        deploymentSourceLabel: sourceLabel,
      },
    }
  })

  const managedOnly = inventory.nodes.filter((node) => node.role === 'declared' && !matchedInventoryIds.has(node.id))
  const managedOnlySummary = managedOnly.length > MAX_INDIVIDUAL_SOURCE_ONLY_RESOURCES
    ? summarizeInventory(managedOnly)
    : undefined
  const sourceOnlyNodes = managedOnlySummary
    ? []
    : groupedSourceOnlyNodes(managedOnly, sourceLabel)

  return {
    topology: { ...topology, nodes: [...runtimeNodes, ...sourceOnlyNodes] },
    managedRuntimeCount,
    runtimeOnlyCount,
    managedOnly,
    managedOnlySummary,
    inventoryComplete,
  }
}

function groupedSourceOnlyNodes(nodes: DeploymentInventoryNode[], sourceLabel: string): TopologyNode[] {
  const groups = new Map<string, DeploymentInventoryNode[]>()
  for (const node of nodes) {
    const key = `${node.group ?? ''}/${node.kind}/${node.namespace}`
    groups.set(key, [...(groups.get(key) ?? []), node])
  }

  const result: TopologyNode[] = []
  for (const [groupKey, group] of groups) {
    if (group.length <= 3) {
      result.push(...group.map((node) => sourceOnlyNode(node, sourceLabel)))
      continue
    }
    const representative = group[0]
    result.push({
      id: `source-group/${groupKey}`,
      kind: representative.kind,
      name: pluralize(group.length, representative.kind),
      status: worstTopologyStatus(group.map((node) => node.status)),
      data: {
        namespace: representative.namespace,
        sourceGroup: representative.group,
        sourceInventoryGroup: true,
        sourceInventoryCount: group.length,
        deploymentMembership: 'source-only' satisfies DeploymentMembership,
        deploymentSourceLabel: sourceLabel,
      },
    })
  }
  return result
}

function summarizeInventory(nodes: DeploymentInventoryNode[]): string {
  const counts = new Map<string, number>()
  for (const node of nodes) counts.set(node.kind, (counts.get(node.kind) ?? 0) + 1)
  const leadingKinds = [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, 2)
  const summarizedCount = leadingKinds.reduce((total, [, count]) => total + count, 0)
  const summary = [
    ...leadingKinds.map(([kind, count]) => pluralize(count, kind)),
    ...(nodes.length > summarizedCount ? [`${nodes.length - summarizedCount} more`] : []),
  ].join(' · ')

  return summary
}

function sourceOnlyNode(node: DeploymentInventoryNode, sourceLabel: string): TopologyNode {
  return {
    id: `source/${node.id}`,
    kind: node.kind,
    name: node.name,
    status: node.status ?? 'unknown',
    data: {
      namespace: node.namespace,
      sourceGroup: node.group,
      deploymentMembership: 'source-only' satisfies DeploymentMembership,
      deploymentSourceLabel: sourceLabel,
    },
  }
}

function worstTopologyStatus(statuses: Array<HealthStatus | undefined>): HealthStatus {
  const rank: Record<HealthStatus, number> = { unknown: 0, neutral: 1, healthy: 2, degraded: 3, unhealthy: 4 }
  return statuses.reduce<HealthStatus>((worst, status) => status && rank[status] > rank[worst] ? status : worst, 'unknown')
}
