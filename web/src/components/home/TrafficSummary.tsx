import { useId } from 'react'
import type { DashboardTrafficSummary } from '../../api/client'
import { Activity } from 'lucide-react'
import { useElementSize } from '../../hooks/useElementSize'
import { NAMESPACE_PALETTE } from '../../utils/traffic-colors'
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

// The drawing's height in its own units. Its width follows the card, up to a
// limit, so the picture fills a wide card instead of floating in the middle.
const SKETCH_H = 128
const SKETCH_MIN_W = 296
const SKETCH_MAX_W = 560
const NODE_W = 58
const NODE_H = 24

// Example namespaces shared with the Topology card's picture, coloured from the
// Traffic view's own palette.
const NAMESPACES = {
  storefront: NAMESPACE_PALETTE[1],
  payments: NAMESPACE_PALETTE[3],
}

type Column = 0 | 1 | 2 | 3
interface SketchNode { col: Column; y: number; name: string; namespace: keyof typeof NAMESPACES; port: number }

const NODES: Record<string, SketchNode> = {
  frontend: { col: 1, y: 64, name: 'frontend', namespace: 'storefront', port: 8080 },
  cart: { col: 2, y: 22, name: 'cart', namespace: 'storefront', port: 7070 },
  catalog: { col: 2, y: 64, name: 'catalog', namespace: 'storefront', port: 3550 },
  checkout: { col: 2, y: 106, name: 'checkout', namespace: 'payments', port: 5050 },
  redis: { col: 3, y: 22, name: 'redis', namespace: 'storefront', port: 6379 },
  payment: { col: 3, y: 106, name: 'payment', namespace: 'payments', port: 50051 },
}

const EDGES: [from: string, to: string, count: number][] = [
  ['internet', 'frontend', 182],
  ['frontend', 'cart', 72],
  ['frontend', 'catalog', 32],
  ['frontend', 'checkout', 28],
  ['cart', 'redis', 49],
  ['checkout', 'payment', 10],
]

const INTERNET = { x: 4, y: 64, w: 44 }

// A right-angled route with rounded corners, the shape the Traffic view draws.
function route(x1: number, y1: number, x2: number, y2: number): string {
  if (y1 === y2) return `M ${x1} ${y1} H ${x2}`
  const mx = (x1 + x2) / 2
  const r = Math.min(3, Math.abs(y2 - y1) / 2)
  const dy = y2 > y1 ? r : -r
  return `M ${x1} ${y1} H ${mx - r} Q ${mx} ${y1} ${mx} ${y1 + dy} V ${y2 - dy} Q ${mx} ${y2} ${mx + r} ${y2} H ${x2}`
}

// A static picture of what Live Traffic draws: services as boxes in their
// namespace's colour, reached on a port, with connection counts on the lines.
// It is a hint at the view, not this cluster's traffic, so it never moves and
// carries no health colour.
function TrafficSketch() {
  const arrow = `${useId()}-arrow`
  const [ref, size] = useElementSize<HTMLDivElement>({ width: SKETCH_MIN_W, height: SKETCH_H })
  const width = Math.min(SKETCH_MAX_W, Math.max(SKETCH_MIN_W, (SKETCH_H * size.width) / size.height))
  // Extra width spreads the columns apart rather than stretching the boxes.
  const extra = width - SKETCH_MIN_W
  const colX = (c: Column) => [66, 146, 226][c - 1] + (extra * c) / 3

  const anchor = (key: string) => key === 'internet'
    ? { left: INTERNET.x, right: INTERNET.x + INTERNET.w, y: INTERNET.y }
    : { left: colX(NODES[key].col), right: colX(NODES[key].col) + NODE_W, y: NODES[key].y }

  return (
    <div ref={ref} className="w-full h-full">
      <svg viewBox={`0 0 ${width} ${SKETCH_H}`} className="w-full h-full" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
        <defs>
          <marker id={arrow} viewBox="0 0 8 6" refX="7" refY="3" markerWidth="5" markerHeight="4" orient="auto">
            <path d="M 0 0 L 8 3 L 0 6 z" fill="var(--text-tertiary)" />
          </marker>
        </defs>

        {EDGES.map(([from, to, count]) => {
          const a = anchor(from)
          const b = anchor(to)
          const x1 = a.right
          const x2 = b.left - 1.5
          // The count sits on the destination's run, past any fan-out bend,
          // where the Traffic view puts it.
          const labelX = ((x1 + x2) / 2 + x2) / 2
          const labelW = 3 + String(count).length * 2.8
          return (
            <g key={`${from}-${to}`}>
              <path d={route(x1, a.y, x2, b.y)} fill="none" stroke="var(--text-tertiary)" strokeOpacity={0.7}
                strokeWidth={0.6 + count / 182} markerEnd={`url(#${arrow})`} />
              <rect x={labelX - labelW / 2} y={b.y - 3.5} width={labelW} height={7} rx={1.5} className="fill-gray-800/90" />
              <text x={labelX} y={b.y + 1.6} textAnchor="middle" fontSize={4.6} className="fill-gray-300">{count}</text>
            </g>
          )
        })}

        <rect x={INTERNET.x} y={INTERNET.y - NODE_H / 2} width={INTERNET.w} height={NODE_H} rx={4}
          className="fill-sky-500/20 stroke-sky-500/50" strokeWidth={0.6} />
        <text x={INTERNET.x + 5} y={INTERNET.y - 1.5} fontSize={5.6} fontWeight={600} className="fill-sky-700 dark:fill-sky-300">Internet</text>
        <text x={INTERNET.x + 5} y={INTERNET.y + 5} fontSize={4.4} className="fill-sky-700/70 dark:fill-sky-300/70">inbound</text>

        {Object.entries(NODES).map(([key, n]) => {
          const x = colX(n.col)
          const top = n.y - NODE_H / 2
          return (
            <g key={key}>
              <rect x={x} y={top} width={NODE_W} height={NODE_H} rx={4} fill={NAMESPACES[n.namespace]} />
              <text x={x + 5} y={top + 7.6} fontSize={5.6} fontWeight={600} className="fill-white">{n.name}</text>
              <text x={x + 5} y={top + 13.4} fontSize={4.4} className="fill-white/70">{n.namespace}</text>
              <text x={x + 5} y={top + 20} fontSize={4.4} fontFamily="var(--font-mono, monospace)" className="fill-cyan-300">:{n.port}</text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}

export function TrafficSummary({ data, onNavigate }: TrafficSummaryProps) {
  // The server names a source only once Radar has detected one, so no
  // name means "not known yet", never "none installed".
  return (
    <HomeDiagramCard
      icon={Activity}
      title="Live Traffic"
      fact={data ? trafficSourceName(data.source) : undefined}
      drawing={<TrafficSketch />}
      caption="Which services talk to each other, on which ports, and how much."
      footer="Open Live Traffic"
      onNavigate={onNavigate}
    />
  )
}
