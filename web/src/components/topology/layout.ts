import type { Node } from '@xyflow/react'
import ELK from 'elkjs/lib/elk.bundled.js'
import type { TopologyNode, GroupingMode, NodeKind } from '../../types'
import { NODE_DIMENSIONS } from './K8sResourceNode'

const elk = new ELK()

// ELK options for hierarchical layout with compound nodes
const elkOptionsHierarchical = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.spacing.nodeNode': '20',
  'elk.layered.spacing.nodeNodeBetweenLayers': '50',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
  'elk.separateConnectedComponents': 'true',
  'elk.spacing.componentComponent': '60',
}

// Group padding - space for header + internal spacing (must account for border)
const GROUP_PADDING = {
  top: 70,    // Space for group header (~50px) + margin
  left: 24,
  bottom: 32,
  right: 24,
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

interface ElkLayoutResult {
  id: string
  x?: number
  y?: number
  width?: number
  height?: number
  children?: ElkLayoutResult[]
}

// Get group key for a node based on grouping mode
export function getGroupKey(node: TopologyNode, groupingMode: GroupingMode): string | null {
  if (groupingMode === 'none') return null

  if (groupingMode === 'namespace') {
    return (node.data.namespace as string) || null
  }

  if (groupingMode === 'app') {
    const labels = (node.data.labels as Record<string, string>) || {}
    return labels['app.kubernetes.io/name'] || labels['app'] || labels['app.kubernetes.io/instance'] || null
  }

  return null
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

  // Group nodes by their group key
  for (const node of topologyNodes) {
    const groupKey = getGroupKey(node, groupingMode)
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
        const collapsedWidth = Math.max(280, groupKey.length * 12 + 100)
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

        // Calculate minimum width based on label length (approx 10px per char + padding)
        const minWidth = Math.max(300, groupKey.length * 12 + 120)

        children.push({
          id: groupId,
          children: groupChildren,
          layoutOptions: {
            'elk.padding': `[left=${GROUP_PADDING.left}, top=${GROUP_PADDING.top}, right=${GROUP_PADDING.right}, bottom=${GROUP_PADDING.bottom}]`,
            'elk.algorithm': 'layered',
            'elk.direction': 'RIGHT',
            'elk.spacing.nodeNode': '15',
            'elk.layered.spacing.nodeNodeBetweenLayers': '30',
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
      layoutOptions: elkOptionsHierarchical,
      children,
      edges: elkEdges,
    },
    groupMap,
    nodeToGroup,
  }
}

// Apply ELK layout results to ReactFlow nodes
export async function applyHierarchicalLayout(
  elkGraph: ElkGraph,
  topologyNodes: TopologyNode[],
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  collapsedGroups: Set<string>,
  onToggleCollapse: (groupId: string) => void
): Promise<{ nodes: Node[]; positions: Map<string, { x: number; y: number }> }> {
  try {
    const layoutResult = await elk.layout(elkGraph) as ElkLayoutResult
    const nodes: Node[] = []
    const positions = new Map<string, { x: number; y: number }>()

    // Process layout result recursively
    function processElkNode(elkNode: ElkLayoutResult, parentId?: string, parentX = 0, parentY = 0) {
      const absoluteX = (elkNode.x || 0) + parentX
      const absoluteY = (elkNode.y || 0) + parentY

      positions.set(elkNode.id, { x: absoluteX, y: absoluteY })

      // Check if this is a group node
      const isGroup = elkNode.id.startsWith('group-')

      if (isGroup) {
        const groupKey = elkNode.id.replace(`group-${groupingMode}-`, '')
        const memberIds = groupMap.get(groupKey) || []
        const isCollapsed = collapsedGroups.has(elkNode.id)

        nodes.push({
          id: elkNode.id,
          type: 'group',
          position: { x: elkNode.x || 0, y: elkNode.y || 0 },
          parentId,
          data: {
            type: groupingMode,
            name: groupKey,
            nodeCount: memberIds.length,
            collapsed: isCollapsed,
            onToggleCollapse,
          },
          style: {
            width: elkNode.width || 280,
            height: elkNode.height || 90,
          },
          // Groups should be behind other nodes
          zIndex: -1,
        })

        // Process children (resource nodes inside this group)
        if (elkNode.children) {
          for (const child of elkNode.children) {
            processElkNode(child, elkNode.id, absoluteX, absoluteY)
          }
        }
      } else {
        // Resource node
        const topoNode = topologyNodes.find(n => n.id === elkNode.id)
        if (topoNode) {
          nodes.push({
            id: elkNode.id,
            type: 'k8sResource',
            position: { x: elkNode.x || 0, y: elkNode.y || 0 },
            parentId,
            extent: parentId ? 'parent' : undefined,
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

    // Process top-level nodes
    if (layoutResult.children) {
      for (const child of layoutResult.children) {
        processElkNode(child)
      }
    }

    return { nodes, positions }
  } catch (err) {
    console.error('ELK hierarchical layout error:', err)
    // Return empty - caller should handle fallback
    return { nodes: [], positions: new Map() }
  }
}
