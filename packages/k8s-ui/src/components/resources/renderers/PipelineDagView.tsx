// Task-dependency DAG for a Tekton Pipeline or PipelineRun. Lives only in
// the resource's fullscreen "full view" (wired via renderExpandedOverview in
// web/src/components/workload/WorkloadView.tsx) — never in the compact
// drawer, which has no room to render a DAG legibly.
//
// Layout uses the same ELK.js engine as the main Topology view
// (packages/k8s-ui/src/components/topology/layout.ts: layered, RIGHT,
// ORTHOGONAL edge routing, NETWORK_SIMPLEX placement) rather than a hand-
// rolled rank/row placement — NETWORK_SIMPLEX minimizes edge crossings,
// which a same-order placement does not, and that crossing-minimization is
// exactly what was missing (overlapping, hard-to-follow lines).
//
// Spacing/merge options below deliberately go further than Topology's own
// (40/85/25): a Pipeline's task graph is a much denser fan-out/fan-in shape
// than a typical ownership hierarchy — several governance-check tasks (sast,
// sbom, image-scan, policy-check, ...) commonly all depend on one upstream
// task and converge on one downstream task, which is exactly the shape that
// reads as "overlapping arrows" without extra edge/node breathing room and
// edge merging at the shared endpoints.
//
// Node cards intentionally mirror K8sResourceNode's visual language
// (topology-node-card CSS, icon + kind-label header row, status dot) so a
// task in this DAG reads as "the same kind of thing" as a node in the real
// Topology view, without extending the shared NodeKind enum or wiring
// Tekton into pkg/topology/builder.go — that stays a separate, larger PR.

