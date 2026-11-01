import { useId, useMemo } from 'react'
import type { Topology } from '../../types'
import { Network } from 'lucide-react'
import { HomeDiagramCard } from './HomeDiagramCard'

interface TopologyPreviewProps {
  topology: Topology | null
  namespaceSelected: boolean
  onNavigate: () => void
}

// One node drawn like a Topology card: surface, border, a health bar on the
// left, the kind as a caption and a bar standing in for the name. Topology
// marks kind with an icon, not a color. Every bar is healthy: a made-up problem
// next to this cluster's real counts would read as one of its own.
function SchematicNode({ x, y, w = 46, label, dashed }: {
  x: number; y: number; w?: number; label: string; dashed?: boolean
}) {
  return (
    <g>
      <rect x={x} y={y} width={w} height={17} rx={3.5} fill="var(--bg-surface)"
        stroke="var(--border-light)" strokeWidth={0.7} strokeDasharray={dashed ? '2 1.5' : undefined} />
      <rect x={x} y={y + 2.5} width={1.8} height={12} rx={0.9} fill="var(--color-success)" />
      <rect x={x + 4.8} y={y + 4.4} width={3.6} height={3.6} rx={0.9} fill="var(--text-tertiary)" opacity={0.55} />
      <text x={x + 10.5} y={y + 7.8} fontSize={4.6} fontWeight={600} letterSpacing={0.3} fill="var(--text-tertiary)">{label}</text>
      <rect x={x + 4.8} y={y + 10.8} width={w * 0.55} height={2.4} rx={1.2} fill="var(--text-tertiary)" opacity={0.35} />
    </g>
  )
}

// A static picture of what Topology draws: traffic entering an Ingress and
// fanning out through Services to workloads, in two namespace groups. It is a
// hint at the view, not a rendering of this cluster, so it never moves and
// looks the same whatever the cluster's size.
function TopologySchematic() {
  const arrow = `${useId()}-arrow`
  const edge = (x1: number, y1: number, x2: number, y2: number, dashed?: boolean) => (
    <path d={`M ${x1} ${y1} C ${x1 + 10} ${y1}, ${x2 - 10} ${y2}, ${x2} ${y2}`} fill="none"
      stroke="var(--text-tertiary)" strokeWidth={0.7} opacity={dashed ? 0.35 : 0.55}
      strokeDasharray={dashed ? '2 1.5' : undefined} markerEnd={`url(#${arrow})`} />
  )
  const group = (y: number, h: number, name: string) => (
    <g>
      <rect x={46} y={y} width={248} height={h} rx={7} fill="var(--group-header-namespace)" fillOpacity={0.45}
        stroke="var(--group-border-namespace)" strokeWidth={0.7} strokeDasharray="3 2" />
      <text x={52} y={y + 8} fontSize={4.6} fontFamily="var(--font-mono, monospace)" fill="var(--text-tertiary)">{name}</text>
    </g>
  )

  return (
    <svg viewBox="0 0 300 132" className="w-full h-full" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
      <defs>
        <marker id={arrow} viewBox="0 0 8 6" refX="7" refY="3" markerWidth="5" markerHeight="4" orient="auto">
          <path d="M 0 0 L 8 3 L 0 6 z" fill="var(--text-tertiary)" opacity="0.7" />
        </marker>
      </defs>

      {group(4, 62, 'storefront')}
      {group(72, 56, 'payments')}

      <rect x={4} y={54} width={32} height={16} rx={8} fill="var(--bg-surface)" stroke="var(--border-light)" strokeWidth={0.7} />
      <circle cx={11.5} cy={62} r={2.5} fill="none" stroke="var(--color-brand)" strokeWidth={0.8} />
      <text x={16} y={63.6} fontSize={4.6} fontWeight={600} fill="var(--text-tertiary)">WEB</text>

      {edge(36, 62, 54, 36)}
      {edge(100, 36, 112, 36)}
      {edge(100, 36, 112, 102)}
      {edge(158, 36, 170, 24)}
      {edge(158, 36, 170, 48)}
      {edge(216, 24, 228, 24)}
      {edge(216, 48, 228, 48)}
      {edge(158, 102, 170, 90)}
      {edge(158, 102, 170, 112)}
      {edge(216, 90, 228, 90)}
      {edge(216, 112, 228, 112, true)}

      <SchematicNode x={54} y={28} label="INGRESS" />
      <SchematicNode x={112} y={28} label="SERVICE" />
      <SchematicNode x={170} y={16} label="DEPLOY" />
      <SchematicNode x={170} y={40} label="DEPLOY" />
      <SchematicNode x={228} y={16} w={56} label="PODS ×3" />
      <SchematicNode x={228} y={40} w={56} label="PODS ×2" />

      <SchematicNode x={112} y={94} label="SERVICE" />
      <SchematicNode x={170} y={82} label="DEPLOY" />
      <SchematicNode x={170} y={104} label="STATEFUL" />
      <SchematicNode x={228} y={82} w={56} label="PODS ×2" />
      <SchematicNode x={228} y={104} w={56} label="CONFIG" dashed />
    </svg>
  )
}

export function TopologyPreview({ topology, namespaceSelected, onNavigate }: TopologyPreviewProps) {
  // A large cluster with no namespace filter gets an empty graph carrying this
  // flag instead of a build, and Topology asks for a namespace. The card keeps
  // its drawing but states no counts, which would read the refusal as zero.
  //
  // With a pick already active the same flag means something else entirely: SSE
  // only learns to filter server-side after a flagged frame arrives, so a
  // cluster-wide refusal still reaches a viewer whose namespace is on its way
  // to the server. That resolves on its own, so the header keeps loading.
  const needsNamespaceFilter = topology?.requiresNamespaceFilter === true && !namespaceSelected

  // Node count is not the test for whether a frame counts — the stream holds
  // topology at null until one lands and resets it there on reconnect and
  // context switch, so a delivered graph with no nodes is an empty scope and an
  // honest zero.
  const counts = useMemo(() => {
    if (!topology || topology.requiresNamespaceFilter) return null
    return { nodeCount: topology.nodes.length, edgeCount: topology.edges.length }
  }, [topology])

  const fact = counts ? (
    <>{counts.nodeCount} resources &middot; {counts.edgeCount} relationships</>
  ) : needsNamespaceFilter ? undefined : (
    <span data-testid="topology-counts-loading" className="inline-block h-3 w-28 rounded bg-theme-text-tertiary/20 animate-pulse align-middle" />
  )

  return (
    <HomeDiagramCard
      icon={Network}
      title="Topology"
      fact={fact}
      drawing={<TopologySchematic />}
      caption="How ingresses, services and workloads connect, grouped by namespace."
      footer="Open Topology"
      onNavigate={onNavigate}
    />
  )
}
