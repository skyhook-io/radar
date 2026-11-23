import { isValidElement, type ReactNode } from 'react'
import {
  getBezierPath,
  getSimpleBezierPath,
  getSmoothStepPath,
  getStraightPath,
  Position,
  type Edge,
} from '@xyflow/react'
import type { LucideIcon } from 'lucide-react'
import type { HealthStatus } from '../types'
import { getTopologyIcon } from './resource-icons'

/** A rectangle in graph coordinates. */
export interface GraphRect {
  x: number
  y: number
  width: number
  height: number
}

export type GraphBoxShape = 'resource' | 'internet' | 'group' | 'collapsed-group'
export type GraphGroupTone = 'namespace' | 'app' | 'label' | 'addon'
export type GraphStatus = HealthStatus

/** What a graph says about one of its nodes. Returned by a {@link GraphNodeMapper}. */
export interface GraphBoxSpec {
  shape: GraphBoxShape
  /** Kubernetes kind or traffic endpoint kind. Picks the draw.io icon. */
  kind: string
  name: string
  /** Short kind name shown above the name, e.g. "Service Account". */
  kindLabel?: string
  /** Secondary line, e.g. "3/3 ready" or a namespace. */
  detail?: string
  status?: GraphStatus
  /** Color family of a group or collapsed group. */
  tone?: GraphGroupTone
  /** Count badge next to a group name. */
  count?: number
  /** Left bar color of a resource box, or the color of an internet box. Overrides status. */
  accent?: string
  /** Solid fill of a resource box, e.g. a namespace tint. */
  fill?: string
  /** Text color on top of {@link fill}. */
  textColor?: string
  /** External service category, e.g. "database". */
  category?: string
  /** SVG only: a translucent color over the box background, e.g. an external service's category color. */
  tint?: { color: string; opacity: number }
  /** SVG only: open with the name and leave out the kind label line, as a Live Traffic card does. */
  nameFirst?: boolean
  /** SVG only: lines under the detail, e.g. a port with its request count on the right. */
  rows?: GraphBoxRow[]
  /** Icon drawn in the SVG export. Defaults to the topology icon of {@link kind}. */
  icon?: LucideIcon
  /** Boxes drawn inside this one, such as the workload cards of a collapsed group. */
  inner?: GraphInnerBoxSpec[]
}

/** One line of a box's {@link GraphBoxSpec.rows}. */
export interface GraphBoxRow {
  text: string
  /** Right-aligned value, e.g. a count. */
  value?: string
  /** Draws {@link text} in a monospace accent color, as for a port. */
  code?: boolean
  /** Draws the row's figures in the hot-path color. */
  hot?: boolean
  /** Draws the row dimmer, e.g. "+2 more". */
  muted?: boolean
}

/** A box inside a node, positioned relative to the node's top-left corner. */
export interface GraphInnerBoxSpec extends Omit<GraphBoxSpec, 'inner'> {
  x: number
  y: number
  w: number
  h: number
}

/** A node as positioned on screen, plus its spec. Coordinates are absolute. */
export interface GraphBox extends Omit<GraphBoxSpec, 'icon' | 'inner'> {
  id: string
  /** Containing group box. The box lies inside it. */
  parent?: string
  x: number
  y: number
  w: number
  h: number
  /** Key into {@link GraphModel.icons}. */
  icon?: string
}

export interface GraphLineLabelStyle {
  color?: string
  background?: string
  backgroundOpacity?: number
  fontSize?: number
  fontWeight?: number
}

export interface GraphLine {
  id: string
  from: string
  to: string
  label?: string
  color: string
  width: number
  dash?: string
  /** Arrowhead size at the target end, or null for no arrowhead. */
  arrow: number | null
  /** SVG path data, as React Flow draws the edge. */
  path: string
  /** Corners of a step edge, in drawing order. Empty for curved and straight edges. */
  bends: { x: number; y: number }[]
  labelX: number
  labelY: number
  labelStyle?: GraphLineLabelStyle
}

