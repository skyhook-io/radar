import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  type Node,
  type Edge,
  type NodeTypes,
  BackgroundVariant,
  Position,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import ELK from 'elkjs/lib/elk.bundled.js'

import { K8sResourceNode, NODE_DIMENSIONS } from './K8sResourceNode'
import { GroupNode } from './GroupNode'
import type { Topology, TopologyNode, TopologyEdge, ViewMode, GroupingMode, NodeKind } from '../../types'

// ELK layout options - simpler approach matching koala-frontend
// Relies on natural edge-based layering without partitioning
const elk = new ELK()

const elkOptions = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.spacing.nodeNode': '12',
  'elk.layered.spacing.nodeNodeBetweenLayers': '25',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
  'elk.separateConnectedComponents': 'true',
  'elk.spacing.componentComponent': '50',
}

// Reserved for future hierarchical layout
// const elkOptionsGrouped = {
//   'elk.algorithm': 'layered',
//   'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
// }

// Edge colors by type
const EDGE_COLORS = {
  'routes-to': '#22c55e',  // Green for traffic flow
  'exposes': '#3b82f6',    // Blue for service exposure
  'manages': '#64748b',    // Gray for management relationships
  'configures': '#f59e0b', // Amber for config
  'uses': '#ec4899',       // Pink for HPA
} as const

function getEdgeColor(type: string, isTrafficView: boolean): string {
  if (isTrafficView) {
    // In traffic view, use green for all edges
    return '#22c55e'
  }
  return EDGE_COLORS[type as keyof typeof EDGE_COLORS] || '#64748b'
}

function getNodeColor(kind: string): string {
  switch (kind) {
    case 'Internet':
      return '#6366f1'
    case 'Ingress':
      return '#8b5cf6'
    case 'Service':
      return '#3b82f6'
    case 'Deployment':
      return '#10b981'
    case 'DaemonSet':
      return '#14b8a6'
    case 'StatefulSet':
      return '#06b6d4'
    case 'ReplicaSet':
      return '#22c55e'
    case 'Pod':
      return '#84cc16'
    case 'ConfigMap':
      return '#f59e0b'
    case 'Secret':
      return '#ef4444'
    case 'HPA':
      return '#ec4899'
    case 'group':
      return '#4f46e5'
    default:
      return '#64748b'
  }
}

// Get group key for a node based on grouping mode
function getGroupKey(node: TopologyNode, groupingMode: GroupingMode): string | null {
  if (groupingMode === 'none') return null

  if (groupingMode === 'namespace') {
    return (node.data.namespace as string) || null
  }

  if (groupingMode === 'app') {
    // Check common app labels
    const labels = (node.data.labels as Record<string, string>) || {}
    return labels['app.kubernetes.io/name'] || labels['app'] || labels['app.kubernetes.io/instance'] || null
  }

  return null
}

// Build nodes with grouping
function buildNodesWithGroups(
  topologyNodes: TopologyNode[],
  groupingMode: GroupingMode,
  collapsedGroups: Set<string>,
  onToggleCollapse: (groupId: string) => void
): { nodes: Node[]; groupMap: Map<string, string[]> } {
  const groupMap = new Map<string, string[]>()
  const nodes: Node[] = []

  if (groupingMode === 'none') {
    // No grouping - just convert nodes
    for (const node of topologyNodes) {
      nodes.push({
        id: node.id,
        type: 'k8sResource',
        position: { x: 0, y: 0 },
        data: {
          kind: node.kind,
          name: node.name,
          status: node.status,
          nodeData: node.data,
          selected: false,
        },
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      })
    }
    return { nodes, groupMap }
  }

  // Group nodes
  for (const node of topologyNodes) {
    const groupKey = getGroupKey(node, groupingMode)
    if (groupKey) {
      if (!groupMap.has(groupKey)) {
        groupMap.set(groupKey, [])
      }
      groupMap.get(groupKey)!.push(node.id)
    }
  }

  // Create group nodes
  for (const [groupKey, memberIds] of groupMap) {
    const groupId = `group-${groupingMode}-${groupKey}`
    const isCollapsed = collapsedGroups.has(groupId)

    nodes.push({
      id: groupId,
      type: 'group',
      position: { x: 0, y: 0 },
      data: {
        type: groupingMode,
        name: groupKey,
        nodeCount: memberIds.length,
        collapsed: isCollapsed,
        onToggleCollapse,
      },
      style: isCollapsed ? {} : {
        width: 400,
        height: 200,
      },
    })
  }

  // Create resource nodes
  for (const node of topologyNodes) {
    const groupKey = getGroupKey(node, groupingMode)
    const groupId = groupKey ? `group-${groupingMode}-${groupKey}` : undefined
    const isGroupCollapsed = groupId ? collapsedGroups.has(groupId) : false

    // Skip nodes in collapsed groups
    if (isGroupCollapsed) continue

    nodes.push({
      id: node.id,
      type: 'k8sResource',
      position: { x: 0, y: 0 },
      data: {
        kind: node.kind,
        name: node.name,
        status: node.status,
        nodeData: node.data,
        selected: false,
      },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })
  }

  return { nodes, groupMap }
}

