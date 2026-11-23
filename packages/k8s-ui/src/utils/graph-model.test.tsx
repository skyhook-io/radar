// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { MarkerType, Position, type Edge } from '@xyflow/react'
import { collectGraphModel, frameGraphModel, labelText, type FlowNodeInput, type GraphNodeMapper } from './graph-model'
import { writeSvg } from './graph-svg'

const describeNode: GraphNodeMapper = ({ type, data }) => {
  if (type === 'skip') return null
  if (type === 'group') return { shape: 'group', kind: 'Namespace', name: String(data.name) }
  return { shape: 'resource', kind: String(data.kind), name: String(data.name) }
}

const right = (h = 0) => ({ id: null, x: 100, y: 25 - h / 2, width: 0, height: h, position: Position.Right })
const left = (h = 0) => ({ id: null, x: 0, y: 25 - h / 2, width: 0, height: h, position: Position.Left })

function node(id: string, x: number, y: number, extra: Partial<FlowNodeInput> = {}): FlowNodeInput {
  return {
    id, type: 'k8sResource', data: { kind: 'Pod', name: id }, x, y, width: 100, height: 50,
    handles: { source: [right()], target: [left()] },
    ...extra,
  }
}

const nodes: FlowNodeInput[] = [
  { id: 'g', type: 'group', data: { name: 'default' }, x: 0, y: 0, width: 400, height: 300 },
  node('a', 20, 100, { parentId: 'g' }),
  node('b', 250, 100, { parentId: 'g' }),
  node('c', 1000, 1000),
]
const edges: Edge[] = [
  { id: 'a-b', source: 'a', target: 'b', type: 'smoothstep', style: { stroke: '#3b82f6', strokeWidth: 2 }, markerEnd: { type: MarkerType.ArrowClosed, width: 16 } },
  { id: 'b-c', source: 'b', target: 'c', label: 'HTTP · 3', style: { strokeDasharray: '6 3' } },
]
const renderIcon = () => '<path d="M0 0"/>'

describe('collectGraphModel', () => {
  it('keeps absolute positions and parents, with each parent before its children', () => {
    const model = collectGraphModel({ nodes: [...nodes].reverse(), edges, describeNode, renderIcon })
    const order = model.boxes.map(b => b.id)

    expect(order.indexOf('g')).toBeLessThan(order.indexOf('a'))
    expect(order.indexOf('g')).toBeLessThan(order.indexOf('b'))
    expect(model.boxes.find(b => b.id === 'a')).toMatchObject({ parent: 'g', x: 20, y: 100, w: 100, h: 50 })
    expect(model.boxes.find(b => b.id === 'c')?.parent).toBeUndefined()
  })

  it('reads edge color, width, dash, arrow and label from React Flow props', () => {
    const model = collectGraphModel({ nodes, edges, describeNode, renderIcon })
    const [ab, bc] = model.lines

    expect(ab).toMatchObject({ from: 'a', to: 'b', color: '#3b82f6', width: 2, arrow: 16 })
    expect(ab.path).toMatch(/^M ?120[ ,]125/)
    expect(bc).toMatchObject({ label: 'HTTP · 3', dash: '6 3', arrow: null })
  })

  it('keeps the corners of a step edge for redrawing it elsewhere', () => {
    const model = collectGraphModel({
      nodes, edges: [{ id: 'a-c', source: 'a', target: 'c', type: 'smoothstep' }], describeNode, renderIcon,
    })
    const [line] = model.lines
    expect(line.bends.length).toBeGreaterThanOrEqual(2)
    for (const b of line.bends) {
      expect(b.x).toBeGreaterThanOrEqual(120)
      expect(b.x).toBeLessThanOrEqual(1000)
    }
    expect(collectGraphModel({ nodes, edges, describeNode, renderIcon }).lines[1].bends).toEqual([])
  })

  it('drops nodes the mapper skips, hidden nodes, and their lines', () => {
    const model = collectGraphModel({
      nodes: [...nodes, node('d', 0, 500, { type: 'skip' }), node('e', 0, 600, { hidden: true })],
      edges: [...edges, { id: 'c-d', source: 'c', target: 'd' }, { id: 'c-e', source: 'c', target: 'e' }],
      describeNode,
      renderIcon,
    })

    expect(model.boxes.map(b => b.id)).not.toContain('d')
    expect(model.boxes.map(b => b.id)).not.toContain('e')
    expect(model.lines.map(l => l.id)).toEqual(['a-b', 'b-c'])
  })

  it('renders each icon once and covers boxes and lines in the bounds', () => {
    const model = collectGraphModel({ nodes, edges, describeNode, renderIcon })

    expect(Object.keys(model.icons).length).toBe(2)
    expect(model.bounds.x).toBeLessThanOrEqual(0)
    expect(model.bounds.x + model.bounds.width).toBeGreaterThanOrEqual(1100)
  })
})