export interface GraphModel {
  boxes: GraphBox[]
  lines: GraphLine[]
  /** Icon key -> inner SVG markup of a 24x24 stroke icon. */
  icons: Record<string, string>
  /** Area covered by boxes and lines. */
  bounds: GraphRect
}

/** Maps a React Flow node to a box, or null to leave it out. */
export type GraphNodeMapper = (node: { id: string; type?: string; data: Record<string, unknown> }) => GraphBoxSpec | null

export interface FlowHandleBox {
  id?: string | null
  x: number
  y: number
  width: number
  height: number
  position: Position
}

/** A React Flow node with its absolute position and measured size. */
export interface FlowNodeInput {
  id: string
  type?: string
  parentId?: string
  hidden?: boolean
  data: Record<string, unknown>
  x: number
  y: number
  width: number
  height: number
  handles?: { source?: FlowHandleBox[] | null; target?: FlowHandleBox[] | null }
}

export interface CollectGraphOptions {
  nodes: FlowNodeInput[]
  edges: Edge[]
  describeNode: GraphNodeMapper
  /** Turns an icon into inner SVG markup. */
  renderIcon: (icon: LucideIcon) => string
}

// React Flow's edge stroke when an edge sets none.
const DEFAULT_EDGE_COLOR = '#b1b1b7'
// React Flow's default arrowhead size.
const DEFAULT_ARROW_SIZE = 12.5
// smoothstep edges leave a node this far before turning.
const EDGE_OFFSET = 20

function intersects(a: GraphRect, b: GraphRect): boolean {
  return a.x < b.x + b.width && a.x + a.width > b.x && a.y < b.y + b.height && a.y + a.height > b.y
}

/** Plain text of an edge label, leaving out tooltip <title> elements. */
export function labelText(label: ReactNode): string {
  if (label == null || typeof label === 'boolean') return ''
  if (typeof label === 'string' || typeof label === 'number' || typeof label === 'bigint') return String(label)
  if (Array.isArray(label)) return label.map(labelText).join('')
  if (isValidElement<{ children?: ReactNode }>(label)) {
    if (label.type === 'title') return ''
    return labelText(label.props.children)
  }
  return ''
}

function handlePoint(node: FlowNodeInput, handles: FlowHandleBox[] | null | undefined, handleId: string | null | undefined, fallback: Position): { x: number; y: number; position: Position } {
  const handle = handles?.length ? (handleId ? handles.find(h => h.id === handleId) : handles[0]) : undefined
  const x = node.x + (handle?.x ?? 0)
  const y = node.y + (handle?.y ?? 0)
  const width = handle?.width ?? node.width
  const height = handle?.height ?? node.height
  const position = handle?.position ?? fallback
  switch (position) {
    case Position.Top: return { x: x + width / 2, y, position }
    case Position.Right: return { x: x + width, y: y + height / 2, position }
    case Position.Bottom: return { x: x + width / 2, y: y + height, position }
    default: return { x, y: y + height / 2, position }
  }
}

function edgePath(type: string | undefined, s: { x: number; y: number; position: Position }, t: { x: number; y: number; position: Position }): [string, number, number] {
  const params = { sourceX: s.x, sourceY: s.y, sourcePosition: s.position, targetX: t.x, targetY: t.y, targetPosition: t.position }
  switch (type) {
    case 'smoothstep': {
      const [path, labelX, labelY] = getSmoothStepPath(params)
      return [path, labelX, labelY]
    }
    case 'step': {
      const [path, labelX, labelY] = getSmoothStepPath({ ...params, borderRadius: 0 })
      return [path, labelX, labelY]
    }
    case 'straight': {
      const [path, labelX, labelY] = getStraightPath(params)
      return [path, labelX, labelY]
    }
    case 'simplebezier': {
      const [path, labelX, labelY] = getSimpleBezierPath(params)
      return [path, labelX, labelY]
    }
    default: {
      const [path, labelX, labelY] = getBezierPath(params)
      return [path, labelX, labelY]
    }
  }
}

