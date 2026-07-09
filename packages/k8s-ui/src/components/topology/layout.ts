import type { Node } from '@xyflow/react'
import type { TopologyNode, GroupingMode, NodeKind, HealthStatus } from '../../types'
import { NODE_DIMENSIONS } from './K8sResourceNode'

// --- 3-level display system types ---
export type GroupDisplayLevel = 'chip' | 'cardGrid' | 'topology'

export interface WorkloadCard {
  id: string             // Node ID of primary resource (for click handling)
  kind: NodeKind         // Kind of primary resource (Deployment, StatefulSet, etc.)
  name: string           // Name of primary resource
  status: HealthStatus   // Worst health status in the connected subgraph
  subtitle: string       // Status info (e.g., "3/3 ready", "ClusterIP", "*/5 * * * *")
  resourceCount: number  // Total connected resources in this workload
}

// Card grid layout constants — must match CSS in GroupNode.tsx cardGrid rendering
const GRID_CARD_W = 200
const GRID_CARD_H = 68
const GRID_GAP = 6
const GRID_PAD = { top: 48, left: 14, bottom: 14, right: 14 }

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

// ---------------------------------------------------------------------------
// Layout engine abstraction
//
// Two execution strategies for ELK layout:
//   'worker'      — Web Worker (Vite/Radar). Keeps the main thread free.
//   'main-thread' — Inline ELK (webpack/Next.js). Workers can't load the
//                   layout.worker.ts file under webpack, so we run ELK
//                   directly using elkjs/lib/elk.bundled.js.
//
// The active strategy is set once via `setLayoutEngine()` (exported).
// Defaults to 'worker' — consumers on webpack call setLayoutEngine('main-thread').
// ---------------------------------------------------------------------------

type LayoutEngine = 'worker' | 'main-thread'
let layoutEngine: LayoutEngine = 'worker'

/** Set the ELK layout execution strategy. Call once at app init. */
export function setLayoutEngine(engine: LayoutEngine) {
  layoutEngine = engine
}

interface LayoutResult {
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

// ELK algorithm options (shared between worker and main-thread engines)
const elkOptionsGroup = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.spacing.nodeNode': '40',
  'elk.layered.spacing.nodeNodeBetweenLayers': '85',
  'elk.layered.spacing.edgeNodeBetweenLayers': '25',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
}

const elkOptionsGroupLayout = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.spacing.nodeNode': '80',
  'elk.layered.spacing.nodeNodeBetweenLayers': '120',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
}

// ---------------------------------------------------------------------------
// Worker engine
// ---------------------------------------------------------------------------
let layoutWorker: Worker | null = null
let requestIdCounter = 0
const pendingRequests = new Map<number, {
  resolve: (result: LayoutResult) => void
  reject: (error: Error) => void
}>()

const WORKER_UNAVAILABLE = 'Worker unavailable'

function rejectPendingWorkerRequests() {
  for (const [, pending] of pendingRequests) {
    pending.reject(new Error(WORKER_UNAVAILABLE))
  }
  pendingRequests.clear()
  layoutWorker?.terminate()
  layoutWorker = null
}

function getOrCreateWorker(): Worker {
  if (!layoutWorker) {
    layoutWorker = new Worker(new URL('./layout.worker.ts', import.meta.url), { type: 'module' })
    layoutWorker.onmessage = (e: MessageEvent) => {
      const { requestId, ...result } = e.data
      const pending = pendingRequests.get(requestId)
      if (pending) {
        pendingRequests.delete(requestId)
        if (result.error) {
          pending.reject(new Error(result.error))
        } else {
          pending.resolve(result as LayoutResult)
        }
      }
    }
    layoutWorker.onerror = (e) => {
      console.error('[TopologyLayout] Worker error:', e)
      rejectPendingWorkerRequests()
    }
  }
  return layoutWorker
}

function runLayoutViaWorker(
  elkGraph: ElkGraph,
  groupingMode: GroupingMode,
  hideGroupHeader: boolean,
  padding: typeof GROUP_PADDING
): Promise<LayoutResult> {
  return new Promise((resolve, reject) => {
    try {
      const worker = getOrCreateWorker()
      const requestId = ++requestIdCounter
      pendingRequests.set(requestId, { resolve, reject })
      worker.postMessage({ type: 'layout', requestId, elkGraph, groupingMode, hideGroupHeader, padding })
    } catch (err) {
      console.error('[TopologyLayout] Worker unavailable:', err)
      rejectPendingWorkerRequests()
      reject(new Error(WORKER_UNAVAILABLE))
    }
  })
}