// Build edges, handling collapsed groups
function buildEdges(
  topologyEdges: { id: string; source: string; target: string; type: string }[],
  collapsedGroups: Set<string>,
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  isTrafficView: boolean
): Edge[] {
  const edges: Edge[] = []
  const nodeToGroup = new Map<string, string>()

  // Build reverse lookup: node -> group
  for (const [groupKey, memberIds] of groupMap) {
    const groupId = `group-${groupingMode}-${groupKey}`
    for (const nodeId of memberIds) {
      nodeToGroup.set(nodeId, groupId)
    }
  }

  for (const edge of topologyEdges) {
    let source = edge.source
    let target = edge.target

    // If source is in a collapsed group, point to the group instead
    const sourceGroup = nodeToGroup.get(source)
    if (sourceGroup && collapsedGroups.has(sourceGroup)) {
      source = sourceGroup
    }

    // If target is in a collapsed group, point to the group instead
    const targetGroup = nodeToGroup.get(target)
    if (targetGroup && collapsedGroups.has(targetGroup)) {
      target = targetGroup
    }

    // Skip self-loops (both ends in same collapsed group)
    if (source === target) continue

    // Skip duplicate edges
    const edgeId = `${source}-${target}-${edge.type}`
    if (edges.find(e => e.id === edgeId)) continue

    const edgeColor = getEdgeColor(edge.type, isTrafficView)
    const isTrafficEdge = edge.type === 'routes-to' || edge.type === 'exposes'

    edges.push({
      id: edgeId,
      source,
      target,
      type: 'smoothstep',
      animated: isTrafficView && isTrafficEdge,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: edgeColor,
        width: 12,
        height: 12,
      },
      style: {
        stroke: edgeColor,
        strokeWidth: isTrafficView ? 2 : 1.5,
        strokeDasharray: isTrafficView && isTrafficEdge ? '5 5' : undefined,
      },
    })
  }

  return edges
}

// Layout nodes using ELK
async function layoutNodes(
  nodes: Node[],
  edges: Edge[],
  _grouped: boolean
): Promise<Node[]> {
  // Build nodes with proper dimensions
  const allNodes = nodes.map(node => {
    if (node.type === 'group') {
      return { id: node.id, width: 250, height: 80 }
    }
    // Get dimensions from NODE_DIMENSIONS based on kind
    const kind = node.data?.kind as NodeKind | undefined
    const dims = kind ? NODE_DIMENSIONS[kind] : { width: 200, height: 56 }

    return {
      id: node.id,
      width: dims.width,
      height: dims.height,
    }
  })

  const graph = {
    id: 'root',
    layoutOptions: elkOptions,
    children: allNodes,
    edges: edges.map(edge => ({
      id: edge.id,
      sources: [edge.source],
      targets: [edge.target],
    })),
  }

  try {
    const layoutedGraph = await elk.layout(graph)

    return nodes.map(node => {
      const elkNode = layoutedGraph.children?.find(n => n.id === node.id)
      return {
        ...node,
        position: {
          x: elkNode?.x ?? 0,
          y: elkNode?.y ?? 0,
        },
      }
    })
  } catch (err) {
    console.error('ELK layout error:', err)
    // Fallback: simple grid layout
    return nodes.map((node, i) => ({
      ...node,
      position: {
        x: (i % 5) * 350,
        y: Math.floor(i / 5) * 100,
      },
    }))
  }
}

