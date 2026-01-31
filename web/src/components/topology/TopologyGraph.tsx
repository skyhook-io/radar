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
  useOnViewportChange,
  useNodes,
  type Node,
  type Edge,
  type NodeTypes,
  type Viewport,
  BackgroundVariant,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import { AlertTriangle, RotateCw, Scissors } from 'lucide-react'

import { K8sResourceNode } from './K8sResourceNode'
import { GroupNode } from './GroupNode'
import { buildHierarchicalElkGraph, applyHierarchicalLayout, getGroupKey } from './layout'
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
    case 'Rollout':
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
  const seenEdgeIds = new Set<string>() // O(1) duplicate detection

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

    // Skip duplicate edges (O(1) with Set)
    const edgeId = `${source}-${target}-${edge.type}`
    if (seenEdgeIds.has(edgeId)) continue
    seenEdgeIds.add(edgeId)

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
  hideGroupHeader?: boolean
  onNodeClick: (node: TopologyNode) => void
  selectedNodeId?: string
}

export function TopologyGraph({
  topology,
  viewMode,
  groupingMode,
  hideGroupHeader = false,
  onNodeClick,
  selectedNodeId,
}: TopologyGraphProps) {
  const isTrafficView = viewMode === 'traffic'
  const [nodes, setNodes, onNodesChange] = useNodesState([] as Node[])
  const [edges, setEdges, onEdgesChange] = useEdgesState([] as Edge[])
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())
  const [expandedPodGroups, setExpandedPodGroups] = useState<Set<string>>(new Set())
  const [layoutError, setLayoutError] = useState<string | null>(null)
  const [layoutRetryCount, setLayoutRetryCount] = useState(0)
  const prevStructureRef = useRef<string>('')
  const layoutVersionRef = useRef(0) // Used to invalidate stale layout results

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
      const podId = `pod/${pod.namespace}/${pod.name}`
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

  // Transform to per-group Internet nodes in traffic view with grouping
  const createPerGroupInternetNodes = useCallback((
    nodes: TopologyNode[],
    edges: TopologyEdge[],
    groupMode: GroupingMode
  ): { nodes: TopologyNode[]; edges: TopologyEdge[] } => {
    if (groupMode === 'none') {
      return { nodes, edges }
    }

    // Find the single Internet node
    const internetNode = nodes.find(n => n.kind === 'Internet')
    if (!internetNode) {
      return { nodes, edges }
    }

    // Find all ingresses and group them
    const ingresses = nodes.filter(n => n.kind === 'Ingress')
    const groupsWithIngresses = new Map<string, TopologyNode[]>()

    for (const ingress of ingresses) {
      const groupKey = getGroupKey(ingress, groupMode)
      if (groupKey) {
        if (!groupsWithIngresses.has(groupKey)) {
          groupsWithIngresses.set(groupKey, [])
        }
        groupsWithIngresses.get(groupKey)!.push(ingress)
      }
    }

    // If no groups with ingresses, keep original
    if (groupsWithIngresses.size === 0) {
      return { nodes, edges }
    }

    // Remove original Internet node and its edges
    const newNodes = nodes.filter(n => n.id !== internetNode.id)
    const newEdges = edges.filter(e => e.source !== internetNode.id)

    // Create per-group Internet nodes
    for (const [groupKey, groupIngresses] of groupsWithIngresses) {
      const internetId = `internet-${groupMode}-${groupKey}`

      // Add Internet node for this group with group metadata
      newNodes.push({
        id: internetId,
        kind: 'Internet',
        name: 'Internet',
        status: 'healthy',
        data: {
          // Add group metadata so it gets grouped with its ingresses
          namespace: groupMode === 'namespace' ? groupKey : groupIngresses[0]?.data?.namespace,
          labels: groupMode === 'app' ? { 'app.kubernetes.io/name': groupKey } : {},
        },
      })

      // Add edges from this Internet node to its ingresses
      for (const ingress of groupIngresses) {
        newEdges.push({
          id: `${internetId}-to-${ingress.id}`,
          source: internetId,
          target: ingress.id,
          type: 'routes-to',
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

    // Expand pod groups
    for (const podGroupId of expandedPodGroups) {
      const result = expandPodGroup(nodes, edges, podGroupId)
      nodes = result.nodes
      edges = result.edges
    }

    // In traffic view with grouping, create per-group Internet nodes
    if (isTrafficView && groupingMode !== 'none') {
      const result = createPerGroupInternetNodes(nodes, edges, groupingMode)
      nodes = result.nodes
      edges = result.edges
    }

    return { workingNodes: nodes, workingEdges: edges }
  }, [topology, expandedPodGroups, expandPodGroup, isTrafficView, groupingMode, createPerGroupInternetNodes])

  // Structure key for change detection
  const structureKey = useMemo(() => {
    const nodeIds = workingNodes.map(n => n.id).sort().join(',')
    const collapsed = Array.from(collapsedGroups).sort().join(',')
    const expanded = Array.from(expandedPodGroups).sort().join(',')
    return `${viewMode}|${nodeIds}|${collapsed}|${expanded}|${groupingMode}|${layoutRetryCount}`
  }, [viewMode, workingNodes, collapsedGroups, expandedPodGroups, groupingMode, layoutRetryCount])

  // Layout when structure changes - use hierarchical ELK layout
  useEffect(() => {
    if (workingNodes.length === 0) {
      setNodes([])
      setEdges([])
      prevStructureRef.current = ''
      return
    }

    const structureChanged = structureKey !== prevStructureRef.current

    if (!structureChanged) {
      return
    }

    prevStructureRef.current = structureKey

    // Increment version to invalidate any previous in-flight layout
    const thisLayoutVersion = ++layoutVersionRef.current

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
      handleToggleCollapse,
      hideGroupHeader
    ).then(({ nodes: layoutedNodes, error }) => {
      // Check if a newer layout has started - if so, discard this stale result
      if (layoutVersionRef.current !== thisLayoutVersion) {
        return
      }

      // Handle layout errors
      if (error) {
        console.error('Layout error:', error)
        setLayoutError(error)
        return
      }
      setLayoutError(null)

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

    // No cleanup function - we use version-based invalidation instead
    // This prevents React's effect re-runs from canceling in-flight layouts
    // when the actual structure hasn't changed
  }, [workingNodes, workingEdges, structureKey, groupingMode, hideGroupHeader, collapsedGroups, handleToggleCollapse, isTrafficView, expandedPodGroups, handleExpandPodGroup, handleCollapsePodGroup, setNodes, setEdges, layoutRetryCount])

  // Handle node click
  const handleNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      // Ignore clicks on group nodes
      if (node.type === 'group') return

      // First try to find in original topology
      let topologyNode = topology?.nodes.find(n => n.id === node.id)

      // If not found, check workingNodes (for expanded pods from PodGroup)
      if (!topologyNode) {
        topologyNode = workingNodes.find(n => n.id === node.id)
      }

      if (topologyNode) {
        onNodeClick(topologyNode)
      }
    },
    [topology, workingNodes, onNodeClick]
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
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <div className="text-center">
          <p className="text-lg">No resources found</p>
          <p className="text-sm mt-2">
            Select a namespace or check your cluster connection
          </p>
        </div>
      </div>
    )
  }

  // Show layout error if we have topology data but layout failed
  if (layoutError && nodes.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <div className="text-center max-w-md">
          <p className="text-lg text-amber-400">Layout Error</p>
          <p className="text-sm mt-2">
            Failed to compute topology layout. The graph has {topology.nodes.length} nodes.
          </p>
          <p className="text-xs mt-2 text-theme-text-tertiary font-mono bg-theme-surface-secondary p-2 rounded">
            {layoutError}
          </p>
          <button
            onClick={() => {
              setLayoutError(null)
              setLayoutRetryCount(c => c + 1)
            }}
            className="mt-4 inline-flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-surface hover:bg-theme-elevated border border-theme-border rounded-lg transition-colors"
          >
            <RotateCw className="w-4 h-4" />
            Retry Layout
          </button>
        </div>
      </div>
    )
  }

  return (
    <ReactFlowProvider>
      {/* Truncation banner - shown when topology has too many nodes */}
      {topology?.truncated && (
        <div className="absolute top-2 left-2 right-2 z-10 bg-blue-500/10 border border-blue-500/30 rounded-lg p-2 backdrop-blur-sm">
          <div className="flex items-center gap-2">
            <Scissors className="w-4 h-4 text-blue-400 shrink-0" />
            <div className="text-sm">
              <span className="font-medium text-blue-400">Large cluster:</span>
              <span className="text-theme-text-secondary ml-1">
                Showing {topology.nodes.length} of {topology.totalNodes} nodes.
                Select a namespace for better performance.
              </span>
            </div>
          </div>
        </div>
      )}
      {/* Warning banner for partial topology data */}
      {topology?.warnings && topology.warnings.length > 0 && !topology.truncated && (
        <div className="absolute top-2 left-2 right-2 z-10 bg-amber-500/10 border border-amber-500/30 rounded-lg p-2 backdrop-blur-sm">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-amber-500 shrink-0 mt-0.5" />
            <div className="text-sm">
              <span className="font-medium text-amber-400">Warning:</span>
              <span className="text-theme-text-secondary ml-1">
                Some resources failed to load. Data may be incomplete.
              </span>
              <details className="mt-1">
                <summary className="text-xs text-amber-400/80 cursor-pointer hover:text-amber-400">
                  Show details ({topology.warnings.length})
                </summary>
                <ul className="mt-1 text-xs text-theme-text-tertiary space-y-0.5">
                  {topology.warnings.map((w, i) => (
                    <li key={i} className="font-mono">{w}</li>
                  ))}
                </ul>
              </details>
            </div>
          </div>
        </div>
      )}
      {/* Layout error banner - shown even when stale nodes exist */}
      {layoutError && nodes.length > 0 && (
        <div className="absolute top-2 left-2 right-2 z-10 bg-red-500/10 border border-red-500/30 rounded-lg p-2 backdrop-blur-sm">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-500 shrink-0 mt-0.5" />
            <div className="text-sm">
              <span className="font-medium text-red-400">Layout Error:</span>
              <span className="text-theme-text-secondary ml-1">
                Failed to update layout. Showing previous view.
              </span>
              <p className="mt-1 text-xs text-theme-text-tertiary font-mono">{layoutError}</p>
            </div>
          </div>
        </div>
      )}
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
          className="bg-theme-surface border border-theme-border rounded-lg"
          showInteractive={false}
        />
        <MiniMap
          className="bg-theme-surface border border-theme-border rounded-lg"
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

// Inner component to handle animated viewport transitions and zoom-based CSS variables
// Must be inside ReactFlow to use useReactFlow hook
function ViewportController({ structureKey }: { structureKey: string }) {
  const { fitView, getViewport } = useReactFlow()
  const nodes = useNodes() // Reactive hook to watch node changes
  const prevStructureKeyRef = useRef<string>('')
  const prevNodesLengthRef = useRef(0)

  // Update CSS variable for header offset based on zoom
  // This allows child nodes to move up when header shrinks (zoomed in)
  const updateZoomOffset = useCallback((viewport: Viewport) => {
    const { zoom } = viewport
    // Match the headerScale formula from GroupNode
    const headerScale = Math.max(0.35, Math.min(1, 0.5 / zoom))
    // At scale 1.0, offset is 0. At scale 0.35, offset is ~45px (header shrinks by ~45px)
    const headerOffset = (1 - headerScale) * 70
    document.documentElement.style.setProperty('--group-header-offset', `${-headerOffset}px`)
  }, [])

  // Use ReactFlow's viewport change hook instead of polling
  useOnViewportChange({
    onChange: updateZoomOffset,
  })

  // Update on initial mount
  useEffect(() => {
    updateZoomOffset(getViewport())
  }, [updateZoomOffset, getViewport])

  // Fit view when nodes become available or structure changes
  // This handles both initial mount and view switching scenarios
  useEffect(() => {
    const structureChanged = structureKey !== prevStructureKeyRef.current
    const nodesJustPopulated = prevNodesLengthRef.current === 0 && nodes.length > 0

    // Update refs
    prevNodesLengthRef.current = nodes.length
    if (structureChanged) {
      prevStructureKeyRef.current = structureKey
    }

    // Fit view when:
    // 1. Nodes just became available (were 0, now > 0) - handles initial mount/view switch
    // 2. Structure changed AND nodes already exist - handles topology changes
    if (nodesJustPopulated || (structureChanged && nodes.length > 0)) {
      // Small delay to ensure DOM is updated
      const timeoutId = setTimeout(() => {
        fitView({
          padding: 0.15,
          // No animation when nodes first appear, animate on subsequent structure changes
          duration: nodesJustPopulated ? 0 : VIEWPORT_ANIMATION_DURATION,
        })
      }, 10)

      return () => clearTimeout(timeoutId)
    }
  }, [structureKey, nodes.length, fitView])

  return null
}
