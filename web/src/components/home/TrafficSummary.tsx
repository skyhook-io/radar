import { useId } from 'react'
import type { DashboardTopFlow, DashboardTrafficSummary } from '../../api/client'
import { Activity } from 'lucide-react'
import { HomeDiagramCard } from './HomeDiagramCard'

interface TrafficSummaryProps {
  data: DashboardTrafficSummary | null
  onNavigate: () => void
}

const SOURCE_NAMES: Record<string, string> = {
  hubble: 'Hubble',
  beyla: 'Beyla',
  caretta: 'Caretta',
  istio: 'Istio',
}

export function trafficSourceName(source: string): string {
  return SOURCE_NAMES[source] ?? source.charAt(0).toUpperCase() + source.slice(1)
}

const VIEW_W = 300
const VIEW_H = 130
const LABEL_SIZE = 10
// DM Mono's advance is 0.6em, so a label's width follows from its length.
const LABEL_ADVANCE = LABEL_SIZE * 0.6
const NODE_R = 6.5
const MARGIN = 24
// How far an outer column's label starts outside its circle's centre.
const LABEL_OUTSET = NODE_R + 4

const curve = (x1: number, y1: number, x2: number, y2: number, k = 26) =>
  `M ${x1} ${y1} C ${x1 + k} ${y1}, ${x2 - k} ${y2}, ${x2} ${y2}`

// The empty-state picture: services as circles with traffic between them. It
// is an illustration, not observed traffic, so it does not move.
function IllustrativeTraffic() {
  const pts: Record<string, [number, number]> = {
    web: [22, 62], a: [104, 26], b: [104, 62], c: [104, 100], d: [196, 16],
    e: [196, 50], f: [196, 84], g: [272, 36], h: [272, 100],
  }
  const edges: [string, string, number][] = [
    ['web', 'a', 3.2], ['web', 'b', 2.2], ['web', 'c', 1.4], ['a', 'd', 2.4], ['a', 'e', 1.2],
    ['b', 'e', 1.8], ['b', 'f', 1], ['c', 'f', 1.4], ['d', 'g', 1.6], ['f', 'h', 1.1],
  ]
  return (
    <svg viewBox={`0 0 ${VIEW_W} ${VIEW_H}`} className="w-full h-full" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
      {edges.map(([p, q, w]) => {
        const [x1, y1] = pts[p]
        const [x2, y2] = pts[q]
        return (
          <path key={`${p}-${q}`} d={curve(x1 + 7, y1, x2 - 7, y2)} fill="none" stroke="var(--traffic-flow)"
            strokeOpacity={0.3} strokeWidth={w * 0.8 + 0.6} strokeLinecap="round" />
        )
      })}
      {Object.entries(pts).map(([k, [x, y]]) => {
        if (k === 'web') return <InternetNode key={k} x={x} y={y} />
        const external = k === 'g' || k === 'h'
        return (
          <g key={k}>
            <circle cx={x} cy={y} r={NODE_R} fill="var(--bg-surface)" strokeWidth={1}
              stroke={external ? 'var(--text-tertiary)' : 'var(--traffic-flow)'} strokeDasharray={external ? '1.6 1.2' : undefined} />
            <circle cx={x} cy={y} r={2} fill={external ? 'var(--text-tertiary)' : 'var(--traffic-flow)'} />
          </g>
        )
      })}
    </svg>
  )
}

function InternetNode({ x, y }: { x: number; y: number }) {
  return (
    <g>
      <circle cx={x} cy={y} r={8} fill="var(--bg-surface)" stroke="var(--color-brand)" strokeWidth={1} />
      <circle cx={x} cy={y} r={3.4} fill="none" stroke="var(--color-brand)" strokeWidth={0.8} />
      <path d={`M${x - 3.4} ${y}h6.8M${x} ${y - 3.4}c-2 2-2 4.8 0 6.8M${x} ${y - 3.4}c2 2 2 4.8 0 6.8`}
        fill="none" stroke="var(--color-brand)" strokeWidth={0.6} />
    </g>
  )
}

export interface FlowNode {
  key: string
  name: string
  // The name to show: namespace-qualified when another endpoint shares the name.
  label: string
  namespace?: string
  x: number
  y: number
  column: number
  columns: number
}

export interface FlowEdge {
  from: FlowNode
  to: FlowNode
  connections: number
  directionUnknown: boolean
}

const endpointKey = (name: string, namespace?: string) => `${namespace ?? ''}/${name}`

