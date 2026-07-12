import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Panel,
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
  type FitViewOptions,
  BackgroundVariant,
  MarkerType,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { toCanvas } from 'html-to-image'

import { AlertTriangle, ChevronsDownUp, ChevronsUpDown, Download, Info, Layers, LayoutGrid, Loader2, Maximize, Minus, Pause, Play, Plus, RotateCw, Shield } from 'lucide-react'
import { PaneLoader } from '../ui/PaneLoader'
import { Input } from '../ui/Input'
import { Tooltip } from '../ui/Tooltip'
import { useToast } from '../ui/Toast'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'

import { K8sResourceNode } from './K8sResourceNode'
import { GroupNode } from './GroupNode'
import { NEUTRAL_OWNER, type WorkloadFocus } from '../../utils/workload-colors'
import { ownershipOf } from '../../utils/topology-neighborhood'
import { buildHierarchicalElkGraph, applyHierarchicalLayout, getGroupKey, isGroupEffectivelyCollapsed, type GroupDisplayLevel } from './layout'
import type { Topology, TopologyNode, TopologyEdge, ViewMode, GroupingMode } from '../../types'
import { pluralize } from '../../utils/pluralize'
import { foldHash } from '../../utils/structure-hash'
import { recordLayoutDuration, recordLayoutSkipped, recordStructureKeyDuration } from '../../perf'

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

// Human-readable edge legend for the resources view (traffic view is all-green).
const EDGE_LEGEND: { label: string; color: string }[] = [
  { label: 'owns', color: EDGE_COLORS['manages'] },
  { label: 'exposes', color: EDGE_COLORS['exposes'] },
  { label: 'configures', color: EDGE_COLORS['configures'] },
  { label: 'scales', color: EDGE_COLORS['uses'] },
  { label: 'routes to', color: EDGE_COLORS['routes-to'] },
]

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

// Auto-collapse all namespace groups when cluster has more than this many namespaces
const LARGE_CLUSTER_NS_THRESHOLD = 5

