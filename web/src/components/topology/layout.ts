import type { Node } from '@xyflow/react'
import type { TopologyNode, GroupingMode, NodeKind } from '../../types'
import { NODE_DIMENSIONS } from './K8sResourceNode'

// Group padding - space for header + internal spacing (must account for border)
// Top padding accommodates the header at its largest (when zoomed out)
// Child nodes are translated up dynamically when zoomed in (see TopologyGraph)
const GROUP_PADDING = {
  top: 100,   // Space for group header at max scale
  left: 30,
  bottom: 36,
  right: 30,
}

// Reduced padding when header is hidden (single namespace view)
const GROUP_PADDING_NO_HEADER = {
  top: 30,
  left: 30,
  bottom: 30,
  right: 30,
}

// Worker instance management
let layoutWorker: Worker | null = null
let requestIdCounter = 0
const pendingRequests = new Map<number, {
  resolve: (result: WorkerLayoutResult) => void
  reject: (error: Error) => void
}>()

interface WorkerLayoutResult {
  groupLayouts: Array<{
    groupId: string
    groupKey: string
    width: number
    height: number
    children: Array<{ id: string; x: number; y: number }>
    isCollapsed: boolean
  }>
  ungroupedNodes: Array<{
    id: string
    width: number
    height: number
  }>
  groupPositions: Array<[string, { x: number; y: number }]>
  error?: string
}

function getLayoutWorker(): Worker {
  if (!layoutWorker) {
    // Use URL constructor for better Vite compatibility with workers
    layoutWorker = new Worker(new URL('./layout.worker.ts', import.meta.url), { type: 'module' })
    layoutWorker.onmessage = (e: MessageEvent) => {
      const { requestId, ...result } = e.data
      const pending = pendingRequests.get(requestId)
      if (pending) {
        pendingRequests.delete(requestId)
        if (result.error) {
          pending.reject(new Error(result.error))
        } else {
          pending.resolve(result as WorkerLayoutResult)
        }
      }
    }
    layoutWorker.onerror = (e) => {
      console.error('Layout worker error:', e)
      // Reject all pending requests
      for (const [, pending] of pendingRequests) {
        pending.reject(new Error('Worker error'))
      }
      pendingRequests.clear()
    }
  }
  return layoutWorker
}

function runLayoutInWorker(
  elkGraph: ElkGraph,
  groupingMode: GroupingMode,
  hideGroupHeader: boolean,
  padding: typeof GROUP_PADDING
): Promise<WorkerLayoutResult> {
  return new Promise((resolve, reject) => {
    const worker = getLayoutWorker()
    const requestId = ++requestIdCounter
    pendingRequests.set(requestId, { resolve, reject })
    worker.postMessage({
      type: 'layout',
      requestId,
      elkGraph,
      groupingMode,
      hideGroupHeader,
      padding,
    })
  })
}

interface ElkNode {
  id: string
  width?: number
  height?: number
  children?: ElkNode[]
  layoutOptions?: Record<string, string>
  labels?: Array<{ text: string }>
}

interface ElkEdge {
  id: string
  sources: string[]
  targets: string[]
}

interface ElkGraph {
  id: string
  layoutOptions: Record<string, string>
  children: ElkNode[]
  edges: ElkEdge[]
}

// Get app label from a node (if it has one)
function getAppLabel(node: TopologyNode): string | null {
  const labels = (node.data.labels as Record<string, string>) || {}
  return labels['app.kubernetes.io/name'] || labels['app'] || labels['app.kubernetes.io/instance'] || null
}

// Get group key for a node based on grouping mode
export function getGroupKey(node: TopologyNode, groupingMode: GroupingMode): string | null {
  if (groupingMode === 'none') return null

  if (groupingMode === 'namespace') {
    return (node.data.namespace as string) || null
  }

  if (groupingMode === 'app') {
    return getAppLabel(node)
  }

  return null
}