// A smoothstep path rounds each corner with a quadratic curve whose control point is the corner.
function stepBends(type: string | undefined, path: string): { x: number; y: number }[] {
  if (type !== 'smoothstep' && type !== 'step') return []
  return [...path.matchAll(/Q\s*(-?[\d.]+(?:e-?\d+)?)[\s,]+(-?[\d.]+(?:e-?\d+)?)/g)].map(m => ({ x: Number(m[1]), y: Number(m[2]) }))
}

function numberOf(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '' && Number.isFinite(Number(value))) return Number(value)
  return undefined
}

function stringOf(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

function lineStyle(edge: Edge): Pick<GraphLine, 'color' | 'width' | 'dash' | 'arrow' | 'labelStyle'> {
  const style = edge.style ?? {}
  const color = stringOf(style.stroke) ?? DEFAULT_EDGE_COLOR
  const marker = edge.markerEnd
  const arrow = marker == null
    ? null
    : typeof marker === 'string'
      ? DEFAULT_ARROW_SIZE
      : marker.width ?? DEFAULT_ARROW_SIZE
  const labelStyle: GraphLineLabelStyle = {
    color: stringOf(edge.labelStyle?.fill),
    background: stringOf(edge.labelBgStyle?.fill),
    backgroundOpacity: numberOf(edge.labelBgStyle?.fillOpacity),
    fontSize: numberOf(edge.labelStyle?.fontSize),
    fontWeight: numberOf(edge.labelStyle?.fontWeight),
  }
  const hasLabelStyle = Object.values(labelStyle).some(v => v !== undefined)
  return {
    color,
    width: numberOf(style.strokeWidth) ?? 1,
    // React Flow draws an animated edge as a moving dashed line.
    dash: stringOf(style.strokeDasharray) ?? (typeof style.strokeDasharray === 'number' ? String(style.strokeDasharray) : edge.animated ? '5' : undefined),
    arrow,
    ...(hasLabelStyle ? { labelStyle } : {}),
  }
}

// Every coordinate pair in a path. The curves React Flow draws stay inside their control points.
function pathBounds(path: string): GraphRect | null {
  const nums = (path.match(/-?\d*\.?\d+(?:e-?\d+)?/gi) ?? []).map(Number)
  if (nums.length < 2) return null
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  for (let i = 0; i + 1 < nums.length; i += 2) {
    minX = Math.min(minX, nums[i]); maxX = Math.max(maxX, nums[i])
    minY = Math.min(minY, nums[i + 1]); maxY = Math.max(maxY, nums[i + 1])
  }
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
}

/**
 * Keeps what a frame shows: boxes that intersect it (with their containers) and lines whose
 * path passes through it, even when both ends are outside. Bounds become the frame.
 */
export function frameGraphModel(model: GraphModel, frame: GraphRect): GraphModel {
  const byId = new Map(model.boxes.map(b => [b.id, b]))
  const kept = new Set<string>()
  for (const box of model.boxes) {
    if (!intersects({ x: box.x, y: box.y, width: box.w, height: box.h }, frame)) continue
    for (let cur: GraphBox | undefined = box; cur && !kept.has(cur.id); cur = cur.parent ? byId.get(cur.parent) : undefined) kept.add(cur.id)
  }
  const lines = model.lines.filter(line => {
    const b = pathBounds(line.path)
    // Grow by half the stroke (at least a pixel), so a straight line and a thick stroke's painted edge count.
    // A path whose bounds touch the frame is kept even if it bends around it; the viewBox clips it.
    const pad = Math.max(1, line.width / 2)
    return b !== null && intersects({ x: b.x - pad, y: b.y - pad, width: b.width + 2 * pad, height: b.height + 2 * pad }, frame)
  })
  const boxes = model.boxes.filter(b => kept.has(b.id))
  const used = new Set(boxes.map(b => b.icon).filter((k): k is string => !!k))
  const icons = Object.fromEntries(Object.entries(model.icons).filter(([key]) => used.has(key)))
  return { boxes, lines, icons, bounds: frame }
}

/**
 * Collects the boxes and lines of a React Flow graph as placed on screen.
 * Each graph supplies `describeNode`; edges are read from their React Flow props.
 */
export function collectGraphModel({ nodes, edges, describeNode, renderIcon }: CollectGraphOptions): GraphModel {
  const flowNodes = new Map<string, FlowNodeInput>()
  const specs = new Map<string, GraphBoxSpec>()
  for (const node of nodes) {
    if (node.hidden) continue
    const spec = describeNode({ id: node.id, type: node.type, data: node.data })
    if (!spec) continue
    flowNodes.set(node.id, node)
    specs.set(node.id, spec)
  }

  const parentOf = (id: string): string | undefined => {
    const parentId = flowNodes.get(id)?.parentId
    return parentId && specs.has(parentId) ? parentId : undefined
  }

  const kept = new Set(specs.keys())

  const icons: Record<string, string> = {}
  const iconKeys = new Map<LucideIcon, string>()
  const iconKey = (icon: LucideIcon): string => {
    let key = iconKeys.get(icon)
    if (!key) {
      key = `i${iconKeys.size}`
      iconKeys.set(icon, key)
      icons[key] = renderIcon(icon)
    }
    return key
  }

  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  const grow = (x1: number, y1: number, x2: number, y2: number) => {
    minX = Math.min(minX, x1); minY = Math.min(minY, y1)
    maxX = Math.max(maxX, x2); maxY = Math.max(maxY, y2)
  }

  const boxes: GraphBox[] = []
  for (const id of kept) {
    const node = flowNodes.get(id)
    const spec = specs.get(id)
    if (!node || !spec) continue
    const { icon, inner, ...rest } = spec
    const parent = parentOf(id)
    boxes.push({
      ...rest,
      id,
      ...(parent ? { parent } : {}),
      x: node.x,
      y: node.y,
      w: node.width,
      h: node.height,
      icon: iconKey(icon ?? getTopologyIcon(spec.kind)),
    })
    inner?.forEach(({ icon: innerIcon, ...box }, i) => {
      boxes.push({
        ...box,
        id: `${id}#${i}`,
        parent: id,
        x: node.x + box.x,
        y: node.y + box.y,
        icon: iconKey(innerIcon ?? getTopologyIcon(box.kind)),
      })
    })
    grow(node.x, node.y, node.x + node.width, node.y + node.height)
  }
  // Parents before children: draw.io needs a parent cell defined first.
  const parentOfBox = new Map(boxes.map(b => [b.id, b.parent]))
  const depth = (box: GraphBox): number => {
    let d = 0
    for (let p = box.parent; p; p = parentOfBox.get(p)) d++
    return d
  }
  boxes.sort((a, b) => depth(a) - depth(b))

  const lines: GraphLine[] = []
  for (const edge of edges) {
    if (edge.hidden || !kept.has(edge.source) || !kept.has(edge.target)) continue
    const source = flowNodes.get(edge.source)
    const target = flowNodes.get(edge.target)
    if (!source || !target) continue
    const s = handlePoint(source, source.handles?.source, edge.sourceHandle, Position.Right)
    const t = handlePoint(target, target.handles?.target, edge.targetHandle, Position.Left)
    const [path, labelX, labelY] = edgePath(edge.type, s, t)
    const label = labelText(edge.label)
    lines.push({
      id: edge.id,
      from: edge.source,
      to: edge.target,
      ...(label ? { label } : {}),
      ...lineStyle(edge),
      path,
      bends: stepBends(edge.type, path),
      labelX,
      labelY,
    })
    grow(Math.min(s.x, t.x) - EDGE_OFFSET, Math.min(s.y, t.y) - EDGE_OFFSET, Math.max(s.x, t.x) + EDGE_OFFSET, Math.max(s.y, t.y) + EDGE_OFFSET)
  }

  const bounds = boxes.length === 0
    ? { x: 0, y: 0, width: 0, height: 0 }
    : { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  return { boxes, lines, icons, bounds }
}
