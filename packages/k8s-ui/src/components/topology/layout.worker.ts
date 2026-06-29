/**
 * Web Worker for ELK layout calculations.
 * Moves expensive layout operations off the main thread.
 *
 * We use elk-api.js which doesn't spawn its own worker - we ARE the worker.
 * The algorithms run synchronously here but off the main thread.
 */
import ELK from 'elkjs/lib/elk-api.js'
import elkWorkerAlgorithm from 'elkjs/lib/elk-worker.min.js?url'

// Create ELK with explicit worker URL (runs the algorithm in our worker context)
const elk = new ELK({
  workerUrl: elkWorkerAlgorithm,
})

// ELK options for laying out nodes within a single group
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

// ELK options for positioning groups based on inter-group connections
const elkOptionsGroupLayout = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.spacing.nodeNode': '80',
  'elk.layered.spacing.nodeNodeBetweenLayers': '120',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
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

interface GroupPadding {
  top: number
  left: number
  bottom: number
  right: number
}

interface LayoutRequest {
  type: 'layout'
  requestId: number
  elkGraph: ElkGraph
  groupingMode: string
  hideGroupHeader: boolean
  padding: GroupPadding
}

interface GroupLayoutResult {
  groupId: string
  groupKey: string
  width: number
  height: number
  children: Array<{ id: string; x: number; y: number }>
  isCollapsed: boolean
}

interface UngroupedNodeResult {
  id: string
  width: number
  height: number
}

interface LayoutResponse {
  type: 'layout-result'
  requestId: number
  groupLayouts: GroupLayoutResult[]
  ungroupedNodes: UngroupedNodeResult[]
  groupPositions: Array<[string, { x: number; y: number }]>
  error?: string
}

self.onmessage = async (e: MessageEvent<LayoutRequest>) => {
  const { type, requestId, elkGraph, groupingMode, hideGroupHeader, padding } = e.data

  if (type !== 'layout') return

  try {
    const groupLayouts: GroupLayoutResult[] = []
    const ungroupedNodes: UngroupedNodeResult[] = []

    // Map each node to its group once. Serves both the intra-group edge
    // bucketing here (filtering all edges per group would be O(groups × edges))
    // and the node→group lookup for Phase 2's inter-group edges. This is the
    // default (worker) layout path, so the win actually lands here.
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

        // Layout this group independently with only intra-group edges
        const intraGroupEdges = intraEdgesByGroup.get(child.id) ?? []

        const groupGraph: ElkGraph = {
          id: child.id,
          layoutOptions: {
            ...elkOptionsGroup,
            'elk.padding': `[left=${padding.left}, top=${padding.top}, right=${padding.right}, bottom=${padding.bottom}]`,
          },
          children: child.children,
          edges: intraGroupEdges,
        }

        const layoutResult = await elk.layout(groupGraph) as ElkLayoutResult

        const finalWidth = hideGroupHeader
          ? (layoutResult.width || 300)
          : Math.max(layoutResult.width || 300, minWidth)

        groupLayouts.push({
          groupId: child.id,
          groupKey,
          width: finalWidth,
          height: layoutResult.height || 200,
          children: (layoutResult.children || []).map(c => ({
            id: c.id,
            x: c.x || 0,
            y: c.y || 0,
          })),
          isCollapsed: false,
        })
      } else if (isGroup) {
        // Collapsed group
        const groupKey = child.id.replace(`group-${groupingMode}-`, '')
        const minWidth = Math.max(400, groupKey.length * 16 + 180)
        groupLayouts.push({
          groupId: child.id,
          groupKey,
          width: Math.max(child.width || 280, minWidth),
          height: child.height || 90,
          children: [],
          isCollapsed: true,
        })
      } else {
        // Ungrouped node
        ungroupedNodes.push({
          id: child.id,
          width: child.width || 200,
          height: child.height || 56,
        })
      }
    }

    // Phase 2: Build meta-graph and position groups (nodeToGroup built once above).
    // Canonical version: buildInterGroupEdges in layout.ts — kept inline here because
    // the worker is intentionally self-contained. Normalize each endpoint to its meta
    // node (expanded member → its group; a chip id or ungrouped id is already a meta
    // node) and keep edges between two already-meta nodes (chip↔chip, chip↔ungrouped),
    // which the old branching dropped — losing the connectivity used to place groups.
    const metaChildren: ElkNode[] = [
      ...groupLayouts.map(g => ({ id: g.groupId, width: g.width, height: g.height })),
      ...ungroupedNodes.map(n => ({ id: n.id, width: n.width, height: n.height })),
    ]
    const metaIds = new Set<string>(metaChildren.map(c => c.id))

    const interGroupEdges: ElkEdge[] = []
    const seenInterGroupEdges = new Set<string>()
    for (const edge of elkGraph.edges) {
      const source = nodeToGroup.get(edge.sources[0]) ?? edge.sources[0]
      const target = nodeToGroup.get(edge.targets[0]) ?? edge.targets[0]
      if (source === target || !metaIds.has(source) || !metaIds.has(target)) continue
      const edgeKey = `${source}->${target}`
      if (seenInterGroupEdges.has(edgeKey)) continue
      seenInterGroupEdges.add(edgeKey)
      interGroupEdges.push({ id: `inter-${edgeKey}`, sources: [source], targets: [target] })
    }

    const metaGraph: ElkGraph = {
      id: 'meta-root',
      layoutOptions: elkOptionsGroupLayout,
      children: metaChildren,
      edges: interGroupEdges,
    }

    const metaLayoutResult = await elk.layout(metaGraph) as ElkLayoutResult

    // Build position map
    const groupPositions: Array<[string, { x: number; y: number }]> = []
    for (const child of metaLayoutResult.children || []) {
      groupPositions.push([child.id, { x: child.x || 0, y: child.y || 0 }])
    }

    const response: LayoutResponse = {
      type: 'layout-result',
      requestId,
      groupLayouts,
      ungroupedNodes,
      groupPositions,
    }

    self.postMessage(response)
  } catch (err) {
    const errorMessage = err instanceof Error ? err.message : String(err)
    const response: LayoutResponse = {
      type: 'layout-result',
      requestId,
      groupLayouts: [],
      ungroupedNodes: [],
      groupPositions: [],
      error: `Layout failed: ${errorMessage}`,
    }
    self.postMessage(response)
  }
}