// Propagate app labels through connected resources and create groups for unlabeled connected components
// Returns a map of nodeId -> groupName for all nodes that should be grouped
function propagateAppLabels(
  nodes: TopologyNode[],
  edges: Array<{ id: string; source: string; target: string; type: string }>
): Map<string, string> {
  const nodeMap = new Map<string, TopologyNode>()
  for (const node of nodes) {
    nodeMap.set(node.id, node)
  }

  // Build adjacency list (bidirectional for propagation) - only within same namespace
  const connections = new Map<string, Set<string>>()
  for (const node of nodes) {
    connections.set(node.id, new Set())
  }
  for (const edge of edges) {
    const sourceNode = nodeMap.get(edge.source)
    const targetNode = nodeMap.get(edge.target)
    // Only connect nodes in the same namespace
    if (sourceNode && targetNode && sourceNode.data.namespace === targetNode.data.namespace) {
      connections.get(edge.source)?.add(edge.target)
      connections.get(edge.target)?.add(edge.source)
    }
  }

  // Initial pass: find nodes with explicit app labels
  const nodeGroupLabels = new Map<string, string>()
  for (const node of nodes) {
    const appLabel = getAppLabel(node)
    if (appLabel) {
      nodeGroupLabels.set(node.id, appLabel)
    }
  }

  // Propagate labels through connections (BFS from labeled nodes)
  let changed = true
  const maxIterations = 10
  let iteration = 0

  while (changed && iteration < maxIterations) {
    changed = false
    iteration++

    for (const node of nodes) {
      if (nodeGroupLabels.has(node.id)) continue

      const connectedNodes = connections.get(node.id) || new Set()
      const connectedLabels = new Set<string>()

      for (const connectedId of connectedNodes) {
        const connectedLabel = nodeGroupLabels.get(connectedId)
        if (connectedLabel) {
          connectedLabels.add(connectedLabel)
        }
      }

      // If exactly one connected label, inherit it
      if (connectedLabels.size === 1) {
        const [inheritedLabel] = connectedLabels
        nodeGroupLabels.set(node.id, inheritedLabel)
        changed = true
      }
    }
  }

  // Find connected components among remaining unlabeled nodes
  const unlabeledNodes = nodes.filter(n => !nodeGroupLabels.has(n.id))
  const visited = new Set<string>()

  for (const startNode of unlabeledNodes) {
    if (visited.has(startNode.id)) continue

    // BFS to find connected component
    const component: TopologyNode[] = []
    const queue = [startNode.id]
    visited.add(startNode.id)

    while (queue.length > 0) {
      const nodeId = queue.shift()!
      const node = nodeMap.get(nodeId)
      if (node && !nodeGroupLabels.has(nodeId)) {
        component.push(node)
      }

      for (const connectedId of connections.get(nodeId) || []) {
        if (!visited.has(connectedId) && !nodeGroupLabels.has(connectedId)) {
          visited.add(connectedId)
          queue.push(connectedId)
        }
      }
    }

    // Create a group for this connected component (only if more than 1 node)
    // Singletons remain ungrouped
    if (component.length > 1) {
      // Name the group after the most "important" node (prefer Deployment, Service, etc.)
      const groupName = pickGroupName(component)
      for (const node of component) {
        nodeGroupLabels.set(node.id, groupName)
      }
    }
  }

  return nodeGroupLabels
}

// Pick a representative name for a connected component group
function pickGroupName(nodes: TopologyNode[]): string {
  // Priority order for picking the group name
  const kindPriority: Record<string, number> = {
    'Deployment': 1,
    'Rollout': 1,
    'StatefulSet': 2,
    'DaemonSet': 3,
    'CronJob': 4,
    'Job': 5,
    'Service': 6,
    'Gateway': 7,
    'HTTPRoute': 6,
    'GRPCRoute': 6,
    'TCPRoute': 6,
    'TLSRoute': 6,
    'Ingress': 7,
    'ReplicaSet': 8,
    'Pod': 9,
    'PodGroup': 9,
    'ConfigMap': 10,
    'Secret': 10,
    'PersistentVolumeClaim': 10,
    'HorizontalPodAutoscaler': 10,
  }

  // Sort by priority and pick the first
  const sorted = [...nodes].sort((a, b) => {
    const priorityA = kindPriority[a.kind] || 99
    const priorityB = kindPriority[b.kind] || 99
    return priorityA - priorityB
  })

  return sorted[0].name
}