// Build edges, handling collapsed groups
function buildEdges(
  topologyEdges: TopologyEdge[],
  collapsedGroups: Set<string>,
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  isTrafficView: boolean,
  nodeToGroup?: Map<string, string>,
  nodeCount?: number,
  groupLevels?: Map<string, GroupDisplayLevel>,
  smartDefaultActive = false,
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

    // If source is in a collapsed group, point to the group instead. Same
    // predicate as ELK node placement (isGroupEffectivelyCollapsed) so a
    // rendered edge never references a member hidden inside a chip.
    const sourceGroup = nodeGroupMap.get(source)
    if (sourceGroup && isGroupEffectivelyCollapsed(sourceGroup, collapsedGroups, groupLevels, smartDefaultActive)) {
      source = sourceGroup
    }

    // If target is in a collapsed group, point to the group instead
    const targetGroup = nodeGroupMap.get(target)
    if (targetGroup && isGroupEffectivelyCollapsed(targetGroup, collapsedGroups, groupLevels, smartDefaultActive)) {
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
  /** Called when user clicks "maximize" on a namespace group — sets namespace filter to just that namespace */
  onMaximizeNamespace?: (namespace: string) => void
  /** Shown as a breadcrumb label when viewing a single namespace */
  namespaceBreadcrumb?: string
  /** Called when breadcrumb "back" is clicked to return to all-namespace view */
  onClearNamespace?: () => void
  /** Serialized namespace filter — when this changes, reset groupLevels for fresh smart default */
  namespacesKey?: string
  /** Node to pan/zoom the canvas to. Bump focusNonce to re-trigger for the same id. */
  focusNodeId?: string
  /** Increment to request a focus on focusNodeId (lets the same node be re-focused). */
  focusNonce?: number
  /** Application graph hover-focus (see WorkloadFocus): when set, nodes outside
   *  the focused workload's neighborhood dim. Cheap node-data toggle — never
   *  re-layouts. */
  focusedOwnerId?: WorkloadFocus
  /** Hover a node → reports its TopologyNode (null on leave). Drives the rail's
   *  reciprocal highlight. */
  onNodeHover?: (node: TopologyNode | null) => void
  /** Toggle a stable Deployment's normally-collapsed ReplicaSet tier. */
  onToggleReplicaSets?: (ownerID: string) => void
  /** Padding reserved around fit-to-view operations for overlaid UI. */
  fitViewPadding?: FitViewOptions['padding']
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
  onMaximizeNamespace,
  namespaceBreadcrumb,
  onClearNamespace,
  namespacesKey = '',
  focusNodeId,
  focusNonce,
  focusedOwnerId,
  onNodeHover,
  onToggleReplicaSets,
  fitViewPadding = 0.15,
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
  // 3-level display: chip (compact) → cardGrid (workload cards) → topology (full graph)
  const [groupLevels, setGroupLevels] = useState<Map<string, GroupDisplayLevel>>(new Map())
  const [expandedPodGroups, setExpandedPodGroups] = useState<Set<string>>(new Set())

  // Derive collapsedGroups for ELK/edges: both 'chip' and 'cardGrid' are collapsed
  const collapsedGroups = useMemo(() => {
    const set = new Set<string>()
    for (const [id, level] of groupLevels) {
      if (level !== 'topology') set.add(id)
    }
    return set
  }, [groupLevels])
  const [layoutError, setLayoutError] = useState<string | null>(null)
  const [layoutRetryCount, setLayoutRetryCount] = useState(0)
  const [fitViewCounter, setFitViewCounter] = useState(0)
  const [isExporting, setIsExporting] = useState(false)
  const [showLegend, setShowLegend] = useState(false)
  const prevStructureRef = useRef<string>('')
  const layoutVersionRef = useRef(0) // Used to invalidate stale layout results
  // Saved node positions for preservation across topology updates.
  // Prevents every cluster change from re-running a full ELK layout (which shifts all nodes).
  const savedPositionsRef = useRef<Map<string, { x: number; y: number }>>(new Map())
  const prevRetryCountRef = useRef(0)
  // Stores the current groupMap so collapse-all can reference group IDs without a stale closure
  const groupMapRef = useRef<Map<string, string[]>>(new Map())
  // Tracks whether the one-time smart default (collapse all for large clusters) has fired
  const hasAppliedSmartDefaultRef = useRef(false)
  // After layout completes for a single-group change, stores the group ID so
  // ViewportController can fitView to it (with correct timing — after setNodes)
  const fitToGroupAfterLayoutRef = useRef<string | null>(null)
  // Set by the bulk level controls (collapse/cards/expand all); fits the whole
  // graph once the relayout lands. A flag (not a counter) so the fit is keyed
  // to the post-relayout nodes update, not the click — the click fires before
  // the async ELK relayout, which would frame the pre-change layout.
  const fitAllAfterLayoutRef = useRef(false)

  // Reset group display levels when namespace filter changes (instant switching)
  const prevNamespacesKeyRef = useRef(namespacesKey)
  useEffect(() => {
    if (namespacesKey !== prevNamespacesKeyRef.current) {
      prevNamespacesKeyRef.current = namespacesKey
      setGroupLevels(new Map())
      hasAppliedSmartDefaultRef.current = false
      savedPositionsRef.current.clear()
      setFitViewCounter(c => c + 1)
    }
  }, [namespacesKey])

  // Changing grouping (By Namespace / By App / No Grouping) reorganizes the
  // whole graph, so re-frame it. Skip when a namespace change drove it — that
  // path already fits via the effect above (avoids a double fit).
  const prevGroupingModeRef = useRef(groupingMode)
  const prevNsKeyForGroupingRef = useRef(namespacesKey)
  useEffect(() => {
    const groupingChanged = groupingMode !== prevGroupingModeRef.current
    const nsChanged = namespacesKey !== prevNsKeyForGroupingRef.current
    prevGroupingModeRef.current = groupingMode
    prevNsKeyForGroupingRef.current = namespacesKey
    if (groupingChanged && !nsChanged) {
      fitAllAfterLayoutRef.current = true
    }
  }, [groupingMode, namespacesKey])

  // Set display level for a single group
  const handleSetLevel = useCallback((groupId: string, level: GroupDisplayLevel) => {
    setGroupLevels(prev => {
      const next = new Map(prev)
      next.set(groupId, level)
      return next
    })
    savedPositionsRef.current.clear()
    // After layout completes, ViewportController will fitView to this group
    fitToGroupAfterLayoutRef.current = groupId
  }, [])

  // Set all groups to a given level
  const setAllLevels = useCallback((level: GroupDisplayLevel) => {
    const next = new Map<string, GroupDisplayLevel>()
    for (const groupKey of groupMapRef.current.keys()) {
      next.set(`group-${groupingMode}-${groupKey}`, level)
    }
    setGroupLevels(next)
    savedPositionsRef.current.clear()
    // Re-frame the whole graph after the relayout — collapse/expand changes the
    // content bounds enough that the old viewport no longer fits it.
    fitAllAfterLayoutRef.current = true
  }, [groupingMode])

  // Expand pod group to show individual pods. Clear saved positions so ELK
  // re-lays out the whole graph from scratch — otherwise existing nodes snap
  // back to their saved spots while the newly-added pods get fresh ELK
  // coordinates, and the two coordinate spaces collide (overlapping nodes).
  // Re-fit afterwards since the expanded pods enlarge the content bounds.
  const handleExpandPodGroup = useCallback((podGroupId: string) => {
    setExpandedPodGroups(prev => new Set(prev).add(podGroupId))
    savedPositionsRef.current.clear()
    fitAllAfterLayoutRef.current = true
  }, [])

  // Collapse pod group back — same full relayout + re-fit (the graph shrinks).
  const handleCollapsePodGroup = useCallback((podGroupId: string) => {
    setExpandedPodGroups(prev => {
      const next = new Set(prev)
      next.delete(podGroupId)
      return next
    })
    savedPositionsRef.current.clear()
    fitAllAfterLayoutRef.current = true
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
          ...podGroupNode.data,
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

  // Handle card click in card-grid view — find the topology node and open drawer
  const handleCardClick = useCallback((nodeId: string) => {
    const topoNode = topology?.nodes.find(n => n.id === nodeId) || workingNodes.find(n => n.id === nodeId)
    if (topoNode) onNodeClick(topoNode)
  }, [topology, workingNodes, onNodeClick])

  // Expand the group containing a searched-but-collapsed node, then fit the
  // viewport to that group once the relayout lands. We fit to the GROUP (via
  // the existing fitToGroupAfterLayoutRef path) rather than centering on the
  // node directly: the node's absolute position isn't settled until ELK
  // finishes the async relayout, so a per-node center races it and lands on
  // stale coords. The node still carries data.selected, so it glows inside the
  // framed group.
  const expandGroupForNode = useCallback((nodeId: string) => {
    if (groupingMode === 'none') return
    const target = workingNodes.find(n => n.id === nodeId)
    if (!target) return
    const groupKey = getGroupKey(target, groupingMode)
    if (!groupKey) return
    const groupId = `group-${groupingMode}-${groupKey}`
    fitToGroupAfterLayoutRef.current = groupId
    savedPositionsRef.current.clear()
    setGroupLevels(prev => {
      if (prev.get(groupId) === 'topology') return prev
      const next = new Map(prev)
      next.set(groupId, 'topology')
      return next
    })
  }, [groupingMode, workingNodes])

  // Structure key for change detection — includes groupLevels so chip↔cardGrid triggers relayout.
  //
  // Uses an order-independent fold of per-ID hashes (see foldHash) instead of
  // sort+join. At thousands of nodes the join allocated tens of KB of string
  // every render (and the sort dominated for short ID arrays); the fold is
  // O(n) with constant memory and detects the same structural changes
  // (add/remove/rename) — combined with the element count in the key. Pure
  // reorders no longer trigger a layout, which is correct: ELK relayouts on
  // reorder were wasted work.
  const structureKey = useMemo(() => {
    const t0 = performance.now()
    const nodeHash = foldHash(workingNodes, n => n.id)
    const edgeHash = foldHash(workingEdges, e => `${e.source}->${e.target}:${e.type}`)
    const levelsHash = foldHash(Array.from(groupLevels.entries()), ([k, v]) => `${k}:${v}`)
    const expandedHash = foldHash(Array.from(expandedPodGroups), s => s)
    const key =
      `${viewMode}|${groupingMode}|${layoutRetryCount}` +
      `|n${workingNodes.length}:${nodeHash}` +
      `|e${workingEdges.length}:${edgeHash}` +
      `|l${groupLevels.size}:${levelsHash}` +
      `|x${expandedPodGroups.size}:${expandedHash}`
    recordStructureKeyDuration((performance.now() - t0) * 1000)
    return key
  }, [viewMode, workingNodes, workingEdges, groupLevels, expandedPodGroups, groupingMode, layoutRetryCount])

  // Layout when structure changes - use hierarchical ELK layout
  useEffect(() => {
    if (workingNodes.length === 0) {
      setNodes([])
      setEdges([])
      prevStructureRef.current = ''
      savedPositionsRef.current.clear() // Clear on context switch / topology reset
      hasAppliedSmartDefaultRef.current = false
      return
    }

    const structureChanged = structureKey !== prevStructureRef.current

    if (!structureChanged) {
      recordLayoutSkipped()
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

    // Smart default: start all namespace groups as chips for large clusters.
    // Fires once per topology lifecycle (reset on context switch). Returns early
    // so the re-render with groupLevels set computes the actual layout.
    // Fleet mode: skip collapse — CAPI resources are already filtered, always show expanded
    if (!hasAppliedSmartDefaultRef.current && groupLevels.size === 0 && groupingMode === 'namespace' && !hideGroupHeader && viewMode !== 'fleet') {
      const uniqueNamespaces = new Set(workingNodes.map(n => n.data.namespace as string).filter(Boolean))
      if (uniqueNamespaces.size > LARGE_CLUSTER_NS_THRESHOLD) {
        hasAppliedSmartDefaultRef.current = true
        savedPositionsRef.current.clear()
        const levels = new Map<string, GroupDisplayLevel>()
        for (const ns of uniqueNamespaces) levels.set(`group-namespace-${ns}`, 'chip')
        setGroupLevels(levels)
        return
      }
      // Don't mark as applied when skipping — topology data may be stale (e.g., still
      // showing single-namespace data during a transition to all-namespaces). The real
      // all-namespace data will arrive and re-trigger this check.
    }

    // Increment version to invalidate any previous in-flight layout
    const thisLayoutVersion = ++layoutVersionRef.current

    // The smart-default chip pass only ever materializes namespace groups, so its
    // "no-entry group defaults to collapsed" semantics apply only in namespace
    // mode. Outside it (small clusters, app grouping) a no-entry group stays
    // expanded — see isGroupEffectivelyCollapsed.
    const smartDefaultActive = hasAppliedSmartDefaultRef.current && groupingMode === 'namespace'

    // Build hierarchical ELK graph
    const { elkGraph, groupMap, nodeToGroup } = buildHierarchicalElkGraph(
      workingNodes,
      workingEdges,
      groupingMode,
      collapsedGroups,
      groupLevels,
      smartDefaultActive
    )
    groupMapRef.current = groupMap

    // Apply layout and get positioned nodes
    const layoutStartMs = performance.now()
    applyHierarchicalLayout(
      elkGraph,
      workingNodes,
      workingEdges,
      groupMap,
      groupingMode,
      collapsedGroups,
      { onSetLevel: handleSetLevel, onCardClick: handleCardClick, onMaximizeNamespace },
      hideGroupHeader,
      groupLevels
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
      recordLayoutDuration(performance.now() - layoutStartMs, workingNodes.length, workingEdges.length)

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

      // The ReactFlow fitView prop fires against the pre-layout canvas; once
      // the first ELK layout lands the content can sit off-center. Re-frame it.
      if (isInitialLayout) {
        fitAllAfterLayoutRef.current = true
      }

      // Update saved positions: add/overwrite with positions from this layout run.
      // Remove stale entries for nodes no longer in the topology.
      const currentIds = new Set(positionedNodes.map(n => n.id))
      for (const id of savedPositionsRef.current.keys()) {
        if (!currentIds.has(id)) savedPositionsRef.current.delete(id)
      }
      for (const node of positionedNodes) {
        savedPositionsRef.current.set(node.id, node.position)
      }

      // Add expand/collapse handlers to pod-related nodes. Only PodGroups that
      // actually carry a per-pod array are expandable — summary-only orphan
      // nodes (summary mode) hold counts only, so they get no expand affordance.
      const nodesWithHandlers = positionedNodes.map(node => {
        const isPodGroup = node.data?.kind === 'PodGroup'
        const nodeData = node.data?.nodeData as Record<string, unknown> | undefined
        // The per-pod array lives on the backend node data (nodeData.pods).
        // Summary-only orphan nodes omit it, so they get no expand affordance.
        const podsArray = nodeData?.pods
        const isExpandablePodGroup = isPodGroup && Array.isArray(podsArray) && podsArray.length > 0
        const expandedFromGroup = nodeData?.expandedFromGroup as string | undefined

        return {
          ...node,
          data: {
            ...node.data,
            onExpand: isExpandablePodGroup ? handleExpandPodGroup : undefined,
            onCollapse: expandedFromGroup ? handleCollapsePodGroup : undefined,
            isExpanded: isExpandablePodGroup ? expandedPodGroups.has(node.id) : undefined,
            onToggleReplicaSets: nodeData?.replicaSetsExpandable ? onToggleReplicaSets : undefined,
          },
        }
      })

      setNodes(nodesWithHandlers)

      // Hide edges when all groups are collapsed — inter-namespace edges are noise in the overview.
      // Always show edges in single-namespace view (hideGroupHeader) since there's no group container.
      const hasAnyExpandedGroup = hideGroupHeader || nodesWithHandlers.some(n =>
        n.type === 'group' && (n.data as Record<string, unknown>)?.displayLevel === 'topology'
      )
      if (!hasAnyExpandedGroup && groupingMode !== 'none') {
        setEdges([])
      } else {
        const builtEdges = buildEdges(
          workingEdges,
          collapsedGroups,
          groupMap,
          groupingMode,
          isTrafficView,
          nodeToGroup,
          nodesWithHandlers.length,
          groupLevels,
          smartDefaultActive
        )
        setEdges(builtEdges)
      }
    }).catch((err) => {
      console.error('[TopologyGraph] Layout post-processing error:', err)
      setLayoutError(err instanceof Error ? err.message : String(err))
    })

    // No cleanup function - we use version-based invalidation instead
    // This prevents React's effect re-runs from canceling in-flight layouts
    // when the actual structure hasn't changed
  }, [workingNodes, workingEdges, structureKey, groupingMode, hideGroupHeader, collapsedGroups, groupLevels, handleSetLevel, handleCardClick, onMaximizeNamespace, isTrafficView, expandedPodGroups, handleExpandPodGroup, handleCollapsePodGroup, onToggleReplicaSets, setNodes, setEdges, layoutRetryCount])

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

  const handleNodeMouseEnter = useCallback(
    (_e: React.MouseEvent, node: Node) => {
      if (!onNodeHover || node.type === 'group') return
      const topologyNode =
        topology?.nodes.find(n => n.id === node.id) ?? workingNodes.find(n => n.id === node.id)
      if (topologyNode) onNodeHover(topologyNode)
    },
    [topology, workingNodes, onNodeHover]
  )
  const handleNodeMouseLeave = useCallback(() => onNodeHover?.(null), [onNodeHover])

  // Update selected state - only update nodes that actually changed
  useEffect(() => {
    setNodes(nds => {
      let changed = false
      const updated = nds.map(node => {
        const shouldBeSelected = node.id === selectedNodeId
        const isCurrentlySelected = node.data?.selected ?? false
        // Only act on the select/deselect transition. Don't touch zIndex
        // otherwise — a blanket compare would fight the layout's group zIndex
        // (-1) every render and loop (React #185). Groups are never selectable,
        // so they never enter here and keep their layout zIndex.
        if (shouldBeSelected !== isCurrentlySelected) {
          changed = true
          return {
            ...node,
            // Lift the selected leaf above its siblings (default z 0) so its
            // outline+glow isn't painted over; restore default on deselect.
            zIndex: shouldBeSelected ? 10 : undefined,
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
    // `nodes` is a dep so selection re-applies after a relayout introduces the
    // target node (e.g. search expands a collapsed group). Safe from loops: the
    // functional update returns the same array ref when nothing changed.
  }, [selectedNodeId, setNodes, nodes])

  // Hover-focus dim (application graph): when a workload is focused, dim every
  // resource node not owned by it. A pure data toggle on the existing nodes —
  // positions are untouched, so it never re-runs the (expensive) ELK layout.
  useEffect(() => {
    setNodes(nds => {
      let changed = false
      const updated = nds.map(node => {
        if (node.type === 'group') return node
        const stamp = ownershipOf(node.data?.nodeData as Record<string, unknown> | undefined)
        // A focused workload lights its whole neighborhood (focusWorkloadIds);
        // the "Shared / unscoped" focus lights every neutral node.
        const inFocus =
          focusedOwnerId == null
            ? true
            : focusedOwnerId === NEUTRAL_OWNER
              ? stamp.ownerWorkloadId == null
              : stamp.focusWorkloadIds.includes(focusedOwnerId)
        const shouldDim = focusedOwnerId != null && !inFocus
        if (!!node.data?.dimmed === shouldDim) return node
        changed = true
        return { ...node, data: { ...node.data, dimmed: shouldDim } }
      })
      return changed ? updated : nds
    })
  }, [focusedOwnerId, setNodes, nodes])

  // Data-only topology updates must repaint existing nodes without forcing an
  // ELK relayout. Structure hashing deliberately ignores status and metadata;
  // keep the React Flow node payload in sync on that fast path.
  useEffect(() => {
    const topologyById = new Map(workingNodes.map(node => [node.id, node]))
    setNodes(nds => {
      let changed = false
      const updated = nds.map(node => {
        if (node.type === 'group') return node
        const topologyNode = topologyById.get(node.id)
        if (!topologyNode) return node
        const nodeData = topologyNode.data as Record<string, unknown>
        const pods = nodeData.pods
        const isExpandablePodGroup = topologyNode.kind === 'PodGroup' && Array.isArray(pods) && pods.length > 0
        const expandedFromGroup = nodeData.expandedFromGroup as string | undefined
        const onExpand = isExpandablePodGroup ? handleExpandPodGroup : undefined
        const onCollapse = expandedFromGroup ? handleCollapsePodGroup : undefined
        const isExpanded = isExpandablePodGroup ? expandedPodGroups.has(node.id) : undefined
        const toggleReplicaSets = nodeData.replicaSetsExpandable ? onToggleReplicaSets : undefined
        if (
          node.data?.kind === topologyNode.kind &&
          node.data?.name === topologyNode.name &&
          node.data?.status === topologyNode.status &&
          node.data?.nodeData === topologyNode.data &&
          node.data?.onExpand === onExpand &&
          node.data?.onCollapse === onCollapse &&
          node.data?.isExpanded === isExpanded &&
          node.data?.onToggleReplicaSets === toggleReplicaSets
        ) return node
        changed = true
        return {
          ...node,
          data: {
            ...node.data,
            kind: topologyNode.kind,
            name: topologyNode.name,
            status: topologyNode.status,
            nodeData: topologyNode.data,
            onExpand,
            onCollapse,
            isExpanded,
            onToggleReplicaSets: toggleReplicaSets,
          },
        }
      })
      return changed ? updated : nds
    })
  }, [workingNodes, expandedPodGroups, handleExpandPodGroup, handleCollapsePodGroup, onToggleReplicaSets, setNodes])

  if (!topology) {
    return <PaneLoader label="Loading topology…" className="absolute inset-0" />
  }

  if (topology.nodes.length === 0) {
    return (
      <div className="absolute inset-0 flex items-center justify-center text-theme-text-secondary">
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
      <div className="absolute inset-0 flex items-center justify-center text-theme-text-secondary">
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
      {/* Namespace breadcrumb — shown when viewing a single namespace */}
      {namespaceBreadcrumb && (
        <div className="absolute top-3 left-3 z-10 flex items-center gap-1.5">
          {onClearNamespace && (
            <button
              onClick={onClearNamespace}
              className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            >
              All Namespaces
            </button>
          )}
          {onClearNamespace && (
            <span className="text-xs text-theme-text-tertiary">/</span>
          )}
          <span className="text-xs font-medium text-theme-text-secondary bg-theme-surface/80 backdrop-blur-sm border border-theme-border/50 rounded-md px-2 py-0.5">
            {namespaceBreadcrumb}
          </span>
        </div>
      )}
      {/* Warning banner for partial topology data */}
      {topology?.warnings && topology.warnings.length > 0 && (() => {
        const rbacWarnings = topology.warnings.filter(w => w.includes('RBAC not granted'))
        const viewWarnings = topology.warnings.filter(w => w.startsWith('Topology view:'))
        const otherWarnings = topology.warnings.filter(w => !w.includes('RBAC not granted') && !w.startsWith('Topology view:'))
        const isAllRbac = otherWarnings.length === 0 && viewWarnings.length === 0
        const isViewLimited = otherWarnings.length === 0 && rbacWarnings.length === 0 && viewWarnings.length > 0
        return (
          <div className={`absolute top-2 z-10 ${isViewLimited ? 'left-2 max-w-xl' : 'left-2 right-2'} ${isAllRbac ? 'bg-amber-500/10 border-amber-500/20' : 'bg-amber-500/10 border-amber-500/30'} border rounded-lg p-2 backdrop-blur-sm`}>
            <div className="flex items-start gap-2">
              {isAllRbac ? (
                <Shield className="w-4 h-4 text-amber-400 shrink-0 mt-0.5" />
              ) : (
                <AlertTriangle className="w-4 h-4 text-amber-500 shrink-0 mt-0.5" />
              )}
              <div className="text-sm">
                <span className="font-medium text-amber-400">
                  {isAllRbac ? 'Limited Access:' : isViewLimited ? 'View limited:' : 'Warning:'}
                </span>
                <span className="text-theme-text-secondary ml-1">
                  {isAllRbac
                    ? `${pluralize(rbacWarnings.length, 'resource type')} not accessible due to RBAC restrictions.`
                    : isViewLimited
                      ? 'Large retained run history is summarized to keep the graph readable.'
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
                    {viewWarnings.length > 0 && (rbacWarnings.length > 0 || otherWarnings.length > 0) && (
                      <li className="text-amber-400/60 font-medium mt-1">View limits:</li>
                    )}
                    {viewWarnings.map((w, i) => (
                      <li key={`view-${i}`} className="font-mono">{w}</li>
                    ))}
                    {otherWarnings.length > 0 && (rbacWarnings.length > 0 || viewWarnings.length > 0) && (
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
      {/* Summary-mode pill — pod tier collapsed to per-workload/service counts */}
      {topology?.summaryMode && (
        <div className="absolute bottom-3 left-1/2 -translate-x-1/2 z-10 flex items-center gap-1.5 bg-blue-500/10 border border-blue-500/30 rounded-full px-3 py-1 backdrop-blur-sm">
          <Layers className="w-3.5 h-3.5 text-blue-400 shrink-0" />
          <span className="text-xs text-theme-text-secondary">
            Summary view — pods collapsed to counts. Filter to a smaller namespace to see individual pods.
          </span>
        </div>
      )}
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={handleNodeClick}
        onNodeMouseEnter={handleNodeMouseEnter}
        onNodeMouseLeave={handleNodeMouseLeave}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: fitViewPadding }}
        minZoom={0.1}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
        onlyRenderVisibleElements={!isExporting}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#334155" />
        {/* Bottom-left controls. Two distinct pills with a gap rather than one
            long strip: a viewport group (zoom/fit/export/pause) and, only when
            grouping is active, a level-of-detail group. */}
        <Panel position="bottom-left" className="flex flex-col items-start gap-2">
          {groupingMode !== 'none' && (
            <div className="react-flow__controls overflow-hidden" style={{ position: 'static', margin: 0 }}>
              {!hideGroupHeader && (
                <Tooltip content="Collapse all groups" delay={100} position="right">
                  <button
                    className="react-flow__controls-button"
                    onClick={() => setAllLevels('chip')}
                  >
                    <ChevronsDownUp className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              )}
              <Tooltip content="All workload cards" delay={100} position="right">
                <button
                  className="react-flow__controls-button"
                  onClick={() => setAllLevels('cardGrid')}
                >
                  <LayoutGrid className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              <Tooltip content="Expand all groups" delay={100} position="right">
                <button
                  className="react-flow__controls-button"
                  onClick={() => setAllLevels('topology')}
                >
                  <ChevronsUpDown className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
            </div>
          )}
          <div className="react-flow__controls overflow-hidden" style={{ position: 'static', margin: 0 }}>
            <CustomControlButtons
              showExportButton={showExportButton}
              paused={paused}
              onTogglePause={onTogglePause}
              onExportingChange={setIsExporting}
              fitViewPadding={fitViewPadding}
            />
          </div>
          {!isTrafficView && (
            <>
              {showLegend && (
                <div className="rounded-md border border-theme-border bg-theme-surface/95 px-3 py-2 shadow-theme-md backdrop-blur">
                  <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Edge colors</div>
                  <div className="flex flex-col gap-1">
                    {EDGE_LEGEND.map((e) => (
                      <div key={e.label} className="flex items-center gap-2 text-[11px] text-theme-text-secondary">
                        <span className="inline-block h-0.5 w-5 rounded-full" style={{ background: e.color }} />
                        {e.label}
                      </div>
                    ))}
                  </div>
                </div>
              )}
              <div className="react-flow__controls overflow-hidden" style={{ position: 'static', margin: 0 }}>
                <Tooltip content="Edge color legend" delay={100} position="right">
                  <button className="react-flow__controls-button" onClick={() => setShowLegend((v) => !v)}>
                    <Info className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              </div>
            </>
          )}
        </Panel>
        <ViewportController
          viewMode={viewMode}
          layoutRetryCount={layoutRetryCount}
          fitViewCounter={fitViewCounter}
          fitToGroupAfterLayoutRef={fitToGroupAfterLayoutRef}
          fitAllAfterLayoutRef={fitAllAfterLayoutRef}
          focusNodeId={focusNodeId}
          focusNonce={focusNonce}
          onRequestExpandForNode={expandGroupForNode}
          fitViewPadding={fitViewPadding}
        />
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
      <Tooltip content="Export as image" delay={100} position="right">
        <button
          className="react-flow__controls-button"
          onClick={openDialog}
          disabled={exporting}
        >
          {exporting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Download className="w-3 h-3" />}
        </button>
      </Tooltip>
      {showDialog && (
        <div
          className="absolute bottom-12 left-0 z-50 bg-theme-surface border border-theme-border rounded-lg shadow-2xl p-3 w-72"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="text-sm font-medium text-theme-text-primary mb-3">Export topology</div>
          <label className="block text-xs text-theme-text-secondary mb-1">Filename</label>
          <Input
            ref={inputRef}
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

// Custom control buttons — replaces ReactFlow defaults so we can use our Tooltip component
function CustomControlButtons({
  showExportButton,
  paused,
  onTogglePause,
  onExportingChange,
  fitViewPadding,
}: {
  showExportButton: boolean
  paused: boolean
  onTogglePause?: () => void
  onExportingChange: (v: boolean) => void
  fitViewPadding: FitViewOptions['padding']
}) {
  const { zoomIn, zoomOut, fitView } = useReactFlow()
  const TIP = 100
  return (
    <>
      <Tooltip content="Zoom in" delay={TIP} position="right">
        <button className="react-flow__controls-button" onClick={() => zoomIn({ duration: 200 })}>
          <Plus className="w-3 h-3" />
        </button>
      </Tooltip>
      <Tooltip content="Zoom out" delay={TIP} position="right">
        <button className="react-flow__controls-button" onClick={() => zoomOut({ duration: 200 })}>
          <Minus className="w-3 h-3" />
        </button>
      </Tooltip>
      <Tooltip content="Fit view" delay={TIP} position="right">
        <button className="react-flow__controls-button" onClick={() => fitView({ padding: fitViewPadding, duration: 400 })}>
          <Maximize className="w-3 h-3" />
        </button>
      </Tooltip>
      {showExportButton && <ExportImageButton onExportingChange={onExportingChange} />}
      {onTogglePause && (
        <Tooltip content={paused ? 'Resume live updates' : 'Pause live updates'} delay={TIP} position="right">
          <button
            className={`react-flow__controls-button ${paused ? 'text-amber-400' : ''}`}
            onClick={onTogglePause}
          >
            {paused ? <Play className="w-3 h-3" /> : <Pause className="w-3 h-3" />}
          </button>
        </Tooltip>
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
  fitViewCounter = 0,
  fitToGroupAfterLayoutRef,
  fitAllAfterLayoutRef,
  focusNodeId,
  focusNonce = 0,
  onRequestExpandForNode,
  fitViewPadding,
}: {
  viewMode: string
  layoutRetryCount: number
  fitViewCounter?: number
  fitToGroupAfterLayoutRef?: React.MutableRefObject<string | null>
  fitAllAfterLayoutRef?: React.MutableRefObject<boolean>
  focusNodeId?: string
  focusNonce?: number
  onRequestExpandForNode?: (nodeId: string) => void
  fitViewPadding: FitViewOptions['padding']
}) {
  const { fitView, zoomIn, zoomOut, setViewport, getViewport, getInternalNode, setCenter } = useReactFlow()
  const nodes = useNodes() // Reactive hook to watch node changes

  // Pan/zoom the viewport so a single node is centered.
  const centerOnNode = useCallback((nodeId: string): boolean => {
    const node = getInternalNode(nodeId)
    if (!node) return false
    const { x, y } = node.internals.positionAbsolute
    const w = node.measured?.width ?? 0
    const h = node.measured?.height ?? 0
    setCenter(x + w / 2, y + h / 2, { zoom: 1.2, duration: VIEWPORT_ANIMATION_DURATION })
    return true
  }, [getInternalNode, setCenter])
  const prevViewModeRef = useRef<string>(viewMode)
  const prevRetryCountRef = useRef(layoutRetryCount)
  const prevFitViewCounterRef = useRef(fitViewCounter)
  const prevFocusNonceRef = useRef(focusNonce)
  const prevNodesLengthRef = useRef(0)

  // Topology keyboard shortcuts
  useRegisterShortcuts([
    {
      id: 'topology-fit-view',
      keys: 'f',
      description: 'Fit graph to screen',
      category: 'Topology',
      scope: 'topology',
      handler: () => fitView({ padding: fitViewPadding, duration: VIEWPORT_ANIMATION_DURATION }),
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
    const fitViewRequested = fitViewCounter !== prevFitViewCounterRef.current

    prevNodesLengthRef.current = nodes.length
    prevViewModeRef.current = viewMode
    prevRetryCountRef.current = layoutRetryCount
    prevFitViewCounterRef.current = fitViewCounter

    if (nodesJustPopulated || viewModeChanged || retryRequested || fitViewRequested) {
      const timeoutId = setTimeout(() => {
        fitView({
          padding: fitViewPadding,
          duration: nodesJustPopulated ? 0 : VIEWPORT_ANIMATION_DURATION,
        })
      }, 10)

      return () => clearTimeout(timeoutId)
    }
  }, [viewMode, layoutRetryCount, fitViewCounter, nodes.length, fitView, fitViewPadding])

  // Pan/zoom to a single searched node. Gated on focusNonce so the same
  // node can be re-focused, and so this never fires on background updates.
  // If the node is already on the canvas, center now. If it isn't (it's
  // collapsed inside a group chip), ask the parent to expand that group
  // (onRequestExpandForNode); the fit-to-group effect then frames the group
  // once the relayout lands, and the node glows inside it via data.selected.
  useEffect(() => {
    if (focusNonce === prevFocusNonceRef.current) return
    prevFocusNonceRef.current = focusNonce
    if (!focusNodeId) return
    if (!centerOnNode(focusNodeId)) {
      onRequestExpandForNode?.(focusNodeId)
    }
  }, [focusNonce, focusNodeId, centerOnNode, onRequestExpandForNode])

  // After a single-group expand/collapse, fit the viewport to that group, once
  // the relayout has SETTLED. Debounced (reschedules on each nodes update) so
  // it frames the final positions, not an intermediate layout — and so the
  // group's nodes are measured when fitView reads their bounds.
  useEffect(() => {
    if (!fitToGroupAfterLayoutRef?.current) return
    const targetGroupId = fitToGroupAfterLayoutRef.current
    const id = setTimeout(() => {
      fitToGroupAfterLayoutRef.current = null
      const targetNodes = nodes.filter(n => n.id === targetGroupId || n.parentId === targetGroupId)
      if (targetNodes.length > 0) {
        fitView({
          nodes: targetNodes.map(n => ({ id: n.id })),
          padding: 0.2,
          duration: VIEWPORT_ANIMATION_DURATION,
          maxZoom: 1.5,
        })
      }
    }, 250)
    return () => clearTimeout(id)
  }, [nodes, fitView, fitToGroupAfterLayoutRef])

  // After a bulk level change (collapse/cards/expand all), fit the whole graph
  // once the relayout has SETTLED. The expand relayout lands in phases, so a
  // fit on the first nodes update frames an intermediate (compact) layout.
  // Debounce instead: each nodes update reschedules, so the fit fires only
  // after nodes stop changing, then clears the flag.
  useEffect(() => {
    if (!fitAllAfterLayoutRef?.current) return
    const id = setTimeout(() => {
      fitAllAfterLayoutRef.current = false
      fitView({ padding: fitViewPadding, duration: VIEWPORT_ANIMATION_DURATION })
    }, 250)
    return () => clearTimeout(id)
  }, [nodes, fitView, fitAllAfterLayoutRef, fitViewPadding])

  return null
}
