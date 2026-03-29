import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  useNodesState,
  useEdgesState,
  useReactFlow,
  useOnViewportChange,
  useNodes,
  type Node,
  type Edge,
  type NodeTypes,
  type NodeChange,
  type Viewport,
  BackgroundVariant,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { toCanvas } from 'html-to-image'

import { AlertTriangle, Download, Loader2, Pause, Play, RotateCw, Shield } from 'lucide-react'
import { useToast } from '../ui/Toast'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'

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

// Memoized edge style cache to avoid creating new objects on every render
const edgeStyleCache = new Map<string, React.CSSProperties>()

function getEdgeStyle(type: string, isTrafficView: boolean, isTrafficEdge: boolean, animated: boolean): React.CSSProperties {
  const cacheKey = `${type}-${isTrafficView}-${isTrafficEdge}-${animated}`
  let style = edgeStyleCache.get(cacheKey)
  if (!style) {
    const edgeColor = getEdgeColor(type, isTrafficView)
    style = {
      stroke: edgeColor,
      strokeWidth: isTrafficView ? 2 : 1.5,
      strokeDasharray: isTrafficView && isTrafficEdge && animated ? '5 5' : undefined,
    }
    edgeStyleCache.set(cacheKey, style)
  }
  return style
}

// Threshold for disabling edge animations (performance optimization)
const EDGE_ANIMATION_THRESHOLD = 200