// Build hierarchical ELK graph with groups containing children
export function buildHierarchicalElkGraph(
  topologyNodes: TopologyNode[],
  edges: Array<{ id: string; source: string; target: string; type: string }>,
  groupingMode: GroupingMode,
  collapsedGroups: Set<string>
): { elkGraph: ElkGraph; groupMap: Map<string, string[]>; nodeToGroup: Map<string, string> } {
  const groupMap = new Map<string, string[]>()
  const nodeToGroup = new Map<string, string>()

  // For app grouping, propagate labels through connected resources
  const propagatedAppLabels = groupingMode === 'app'
    ? propagateAppLabels(topologyNodes, edges)
    : null

  // Group nodes by their group key
  for (const node of topologyNodes) {
    let groupKey: string | null = null

    if (groupingMode === 'namespace') {
      groupKey = (node.data.namespace as string) || null
    } else if (groupingMode === 'app') {
      // Use propagated label if available, otherwise direct label
      groupKey = propagatedAppLabels?.get(node.id) || getAppLabel(node)
    }

    if (groupKey) {
      const groupId = `group-${groupingMode}-${groupKey}`
      if (!groupMap.has(groupKey)) {
        groupMap.set(groupKey, [])
      }
      groupMap.get(groupKey)!.push(node.id)
      nodeToGroup.set(node.id, groupId)
    }
  }

  const children: ElkNode[] = []
  const processedNodes = new Set<string>()

  if (groupingMode === 'none') {
    // No grouping - all nodes as direct children of root
    for (const node of topologyNodes) {
      const kind = node.kind as NodeKind
      const dims = NODE_DIMENSIONS[kind] || { width: 200, height: 56 }
      children.push({
        id: node.id,
        width: dims.width,
        height: dims.height,
      })
    }
  } else {
    // Create group nodes with children
    for (const [groupKey, memberIds] of groupMap) {
      const groupId = `group-${groupingMode}-${groupKey}`
      const isCollapsed = collapsedGroups.has(groupId)

      if (isCollapsed) {
        // Collapsed group is a single node - width based on label length
        const collapsedWidth = Math.max(400, groupKey.length * 16 + 180)
        children.push({
          id: groupId,
          width: collapsedWidth,
          height: 90,
          labels: [{ text: groupKey }],
        })
      } else {
        // Expanded group contains its children
        const groupChildren: ElkNode[] = []
        for (const nodeId of memberIds) {
          const node = topologyNodes.find(n => n.id === nodeId)
          if (node) {
            const kind = node.kind as NodeKind
            const dims = NODE_DIMENSIONS[kind] || { width: 200, height: 56 }
            groupChildren.push({
              id: nodeId,
              width: dims.width,
              height: dims.height,
            })
            processedNodes.add(nodeId)
          }
        }

        // Calculate minimum width based on label length (approx 14px per char for text-4xl + padding)
        const minWidth = Math.max(500, groupKey.length * 16 + 200)

        children.push({
          id: groupId,
          children: groupChildren,
          layoutOptions: {
            'elk.padding': `[left=${GROUP_PADDING.left}, top=${GROUP_PADDING.top}, right=${GROUP_PADDING.right}, bottom=${GROUP_PADDING.bottom}]`,
            'elk.algorithm': 'layered',
            'elk.direction': 'RIGHT',
            'elk.spacing.nodeNode': '40',
            'elk.layered.spacing.nodeNodeBetweenLayers': '85',
            'elk.layered.spacing.edgeNodeBetweenLayers': '25',
            'elk.nodeSize.minimum': `(${minWidth}, 100)`,
          },
          labels: [{ text: groupKey }],
        })
      }

      // Mark all members as processed
      for (const nodeId of memberIds) {
        processedNodes.add(nodeId)
      }
    }

    // Add ungrouped nodes as direct children
    for (const node of topologyNodes) {
      if (!processedNodes.has(node.id)) {
        const kind = node.kind as NodeKind
        const dims = NODE_DIMENSIONS[kind] || { width: 200, height: 56 }
        children.push({
          id: node.id,
          width: dims.width,
          height: dims.height,
        })
      }
    }
  }

  // Build edges, redirecting to groups when collapsed
  const elkEdges: ElkEdge[] = []
  const seenEdges = new Set<string>()

  for (const edge of edges) {
    let source = edge.source
    let target = edge.target

    // Redirect edges to collapsed groups
    const sourceGroup = nodeToGroup.get(source)
    if (sourceGroup && collapsedGroups.has(sourceGroup)) {
      source = sourceGroup
    }

    const targetGroup = nodeToGroup.get(target)
    if (targetGroup && collapsedGroups.has(targetGroup)) {
      target = targetGroup
    }

    // Skip self-loops
    if (source === target) continue

    // Skip duplicates
    const edgeKey = `${source}->${target}`
    if (seenEdges.has(edgeKey)) continue
    seenEdges.add(edgeKey)

    elkEdges.push({
      id: edge.id,
      sources: [source],
      targets: [target],
    })
  }

  return {
    elkGraph: {
      id: 'root',
      layoutOptions: {},  // Root layout options not used - we manually arrange groups
      children,
      edges: elkEdges,
    },
    groupMap,
    nodeToGroup,
  }
}