// ---------------------------------------------------------------------------
// Main-thread engine — same two-phase ELK layout as layout.worker.ts
// but runs inline using elkjs/lib/elk.bundled.js (no web-worker dependency).
// ---------------------------------------------------------------------------
async function runLayoutOnMainThread(
  elkGraph: ElkGraph,
  groupingMode: GroupingMode,
  hideGroupHeader: boolean,
  padding: typeof GROUP_PADDING
): Promise<LayoutResult> {
  const ELK = (await import('elkjs/lib/elk.bundled.js')).default
  const elk = new ELK()

  const groupLayouts: LayoutResult['groupLayouts'] = []
  const ungroupedNodes: LayoutResult['ungroupedNodes'] = []
  // Map each node to its group once. Serves both the intra-group edge bucketing
  // here (filtering all edges per group would be O(groups × edges)) and the
  // node→group lookup for Phase 2's inter-group edges below.
  const nodeToGroup = new Map<string, string>()
  for (const child of elkGraph.children) {
    if (child.id.startsWith('group-') && child.children) {
      for (const c of child.children) nodeToGroup.set(c.id, child.id)
    }
  }
  const intraEdgesByGroup = new Map<string, ElkEdge[]>()
  for (const e of elkGraph.edges) {
    const sg = nodeToGroup.get(e.sources[0])
    if (sg && sg === nodeToGroup.get(e.targets[0])) {
      const arr = intraEdgesByGroup.get(sg)
      if (arr) arr.push(e)
      else intraEdgesByGroup.set(sg, [e])
    }
  }

  // Phase 1: Layout each group independently
  for (const child of elkGraph.children) {
    const isGroup = child.id.startsWith('group-')

    if (isGroup && child.children && child.children.length > 0) {
      const groupKey = child.id.replace(`group-${groupingMode}-`, '')
      const minWidth = hideGroupHeader ? 300 : Math.max(500, groupKey.length * 16 + 200)

      const intraGroupEdges = intraEdgesByGroup.get(child.id) ?? []

      const layoutResult = await elk.layout({
        id: child.id,
        layoutOptions: {
          ...elkOptionsGroup,
          'elk.padding': `[left=${padding.left}, top=${padding.top}, right=${padding.right}, bottom=${padding.bottom}]`,
        },
        children: child.children,
        edges: intraGroupEdges,
      }) as any

      groupLayouts.push({
        groupId: child.id,
        groupKey,
        width: hideGroupHeader
          ? (layoutResult.width || 300)
          : Math.max(layoutResult.width || 300, minWidth),
        height: layoutResult.height || 200,
        children: (layoutResult.children || []).map((c: any) => ({
          id: c.id, x: c.x || 0, y: c.y || 0,
        })),
        isCollapsed: false,
      })
    } else if (isGroup) {
      const groupKey = child.id.replace(`group-${groupingMode}-`, '')
      const minWidth = Math.max(400, groupKey.length * 16 + 180)
      groupLayouts.push({
        groupId: child.id, groupKey,
        width: Math.max(child.width || 280, minWidth),
        height: child.height || 82,
        children: [], isCollapsed: true,
      })
    } else {
      ungroupedNodes.push({ id: child.id, width: child.width || 200, height: child.height || 56 })
    }
  }

  // Phase 2: Build meta-graph and position groups based on inter-group edges
  // (nodeToGroup was built once above).
  const metaIds = new Set<string>([
    ...groupLayouts.map(g => g.groupId),
    ...ungroupedNodes.map(n => n.id),
  ])
  const interGroupEdges = buildInterGroupEdges(elkGraph.edges, nodeToGroup, metaIds)

  const metaResult = await elk.layout({
    id: 'meta-root',
    layoutOptions: elkOptionsGroupLayout,
    children: [
      ...groupLayouts.map(g => ({ id: g.groupId, width: g.width, height: g.height })),
      ...ungroupedNodes.map(n => ({ id: n.id, width: n.width, height: n.height })),
    ],
    edges: interGroupEdges,
  }) as any

  const groupPositions: Array<[string, { x: number; y: number }]> =
    (metaResult.children || []).map((c: any) => [c.id, { x: c.x || 0, y: c.y || 0 }])

  return { groupLayouts, ungroupedNodes, groupPositions }
}