// Lays the top flows out left to right: an endpoint sits one column right of
// the first endpoint found sending to it, so a chain reads as a chain. An
// endpoint named in two flows is one circle; the same name in two namespaces is
// two.
export function layoutTopFlows(flows: DashboardTopFlow[]): { nodes: FlowNode[]; edges: FlowEdge[] } {
  const info = new Map<string, { name: string; namespace?: string }>()
  const out = new Map<string, string[]>()
  const incoming = new Set<string>()
  const order: string[] = []
  // Radar servers that predate per-pair summing can name one pair twice.
  const pairs = new Map<string, { src: string; dst: string; connections: number; directionUnknown: boolean }>()
  for (const f of flows) {
    const src = endpointKey(f.src, f.srcNamespace)
    const dst = endpointKey(f.dst, f.dstNamespace)
    for (const [k, name, namespace] of [[src, f.src, f.srcNamespace], [dst, f.dst, f.dstNamespace]] as const) {
      if (!info.has(k)) {
        info.set(k, { name, namespace })
        order.push(k)
      }
    }
    const pairKey = `${src}>${dst}`
    const pair = pairs.get(pairKey)
    if (pair) {
      pair.connections += f.connections
      pair.directionUnknown ||= f.directionUnknown === true
      continue
    }
    pairs.set(pairKey, { src, dst, connections: f.connections, directionUnknown: f.directionUnknown === true })
    if (src !== dst) {
      out.set(src, [...(out.get(src) ?? []), dst])
      incoming.add(dst)
    }
  }

  // Breadth-first from endpoints nothing sends to, then from whatever a cycle
  // left unplaced. First visit wins, so a cycle cannot push columns outward.
  const column = new Map<string, number>()
  const visit = (root: string) => {
    column.set(root, 0)
    const queue = [root]
    while (queue.length > 0) {
      const k = queue.shift()!
      for (const next of out.get(k) ?? []) {
        if (column.has(next)) continue
        column.set(next, column.get(k)! + 1)
        queue.push(next)
      }
    }
  }
  for (const k of order) if (!incoming.has(k) && !column.has(k)) visit(k)
  for (const k of order) if (!column.has(k)) visit(k)

  const columns = Math.max(...column.values()) + 1
  const byColumn: string[][] = Array.from({ length: columns }, () => [])
  for (const k of order) byColumn[column.get(k)!].push(k)

  const xAt = (c: number) => columns === 1 ? VIEW_W / 2 : MARGIN + (c * (VIEW_W - 2 * MARGIN)) / (columns - 1)
  const nameUses = new Map<string, number>()
  for (const { name } of info.values()) nameUses.set(name, (nameUses.get(name) ?? 0) + 1)
  const nodes = new Map<string, FlowNode>()
  byColumn.forEach((keys, c) => {
    const gap = Math.min(40, (VIEW_H - 24) / keys.length)
    keys.forEach((k, i) => {
      const { name, namespace } = info.get(k)!
      const y = VIEW_H / 2 - 6 + (i - (keys.length - 1) / 2) * gap
      const label = namespace && nameUses.get(name)! > 1 ? `${namespace}/${name}` : name
      nodes.set(k, { key: k, name, label, namespace, x: xAt(c), y, column: c, columns })
    })
  })

  const edges = [...pairs.values()].map(p => ({
    from: nodes.get(p.src)!,
    to: nodes.get(p.dst)!,
    connections: p.connections,
    directionUnknown: p.directionUnknown,
  }))
  return { nodes: [...nodes.values()], edges }
}

// Room for a label under its circle. Outer columns anchor at the circle's
// outside edge and inner ones are centred; either way a label stops at the
// midpoint to its neighbour, so labels at the same height never overlap.
export function fitLabel(name: string, node: Pick<FlowNode, 'column' | 'columns'>): string {
  let room = VIEW_W - 16
  if (node.columns > 1) {
    const spacing = (VIEW_W - 2 * MARGIN) / (node.columns - 1)
    const outer = node.column === 0 || node.column === node.columns - 1
    room = outer ? spacing / 2 + LABEL_OUTSET - 3 : spacing - 6
  }
  const max = Math.max(4, Math.floor(room / LABEL_ADVANCE))
  return name.length <= max ? name : `${name.slice(0, max - 1)}…`
}

const fullName = (n: FlowNode) => n.namespace ? `${n.namespace}/${n.name}` : n.name

function edgePath(a: FlowNode, b: FlowNode): string {
  // Traffic between replicas of one workload: a loop over its circle.
  if (a === b) return `M ${a.x - 5} ${a.y - 4} C ${a.x - 26} ${a.y - 36}, ${a.x + 26} ${a.y - 36}, ${a.x + 5} ${a.y - NODE_R - 1}`
  if (b.x > a.x) return curve(a.x + NODE_R, a.y, b.x - NODE_R - 2, b.y)
  // Backward or within a column: bow out so the line clears the circles.
  const mx = (a.x + b.x) / 2 + (a.x === b.x ? 26 : 0)
  const my = (a.y + b.y) / 2 - (a.x === b.x ? 0 : 26)
  return `M ${a.x} ${a.y - NODE_R} Q ${mx} ${my}, ${b.x} ${b.y - NODE_R - 2}`
}

