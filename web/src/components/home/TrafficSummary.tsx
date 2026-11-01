import { useId } from 'react'
import type { DashboardTopFlow, DashboardTrafficSummary } from '../../api/client'
import { Activity } from 'lucide-react'
import { useElementSize } from '../../hooks/useElementSize'
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
const NODE_R = 6.5
// Space kept between two labels on one line, so they never read as one name.
const LABEL_GAP = 16

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
  namespace?: string
  // What the circle is labelled: namespace-qualified when another endpoint
  // shares the name, then cut to the room the layout leaves it.
  label: string
  labelX: number
  labelY: number
  labelAnchor: 'start' | 'middle' | 'end'
  x: number
  y: number
  column: number
}

export interface FlowEdge {
  from: FlowNode
  to: FlowNode
  connections: number
  directionUnknown: boolean
}

export interface FlowLayout {
  nodes: FlowNode[]
  edges: FlowEdge[]
  radius: number
  labelSize: number
}

export interface DrawingSize {
  width: number
  height: number
}

const endpointKey = (name: string, namespace?: string) => `${namespace ?? ''}/${name}`

// Cuts a label to the room it has. DM Mono's advance is 0.6em, so a label's
// width follows from its length.
export function fitLabel(name: string, room: number, labelSize: number): string {
  const max = Math.floor(room / (labelSize * 0.6))
  if (name.length <= max) return name
  // Too little room to say anything; the circle's title still names it.
  return max < 4 ? '' : `${name.slice(0, max - 1)}…`
}

