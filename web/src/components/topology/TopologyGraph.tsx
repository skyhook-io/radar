import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  useReactFlow,
  type Node,
  type Edge,
  type NodeTypes,
  BackgroundVariant,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import { K8sResourceNode } from './K8sResourceNode'
import { GroupNode } from './GroupNode'
import { buildHierarchicalElkGraph, applyHierarchicalLayout } from './layout'
import type { Topology, TopologyNode, TopologyEdge, ViewMode, GroupingMode } from '../../types'

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


// Build edges, handling collapsed groups
function buildEdges(
  topologyEdges: { id: string; source: string; target: string; type: string }[],
  collapsedGroups: Set<string>,
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  isTrafficView: boolean,
  nodeToGroup?: Map<string, string>
): Edge[] {
  const edges: Edge[] = []

  // Build reverse lookup if not provided
  const nodeGroupMap = nodeToGroup || new Map<string, string>()
  if (!nodeToGroup) {
    for (const [groupKey, memberIds] of groupMap) {
      const groupId = `group-${groupingMode}-${groupKey}`
      for (const nodeId of memberIds) {
        nodeGroupMap.set(nodeId, groupId)
      }
    }
  }

  for (const edge of topologyEdges) {
    let source = edge.source
    let target = edge.target

    // If source is in a collapsed group, point to the group instead
    const sourceGroup = nodeGroupMap.get(source)
    if (sourceGroup && collapsedGroups.has(sourceGroup)) {
      source = sourceGroup
    }

    // If target is in a collapsed group, point to the group instead
    const targetGroup = nodeGroupMap.get(target)
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

  // Prepare topology data with expanded pod groups
  const { workingNodes, workingEdges } = useMemo(() => {
    if (!topology) {
      return { workingNodes: [] as TopologyNode[], workingEdges: [] as TopologyEdge[] }
    }

    let nodes = [...topology.nodes]
    let edges = [...topology.edges]

    for (const podGroupId of expandedPodGroups) {
      const result = expandPodGroup(nodes, edges, podGroupId)
      nodes = result.nodes
      edges = result.edges
    }

    return { workingNodes: nodes, workingEdges: edges }
  }, [topology, expandedPodGroups, expandPodGroup])

  // Structure key for change detection
  const structureKey = useMemo(() => {
    const nodeIds = workingNodes.map(n => n.id).sort().join(',')
    const collapsed = Array.from(collapsedGroups).sort().join(',')
    const expanded = Array.from(expandedPodGroups).sort().join(',')
    return `${nodeIds}|${collapsed}|${expanded}|${groupingMode}`
  }, [workingNodes, collapsedGroups, expandedPodGroups, groupingMode])

  // Layout when structure changes - use hierarchical ELK layout
  useEffect(() => {
    if (workingNodes.length === 0) {
      setNodes([])
      setEdges([])
      prevStructureRef.current = ''
      return
    }

    const structureChanged = structureKey !== prevStructureRef.current

    if (structureChanged) {
      prevStructureRef.current = structureKey

      // Build hierarchical ELK graph
      const { elkGraph, groupMap, nodeToGroup } = buildHierarchicalElkGraph(
        workingNodes,
        workingEdges,
        groupingMode,
        collapsedGroups
      )

      // Apply layout and get positioned nodes
      applyHierarchicalLayout(
        elkGraph,
        workingNodes,
        groupMap,
        groupingMode,
        collapsedGroups,
        handleToggleCollapse
      ).then(({ nodes: layoutedNodes }) => {
        // Add expand/collapse handlers to pod-related nodes
        const nodesWithHandlers = layoutedNodes.map(node => {
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

        setNodes(nodesWithHandlers)

        // Build edges with styling
        const builtEdges = buildEdges(
          workingEdges,
          collapsedGroups,
          groupMap,
          groupingMode,
          isTrafficView,
          nodeToGroup
        )
        setEdges(builtEdges)
      })
    }
    // Note: When structure hasn't changed, nodes keep their positions
    // Data updates happen via selected state effect
  }, [workingNodes, workingEdges, structureKey, groupingMode, collapsedGroups, handleToggleCollapse, isTrafficView, expandedPodGroups, handleExpandPodGroup, handleCollapsePodGroup, setNodes, setEdges])

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
    <ReactFlowProvider>
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
        <ViewportController structureKey={structureKey} />
      </ReactFlow>
    </ReactFlowProvider>
  )
}

// Animation duration for viewport transitions
const VIEWPORT_ANIMATION_DURATION = 400

// Inner component to handle animated viewport transitions
// Must be inside ReactFlow to use useReactFlow hook
function ViewportController({ structureKey }: { structureKey: string }) {
  const { fitView } = useReactFlow()
  const prevStructureKeyRef = useRef<string>('')
  const isInitialMount = useRef(true)

  useEffect(() => {
    // Skip animation on initial mount (fitView prop handles that)
    if (isInitialMount.current) {
      isInitialMount.current = false
      prevStructureKeyRef.current = structureKey
      return
    }

    // Only animate when structure actually changes
    if (structureKey !== prevStructureKeyRef.current) {
      prevStructureKeyRef.current = structureKey

      // Small delay to let the new nodes render, then animate to fit
      const timeoutId = setTimeout(() => {
        fitView({
          padding: 0.15,
          duration: VIEWPORT_ANIMATION_DURATION,
        })
      }, 50)

      return () => clearTimeout(timeoutId)
    }
  }, [structureKey, fitView])

  return null
}