import { Component, memo, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  Controls,
  Handle,
  MarkerType,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useNodesState,
  type Edge,
  type Node,
  type NodeProps,
  type NodeTypes,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import '../../topology/topology.css'
import { CheckCircle2, CircleDashed, ListTodo, Loader2, MinusCircle, RotateCcw, XCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { healthToSeverity, SEVERITY_DOT } from '../../../utils/badge-colors'
import type { TektonTaskNode, TektonTaskNodeStatus } from '../resource-utils-tekton'

const NODE_WIDTH = 220
const NODE_HEIGHT = 66

const STATUS_ICON: Record<TektonTaskNodeStatus, typeof CheckCircle2> = {
  succeeded: CheckCircle2,
  failed: XCircle,
  running: Loader2,
  pending: CircleDashed,
  skipped: MinusCircle,
  unknown: CircleDashed,
}

// TektonTaskNodeStatus -> the same HealthStatus vocabulary K8sResourceNode's
// status dot uses, so a task's dot color means the same thing a topology
// node's dot color means.
const STATUS_HEALTH: Record<TektonTaskNodeStatus, 'healthy' | 'degraded' | 'unhealthy' | 'unknown' | 'neutral'> = {
  succeeded: 'healthy',
  failed: 'unhealthy',
  running: 'neutral',
  pending: 'unknown',
  skipped: 'degraded',
  unknown: 'unknown',
}

const elkOptions = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.spacing.nodeNode': '48',
  'elk.layered.spacing.nodeNodeBetweenLayers': '100',
  'elk.layered.spacing.edgeNodeBetweenLayers': '32',
  // Default edge-edge/edge-node spacing is tight enough that several
  // parallel edges between the same two layers (a build task with 4-5
  // direct dependents, say) visually run together.
  'elk.spacing.edgeEdge': '24',
  'elk.spacing.edgeNode': '24',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
  // Edges sharing a source or target bundle into one trunk near that shared
  // endpoint instead of fanning out as separate parallel lines the whole
  // way - the single most direct fix for a fan-out/fan-in task depended on
  // by (or converging from) several siblings at once.
  'elk.layered.mergeEdges': 'true',
  // Default (7) is tuned for large graphs where more iterations cost real
  // time; a pipeline's task count is small enough that a much more thorough
  // crossing-minimization pass is still instant, and crossing minimization
  // is exactly the lever for "hard to see what goes where".
  'elk.layered.thoroughness': '30',
}

async function layoutNodes(tasks: TektonTaskNode[]): Promise<{ nodes: Node[]; edges: Edge[] }> {
  const ELK = (await import('elkjs/lib/elk.bundled.js')).default
  const elk = new ELK()

  const byName = new Set(tasks.map((t) => t.name))
  const elkEdges = tasks.flatMap((task) =>
    task.dependsOn.filter((dep) => byName.has(dep)).map((dep) => ({
      id: `${dep}->${task.name}`,
      sources: [dep],
      targets: [task.name],
    })),
  )

  const result = await elk.layout({
    id: 'root',
    layoutOptions: elkOptions,
    children: tasks.map((t) => ({ id: t.name, width: NODE_WIDTH, height: NODE_HEIGHT })),
    edges: elkEdges,
  } as any) as any

  const positionByName = new Map<string, { x: number; y: number }>(
    (result.children ?? []).map((c: any) => [c.id, { x: c.x ?? 0, y: c.y ?? 0 }]),
  )

  const nodes: Node[] = tasks.map((task) => ({
    id: task.name,
    type: 'tektonTask',
    position: positionByName.get(task.name) ?? { x: 0, y: 0 },
    data: { task },
    draggable: true,
    selectable: true,
    // Every card is the same fixed size, declared up front instead of
    // discovered via ResizeObserver. Two reasons this matters, both from
    // @xyflow/system's adoptUserNodes: (1) it rebuilds a node's internal
    // record from scratch whenever the incoming node object isn't the exact
    // same reference as last time (true on every poll here, since task
    // status is rebuilt fresh each time) — the rebuild resets `measured` to
    // undefined and leaves the node visibility:hidden until a resize fires;
    // since the card's on-screen size never actually changes, no resize
    // event ever comes and it stays hidden for good. (2) edge connection
    // points normally come from parseHandles() reading each Handle's live
    // DOM position — also reset on the same rebuild — so edges vanish too.
    // Setting `measured` and `handles` directly here answers both from data
    // instead of a DOM measurement, so neither ever depends on catching a
    // ResizeObserver callback that has nothing new to report.
    width: NODE_WIDTH,
    height: NODE_HEIGHT,
    measured: { width: NODE_WIDTH, height: NODE_HEIGHT },
    handles: [
      { type: 'target' as const, position: Position.Left, x: 0, y: NODE_HEIGHT / 2 },
      { type: 'source' as const, position: Position.Right, x: NODE_WIDTH, y: NODE_HEIGHT / 2 },
    ],
  }))

  const edges: Edge[] = elkEdges.map((e) => ({
    id: e.id,
    source: e.sources[0],
    target: e.targets[0],
    type: 'smoothstep',
    style: { stroke: '#64748b', strokeWidth: 1.5 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#64748b', width: 16, height: 16 },
  }))

  return { nodes, edges }
}

interface TektonTaskCardData extends Record<string, unknown> {
  task: TektonTaskNode
  onClick?: (task: TektonTaskNode) => void
}

const TektonTaskCard = memo(function TektonTaskCard({ data }: NodeProps<Node<TektonTaskCardData>>) {
  const { task, onClick } = data
  const status = task.status ?? 'unknown'
  const Icon = STATUS_ICON[status]
  const severity = healthToSeverity(STATUS_HEALTH[status])
  const clickable = Boolean(onClick && task.taskRunName)
  return (
    <>
      <Handle type="target" position={Position.Left} className="!h-0 !w-0 !border-0 !bg-transparent" />
      <div
        className={clsx(
          'topology-node-card relative rounded-lg',
          // Solid (non-alpha) background, not opacity-pulsed — a translucent
          // or pulsing card would let the DAG's edge lines show through it,
          // and the spinning icon alone already carries the "in progress"
          // motion cue without animating the whole card's opacity.
          status === 'running' ? 'bg-sky-50 ring-1 ring-sky-500/40 dark:bg-sky-950' : 'bg-theme-surface',
          clickable && 'cursor-pointer hover:ring-1 hover:ring-skyhook-500/50',
        )}
        style={{ width: NODE_WIDTH, height: NODE_HEIGHT }}
        onClick={clickable ? () => onClick?.(task) : undefined}
        title={task.reason ? `${task.name} — ${task.reason}` : task.name}
      >
        <div className="px-3 py-2">
          <div className="mb-0.5 flex items-center gap-1.5">
            <ListTodo className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
            <span className="text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Task</span>
            <span className={clsx('ml-auto h-1.5 w-1.5 shrink-0 rounded-full', SEVERITY_DOT[severity])} />
          </div>
          <div className="flex items-center gap-1.5">
            <Icon className={clsx('h-3.5 w-3.5 shrink-0', status === 'running' && 'animate-spin', {
              'text-emerald-500': status === 'succeeded',
              'text-red-500': status === 'failed',
              'text-sky-500': status === 'running',
              'text-amber-500': status === 'skipped',
              'text-theme-text-tertiary': status === 'pending' || status === 'unknown',
            })} />
            <span className="truncate text-sm font-medium text-theme-text-primary">{task.name}</span>
          </div>
        </div>
      </div>
      <Handle type="source" position={Position.Right} className="!h-0 !w-0 !border-0 !bg-transparent" />
    </>
  )
})

const NODE_TYPES: NodeTypes = { tektonTask: TektonTaskCard }

// Isolates a ReactFlow render failure to this panel — a bad status/edge
// combination here shouldn't blank the rest of the drawer with no signal.
class DagErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) {
    return { error }
  }
  componentDidCatch(error: Error, info: { componentStack?: string | null }) {
    console.error('[PipelineDagView] render error', error, info.componentStack)
  }
  render() {
    if (this.state.error) {
      return (
        <div className="flex h-full items-center justify-center text-sm text-red-400">
          DAG render error: {this.state.error.message}
        </div>
      )
    }
    return this.props.children
  }
}

