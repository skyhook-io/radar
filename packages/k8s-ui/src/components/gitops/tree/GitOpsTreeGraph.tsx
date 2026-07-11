import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type NodeTypes,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { AlertTriangle, ChevronRight, Maximize, Search, X } from 'lucide-react'
import { PaneLoader } from '../../ui/PaneLoader'
import { Input } from '../../ui/Input'
import { clsx } from 'clsx'

import type { GitOpsResourceTree, GitOpsTreeNode, GitOpsTreeRef, HealthStatus } from '../../../types'
import { displayKind } from '../../../types'
import { healthToSeverity, SEVERITY_DOT } from '../../../utils/badge-colors'
import { formatCompactAge } from '../../../utils/format'
import { getTopologyIcon } from '../../../utils/resource-icons'
import { Tooltip } from '../../ui/Tooltip'
import { hasGitOpsTreeFilters, matchesGitOpsTreeFilters, type GitOpsTreeFilters } from './tree-helpers'

export type GitOpsTreePreset = 'full' | 'compact' | 'workloads' | 'app'

const RANK_GAP = 390
const ROW_GAP = 118
const NODE_WIDTH = 320
const NODE_HEIGHT = 84
const GROUP_WIDTH = 230
const GROUP_HEIGHT = 64
const ROOT_Y_OFFSET = 28

const WORKLOAD_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'Job', 'CronJob'])
const WORKLOAD_CHILD_KINDS = new Set(['ReplicaSet', 'Pod', 'PodGroup'])
const COMPACT_INFRA_KINDS = new Set([
  'AppProject',
  'ClusterRole',
  'ClusterRoleBinding',
  'ConfigMap',
  'CustomResourceDefinition',
  'Role',
  'RoleBinding',
  'Secret',
  'SealedSecret',
  'ServiceAccount',
])

interface GitOpsTreeGraphProps {
  tree: GitOpsResourceTree | null
  loading?: boolean
  error?: Error | null
  onNodeClick?: (ref: GitOpsTreeRef, node: GitOpsTreeNode) => void
  preset?: GitOpsTreePreset
  onPresetChange?: (preset: GitOpsTreePreset) => void
  query?: string
  onQueryChange?: (query: string) => void
  filters?: GitOpsTreeFilters
  showToolbar?: boolean
}

export function GitOpsTreeGraph(props: GitOpsTreeGraphProps) {
  return (
    <ReactFlowProvider>
      <GitOpsTreeGraphInner {...props} />
    </ReactFlowProvider>
  )
}

function GitOpsTreeGraphInner({
  tree,
  loading = false,
  error = null,
  onNodeClick,
  preset: controlledPreset,
  onPresetChange,
  query: controlledQuery,
  onQueryChange,
  filters,
  showToolbar = true,
}: GitOpsTreeGraphProps) {
  const [internalPreset, setInternalPreset] = useState<GitOpsTreePreset>('compact')
  const [internalQuery, setInternalQuery] = useState('')
  const preset = controlledPreset ?? internalPreset
  const query = controlledQuery ?? internalQuery
  const setPreset = onPresetChange ?? setInternalPreset
  const setQuery = onQueryChange ?? setInternalQuery
  const reactFlow = useReactFlow()
  // Per-group expand state (keyed by group node ID, e.g. "<parent>/compact/<kind>").
  // When a group ID is in this set, compactInfra leaves the children visible
  // instead of collapsing them into a single "N <kind>s" node. Collapses
  // back to the default when the user toggles off or switches preset.
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  // Reset expansion when leaving compact preset — there are no groups to
  // expand in 'full', and stale state would surprise the user when they
  // toggle back.
  useEffect(() => {
    if (preset !== 'compact') setExpandedGroups(new Set())
  }, [preset])
  const { nodes, edges } = useMemo(() => buildFlowGraph(tree, preset, query, filters, expandedGroups), [tree, preset, query, filters, expandedGroups])

  useEffect(() => {
    if (nodes.length === 0) return
    const id = window.setTimeout(() => reactFlow.fitView({ padding: 0.18, maxZoom: 1.15, duration: 180 }), 0)
    return () => window.clearTimeout(id)
  }, [nodes.length, edges.length, preset, query, reactFlow])

  const handleNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    const gitOpsNode = node.data.node as GitOpsTreeNode | undefined
    if (!gitOpsNode) return
    if (gitOpsNode.role === 'group') {
      // Toggle expand/collapse for this group. Re-render replaces the
      // synthetic group node with the original children (or vice versa).
      setExpandedGroups((prev) => {
        const next = new Set(prev)
        if (next.has(gitOpsNode.id)) next.delete(gitOpsNode.id)
        else next.add(gitOpsNode.id)
        return next
      })
      return
    }
    onNodeClick?.(gitOpsNode.ref, gitOpsNode)
  }, [onNodeClick])

  if (loading) {
    return <PaneLoader label="Loading GitOps resource tree…" className="h-full" />
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-red-500">
        <AlertTriangle className="mr-2 h-4 w-4" />
        Failed to load GitOps tree: {error.message}
      </div>
    )
  }

  if (!tree || nodes.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
        No managed resources found for this GitOps object.
      </div>
    )
  }

  return (
    <div className="relative h-full min-h-0 min-w-0">
      {showToolbar && (
        <GitOpsTreeToolbar
          preset={preset}
          onPresetChange={setPreset}
          query={query}
          onQueryChange={setQuery}
          onFit={() => reactFlow.fitView({ padding: 0.18, maxZoom: 1.15, duration: 180 })}
        />
      )}
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodeClick={handleNodeClick}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        fitView
        fitViewOptions={{ padding: 0.18, maxZoom: 1.15 }}
        minZoom={0.15}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
        className="bg-theme-base"
      >
        <Background
          variant={BackgroundVariant.Dots}
          gap={20}
          size={1}
          className="opacity-40"
        />
        <Controls
          className="!border-theme-border !bg-theme-surface"
          showInteractive={false}
        />
      </ReactFlow>
      <EdgeLegend edges={edges} />
    </div>
  )
}