function ObservedTraffic({ flows }: { flows: DashboardTopFlow[] }) {
  const arrow = `${useId()}-arrow`
  const { nodes, edges } = layoutTopFlows(flows)
  const maxConnections = Math.max(1, ...edges.map(e => e.connections))
  return (
    <svg viewBox={`0 0 ${VIEW_W} ${VIEW_H}`} className="w-full h-full" preserveAspectRatio="xMidYMid meet" role="img"
      aria-label={edges.map(e => e.directionUnknown
        ? `between ${fullName(e.from)} and ${fullName(e.to)}, direction unknown`
        : `${fullName(e.from)} to ${fullName(e.to)}`).join('; ')}>
      <defs>
        <marker id={arrow} viewBox="0 0 8 6" refX="6" refY="3" markerWidth="6" markerHeight="4.5" markerUnits="userSpaceOnUse" orient="auto">
          <path d="M 0 0 L 8 3 L 0 6 z" fill="var(--traffic-flow)" />
        </marker>
      </defs>
      {edges.map((e, i) => {
        const d = edgePath(e.from, e.to)
        const width = 1.2 + 2.4 * Math.sqrt(e.connections / maxConnections)
        return (
          <g key={i}>
            <path d={d} fill="none" stroke="var(--traffic-flow)" strokeOpacity={0.22} strokeWidth={width + 1.2}
              strokeLinecap="round" markerEnd={e.directionUnknown ? undefined : `url(#${arrow})`} />
            {!e.directionUnknown && (
              <path d={d} fill="none" stroke="var(--traffic-flow)" strokeWidth={Math.max(1.2, width * 0.75)}
                strokeLinecap="round" className="animate-traffic-dash" data-testid="traffic-dash" />
            )}
          </g>
        )
      })}
      {nodes.map(n => {
        const anchor = n.columns > 1 && n.column === 0 ? 'start' : n.columns > 1 && n.column === n.columns - 1 ? 'end' : 'middle'
        const lx = anchor === 'start' ? n.x - LABEL_OUTSET : anchor === 'end' ? n.x + LABEL_OUTSET : n.x
        return (
          <g key={n.key}>
            <title>{fullName(n)}</title>
            <circle cx={n.x} cy={n.y} r={NODE_R} fill="var(--bg-surface)" stroke="var(--traffic-flow)" strokeWidth={1} />
            <circle cx={n.x} cy={n.y} r={2} fill="var(--traffic-flow)" />
            <text x={lx} y={n.y + NODE_R + LABEL_SIZE + 1} textAnchor={anchor} fontSize={LABEL_SIZE}
              fontFamily="var(--font-mono)" fill="var(--text-secondary)">
              {fitLabel(n.label, n)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}

function LiveDot() {
  return (
    <span className="relative inline-flex h-1.5 w-1.5">
      <span className="absolute inline-flex h-full w-full rounded-full bg-traffic opacity-60 animate-ping" />
      <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-traffic" />
    </span>
  )
}

export function TrafficSummary({ data, onNavigate }: TrafficSummaryProps) {
  const source = data ? trafficSourceName(data.source) : null
  const observed = data && data.flowCount > 0 && data.topFlows.length > 0

  let fact
  let caption
  if (!data) {
    caption = 'Who is talking to whom, live. Needs a traffic source such as Hubble, Beyla, Caretta or Istio.'
  } else if (!observed) {
    fact = source
    // The server reports a failed read as zero flows too, so this says only
    // that there is nothing to show, not that the cluster is quiet.
    caption = `Who is talking to whom, live, from ${source}. Nothing to show right now.`
  } else {
    fact = (
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-flex items-center gap-1.5 font-medium text-traffic">
          <LiveDot />
          Live
        </span>
        <span>&middot; {source} &middot; {data.flowCount} {data.flowCount === 1 ? 'flow' : 'flows'}</span>
      </span>
    )
    caption = `The busiest paths ${source} reports right now.`
  }

  return (
    <HomeDiagramCard
      icon={Activity}
      title="Live Traffic"
      fact={fact}
      drawing={observed ? <ObservedTraffic flows={data.topFlows} /> : <IllustrativeTraffic />}
      caption={caption}
      footer="Open Live Traffic"
      onNavigate={onNavigate}
    />
  )
}