// Manual layout persistence: browser-local, keyed by the task set's own
// structure (names + dependency shape, the same `taskKey` the layout effect
// already keys off) rather than a pipeline name/namespace — a saved layout
// is really about "this DAG shape", so it's naturally shared across every
// PipelineRun of the same underlying Pipeline, not just the one the user
// happened to drag. Never load-bearing: any localStorage failure (quota,
// private browsing) just means the auto layout is used instead.
const LAYOUT_STORAGE_PREFIX = 'radar:pipeline-dag-layout:v1:'

type SavedPositions = Record<string, { x: number; y: number }>

function loadSavedPositions(taskKey: string): SavedPositions | null {
  try {
    const raw = localStorage.getItem(LAYOUT_STORAGE_PREFIX + taskKey)
    return raw ? (JSON.parse(raw) as SavedPositions) : null
  } catch {
    return null
  }
}

function saveSavedPositions(taskKey: string, positions: SavedPositions): void {
  try {
    localStorage.setItem(LAYOUT_STORAGE_PREFIX + taskKey, JSON.stringify(positions))
  } catch {
    // best-effort — see comment above
  }
}

function clearSavedPositions(taskKey: string): void {
  try {
    localStorage.removeItem(LAYOUT_STORAGE_PREFIX + taskKey)
  } catch {
    // best-effort — see comment above
  }
}

export interface PipelineDagViewProps {
  tasks: TektonTaskNode[]
  height?: number
  onTaskClick?: (task: TektonTaskNode) => void
}