// Build edges, handling collapsed groups
function buildEdges(
  topologyEdges: { id: string; source: string; target: string; type: string }[],
  collapsedGroups: Set<string>,
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  isTrafficView: boolean,
  nodeToGroup?: Map<string, string>,
  nodeCount?: number
): Edge[] {
  const edges: Edge[] = []
  const seenEdgeIds = new Set<string>() // O(1) duplicate detection

  // Disable animations for large graphs (performance optimization)
  const enableAnimations = (nodeCount ?? 0) < EDGE_ANIMATION_THRESHOLD

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
    const animated = enableAnimations && isTrafficView && isTrafficEdge

    edges.push({
      id: edgeId,
      source,
      target,
      type: 'smoothstep',
      animated,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: edgeColor,
        width: 12,
        height: 12,
      },
      style: getEdgeStyle(edge.type, isTrafficView, isTrafficEdge, animated),
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
  /** Show image export button in controls. Default: true */
  showExportButton?: boolean
  paused?: boolean
  onTogglePause?: () => void
}

export function TopologyGraph({
  topology,
  viewMode,
  groupingMode,
  hideGroupHeader = false,
  onNodeClick,
  selectedNodeId,
  showExportButton = true,
  paused = false,
  onTogglePause,
}: TopologyGraphProps) {
  const isTrafficView = viewMode === 'traffic'
  const [nodes, setNodes, onNodesChangeBase] = useNodesState([] as Node[])
  const [edges, setEdges, onEdgesChange] = useEdgesState([] as Edge[])

  // Wrap onNodesChange to track user-dragged positions so they survive re-layouts.
  const onNodesChange = useCallback((changes: NodeChange<Node>[]) => {
    onNodesChangeBase(changes)
    for (const change of changes) {
      if (change.type === 'position' && change.position) {
        savedPositionsRef.current.set(change.id, change.position)
      }
    }
  }, [onNodesChangeBase])
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())
  const [expandedPodGroups, setExpandedPodGroups] = useState<Set<string>>(new Set())
  const [layoutError, setLayoutError] = useState<string | null>(null)
  const [layoutRetryCount, setLayoutRetryCount] = useState(0)
  const [isExporting, setIsExporting] = useState(false)
  const prevStructureRef = useRef<string>('')
  const layoutVersionRef = useRef(0) // Used to invalidate stale layout results
  // Saved node positions for preservation across topology updates.
  // Prevents every cluster change from re-running a full ELK layout (which shifts all nodes).
  const savedPositionsRef = useRef<Map<string, { x: number; y: number }>>(new Map())
  const prevRetryCountRef = useRef(0)

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

    // Find all ingresses/gateways and group them
    const ingresses = nodes.filter(n => n.kind === 'Ingress' || n.kind === 'Gateway')
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
      savedPositionsRef.current.clear() // Clear on context switch / topology reset
      return
    }

    const structureChanged = structureKey !== prevStructureRef.current

    if (!structureChanged) {
      return
    }

    // Detect explicit re-layout request (Retry button) — clear saved positions so
    // ELK computes a fresh layout from scratch instead of preserving old positions.
    const isRetry = layoutRetryCount !== prevRetryCountRef.current
    if (isRetry) {
      prevRetryCountRef.current = layoutRetryCount
      savedPositionsRef.current.clear()
    }

    // For the initial layout (no saved positions yet) run ELK for all nodes.
    // For subsequent updates, preserve existing positions — only new nodes get
    // ELK-computed positions. This prevents the whole graph from shifting every
    // time the cluster adds a new resource.
    const isInitialLayout = savedPositionsRef.current.size === 0

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

      // Preserve positions for nodes that already have a saved position (i.e. were
      // present in a previous layout). New nodes use the ELK-computed position.
      // This prevents the whole graph from shifting every time the topology changes.
      // isInitialLayout is captured in the outer effect scope.
      const positionedNodes = isInitialLayout
        ? layoutedNodes
        : layoutedNodes.map(node => {
            const saved = savedPositionsRef.current.get(node.id)
            return saved ? { ...node, position: saved } : node
          })

      // Update saved positions: add/overwrite with positions from this layout run.
      // Remove stale entries for nodes no longer in the topology.
      const currentIds = new Set(positionedNodes.map(n => n.id))
      for (const id of savedPositionsRef.current.keys()) {
        if (!currentIds.has(id)) savedPositionsRef.current.delete(id)
      }
      for (const node of positionedNodes) {
        savedPositionsRef.current.set(node.id, node.position)
      }

      // Add expand/collapse handlers to pod-related nodes
      const nodesWithHandlers = positionedNodes.map(node => {
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

      // Build edges with styling (pass node count for animation threshold)
      const builtEdges = buildEdges(
        workingEdges,
        collapsedGroups,
        groupMap,
        groupingMode,
        isTrafficView,
        nodeToGroup,
        nodesWithHandlers.length
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

  // Update selected state - only update nodes that actually changed
  useEffect(() => {
    setNodes(nds => {
      let changed = false
      const updated = nds.map(node => {
        const shouldBeSelected = node.id === selectedNodeId
        const isCurrentlySelected = node.data?.selected ?? false
        if (shouldBeSelected !== isCurrentlySelected) {
          changed = true
          return {
            ...node,
            data: {
              ...node.data,
              selected: shouldBeSelected,
            },
          }
        }
        return node // Return same reference if unchanged
      })
      return changed ? updated : nds // Return same array if nothing changed
    })
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
      {/* Warning banner for partial topology data */}
      {topology?.warnings && topology.warnings.length > 0 && (() => {
        const rbacWarnings = topology.warnings.filter(w => w.includes('RBAC not granted'))
        const otherWarnings = topology.warnings.filter(w => !w.includes('RBAC not granted'))
        const isAllRbac = otherWarnings.length === 0
        return (
          <div className={`absolute top-2 left-2 right-2 z-10 ${isAllRbac ? 'bg-amber-500/10 border-amber-500/20' : 'bg-amber-500/10 border-amber-500/30'} border rounded-lg p-2 backdrop-blur-sm`}>
            <div className="flex items-start gap-2">
              {isAllRbac ? (
                <Shield className="w-4 h-4 text-amber-400 shrink-0 mt-0.5" />
              ) : (
                <AlertTriangle className="w-4 h-4 text-amber-500 shrink-0 mt-0.5" />
              )}
              <div className="text-sm">
                <span className="font-medium text-amber-400">
                  {isAllRbac ? 'Limited Access:' : 'Warning:'}
                </span>
                <span className="text-theme-text-secondary ml-1">
                  {isAllRbac
                    ? `${rbacWarnings.length} resource type${rbacWarnings.length > 1 ? 's' : ''} not accessible due to RBAC restrictions.`
                    : 'Some resources failed to load. Data may be incomplete.'}
                </span>
                <details className="mt-1">
                  <summary className="text-xs text-amber-400/80 hover:text-amber-400">
                    Show details ({topology.warnings.length})
                  </summary>
                  <ul className="mt-1 text-xs text-theme-text-tertiary space-y-0.5">
                    {rbacWarnings.length > 0 && otherWarnings.length > 0 && (
                      <li className="text-amber-400/60 font-medium mt-1">RBAC restrictions:</li>
                    )}
                    {rbacWarnings.map((w, i) => (
                      <li key={`rbac-${i}`} className="font-mono">{w}</li>
                    ))}
                    {otherWarnings.length > 0 && rbacWarnings.length > 0 && (
                      <li className="text-amber-400/60 font-medium mt-1">Other warnings:</li>
                    )}
                    {otherWarnings.map((w, i) => (
                      <li key={`other-${i}`} className="font-mono">{w}</li>
                    ))}
                  </ul>
                </details>
              </div>
            </div>
          </div>
        )
      })()}
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
        onlyRenderVisibleElements={!isExporting}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#334155" />
        <Controls
          className="bg-theme-surface border border-theme-border rounded-lg"
          showInteractive={false}
        >
          {showExportButton && <ExportImageButton onExportingChange={setIsExporting} />}
          {onTogglePause && (
            <button
              className={`react-flow__controls-button ${paused ? 'text-amber-400' : ''}`}
              onClick={onTogglePause}
              title={paused ? 'Resume live updates' : 'Pause live updates'}
            >
              {paused ? <Play className="w-3 h-3" /> : <Pause className="w-3 h-3" />}
            </button>
          )}
        </Controls>
        <ViewportController viewMode={viewMode} layoutRetryCount={layoutRetryCount} />
      </ReactFlow>
    </ReactFlowProvider>
  )
}

// Read the effective background color from the topology container
function getTopologyBgColor(): string {
  const el = document.querySelector('.react-flow')
  if (el) {
    const bg = getComputedStyle(el).backgroundColor
    if (bg && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent') return bg
  }
  return '#0f172a'
}

// Compute export dimensions for the dialog preview
function useExportDimensions(captureMode: 'viewport' | 'full', scale: number) {
  const { getNodes, getNodesBounds } = useReactFlow()
  return useMemo(() => {
    if (captureMode === 'viewport') {
      const el = document.querySelector('.react-flow') as HTMLElement
      if (!el) return null
      const { width, height } = el.getBoundingClientRect()
      const w = Math.ceil(width)
      const h = Math.ceil(height)
      return { pw: w * scale, ph: h * scale }
    }
    const nodes = getNodes()
    if (nodes.length === 0) return null
    const bounds = getNodesBounds(nodes)
    const w = Math.ceil(bounds.width + EXPORT_PADDING * 2)
    const h = Math.ceil(bounds.height + EXPORT_PADDING * 2)
    // Full capture uses pixelRatio=1, so dimensions are 1:1 with graph bounds
    return { pw: w, ph: h }
  }, [captureMode, scale, getNodes, getNodesBounds])
}

type ImageFormat = 'image/png' | 'image/webp'
const FORMAT_LABELS: Record<ImageFormat, string> = { 'image/png': 'PNG', 'image/webp': 'WebP' }
const FORMAT_EXT: Record<ImageFormat, string> = { 'image/png': 'png', 'image/webp': 'webp' }

const EXPORT_PADDING = 16
const EXPORT_TIMEOUT_MS = 30_000

function withTimeout<T>(promise: Promise<T>, ms: number, msg: string): Promise<T> {
  return Promise.race([
    promise,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error(msg)), ms)),
  ])
}

// Export topology as image button + dialog (must be inside ReactFlowProvider)
function ExportImageButton({ onExportingChange }: { onExportingChange: (v: boolean) => void }) {
  const [showDialog, setShowDialog] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [filename, setFilename] = useState('')
  const [transparent, setTransparent] = useState(false)
  const [scale, setScale] = useState(2)
  const [captureMode, setCaptureMode] = useState<'viewport' | 'full'>('full')
  const [format, setFormat] = useState<ImageFormat>('image/webp')
  const { getNodes, getNodesBounds } = useReactFlow()
  const { showError, showSuccess } = useToast()
  const inputRef = useRef<HTMLInputElement>(null)
  const dims = useExportDimensions(captureMode, scale)

  const openDialog = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    e.preventDefault()
    const nodes = getNodes()
    if (nodes.length === 0) return
    setFilename(`topology-${new Date().toISOString().slice(0, 19).replace(/:/g, '-')}`)
    setShowDialog(true)
    setTimeout(() => inputRef.current?.select(), 50)
  }, [getNodes])

  const doExport = useCallback(async () => {
    const flowEl = document.querySelector('.react-flow__viewport') as HTMLElement
    if (!flowEl) return

    const nodes = getNodes()
    if (nodes.length === 0) return

    setExporting(true)

    const isFullCapture = captureMode === 'full'
    if (isFullCapture) {
      onExportingChange(true)
      // Wait for React to render all off-screen nodes
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    }

    // Yield to let the UI paint the exporting state before heavy DOM work
    await new Promise(resolve => setTimeout(resolve, 50))

    try {
      const bgColor = transparent ? 'transparent' : getTopologyBgColor()

      let canvas: HTMLCanvasElement
      if (isFullCapture) {
        const bounds = getNodesBounds(nodes)
        const w = Math.ceil(bounds.width + EXPORT_PADDING * 2)
        const h = Math.ceil(bounds.height + EXPORT_PADDING * 2)
        const tx = -bounds.x + EXPORT_PADDING
        const ty = -bounds.y + EXPORT_PADDING
        canvas = await withTimeout(toCanvas(flowEl, {
          backgroundColor: bgColor,
          width: w,
          height: h,
          pixelRatio: 1,
          skipFonts: true,
          style: {
            width: `${w}px`,
            height: `${h}px`,
            transform: `translate(${tx}px, ${ty}px) scale(1)`,
          },
        }), EXPORT_TIMEOUT_MS, 'Export timed out — topology may be too large')
      } else {
        const flowContainer = document.querySelector('.react-flow') as HTMLElement
        if (!flowContainer) throw new Error('Topology container not found')
        const { width: vw, height: vh } = flowContainer.getBoundingClientRect()

        canvas = await withTimeout(toCanvas(flowEl, {
          backgroundColor: bgColor,
          width: Math.ceil(vw),
          height: Math.ceil(vh),
          pixelRatio: scale,
          skipFonts: true,
        }), EXPORT_TIMEOUT_MS, 'Export timed out — topology may be too large')
      }

      const ext = FORMAT_EXT[format]
      // WebP: quality 1.0 (lossless) when transparent to avoid alpha artifacts, 0.92 for opaque. PNG ignores quality.
      const quality = format === 'image/webp' ? (transparent ? 1.0 : 0.92) : undefined
      const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, format, quality))
      if (!blob) throw new Error('Failed to create image — canvas may be too large or format unsupported')

      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${filename || 'topology'}.${ext}`
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      const sizeMB = (blob.size / 1024 / 1024).toFixed(1)
      showSuccess(`Exported ${ext.toUpperCase()} (${sizeMB} MB)`)
    } catch (err) {
      console.error('Failed to export topology:', err)
      showError(`Export failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setExporting(false)
      onExportingChange(false)
      setShowDialog(false)
    }
  }, [getNodes, getNodesBounds, transparent, scale, captureMode, format, filename, showError, showSuccess, onExportingChange])

  useEffect(() => {
    if (!showDialog) return
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setShowDialog(false) }
      if (e.key === 'Enter' && !exporting) { e.stopPropagation(); doExport() }
    }
    document.addEventListener('keydown', handleKey, true)
    return () => document.removeEventListener('keydown', handleKey, true)
  }, [showDialog, exporting, doExport])

  return (
    <>
      <button
        className="react-flow__controls-button"
        onClick={openDialog}
        disabled={exporting}
        title="Export as image"
      >
        {exporting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Download className="w-3 h-3" />}
      </button>
      {showDialog && (
        <div
          className="absolute bottom-12 left-0 z-50 bg-theme-surface border border-theme-border rounded-lg shadow-2xl p-3 w-72"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="text-sm font-medium text-theme-text-primary mb-3">Export topology</div>
          <label className="block text-xs text-theme-text-secondary mb-1">Filename</label>
          <input
            ref={inputRef}
            type="text"
            value={filename}
            onChange={(e) => setFilename(e.target.value)}
            className="w-full px-2 py-1.5 text-sm bg-theme-base border border-theme-border rounded text-theme-text-primary outline-none focus:border-blue-500 mb-3"
          />
          <label className="block text-xs text-theme-text-secondary mb-1">Capture</label>
          <div className="flex gap-1 mb-3">
            {(['full', 'viewport'] as const).map(mode => (
              <button
                key={mode}
                onClick={() => setCaptureMode(mode)}
                className={`flex-1 px-2 py-1.5 text-xs rounded transition-colors ${captureMode === mode ? 'btn-brand' : 'bg-theme-base text-theme-text-secondary hover:text-theme-text-primary border border-theme-border'}`}
              >
                {mode === 'full' ? 'Entire graph' : 'Visible area'}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-3 mb-2">
            <div className="flex-1">
              <label className="block text-xs text-theme-text-secondary mb-1">Format</label>
              <div className="flex gap-1">
                {(['image/webp', 'image/png'] as ImageFormat[]).map(f => (
                  <button
                    key={f}
                    onClick={() => setFormat(f)}
                    className={`flex-1 px-2 py-1.5 text-xs rounded transition-colors ${format === f ? 'btn-brand' : 'bg-theme-base text-theme-text-secondary hover:text-theme-text-primary border border-theme-border'}`}
                  >
                    {FORMAT_LABELS[f]}
                  </button>
                ))}
              </div>
            </div>
            {captureMode === 'viewport' && (
              <div className="flex-1">
                <label className="block text-xs text-theme-text-secondary mb-1">Quality</label>
                <select
                  value={scale}
                  onChange={(e) => setScale(Number(e.target.value))}
                  className="w-full px-2 py-1.5 text-sm bg-theme-base border border-theme-border rounded text-theme-text-primary outline-none focus:border-blue-500"
                >
                  <option value={1}>Standard</option>
                  <option value={2}>High (2x)</option>
                  <option value={3}>Ultra (3x)</option>
                </select>
              </div>
            )}
          </div>
          {dims && (
            <div className="text-[10px] text-theme-text-tertiary mb-2">
              Output: {dims.pw} × {dims.ph} px
            </div>
          )}
          <div className="flex items-center mb-3">
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={transparent}
                onChange={(e) => setTransparent(e.target.checked)}
                className="rounded"
              />
              <span className="text-xs text-theme-text-secondary">Transparent background</span>
            </label>
          </div>
          <div className="flex gap-2">
            <button
              onClick={() => setShowDialog(false)}
              className="flex-1 px-3 py-1.5 text-sm text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={doExport}
              disabled={exporting}
              className="flex-1 px-3 py-1.5 text-sm font-medium btn-brand rounded flex items-center justify-center gap-1.5"
            >
              {exporting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Download className="w-3 h-3" />}
              Export
            </button>
          </div>
        </div>
      )}
      {exporting && (
        <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/40">
          <div className="bg-theme-surface border border-theme-border rounded-lg px-5 py-4 shadow-2xl">
            <div className="text-sm text-theme-text-primary animate-pulse">Exporting topology…</div>
          </div>
        </div>
      )}
    </>
  )
}

// Animation duration for viewport transitions
const VIEWPORT_ANIMATION_DURATION = 400

// Inner component to handle animated viewport transitions and zoom-based CSS variables
// Must be inside ReactFlow to use useReactFlow hook
function ViewportController({
  viewMode,
  layoutRetryCount,
}: {
  viewMode: string
  layoutRetryCount: number
}) {
  const { fitView, zoomIn, zoomOut, setViewport, getViewport } = useReactFlow()
  const nodes = useNodes() // Reactive hook to watch node changes
  const prevViewModeRef = useRef<string>(viewMode)
  const prevRetryCountRef = useRef(layoutRetryCount)
  const prevNodesLengthRef = useRef(0)

  // Topology keyboard shortcuts
  useRegisterShortcuts([
    {
      id: 'topology-fit-view',
      keys: 'f',
      description: 'Fit graph to screen',
      category: 'Topology',
      scope: 'topology',
      handler: () => fitView({ padding: 0.15, duration: VIEWPORT_ANIMATION_DURATION }),
    },
    {
      id: 'topology-zoom-in',
      keys: '+',
      description: 'Zoom in',
      category: 'Topology',
      scope: 'topology',
      handler: () => zoomIn({ duration: 200 }),
    },
    {
      id: 'topology-zoom-in-equals',
      keys: '=',
      description: 'Zoom in',
      category: 'Topology',
      scope: 'topology',
      handler: () => zoomIn({ duration: 200 }),
    },
    {
      id: 'topology-zoom-out',
      keys: '-',
      description: 'Zoom out',
      category: 'Topology',
      scope: 'topology',
      handler: () => zoomOut({ duration: 200 }),
    },
    {
      id: 'topology-reset-zoom',
      keys: '0',
      description: 'Reset zoom',
      category: 'Topology',
      scope: 'topology',
      handler: () => setViewport({ x: 0, y: 0, zoom: 1 }, { duration: 200 }),
    },
  ])

  // Update CSS variables for header offset and scale based on zoom
  // This allows child nodes to move up when header shrinks (zoomed in)
  // and allows GroupNode to use CSS var instead of useViewport() (prevents re-renders)
  const updateZoomOffset = useCallback((viewport: Viewport) => {
    const { zoom } = viewport
    // Match the headerScale formula from GroupNode
    // Min 0.5 = header never shrinks below 50%, formula 0.7/zoom = less aggressive scaling
    const headerScale = Math.max(0.5, Math.min(1, 0.7 / zoom))
    // At scale 1.0, offset is 0. At scale 0.5, offset is ~35px (header shrinks by ~35px)
    const headerOffset = (1 - headerScale) * 70
    document.documentElement.style.setProperty('--group-header-offset', `${-headerOffset}px`)
    document.documentElement.style.setProperty('--group-header-scale', String(headerScale))
  }, [])

  // Use ReactFlow's viewport change hook instead of polling
  useOnViewportChange({
    onChange: updateZoomOffset,
  })

  // Update on initial mount
  useEffect(() => {
    updateZoomOffset(getViewport())
  }, [updateZoomOffset, getViewport])

  // Fit view only on intentional changes: initial load, namespace/view switch, explicit retry.
  // Background topology updates (new pods, status changes) must NOT trigger fitView —
  // doing so would cause the viewport to zoom/pan every few seconds in active clusters.
  useEffect(() => {
    const nodesJustPopulated = prevNodesLengthRef.current === 0 && nodes.length > 0
    const viewModeChanged = viewMode !== prevViewModeRef.current
    const retryRequested = layoutRetryCount !== prevRetryCountRef.current

    prevNodesLengthRef.current = nodes.length
    prevViewModeRef.current = viewMode
    prevRetryCountRef.current = layoutRetryCount

    if (nodesJustPopulated || viewModeChanged || retryRequested) {
      const timeoutId = setTimeout(() => {
        fitView({
          padding: 0.15,
          duration: nodesJustPopulated ? 0 : VIEWPORT_ANIMATION_DURATION,
        })
      }, 10)

      return () => clearTimeout(timeoutId)
    }
  }, [viewMode, layoutRetryCount, nodes.length, fitView])

  return null
}