// EdgeLegend documents the color encoding on the GitOps tree graph. Only
// renders rows for edge types actually present in the current view —
// hides the legend entirely when the graph has only `owns` edges (the
// default for Argo apps without dependencies).
function EdgeLegend({ edges }: { edges: Edge[] }) {
  const types = new Set(edges.map((e) => e.data?.type as string).filter(Boolean))
  const rows: Array<{ key: string; color: string; label: string }> = []
  if (types.has('source')) rows.push({ key: 'source', color: getEdgeColor('source'), label: 'Source repo' })
  if (types.has('dependsOn')) rows.push({ key: 'dependsOn', color: getEdgeColor('dependsOn'), label: 'Depends on' })
  // Always show the default ownership color when there are 2+ edge kinds —
  // operators need to compare against it. Hide when "owns" is the only kind.
  if (rows.length > 0) rows.unshift({ key: 'owns', color: getEdgeColor('owns'), label: 'Ownership' })
  if (rows.length === 0) return null
  return (
    <div className="absolute bottom-4 left-4 z-10 rounded-md border border-theme-border bg-theme-surface/90 px-2.5 py-1.5 text-[10px] backdrop-blur">
      <div className="mb-1 font-medium uppercase tracking-wide text-theme-text-tertiary">Edges</div>
      <div className="flex flex-col gap-1">
        {rows.map((row) => (
          <div key={row.key} className="flex items-center gap-2 text-theme-text-secondary">
            <span className="inline-block h-[2px] w-5 rounded" style={{ backgroundColor: row.color }} />
            {row.label}
          </div>
        ))}
      </div>
    </div>
  )
}