export function PipelineDagView({ tasks, height, onTaskClick }: PipelineDagViewProps) {
  // The pure ELK result — always the "reset" target, and the source of truth
  // for edges (edges never move, only node positions do).
  const [elkResult, setElkResult] = useState<{ nodes: Node[]; edges: Edge[] } | null>(null)
  const [rfNodes, setRfNodes, onNodesChangeBase] = useNodesState<Node>([])
  const [hasOverride, setHasOverride] = useState(false)
  // Read inside onNodeDragStop without needing rfNodes in its deps — a plain
  // closure over state would capture whatever rfNodes was when the callback
  // was LAST created, not the latest positions from the drag that just ended.
  const rfNodesRef = useRef<Node[]>([])
  rfNodesRef.current = rfNodes

  const taskKey = useMemo(() => tasks.map((t) => `${t.name}:${t.dependsOn.join(',')}`).join('|'), [tasks])

  // Structural layout: ELK, then any saved manual override applied on top.
  // Re-runs only when the task set or its dependency shape changes — not on
  // every status update, which would re-run ELK (and blow away a manual
  // layout) on every poll tick.
  useEffect(() => {
    let cancelled = false
    layoutNodes(tasks).then((result) => {
      if (cancelled) return
      setElkResult(result)
      const saved = loadSavedPositions(taskKey)
      const byName = new Map(tasks.map((t) => [t.name, t]))
      setRfNodes(result.nodes.map((n) => ({
        ...n,
        position: saved?.[n.id] ?? n.position,
        data: { task: byName.get(n.id) ?? (n.data as TektonTaskCardData).task, onClick: onTaskClick },
      })))
      setHasOverride(saved !== null)
    })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [taskKey])

  // Status/taskRunName changes (live polling) update node data in place
  // without touching positions — keeps the graph stable, whether auto-laid-
  // out or hand-placed, while a run progresses instead of jittering it every
  // few seconds. A no-op before the structural effect above has populated
  // rfNodes for the first time.
  useEffect(() => {
    const byName = new Map(tasks.map((t) => [t.name, t]))
    setRfNodes((prev) => prev.map((n) => ({
      ...n,
      data: { task: byName.get(n.id) ?? (n.data as TektonTaskCardData).task, onClick: onTaskClick },
    })))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tasks, onTaskClick])

  // Fires once when a drag ends (not on every intermediate move) — persists
  // every node's current position, not just the one that moved, so a saved
  // layout is always a complete map rather than a diff against ELK's.
  const persistPositions = useCallback(() => {
    const positions: SavedPositions = {}
    for (const n of rfNodesRef.current) positions[n.id] = n.position
    saveSavedPositions(taskKey, positions)
    setHasOverride(true)
  }, [taskKey])

  const resetLayout = useCallback(() => {
    clearSavedPositions(taskKey)
    setRfNodes((prev) => {
      if (!elkResult) return prev
      const basePosition = new Map(elkResult.nodes.map((n) => [n.id, n.position]))
      return prev.map((n) => ({ ...n, position: basePosition.get(n.id) ?? n.position }))
    })
    setHasOverride(false)
  }, [taskKey, elkResult, setRfNodes])

  if (tasks.length === 0) return null

  return (
    <div
      className="h-full overflow-hidden rounded-md border border-theme-border bg-theme-base"
      style={height !== undefined ? { height } : undefined}
    >
      {!elkResult ? (
        <div className="flex h-full items-center justify-center text-sm text-theme-text-tertiary">
          Laying out task graph…
        </div>
      ) : (
        <DagErrorBoundary>
          <ReactFlowProvider>
            <ReactFlow
              nodes={rfNodes}
              edges={elkResult.edges}
              nodeTypes={NODE_TYPES}
              onNodesChange={onNodesChangeBase}
              onNodeDragStop={persistPositions}
              fitView
              fitViewOptions={{ padding: 0.2, maxZoom: 1.5 }}
              nodesDraggable
              nodesConnectable={false}
              elementsSelectable={false}
              minZoom={0.15}
              maxZoom={1.5}
              zoomOnScroll
              zoomOnPinch
              zoomOnDoubleClick={false}
              proOptions={{ hideAttribution: true }}
            >
              <Controls className="!border-theme-border !bg-theme-surface" showInteractive={false} />
              {hasOverride && (
                <Panel position="top-right">
                  <button
                    type="button"
                    onClick={resetLayout}
                    title="Discard your manual layout and re-run auto layout"
                    className="inline-flex items-center gap-1.5 rounded-md border border-theme-border bg-theme-surface px-2 py-1 text-xs font-medium text-theme-text-secondary shadow-theme-sm hover:bg-theme-hover hover:text-theme-text-primary"
                  >
                    <RotateCcw className="h-3 w-3" aria-hidden />
                    Reset layout
                  </button>
                </Panel>
              )}
            </ReactFlow>
          </ReactFlowProvider>
        </DagErrorBoundary>
      )}
    </div>
  )
}