// Custom node types
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const nodeTypes: NodeTypes = {
  k8sResource: K8sResourceNode as any,
  group: GroupNode as any,
}

interface TopologyGraphProps {
  topology: Topology | null
  viewMode: ViewMode
  groupingMode: GroupingMode
  onNodeClick: (node: TopologyNode) => void
  selectedNodeId?: string
}

export function TopologyGraph({
  topology,
  viewMode,
  groupingMode,
  onNodeClick,
  selectedNodeId,
}: TopologyGraphProps) {
  const isTrafficView = viewMode === 'traffic'
  const [nodes, setNodes, onNodesChange] = useNodesState([] as Node[])
  const [edges, setEdges, onEdgesChange] = useEdgesState([] as Edge[])
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())
  const [expandedPodGroups, setExpandedPodGroups] = useState<Set<string>>(new Set())
  const prevStructureRef = useRef<string>('')

  // Toggle group collapse
  const handleToggleCollapse = useCallback((groupId: string) => {
    setCollapsedGroups(prev => {
      const next = new Set(prev)
      if (next.has(groupId)) {
        next.delete(groupId)
      } else {
        next.add(groupId)
      }
      return next
    })
  }, [])

  // Expand pod group to show individual pods
  const handleExpandPodGroup = useCallback((podGroupId: string) => {
    setExpandedPodGroups(prev => new Set(prev).add(podGroupId))
  }, [])

  // Collapse pod group back
  const handleCollapsePodGroup = useCallback((podGroupId: string) => {
    setExpandedPodGroups(prev => {
      const next = new Set(prev)
      next.delete(podGroupId)
      return next
    })
  }, [])

  // Expand PodGroup to individual pods
  const expandPodGroup = useCallback((
    topoNodes: TopologyNode[],
    topoEdges: TopologyEdge[],
    podGroupId: string
  ): { nodes: TopologyNode[]; edges: TopologyEdge[] } => {
    const podGroupNode = topoNodes.find(n => n.id === podGroupId && n.kind === 'PodGroup')
    if (!podGroupNode || !podGroupNode.data.pods) {
      return { nodes: topoNodes, edges: topoEdges }
    }

    const pods = podGroupNode.data.pods as Array<{
      name: string
      namespace: string
      phase: string
      restarts: number
      containers: number
    }>

    // Find edges pointing to this pod group
    const edgesToGroup = topoEdges.filter(e => e.target === podGroupId)
    const sourceIds = edgesToGroup.map(e => e.source)

    // Remove the PodGroup node and its edges
    const newNodes = topoNodes.filter(n => n.id !== podGroupId)
    const newEdges = topoEdges.filter(e => e.target !== podGroupId)

    // Add individual pod nodes
    for (const pod of pods) {
      const podId = `pod-${pod.namespace}-${pod.name}`
      newNodes.push({
        id: podId,
        kind: 'Pod',
        name: pod.name,
        status: pod.phase === 'Running' ? 'healthy' : pod.phase === 'Pending' ? 'degraded' : 'unhealthy',
        data: {
          namespace: pod.namespace,
          phase: pod.phase,
          restarts: pod.restarts,
          containers: pod.containers,
          expandedFromGroup: podGroupId, // Track which group this came from
        },
      })

      // Add edges from all sources to this pod
      for (const sourceId of sourceIds) {
        newEdges.push({
          id: `${sourceId}-to-${podId}`,
          source: sourceId,
          target: podId,
          type: 'routes-to' as const,
        })
      }
    }

    return { nodes: newNodes, edges: newEdges }
  }, [])

  // Build nodes and edges with grouping
  const { flowNodes, flowEdges } = useMemo(() => {
    if (!topology) {
      return { flowNodes: [] as Node[], flowEdges: [] as Edge[], groupMap: new Map() }
    }

    // Start with topology data, expand any expanded pod groups
    let workingNodes = [...topology.nodes]
    let workingEdges = [...topology.edges]

    for (const podGroupId of expandedPodGroups) {
      const result = expandPodGroup(workingNodes, workingEdges, podGroupId)
      workingNodes = result.nodes
      workingEdges = result.edges
    }

    const { nodes: builtNodes, groupMap } = buildNodesWithGroups(
      workingNodes,
      groupingMode,
      collapsedGroups,
      handleToggleCollapse
    )

    // Add expand/collapse handlers to pod-related nodes
    const nodesWithHandlers = builtNodes.map(node => {
      const isPodGroup = node.data?.kind === 'PodGroup'
      const nodeData = node.data?.nodeData as Record<string, unknown> | undefined
      const expandedFromGroup = nodeData?.expandedFromGroup as string | undefined

      return {
        ...node,
        data: {
          ...node.data,
          onExpand: isPodGroup ? handleExpandPodGroup : undefined,
          onCollapse: expandedFromGroup ? handleCollapsePodGroup : undefined,
          isExpanded: isPodGroup ? expandedPodGroups.has(node.id) : undefined,
        },
      }
    })

    const builtEdges = buildEdges(
      workingEdges,
      collapsedGroups,
      groupMap,
      groupingMode,
      isTrafficView
    )

    return { flowNodes: nodesWithHandlers, flowEdges: builtEdges, groupMap }
  }, [topology, groupingMode, collapsedGroups, handleToggleCollapse, isTrafficView, expandedPodGroups, expandPodGroup, handleExpandPodGroup, handleCollapsePodGroup])

  // Structure key for change detection
  const structureKey = useMemo(() => {
    const nodeIds = flowNodes.map(n => `${n.id}:${n.parentId || ''}`).sort().join(',')
    const collapsed = Array.from(collapsedGroups).sort().join(',')
    const expanded = Array.from(expandedPodGroups).sort().join(',')
    return `${nodeIds}|${collapsed}|${expanded}|${groupingMode}`
  }, [flowNodes, collapsedGroups, expandedPodGroups, groupingMode])

  // Layout when structure changes
  useEffect(() => {
    if (flowNodes.length === 0) {
      setNodes([])
      setEdges([])
      prevStructureRef.current = ''
      return
    }

    const structureChanged = structureKey !== prevStructureRef.current

    if (structureChanged) {
      prevStructureRef.current = structureKey
      const isGrouped = groupingMode !== 'none'
      layoutNodes(flowNodes, flowEdges, isGrouped).then((layoutedNodes) => {
        setNodes(layoutedNodes)
        setEdges(flowEdges)
      })
    } else {
      // Just update data
      setNodes(currentNodes =>
        currentNodes.map(node => {
          const updated = flowNodes.find(n => n.id === node.id)
          if (updated) {
            return { ...node, data: { ...node.data, ...updated.data } }
          }
          return node
        })
      )
      setEdges(flowEdges)
    }
  }, [flowNodes, flowEdges, structureKey, groupingMode, setNodes, setEdges])

  // Handle node click
  const handleNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      // Ignore clicks on group nodes
      if (node.type === 'group') return

      const topologyNode = topology?.nodes.find(n => n.id === node.id)
      if (topologyNode) {
        onNodeClick(topologyNode)
      }
    },
    [topology, onNodeClick]
  )

  // Update selected state
  useEffect(() => {
    setNodes(nds =>
      nds.map(node => ({
        ...node,
        data: {
          ...node.data,
          selected: node.id === selectedNodeId,
        },
      }))
    )
  }, [selectedNodeId, setNodes])

  if (!topology || topology.nodes.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center text-slate-400">
        <div className="text-center">
          <p className="text-lg">No resources found</p>
          <p className="text-sm mt-2">
            Select a namespace or check your cluster connection
          </p>
        </div>
      </div>
    )
  }

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      onNodeClick={handleNodeClick}
      nodeTypes={nodeTypes}
      fitView
      fitViewOptions={{ padding: 0.2 }}
      minZoom={0.1}
      maxZoom={2}
      proOptions={{ hideAttribution: true }}
    >
      <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#334155" />
      <Controls
        className="bg-slate-800 border border-slate-700 rounded-lg"
        showInteractive={false}
      />
      <MiniMap
        className="bg-slate-800 border border-slate-700 rounded-lg"
        nodeColor={(node) => getNodeColor(node.data?.kind as string || node.data?.type as string || '')}
        maskColor="rgba(15, 23, 42, 0.8)"
      />
    </ReactFlow>
  )
}