function GitOpsTreeToolbar({
  preset,
  onPresetChange,
  query,
  onQueryChange,
  onFit,
}: {
  preset: GitOpsTreePreset
  onPresetChange: (preset: GitOpsTreePreset) => void
  query: string
  onQueryChange: (query: string) => void
  onFit: () => void
}) {
  return (
    <div className="absolute right-4 top-4 z-10 flex flex-wrap items-center justify-end gap-2">
      <div className="flex items-center gap-1 rounded-lg border border-theme-border bg-theme-surface/90 p-1 backdrop-blur">
        {(['compact', 'workloads', 'app', 'full'] as const).map(value => (
          <button
            key={value}
            type="button"
            onClick={() => onPresetChange(value)}
            className={clsx(
              'rounded-md px-2.5 py-1 text-xs transition-colors',
              preset === value
                ? 'bg-skyhook-600 text-white'
                : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
            )}
          >
            {getPresetLabel(value)}
          </button>
        ))}
      </div>
      <div className="flex items-center gap-1 rounded-lg border border-theme-border bg-theme-surface/90 px-2 py-1.5 backdrop-blur">
        <Search className="h-3.5 w-3.5 text-theme-text-tertiary" />
        <Input
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder="Find node..."
          className="w-36 bg-transparent text-xs text-theme-text-primary outline-none placeholder:text-theme-text-tertiary"
        />
        {query && (
          <button type="button" onClick={() => onQueryChange('')} className="text-theme-text-tertiary hover:text-theme-text-primary">
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <Tooltip content="Fit tree" delay={120}>
        <button
          type="button"
          onClick={onFit}
          className="flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface/90 px-2.5 py-1.5 text-xs text-theme-text-secondary backdrop-blur transition-colors hover:text-theme-text-primary"
        >
          <Maximize className="h-3.5 w-3.5" />
          Fit
        </button>
      </Tooltip>
    </div>
  )
}

function getPresetLabel(preset: GitOpsTreePreset): string {
  switch (preset) {
    case 'compact': return 'Compact'
    case 'workloads': return 'Workloads'
    case 'app': return 'Declared'
    case 'full': return 'Full'
  }
}

function getEdgeColor(type: string): string {
  switch (type) {
    case 'source': return '#0ea5e9'
    case 'dependsOn': return '#f59e0b'
    default: return '#64748b'
  }
}

function buildFlowGraph(tree: GitOpsResourceTree | null, preset: GitOpsTreePreset, query: string, filters?: GitOpsTreeFilters, expandedGroups?: Set<string>): { nodes: Node[]; edges: Edge[] } {
  if (!tree) return { nodes: [], edges: [] }
  const visibleTree = applyGraphFilters(applyPreset(tree, preset, expandedGroups), filters)
  const byID = new Map(visibleTree.nodes.map(node => [node.id, node]))
  const children = new Map<string, string[]>()
  const incoming = new Map<string, number>()
  for (const edge of visibleTree.edges) {
    if (!byID.has(edge.source) || !byID.has(edge.target)) continue
    children.set(edge.source, [...(children.get(edge.source) ?? []), edge.target])
    incoming.set(edge.target, (incoming.get(edge.target) ?? 0) + 1)
  }

  const ranks = assignRanks(visibleTree.root.id, byID, children, incoming)
  const positioned = positionRanks(ranks, byID)
  const normalizedQuery = query.trim().toLowerCase()
  const nodes = Array.from(positioned.entries()).map(([id, position]) => {
    const node = byID.get(id)!
    return {
      id,
      type: 'gitopsResource',
      position,
      data: {
        node,
        highlighted: normalizedQuery !== '' && matchesQuery(node, normalizedQuery),
      },
    }
  })

  const edges = visibleTree.edges
    .filter(edge => positioned.has(edge.source) && positioned.has(edge.target))
    .map(edge => ({
      id: `${edge.source}->${edge.target}`,
      source: edge.source,
      target: edge.target,
      type: 'smoothstep',
      // The legend reads back edge.data?.type to decide which rows to render —
      // ReactFlow doesn't preserve our custom `type` field at the top level
      // because it overloads `type` for the edge component identity ("smoothstep").
      data: { type: edge.type },
      markerEnd: {
        type: MarkerType.ArrowClosed,
        width: 16,
        height: 16,
        color: getEdgeColor(edge.type),
      },
      style: {
        stroke: getEdgeColor(edge.type),
        strokeWidth: edge.type === 'owns' ? 1.5 : 1.75,
      },
    }))

  return { nodes, edges }
}

function applyPreset(tree: GitOpsResourceTree, preset: GitOpsTreePreset, expandedGroups?: Set<string>): GitOpsResourceTree {
  if (preset === 'full') return tree
  if (preset === 'compact') return compactInfra(tree, expandedGroups)

  const byID = new Map(tree.nodes.map(node => [node.id, node]))
  const children = new Map<string, string[]>()
  for (const edge of tree.edges) {
    children.set(edge.source, [...(children.get(edge.source) ?? []), edge.target])
  }

  const keep = new Set<string>([tree.root.id])
  if (preset === 'app') {
    for (const edge of tree.edges) {
      if (edge.source === tree.root.id) keep.add(edge.target)
    }
  } else {
    for (const node of tree.nodes) {
      if (node.role === 'root' || WORKLOAD_KINDS.has(node.ref.kind) || WORKLOAD_CHILD_KINDS.has(node.ref.kind)) {
        keep.add(node.id)
      }
    }
    let changed = true
    while (changed) {
      changed = false
      for (const edge of tree.edges) {
        if (keep.has(edge.target) && !keep.has(edge.source)) {
          keep.add(edge.source)
          changed = true
        }
      }
    }
  }

  return {
    ...tree,
    nodes: tree.nodes.filter(node => keep.has(node.id)),
    edges: tree.edges.filter(edge => keep.has(edge.source) && keep.has(edge.target)),
    root: byID.get(tree.root.id) ?? tree.root,
  }
}

function applyGraphFilters(tree: GitOpsResourceTree, filters?: GitOpsTreeFilters): GitOpsResourceTree {
  if (!hasGitOpsTreeFilters(filters)) return tree

  const parent = new Map<string, string>()
  for (const edge of tree.edges) {
    if (!parent.has(edge.target)) parent.set(edge.target, edge.source)
  }

  const keep = new Set<string>([tree.root.id])
  for (const node of tree.nodes) {
    if (node.id === tree.root.id) continue
    if (matchesGitOpsTreeFilters(node, filters)) {
      let current: string | undefined = node.id
      while (current) {
        keep.add(current)
        current = parent.get(current)
      }
    }
  }

  return {
    ...tree,
    nodes: tree.nodes.filter(node => keep.has(node.id)),
    edges: tree.edges.filter(edge => keep.has(edge.source) && keep.has(edge.target)),
  }
}

function compactInfra(tree: GitOpsResourceTree, expandedGroups?: Set<string>): GitOpsResourceTree {
  const children = new Map<string, string[]>()
  const parent = new Map<string, string>()
  for (const edge of tree.edges) {
    children.set(edge.source, [...(children.get(edge.source) ?? []), edge.target])
    parent.set(edge.target, edge.source)
  }

  const groups = new Map<string, GitOpsTreeNode[]>()
  for (const node of tree.nodes) {
    if (node.id === tree.root.id || node.role === 'group' || !COMPACT_INFRA_KINDS.has(node.ref.kind)) continue
    if ((children.get(node.id) ?? []).length > 0) continue
    const p = parent.get(node.id)
    if (!p) continue
    const key = `${p}|${node.ref.kind}`
    groups.set(key, [...(groups.get(key) ?? []), node])
  }

  const remove = new Set<string>()
  const additions: GitOpsTreeNode[] = []
  const edgeAdditions: GitOpsResourceTree['edges'] = []
  for (const [key, nodes] of groups) {
    if (nodes.length < 2) continue
    const [p, kind] = key.split('|')
    const id = `${p}/compact/${kind}`
    // Per-group expand: when the user has clicked this group's bubble,
    // skip the collapse step for that group only — leave the children
    // visible alongside any other still-collapsed groups in the tree.
    if (expandedGroups?.has(id)) continue
    for (const node of nodes) remove.add(node.id)
    additions.push({
      id,
      ref: { kind, namespace: '', name: `${nodes.length} ${pluralize(kind)}` },
      role: 'group',
      tool: nodes[0].tool,
      topologyStatus: 'unknown',
      groupedNodeIDs: nodes.map(node => node.id),
      count: nodes.length,
      data: { groupedKind: kind },
    })
    edgeAdditions.push({ source: p, target: id, type: 'owns' })
  }

  if (remove.size === 0) return tree
  return {
    ...tree,
    nodes: [...tree.nodes.filter(node => !remove.has(node.id)), ...additions],
    edges: [
      ...tree.edges.filter(edge => !remove.has(edge.source) && !remove.has(edge.target)),
      ...edgeAdditions,
    ],
  }
}

function assignRanks(
  rootID: string,
  nodes: Map<string, GitOpsTreeNode>,
  children: Map<string, string[]>,
  incoming: Map<string, number>
): Map<number, string[]> {
  const rankByID = new Map<string, number>()
  const queue = [{ id: rootID, rank: 0 }]
  // Cycle/explosion guard: GitOps trees are owner-ref DAGs in practice, but
  // the EdgeManages set we feed in could pathologically include a cycle
  // (e.g. a CRD whose owner ref points back at one of its own children, or
  // a malformed inventory). Without this cap, the "previous >= rank" skip
  // never fires for cyclic edges and the loop runs forever, freezing the
  // UI thread. Cap at a large multiple of node count so legitimate trees
  // never hit it; cycles bail with the partial layout we have so far.
  let iterations = 0
  const maxIterations = nodes.size * 8 + 64

  while (queue.length > 0) {
    if (++iterations > maxIterations) break
    const current = queue.shift()!
    const previous = rankByID.get(current.id)
    if (previous !== undefined && previous >= current.rank) continue
    rankByID.set(current.id, current.rank)
    for (const child of children.get(current.id) ?? []) {
      queue.push({ id: child, rank: current.rank + 1 })
    }
  }

  for (const id of nodes.keys()) {
    if (!rankByID.has(id)) {
      rankByID.set(id, incoming.get(id) ? 1 : 0)
    }
  }

  const ranks = new Map<number, string[]>()
  for (const [id, rank] of rankByID.entries()) {
    ranks.set(rank, [...(ranks.get(rank) ?? []), id])
  }
  for (const ids of ranks.values()) {
    ids.sort((a, b) => compareTreeNodes(nodes.get(a), nodes.get(b)))
  }
  return ranks
}

function positionRanks(ranks: Map<number, string[]>, nodes: Map<string, GitOpsTreeNode>): Map<string, { x: number; y: number }> {
  const positioned = new Map<string, { x: number; y: number }>()
  const sortedRanks = Array.from(ranks.keys()).sort((a, b) => a - b)

  for (const rank of sortedRanks) {
    const ids = ranks.get(rank) ?? []
    ids.forEach((id, row) => {
      positioned.set(id, { x: rank * RANK_GAP, y: row * ROW_GAP })
    })
  }

  const rootRank = ranks.get(0) ?? []
  if (rootRank.length === 1) {
    const rootID = rootRank[0]
    const nextRank = ranks.get(1) ?? []
    if (nextRank.length > 0) {
      const maxY = (nextRank.length - 1) * ROW_GAP
      const rootHeight = getNodeDimensions(nodes.get(rootID)!).height
      positioned.set(rootID, { x: 0, y: Math.max(0, (maxY - rootHeight) / 2 - ROOT_Y_OFFSET) })
    }
  }

  return positioned
}

const GitOpsResourceNode = memo(function GitOpsResourceNode({ data }: NodeProps<Node<{ node: GitOpsTreeNode; highlighted?: boolean }>>) {
  const node = data.node
  const kind = normalizeDisplayKind(node)
  const status = normalizeHealth(node.topologyStatus)
  const terminating = isNodeTerminating(node)
  const gitopsTool = gitopsToolForNode(node)
  const chips = buildChips(node)
  const dim = getNodeDimensions(node)
  const KindIcon = getTopologyIcon(kind)

  return (
    <>
      <Handle type="target" position={Position.Left} className="!h-0 !w-0 !border-0 !bg-transparent" />
      <div
        className={clsx(
          'relative overflow-hidden rounded-lg border bg-theme-surface shadow-md transition-colors',
          data.highlighted ? 'border-skyhook-400 ring-2 ring-skyhook-400/40' : 'border-theme-border',
          // Lifecycle dominates the left-stripe color: a Terminating node
          // paints orange regardless of its frozen sync/health state. Same
          // logic as the fleet-row statusStripe — sync/health shouldn't
          // signal "fix me" when the resource is being deleted.
          terminating
            ? 'border-l-orange-500'
            : status === 'healthy' && 'border-l-green-500',
          !terminating && status === 'degraded' && 'border-l-yellow-500',
          !terminating && status === 'unhealthy' && 'border-l-red-500',
          !terminating && status === 'unknown' && 'border-l-slate-500',
          // GitOps portal nodes get a slightly stronger ring so the user's
          // eye picks them out from generic K8s leaves. Combined with the
          // tool-icon overlay + "→ Open" affordance below, this signals
          // "click me to dive into this CR's own GitOps view" without
          // making the node look broken or alarming.
          gitopsTool && !data.highlighted && 'ring-1 ring-skyhook-500/30 hover:ring-skyhook-500/60',
          gitopsTool && 'cursor-pointer',
          // Group nodes are interactive (click toggles expand) but the
          // default ReactFlow handler doesn't add a hover state. A subtle
          // border lift telegraphs "this responds to clicks" without
          // looking like a primary action.
          node.role === 'group' && 'cursor-pointer hover:border-theme-text-tertiary/50 hover:bg-theme-hover',
        )}
        style={{ width: dim.width, minHeight: dim.height, borderLeftWidth: 4 }}
      >
        <div className="px-3 py-2.5">
          <div className="mb-1 flex items-center gap-1.5">
            <KindIcon className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
            <span className="truncate text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">
              {node.role === 'group' ? displayKind((node.data?.groupedKind as string) || kind) : displayKind(kind)}
            </span>
            {/* Tool badge for GitOps portal nodes — sits between the kind
                label and the status dot, signaling "this is a managed-
                GitOps-app, not a leaf K8s resource". Keeps the dot's
                position so non-portal nodes look unchanged. */}
            {gitopsTool && (
              <span className="rounded-sm border border-skyhook-500/30 bg-skyhook-500/10 px-1 py-px text-[9px] font-semibold uppercase tracking-wide text-skyhook-300">
                {gitopsTool === 'argocd' ? 'Argo' : 'Flux'}
              </span>
            )}
            <span className={clsx('ml-auto h-1.5 w-1.5 rounded-full', getStatusDotColor(status))} />
          </div>
          <div className="truncate pr-1 text-sm font-medium text-theme-text-primary">{node.ref.name}</div>
          {/* Group nodes get a chevron at the bottom-right to advertise the
              click-to-expand affordance. GitOps portal nodes get a similar
              chevron at the right of the subtitle to signal "→ opens its
              own page". The subtitle text alone wasn't enough — users were
              treating the count as an immutable fact rather than a button. */}
          <div className="mt-0.5 flex items-center gap-1 text-xs text-theme-text-secondary">
            <span className="truncate">{getSubtitle(node)}</span>
            {(node.role === 'group' || gitopsTool) && <ChevronRight className="ml-auto h-3 w-3 shrink-0 text-theme-text-tertiary" />}
          </div>
          {chips.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1">
              {chips.slice(0, 4).map(chip => (
                <span
                  key={`${chip.label}:${chip.value}`}
                  className={clsx(
                    'max-w-[145px] truncate rounded border px-1.5 py-0.5 text-[10px] leading-3',
                    chip.tone === 'warning'
                      ? 'border-yellow-500/30 bg-yellow-500/10 text-yellow-700 dark:text-yellow-300'
                      : chip.tone === 'danger'
                        ? 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300'
                        : 'border-theme-border bg-theme-elevated/70 text-theme-text-secondary'
                  )}
                >
                  {chip.label ? `${chip.label}: ` : ''}{chip.value}
                </span>
              ))}
            </div>
          )}
        </div>
      </div>
      <Handle type="source" position={Position.Right} className="!h-0 !w-0 !border-0 !bg-transparent" />
    </>
  )
})

const nodeTypes: NodeTypes = {
  gitopsResource: GitOpsResourceNode,
}

// isNodeTerminating returns true when the backend tagged the node with
// metadata.deletionTimestamp (see enrichNodeFromObject in pkg/gitops/tree).
// The check tolerates older payloads that don't carry the field — they
// simply read as not-terminating, which is the safe default.
function isNodeTerminating(node: GitOpsTreeNode): boolean {
  return Boolean(node.data?.deletionTimestamp)
}

// gitopsToolForNode returns 'argocd' | 'fluxcd' | undefined depending on
// whether the backend classified this node as itself a GitOps detail-page
// CR (Argo Application/Set/Project, Flux Kustomization/HelmRelease/source).
// These nodes are *portals* — clicking them opens that CR's own GitOps
// detail page rather than the standard resource drawer. The visual
// treatment around them (tool icon overlay, "→" affordance, stronger
// border) advertises that they're not just leaves.
function gitopsToolForNode(node: GitOpsTreeNode): 'argocd' | 'fluxcd' | undefined {
  const t = node.data?.gitopsTool
  if (t === 'argocd' || t === 'fluxcd') return t
  return undefined
}

function buildChips(node: GitOpsTreeNode): Array<{ label?: string; value: string; tone?: 'neutral' | 'warning' | 'danger' }> {
  const data = node.data ?? {}
  const chips: Array<{ label?: string; value: string; tone?: 'neutral' | 'warning' | 'danger' }> = []
  // Terminating dominates: surface a single Terminating chip and skip the
  // sync/health chips entirely. Showing "OutOfSync" or "Degraded" alongside
  // a deletion-in-progress tag makes a stuck zombie look like a routine
  // sync problem — same misleading-state pattern we removed from the
  // detail title row and fleet rows.
  if (isNodeTerminating(node)) {
    chips.push({ value: 'Terminating', tone: 'warning' })
    if (typeof data.createdAt === 'string') chips.push({ label: 'age', value: formatAge(data.createdAt) })
    return chips
  }
  if (typeof data.createdAt === 'string') chips.push({ label: 'age', value: formatAge(data.createdAt) })
  const revision = stringData(data.revision) || stringData(data.lastSyncRevision)
  if (revision) chips.push({ label: 'rev', value: revision })
  const attempted = stringData(data.attemptedRevision)
  if (attempted && attempted !== revision) chips.push({ label: 'attempted', value: attempted, tone: 'warning' })
  const wave = stringData(data.syncWave)
  if (wave) chips.push({ label: 'wave', value: wave })
  const hook = stringData(data.hook)
  if (hook) chips.push({ value: hook })
  const relationship = stringData(data.relationship)
  if (relationship) chips.push({ value: relationship })
  if (node.sync === 'OutOfSync') chips.push({ value: 'OutOfSync', tone: 'warning' })
  if (node.health === 'Degraded' || node.health === 'Missing') chips.push({ value: node.health, tone: 'danger' })
  return chips
}

function getSubtitle(node: GitOpsTreeNode): string {
  if (node.role === 'group') {
    // Action-oriented copy invites the click; "collapsed" alone reads as
    // a state description, not an affordance.
    return 'Click to expand'
  }
  if (isNodeTerminating(node)) {
    return 'Pending deletion'
  }
  if (node.sync || node.health) {
    return [node.sync, node.health].filter(Boolean).join(' • ')
  }
  if (node.info?.[0]?.value) return node.info[0].value
  return node.ref.namespace || ''
}

function normalizeDisplayKind(node: GitOpsTreeNode): string {
  if (node.role === 'group' && node.ref.kind === 'Pod') return 'PodGroup'
  return node.ref.kind || 'PodGroup'
}

function normalizeHealth(status?: string): HealthStatus {
  if (status === 'healthy' || status === 'degraded' || status === 'unhealthy') return status
  return 'unknown'
}

function getStatusDotColor(status: HealthStatus): string {
  return SEVERITY_DOT[healthToSeverity(status)]
}

function getNodeDimensions(node: GitOpsTreeNode): { width: number; height: number } {
  if (node.role === 'group') return { width: GROUP_WIDTH, height: GROUP_HEIGHT }
  return { width: NODE_WIDTH, height: NODE_HEIGHT }
}

function matchesQuery(node: GitOpsTreeNode, query: string): boolean {
  return [
    node.ref.kind,
    node.ref.name,
    node.ref.namespace,
    node.ref.group,
    node.sync,
    node.health,
  ].some(value => String(value ?? '').toLowerCase().includes(query))
}

function compareTreeNodes(a?: GitOpsTreeNode, b?: GitOpsTreeNode): number {
  if (!a || !b) return 0
  const roleDiff = rolePriority(a.role) - rolePriority(b.role)
  if (roleDiff !== 0) return roleDiff
  const kindDiff = kindPriority(a.ref.kind) - kindPriority(b.ref.kind)
  if (kindDiff !== 0) return kindDiff
  return `${a.ref.namespace}/${a.ref.name}`.localeCompare(`${b.ref.namespace}/${b.ref.name}`)
}

function rolePriority(role: string): number {
  switch (role) {
    case 'root': return 0
    case 'declared': return 1
    case 'generated': return 2
    case 'group': return 3
    default: return 4
  }
}

function kindPriority(kind: string): number {
  const priorities: Record<string, number> = {
    Namespace: 0,
    AppProject: 1,
    ServiceAccount: 2,
    Secret: 3,
    SealedSecret: 3,
    ConfigMap: 4,
    CustomResourceDefinition: 5,
    ClusterRole: 6,
    ClusterRoleBinding: 7,
    Role: 8,
    RoleBinding: 9,
    Service: 10,
    Deployment: 11,
    StatefulSet: 11,
    DaemonSet: 11,
    ReplicaSet: 12,
    Pod: 13,
    Ingress: 14,
    Gateway: 14,
    HTTPRoute: 15,
  }
  return priorities[kind] ?? 20
}

function stringData(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function pluralize(kind: string): string {
  if (kind.endsWith('s')) return kind
  if (kind.endsWith('y')) return `${kind.slice(0, -1)}ies`
  return `${kind}s`
}

function formatAge(timestamp: string): string {
  return formatCompactAge(timestamp)
}