// Lays the top flows out left to right in the drawing's own pixels: an
// endpoint sits one column right of the first endpoint found sending to it, so
// a chain reads as a chain. An endpoint named in two flows is one circle; the
// same name in two namespaces is two. Fewer endpoints get bigger circles and
// labels, and the whole picture is centred, so one flow looks as deliberate as
// three.
export function layoutTopFlows(flows: DashboardTopFlow[], size: DrawingSize = { width: 300, height: 130 }): FlowLayout {
  const info = new Map<string, { name: string; namespace?: string }>()
  const out = new Map<string, string[]>()
  const incoming = new Set<string>()
  const looped = new Set<string>()
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
    if (src === dst) {
      looped.add(src)
    } else {
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
  const rows = Math.max(...byColumn.map(c => c.length))

  const { width, height } = size
  const radius = order.length <= 2 ? 11 : order.length <= 4 ? 9 : 7.5
  const labelSize = order.length <= 2 ? 13 : order.length <= 4 ? 12 : 11
  const outset = radius + 4
  // A loop over an outer circle needs room beside it too.
  const margin = looped.size > 0 ? radius * 2.6 + 6 : radius + 6
  const spacing = columns === 1 ? 0 : (width - 2 * margin) / (columns - 1)
  const xAt = (c: number) => (columns === 1 ? width / 2 : margin + c * spacing)
  const rowGap = Math.min(radius * 2 + labelSize + 28, (height - labelSize - 16) / rows)

  // Outer columns label below their circles. An inner column labels its upper
  // circles above and its lower ones below, keeping its labels off the outer
  // columns' line; a lone inner circle labels above in odd columns. A circle
  // with a loop over it keeps its label below.
  const labelAbove = (c: number, i: number, k: string) => {
    if (c === 0 || c === columns - 1 || looped.has(k)) return false
    const count = byColumn[c].length
    return count === 1 ? c % 2 === 1 : i < count / 2
  }

  const nameUses = new Map<string, number>()
  for (const { name } of info.values()) nameUses.set(name, (nameUses.get(name) ?? 0) + 1)
  const placed: (FlowNode & { full: string })[] = []
  byColumn.forEach((keys, c) => {
    keys.forEach((k, i) => {
      const { name, namespace } = info.get(k)!
      const x = xAt(c)
      const y = (i - (keys.length - 1) / 2) * rowGap
      const anchor = columns > 1 && c === 0 ? 'start' : columns > 1 && c === columns - 1 ? 'end' : 'middle'
      placed.push({
        key: k, name, namespace, x, y, column: c, label: '',
        full: namespace && nameUses.get(name)! > 1 ? `${namespace}/${name}` : name,
        labelX: anchor === 'start' ? x - outset : anchor === 'end' ? x + outset : x,
        labelY: labelAbove(c, i, k) ? y - radius - 5 : y + radius + labelSize + 1,
        labelAnchor: anchor,
      })
    })
  })

  // Centre everything that is drawn: circles, loops over them and labels.
  const top = Math.min(...placed.map(n => Math.min(n.y - radius * (looped.has(n.key) ? 4.2 : 1), n.labelY - labelSize)))
  const bottom = Math.max(...placed.map(n => Math.max(n.y + radius, n.labelY + 3)))
  const shift = height / 2 - (top + bottom) / 2
  for (const n of placed) {
    n.y += shift
    n.labelY += shift
  }

  // A label may reach the card's edge, stops short of any circle beside it,
  // and shares a line with another label only up to the midpoint between
  // them. A second pass, one label at a time against the others' current
  // extents, lets a cut label take room a short neighbour left unused.
  const advance = labelSize * 0.6
  const extent = (n: (typeof placed)[number]) => {
    const w = n.label.length * advance
    const left = n.labelAnchor === 'start' ? n.labelX : n.labelAnchor === 'end' ? n.labelX - w : n.labelX - w / 2
    return [left, left + w]
  }
  const fit = (n: (typeof placed)[number], limit: (o: (typeof placed)[number], toRight: boolean) => number) => {
    let left = 4
    let right = width - 4
    for (const o of placed) {
      if (o === n) continue
      if (o.x !== n.labelX && o.y + radius > n.labelY - labelSize && o.y - radius < n.labelY + 3) {
        if (o.x > n.labelX) right = Math.min(right, o.x - radius - LABEL_GAP / 2)
        else left = Math.max(left, o.x + radius + LABEL_GAP / 2)
      }
      if (Math.abs(o.labelY - n.labelY) >= labelSize + 2 || o.labelX === n.labelX) continue
      if (o.labelX > n.labelX) right = Math.min(right, limit(o, true))
      else left = Math.max(left, limit(o, false))
    }
    const room = n.labelAnchor === 'start' ? right - n.labelX
      : n.labelAnchor === 'end' ? n.labelX - left
        : 2 * Math.min(n.labelX - left, right - n.labelX)
    return fitLabel(n.full, room, labelSize)
  }
  for (const n of placed) {
    n.label = fit(n, (o, toRight) => (o.labelX + n.labelX) / 2 + (toRight ? -LABEL_GAP / 2 : LABEL_GAP / 2))
  }
  for (const n of placed) {
    n.label = fit(n, (o, toRight) => toRight ? extent(o)[0] - LABEL_GAP : extent(o)[1] + LABEL_GAP)
  }
  const nodes: FlowNode[] = placed.map(({ full: _full, ...n }) => n)
  const byKey = new Map(nodes.map(n => [n.key, n]))

  const edges = [...pairs.values()].map(p => ({
    from: byKey.get(p.src)!,
    to: byKey.get(p.dst)!,
    connections: p.connections,
    directionUnknown: p.directionUnknown,
  }))
  return { nodes, edges, radius, labelSize }
}

const fullName = (n: FlowNode) => n.namespace ? `${n.namespace}/${n.name}` : n.name

function edgePath(a: FlowNode, b: FlowNode, r: number): string {
  // Traffic between replicas of one workload: a loop over its circle.
  if (a === b) return `M ${a.x - r * 0.8} ${a.y - r * 0.6} C ${a.x - r * 3.2} ${a.y - r * 5}, ${a.x + r * 3.2} ${a.y - r * 5}, ${a.x + r * 0.8} ${a.y - r - 1.5}`
  if (b.x > a.x) return curve(a.x + r, a.y, b.x - r - 2, b.y, Math.min(60, (b.x - a.x) * 0.4))
  // Backward or within a column: bow out so the line clears the circles.
  const mx = (a.x + b.x) / 2 + (a.x === b.x ? r * 4 : 0)
  const my = (a.y + b.y) / 2 - (a.x === b.x ? 0 : r * 4)
  return `M ${a.x} ${a.y - r} Q ${mx} ${my}, ${b.x} ${b.y - r - 2}`
}

function ObservedTraffic({ flows }: { flows: DashboardTopFlow[] }) {
  const arrow = `${useId()}-arrow`
  const [ref, size] = useElementSize<HTMLDivElement>({ width: 300, height: 130 })
  const { nodes, edges, radius: r, labelSize } = layoutTopFlows(flows, size)
  const maxConnections = Math.max(1, ...edges.map(e => e.connections))
  const scale = r / 7.5
  return (
    <div ref={ref} className="w-full h-full">
      <svg viewBox={`0 0 ${size.width} ${size.height}`} className="w-full h-full" role="img"
        aria-label={edges.map(e => e.directionUnknown
          ? `between ${fullName(e.from)} and ${fullName(e.to)}, direction unknown`
          : `${fullName(e.from)} to ${fullName(e.to)}`).join('; ')}>
        <defs>
          <marker id={arrow} viewBox="0 0 8 6" refX="6" refY="3" markerWidth={7 * scale} markerHeight={5.25 * scale}
            markerUnits="userSpaceOnUse" orient="auto">
            <path d="M 0 0 L 8 3 L 0 6 z" fill="var(--traffic-flow)" />
          </marker>
        </defs>
        {edges.map((e, i) => {
          const d = edgePath(e.from, e.to, r)
          const volume = (1.4 + 2.6 * Math.sqrt(e.connections / maxConnections)) * scale
          // A loop is short and tight, so full width would fill it in.
          const width = e.from === e.to ? Math.min(volume, r * 0.3) : volume
          return (
            <g key={i}>
              <path d={d} fill="none" stroke="var(--traffic-flow)" strokeOpacity={0.22} strokeWidth={width + 1.4}
                strokeLinecap="round" markerEnd={e.directionUnknown ? undefined : `url(#${arrow})`} />
              {!e.directionUnknown && (
                <path d={d} fill="none" stroke="var(--traffic-flow)" strokeWidth={Math.max(1.4, width * 0.75)}
                  strokeLinecap="round" className="animate-traffic-dash" data-testid="traffic-dash" />
              )}
            </g>
          )
        })}
        {nodes.map(n => (
          <g key={n.key}>
            <title>{fullName(n)}</title>
            <circle cx={n.x} cy={n.y} r={r} fill="var(--bg-surface)" stroke="var(--traffic-flow)" strokeWidth={1.2} />
            <circle cx={n.x} cy={n.y} r={r * 0.32} fill="var(--traffic-flow)" />
            <text x={n.labelX} y={n.labelY} textAnchor={n.labelAnchor} fontSize={labelSize} fontFamily="var(--font-mono)"
              fill="var(--text-secondary)" stroke="var(--bg-surface)" strokeWidth={3} paintOrder="stroke">
              {n.label}
            </text>
          </g>
        ))}
      </svg>
    </div>
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