// Two-phase layout: first layout groups internally, then position groups based on connections
// Layout is performed in a Web Worker to avoid blocking the main thread
export async function applyHierarchicalLayout(
  elkGraph: ElkGraph,
  topologyNodes: TopologyNode[],
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  _collapsedGroups: Set<string>,
  onToggleCollapse: (groupId: string) => void,
  hideGroupHeader: boolean = false
): Promise<{ nodes: Node[]; positions: Map<string, { x: number; y: number }>; error?: string }> {
  try {
    const padding = hideGroupHeader ? GROUP_PADDING_NO_HEADER : GROUP_PADDING

    // Run layout in worker (off main thread)
    const workerResult = await runLayoutInWorker(elkGraph, groupingMode, hideGroupHeader, padding)

    if (workerResult.error) {
      return { nodes: [], positions: new Map(), error: workerResult.error }
    }

    // Build position map from worker result
    const groupPositions = new Map<string, { x: number; y: number }>(workerResult.groupPositions)

    // Build ReactFlow nodes using positions from worker
    const nodes: Node[] = []
    const positions = new Map<string, { x: number; y: number }>()

    for (const group of workerResult.groupLayouts) {
      const pos = groupPositions.get(group.groupId) || { x: 0, y: 0 }
      const memberIds = groupMap.get(group.groupKey) || []

      positions.set(group.groupId, pos)

      // Add group node
      nodes.push({
        id: group.groupId,
        type: 'group',
        position: pos,
        data: {
          type: groupingMode,
          name: group.groupKey,
          nodeCount: memberIds.length,
          collapsed: group.isCollapsed,
          onToggleCollapse,
          hideHeader: hideGroupHeader,
        },
        style: {
          width: group.width,
          height: group.height,
        },
        zIndex: -1,
      })

      // Add child nodes with positions relative to group
      for (const child of group.children) {
        const topoNode = topologyNodes.find(n => n.id === child.id)
        if (topoNode) {
          const absX = pos.x + child.x
          const absY = pos.y + child.y
          positions.set(child.id, { x: absX, y: absY })

          nodes.push({
            id: child.id,
            type: 'k8sResource',
            position: { x: child.x, y: child.y },
            parentId: group.groupId,
            extent: 'parent',
            data: {
              kind: topoNode.kind,
              name: topoNode.name,
              status: topoNode.status,
              nodeData: topoNode.data,
              selected: false,
            },
          })
        }
      }
    }

    // Add ungrouped nodes
    for (const node of workerResult.ungroupedNodes) {
      const pos = groupPositions.get(node.id) || { x: 0, y: 0 }
      const topoNode = topologyNodes.find(n => n.id === node.id)
      if (topoNode) {
        positions.set(node.id, pos)

        nodes.push({
          id: node.id,
          type: 'k8sResource',
          position: pos,
          data: {
            kind: topoNode.kind,
            name: topoNode.name,
            status: topoNode.status,
            nodeData: topoNode.data,
            selected: false,
          },
        })
      }
    }

    return { nodes, positions }
  } catch (err) {
    const errorMessage = err instanceof Error ? err.message : String(err)
    console.error('ELK hierarchical layout error:', err)
    return { nodes: [], positions: new Map(), error: `Layout failed: ${errorMessage}` }
  }
}