describe('visible-area SVG', () => {
  it('is the size of the viewport, not of the group it sits in', () => {
    const big: FlowNodeInput[] = [
      { id: 'g', type: 'group', data: { name: 'huge' }, x: 0, y: 0, width: 5000, height: 5000 },
      node('a', 2400, 2400, { parentId: 'g' }),
      node('far', 4800, 4800, { parentId: 'g' }),
    ]
    const visible = { x: 2350, y: 2350, width: 200, height: 200 }
    const model = frameGraphModel(collectGraphModel({ nodes: big, edges: [], describeNode, renderIcon }), visible)
    const svg = new DOMParser().parseFromString(writeSvg(model, { name: 'x', theme: 'light', background: '#fff', frame: visible }), 'image/svg+xml')
    const root = svg.documentElement

    expect(model.boxes.map(b => b.id).sort()).toEqual(['a', 'g'])
    expect([root.getAttribute('width'), root.getAttribute('height')]).toEqual(['200', '200'])
    expect(root.getAttribute('viewBox')).toBe('2350 2350 200 200')
  })
})

describe('frameGraphModel', () => {
  it('keeps a line that crosses the frame with both ends outside it, and drops nodes outside', () => {
    const ends: FlowNodeInput[] = [node('left', 0, 500), node('right', 2000, 500), node('away', 0, 3000)]
    const model = collectGraphModel({
      nodes: ends,
      edges: [{ id: 'across', source: 'left', target: 'right', type: 'smoothstep' }, { id: 'elsewhere', source: 'left', target: 'away' }],
      describeNode,
      renderIcon,
    })
    const frame = { x: 900, y: 400, width: 200, height: 200 }

    const framed = frameGraphModel(model, frame)
    expect(framed.lines.map(l => l.id)).toEqual(['across'])
    expect(framed.boxes).toEqual([])
    expect(framed.bounds).toEqual(frame)

    const svg = new DOMParser().parseFromString(writeSvg(framed, { name: 'x', theme: 'dark', background: null, frame }), 'image/svg+xml')
    expect(svg.getElementsByTagName('path')).toHaveLength(1)
    expect(svg.documentElement.getAttribute('width')).toBe('200')
  })

  it('keeps the container of a node in the frame even when the container starts outside', () => {
    const model = collectGraphModel({ nodes, edges, describeNode, renderIcon })
    const framed = frameGraphModel(model, { x: 260, y: 110, width: 20, height: 20 })
    expect(framed.boxes.map(b => b.id)).toEqual(['g', 'b'])
  })
})

describe('inner boxes', () => {
  it('places them inside their node, after it, and drops them with it', () => {
    const withCards: GraphNodeMapper = ({ id }) => id === 'g'
      ? { shape: 'collapsed-group', kind: 'Namespace', name: 'shop', inner: [{ shape: 'resource', kind: 'Deployment', name: 'api', x: 14, y: 48, w: 200, h: 68 }] }
      : null
    const groupNode: FlowNodeInput = { id: 'g', type: 'group', data: {}, x: 500, y: 300, width: 400, height: 200 }

    const model = collectGraphModel({ nodes: [groupNode], edges: [], describeNode: withCards, renderIcon })
    expect(model.boxes.map(b => b.id)).toEqual(['g', 'g#0'])
    expect(model.boxes[1]).toMatchObject({ parent: 'g', x: 514, y: 348, w: 200, h: 68 })

    const hidden = frameGraphModel(model, { x: 0, y: 0, width: 100, height: 100 })
    expect(hidden.boxes).toEqual([])
  })
})

describe('labelText', () => {
  it('reads text from elements and leaves out tooltip titles', () => {
    expect(labelText(<>Canary 20%<title>declared route</title></>)).toBe('Canary 20%')
    expect(labelText(42)).toBe('42')
    expect(labelText(undefined)).toBe('')
  })
})