// ---------------------------------------------------------------------------
// Dispatcher — routes to the active engine
// ---------------------------------------------------------------------------
function runLayout(
  elkGraph: ElkGraph,
  groupingMode: GroupingMode,
  hideGroupHeader: boolean,
  padding: typeof GROUP_PADDING
): Promise<LayoutResult> {
  if (layoutEngine === 'main-thread') {
    return runLayoutOnMainThread(elkGraph, groupingMode, hideGroupHeader, padding)
  }
  return runLayoutViaWorker(elkGraph, groupingMode, hideGroupHeader, padding).catch((err) => {
    // A worker failure before a layout result arrives usually means the
    // consumer's bundler/CSP couldn't serve or run the worker chunk.
    // Fall back to the inline engine permanently rather than rendering a dead
    // "Layout Error" pane.
    if (err instanceof Error && err.message === WORKER_UNAVAILABLE) {
      console.warn('[TopologyLayout] layout worker unavailable — falling back to main-thread ELK')
      layoutEngine = 'main-thread'
      return runLayoutOnMainThread(elkGraph, groupingMode, hideGroupHeader, padding)
    }
    throw err
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

/**
 * Build the meta-graph edges that position groups relative to each other (Phase 2).
 * Each endpoint is normalized to its meta node: an expanded group's member maps to
 * its group (via nodeToGroup); a collapsed-group chip id or an ungrouped node id is
 * ALREADY a meta node and is used as-is. Edges between two already-meta nodes
 * (chip↔chip, chip↔ungrouped) must still be included — dropping them loses the
 * connectivity ELK uses to place connected groups near each other. Endpoints absent
 * from metaIds are skipped (defensive — keeps the meta graph importable).
 *
 * Mirrored inline in layout.worker.ts, which is intentionally self-contained.
 */
export function buildInterGroupEdges(
  edges: ElkEdge[],
  nodeToGroup: Map<string, string>,
  metaIds: Set<string>,
): ElkEdge[] {
  const out: ElkEdge[] = []
  const seen = new Set<string>()
  for (const edge of edges) {
    const s = nodeToGroup.get(edge.sources[0]) ?? edge.sources[0]
    const t = nodeToGroup.get(edge.targets[0]) ?? edge.targets[0]
    if (s === t || !metaIds.has(s) || !metaIds.has(t)) continue
    const key = `${s}->${t}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push({ id: `inter-${key}`, sources: [s], targets: [t] })
  }
  return out
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

// Pick a representative name for a connected component group (uses shared KIND_PRIORITY)
function pickGroupName(nodes: TopologyNode[]): string {
  const sorted = [...nodes].sort((a, b) => {
    return (KIND_PRIORITY[a.kind] || 99) - (KIND_PRIORITY[b.kind] || 99)
  })
  return sorted[0].name
}

/**
 * Whether a group renders as a single collapsed chip/card (members hidden) vs an
 * expanded container. This is the ONE predicate that node placement, ELK edge
 * redirect, and ReactFlow edge building must all agree on — if they diverge, an
 * edge can reference a member hidden inside a chip, which either crashes ELK
 * ("Referenced shape does not exist") or silently drops the rendered edge.
 *
 * - Explicit chip/cardGrid levels are always collapsed (they're in collapsedGroups).
 * - When the smart-default chip pass is active (large clusters), a group with no
 *   level entry is a late arrival and defaults to collapsed — e.g. a namespace
 *   that only surfaces after switching Resources↔Traffic. When it is NOT active
 *   (small clusters, manual per-group toggles), a no-entry group stays expanded,
 *   so collapsing one group never cascades to untouched groups.
 */
export function isGroupEffectivelyCollapsed(
  groupId: string,
  collapsedGroups: Set<string>,
  groupLevels: Map<string, GroupDisplayLevel> | undefined,
  smartDefaultActive: boolean,
): boolean {
  if (collapsedGroups.has(groupId)) return true
  if (smartDefaultActive && groupLevels?.get(groupId) !== 'topology') return true
  return false
}

// Build hierarchical ELK graph with groups containing children
export function buildHierarchicalElkGraph(
  topologyNodes: TopologyNode[],
  edges: Array<{ id: string; source: string; target: string; type: string }>,
  groupingMode: GroupingMode,
  collapsedGroups: Set<string>,
  groupLevels?: Map<string, GroupDisplayLevel>,
  /** True when the large-cluster smart-default chip pass has materialized levels;
   *  makes no-entry groups default to collapsed (see isGroupEffectivelyCollapsed). */
  smartDefaultActive = false,
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

  // Node placement and edge redirect MUST share this predicate — see
  // isGroupEffectivelyCollapsed for why.
  const isGroupCollapsed = (groupId: string): boolean =>
    isGroupEffectivelyCollapsed(groupId, collapsedGroups, groupLevels, smartDefaultActive)

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
    // Build node lookup once for O(1) access in dimension/card computation
    const nodeMapForGroups = new Map(topologyNodes.map(n => [n.id, n]))
    // Create group nodes with children
    for (const [groupKey, memberIds] of groupMap) {
      const groupId = `group-${groupingMode}-${groupKey}`
      const isCollapsed = isGroupCollapsed(groupId)

      if (isCollapsed) {
        const displayLevel = groupLevels?.get(groupId) || 'chip'

        if (displayLevel === 'cardGrid') {
          // Card grid: compute dimensions from workload card count
          const cards = computeWorkloadCards(memberIds, edges, nodeMapForGroups)
          const { width, height } = computeGridDimensions(cards.length, groupKey)
          children.push({
            id: groupId,
            width,
            height,
            labels: [{ text: groupKey }],
          })
        } else {
          // Chip size scales with resource count (log10 tiers)
          const count = memberIds.length
          const tier = count === 0 ? 0 : Math.min(3, Math.floor(Math.log10(count)))
          const tierWidthBonus = [0, 120, 300, 500][tier]
          const collapsedWidth = Math.max(240 + tierWidthBonus, groupKey.length * 16 + 160)

          // Count unique kinds for pill row height calculation
          const uniqueKinds = new Set<string>()
          for (const nId of memberIds) {
            const n = nodeMapForGroups.get(nId)
            if (n && n.kind !== 'PodGroup') uniqueKinds.add(n.kind)
          }
          const maxPills = [2, 5, 8, 12][tier]
          const pillCount = Math.min(maxPills, uniqueKinds.size)
          const pillWidth = 90
          const pillCols = Math.max(1, Math.floor((collapsedWidth - 24) / (pillWidth + 6)))
          const pillRows = pillCount > 0 ? Math.ceil(pillCount / pillCols) : 0
          const headerHeight = [20, 28, 40, 56][tier]
          const tierHeight = 20 + headerHeight + (pillRows > 0 ? 8 + pillRows * 24 : 0)
          children.push({
            id: groupId,
            width: collapsedWidth,
            height: tierHeight,
            labels: [{ text: groupKey }],
          })
        }
      } else {
        // Expanded group contains its children
        const groupChildren: ElkNode[] = []
        for (const nodeId of memberIds) {
          const node = nodeMapForGroups.get(nodeId)
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

  // Every ELK edge endpoint must reference a node present in the graph (a
  // top-level child, or a member of an expanded group). A single dangling
  // reference makes ELK reject the whole import, blanking the topology — so we
  // drop the stray edge instead. With the unified isGroupCollapsed predicate
  // this should never fire; it's insurance against future placement/redirect
  // drift.
  const validEndpointIds = new Set<string>()
  for (const child of children) {
    validEndpointIds.add(child.id)
    if (child.children) for (const c of child.children) validEndpointIds.add(c.id)
  }

  // Build edges, redirecting to groups when collapsed
  const elkEdges: ElkEdge[] = []
  const seenEdges = new Set<string>()

  for (const edge of edges) {
    let source = edge.source
    let target = edge.target

    // Redirect edges to collapsed groups — same predicate as node placement so
    // an edge never references a member hidden inside a chip.
    const sourceGroup = nodeToGroup.get(source)
    if (sourceGroup && isGroupCollapsed(sourceGroup)) {
      source = sourceGroup
    }

    const targetGroup = nodeToGroup.get(target)
    if (targetGroup && isGroupCollapsed(targetGroup)) {
      target = targetGroup
    }

    // Drop edges whose endpoints aren't in the graph (see validEndpointIds)
    if (!validEndpointIds.has(source) || !validEndpointIds.has(target)) continue

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

// Lower number = higher severity. `neutral` (intentional/idle) is the
// most-benign tier, so a group whose every member is idle rolls up to neutral,
// while a mixed healthy+idle group still reads healthy (healthy out-ranks
// neutral). An empty/unresolved group defaults to healthy.
const HEALTH_PRIORITY: Record<HealthStatus, number> = { unhealthy: 0, degraded: 1, unknown: 2, healthy: 3, neutral: 4 }

function computeGroupHealth(memberIds: string[], nodeMap: Map<string, TopologyNode>): { worstStatus: HealthStatus; unhealthyCount: number } {
  let worstPriority = Infinity
  let worstStatus: HealthStatus = 'healthy'
  let unhealthyCount = 0
  for (const id of memberIds) {
    const node = nodeMap.get(id)
    if (!node) continue
    const priority = HEALTH_PRIORITY[node.status] ?? 2
    if (priority < worstPriority) {
      worstPriority = priority
      worstStatus = node.status
    }
    if (node.status === 'unhealthy' || node.status === 'degraded') {
      unhealthyCount++
    }
  }
  return { worstStatus, unhealthyCount }
}

// Compute workload cards for a card-grid group by finding connected subgraphs.
// Each connected component becomes one card, named after its highest-priority resource.
function computeWorkloadCards(
  memberIds: string[],
  edges: Array<{ id: string; source: string; target: string; type: string }>,
  nodeMap: Map<string, TopologyNode>
): WorkloadCard[] {
  const memberSet = new Set(memberIds)

  // Build adjacency list restricted to this group's members
  const adj = new Map<string, Set<string>>()
  for (const id of memberIds) adj.set(id, new Set())
  for (const edge of edges) {
    if (memberSet.has(edge.source) && memberSet.has(edge.target)) {
      adj.get(edge.source)?.add(edge.target)
      adj.get(edge.target)?.add(edge.source)
    }
  }

  // BFS to find connected components
  const visited = new Set<string>()
  const components: TopologyNode[][] = []
  for (const id of memberIds) {
    if (visited.has(id)) continue
    const component: TopologyNode[] = []
    const queue = [id]
    visited.add(id)
    while (queue.length > 0) {
      const nodeId = queue.shift()!
      const node = nodeMap.get(nodeId)
      if (node) component.push(node)
      for (const neighbor of adj.get(nodeId) || []) {
        if (!visited.has(neighbor)) {
          visited.add(neighbor)
          queue.push(neighbor)
        }
      }
    }
    if (component.length > 0) components.push(component)
  }

  // Convert each component to a workload card
  const cards: WorkloadCard[] = components.map(comp => {
    // Pick primary resource using kindPriority (reuse the same map from pickGroupName)
    const sorted = [...comp].sort((a, b) => {
      const pa = KIND_PRIORITY[a.kind] || 99
      const pb = KIND_PRIORITY[b.kind] || 99
      return pa - pb
    })
    const primary = sorted[0]

    // Compute worst health. Seed with Infinity (not 3) so an all-neutral
    // component can surface neutral — `neutral` is priority 4 (most benign), so a
    // `< 3` seed would never accept it and the card would stay green. Matches
    // computeGroupHealth.
    let worstPriority = Infinity
    let worstStatus: HealthStatus = 'healthy'
    for (const node of comp) {
      const p = HEALTH_PRIORITY[node.status] ?? 2
      if (p < worstPriority) { worstPriority = p; worstStatus = node.status }
    }

    // Extract subtitle from primary resource's data
    const d = primary.data as Record<string, unknown>
    let subtitle = ''
    if (['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'ReplicaSet'].includes(primary.kind)) {
      subtitle = (d.statusSummary as string) || `${d.readyReplicas ?? '?'}/${d.totalReplicas ?? '?'} ready`
    } else if (primary.kind === 'Service') {
      subtitle = (d.type as string) || 'ClusterIP'
    } else if (primary.kind === 'CronJob') {
      subtitle = (d.schedule as string) || ''
    } else if (primary.kind === 'CronWorkflow') {
      subtitle = (d.schedule as string) || ''
    } else if (primary.kind === 'Job') {
      subtitle = (d.phase as string) || ''
    } else if (primary.kind === 'Workflow') {
      subtitle = (d.progress as string) || (d.phase as string) || ''
    } else if (primary.kind === 'WorkflowTemplate' || primary.kind === 'ClusterWorkflowTemplate') {
      const entrypoint = d.entrypoint as string
      const templateCount = d.templateCount as number
      subtitle = entrypoint || (templateCount ? `${templateCount} templates` : '')
    } else if (primary.kind === 'Application') {
      const sync = d.syncStatus as string
      const health = d.healthStatus as string
      subtitle = [sync, health].filter(Boolean).join(' · ')
    } else if (primary.kind === 'KnativeService' || primary.kind === 'Kustomization' || primary.kind === 'HelmRelease') {
      subtitle = d.ready ? 'Ready' : 'Not Ready'
    }

    return {
      id: primary.id,
      kind: primary.kind,
      name: primary.name,
      status: worstStatus,
      subtitle,
      resourceCount: comp.length,
    }
  })

  // Sort by resource count descending (biggest workloads first)
  cards.sort((a, b) => b.resourceCount - a.resourceCount)
  return cards
}

// Shared kind priority map — used by both pickGroupName and computeWorkloadCards
const KIND_PRIORITY: Record<string, number> = {
  'Deployment': 1, 'Rollout': 1, 'StatefulSet': 2, 'DaemonSet': 3,
  'CronJob': 4, 'CronWorkflow': 4, 'Job': 5, 'Workflow': 5, 'Service': 6, 'Gateway': 7,
  'HTTPRoute': 6, 'GRPCRoute': 6, 'TCPRoute': 6, 'TLSRoute': 6, 'Ingress': 7,
  'ReplicaSet': 8, 'Pod': 9, 'PodGroup': 9,
  'ConfigMap': 10, 'Secret': 10, 'WorkflowTemplate': 10, 'ClusterWorkflowTemplate': 10, 'PersistentVolumeClaim': 10, 'HorizontalPodAutoscaler': 10,
  'KnativeService': 1, 'KnativeConfiguration': 3, 'KnativeRevision': 4, 'KnativeRoute': 2,
  'Broker': 2, 'Channel': 2, 'Trigger': 3, 'PingSource': 3, 'ApiServerSource': 3,
  'ContainerSource': 3, 'SinkBinding': 3,
  'IngressRoute': 1, 'IngressRouteTCP': 1, 'IngressRouteUDP': 1, 'TraefikService': 2,
  'Middleware': 3, 'MiddlewareTCP': 3, 'ServersTransport': 4, 'ServersTransportTCP': 4,
  'TLSOption': 4, 'TLSStore': 4, 'HTTPProxy': 1,
  'Application': 1, 'Kustomization': 1, 'HelmRelease': 1, 'GitRepository': 2,
  'CAPICluster': 1, 'MachineDeployment': 2, 'MachineSet': 3, 'Machine': 4,
  'MachinePool': 2, 'KubeadmControlPlane': 1, 'ClusterClass': 1, 'MachineHealthCheck': 5,
  // AWS CAPI infrastructure provider
  'AWSManagedControlPlane': 2, 'AWSManagedMachinePool': 3, 'AWSMachine': 5,
  'AWSMachineTemplate': 5, 'AWSManagedCluster': 2, 'EKSConfig': 5,
  // GCP CAPI infrastructure provider
  'GCPManagedControlPlane': 2, 'GCPManagedMachinePool': 3, 'GCPMachine': 5,
  'GCPMachineTemplate': 5, 'GCPManagedCluster': 2,
  // Azure CAPI infrastructure provider
  'AzureManagedControlPlane': 2, 'AzureManagedMachinePool': 3, 'AzureMachine': 5,
  'AzureMachineTemplate': 5, 'AzureManagedCluster': 2,
}

// Compute grid dimensions from workload card count
function computeGridDimensions(cardCount: number, groupKey: string): { width: number; height: number; columns: number } {
  if (cardCount === 0) return { width: 400, height: 130, columns: 1 }
  const columns = Math.min(4, cardCount)
  const rows = Math.ceil(cardCount / columns)
  const minWidth = Math.max(400, groupKey.length * 16 + 180)
  const width = Math.max(minWidth, columns * (GRID_CARD_W + GRID_GAP) - GRID_GAP + GRID_PAD.left + GRID_PAD.right)
  const height = GRID_PAD.top + rows * (GRID_CARD_H + GRID_GAP) - GRID_GAP + GRID_PAD.bottom
  return { width, height, columns }
}

// Two-phase layout: first layout groups internally, then position groups based on connections
// Layout runs via the active engine — a Web Worker when available, else inline
export async function applyHierarchicalLayout(
  elkGraph: ElkGraph,
  topologyNodes: TopologyNode[],
  topologyEdges: Array<{ id: string; source: string; target: string; type: string }>,
  groupMap: Map<string, string[]>,
  groupingMode: GroupingMode,
  _collapsedGroups: Set<string>,
  callbacks: {
    onSetLevel: (groupId: string, level: GroupDisplayLevel) => void
    onCardClick: (nodeId: string) => void
    onMaximizeNamespace?: (namespace: string) => void
  },
  hideGroupHeader: boolean = false,
  groupLevels?: Map<string, GroupDisplayLevel>
): Promise<{ nodes: Node[]; positions: Map<string, { x: number; y: number }>; error?: string }> {
  try {
    const padding = hideGroupHeader ? GROUP_PADDING_NO_HEADER : GROUP_PADDING

    // Run layout via the active engine
    const workerResult = await runLayout(elkGraph, groupingMode, hideGroupHeader, padding)

    if (workerResult.error) {
      return { nodes: [], positions: new Map(), error: workerResult.error }
    }

    // Build position map from worker result
    const groupPositions = new Map<string, { x: number; y: number }>(workerResult.groupPositions)

    // Build ReactFlow nodes using positions from worker
    const nodes: Node[] = []
    const positions = new Map<string, { x: number; y: number }>()
    // Build node lookup once for O(1) health aggregation across all groups
    const nodeMap = new Map(topologyNodes.map(n => [n.id, n]))

    for (const group of workerResult.groupLayouts) {
      const pos = groupPositions.get(group.groupId) || { x: 0, y: 0 }
      const memberIds = groupMap.get(group.groupKey) || []

      positions.set(group.groupId, pos)

      const { worstStatus, unhealthyCount } = computeGroupHealth(memberIds, nodeMap)
      // Display level derives from the SAME source of truth as ELK node placement:
      // group.isCollapsed reflects buildHierarchicalElkGraph's isGroupEffectivelyCollapsed
      // decision (an expanded group was laid out with its children). Recomputing a
      // separate "default" here would let the rendered chip disagree with the laid-out
      // children — e.g. a manual single collapse rendering untouched groups as chips
      // while their children are still placed. An explicit level (e.g. cardGrid) wins.
      const displayLevel: GroupDisplayLevel = groupLevels?.get(group.groupId) || (group.isCollapsed ? 'chip' : 'topology')

      // Compute kind breakdown for collapsed chips
      const kindCounts: Record<string, number> = {}
      for (const id of memberIds) {
        const node = nodeMap.get(id)
        if (!node || node.kind === 'PodGroup') continue
        kindCounts[node.kind] = (kindCounts[node.kind] || 0) + 1
      }

      // Compute workload cards for card-grid groups
      const workloadCards = displayLevel === 'cardGrid'
        ? computeWorkloadCards(memberIds, topologyEdges, nodeMap)
        : undefined
      const gridColumns = workloadCards ? Math.min(4, workloadCards.length || 1) : undefined

      // Skip the group container node entirely in single-namespace topology view —
      // it creates an invisible bounding box that constrains child node movement
      const skipGroupNode = hideGroupHeader && displayLevel === 'topology'

      if (!skipGroupNode) {
        nodes.push({
          id: group.groupId,
          type: 'group',
          position: pos,
          data: {
            type: groupingMode,
            name: group.groupKey,
            nodeCount: memberIds.length,
            collapsed: group.isCollapsed,
            displayLevel,
            onSetLevel: callbacks.onSetLevel,
            onCardClick: callbacks.onCardClick,
            onMaximizeNamespace: callbacks.onMaximizeNamespace,
            hideHeader: hideGroupHeader,
            worstStatus,
            unhealthyCount,
            kindCounts,
            workloadCards,
            gridColumns,
          },
          style: {
            width: group.width,
            height: group.height,
          },
          zIndex: -1,
        })
      }

      // Add child nodes — use absolute positions (no parent) when header is hidden
      // (single namespace view), otherwise relative positions inside the group container
      for (const child of group.children) {
        const topoNode = nodeMap.get(child.id)
        if (topoNode) {
          const absX = pos.x + child.x
          const absY = pos.y + child.y
          positions.set(child.id, { x: absX, y: absY })

          nodes.push({
            id: child.id,
            type: 'k8sResource',
            position: hideGroupHeader ? { x: absX, y: absY } : { x: child.x, y: child.y },
            ...(hideGroupHeader ? {} : { parentId: group.groupId, extent: 'parent' as const }),
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
      const topoNode = nodeMap.get(node.id)
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
