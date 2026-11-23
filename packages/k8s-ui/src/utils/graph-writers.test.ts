// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import type { GraphModel } from './graph-model'
import { writeDrawio } from './graph-drawio'
import { writeSvg } from './graph-svg'

const model: GraphModel = {
  boxes: [
    { id: 'group-ns-shop', shape: 'group', kind: 'Namespace', name: 'shop', tone: 'namespace', count: 2, x: 100, y: 50, w: 600, h: 300, icon: 'i0' },
    { id: 'shop/web', parent: 'group-ns-shop', shape: 'resource', kind: 'Deployment', name: 'web <prod> & "blue"', kindLabel: 'Deployment', detail: '2/2 ready', status: 'healthy', x: 130, y: 150, w: 280, h: 84, icon: 'i1' },
    { id: 'shop/db', parent: 'group-ns-shop', shape: 'resource', kind: 'Bucket', name: 'db', kindLabel: 'Service Account Token Projection Thing', status: 'unhealthy', x: 430, y: 150, w: 220, h: 84, icon: 'i1' },
    { id: 'internet', shape: 'internet', kind: 'Internet', name: 'Internet', x: -100, y: 170, w: 120, h: 52, icon: 'i0' },
    { id: 'group-ns-other', shape: 'collapsed-group', kind: 'Namespace', name: 'other', tone: 'namespace', count: 7, detail: '3 Pods', x: 800, y: 50, w: 200, h: 64, icon: 'i0' },
  ],
  lines: [
    { id: 'l1', from: 'shop/web', to: 'shop/db', label: 'HTTP < 5ms', color: '#3b82f6', width: 2, arrow: 12, path: 'M 410 192 L 430 192', bends: [{ x: 420, y: 192 }], labelX: 420, labelY: 192 },
    { id: 'l2', from: 'internet', to: 'shop/web', color: '#22c55e', width: 1.5, dash: '5 5', arrow: null, path: 'M 20 196 L 130 192', bends: [], labelX: 75, labelY: 194 },
    { id: 'l3', from: 'shop/web', to: 'missing', color: '#000', width: 1, arrow: 12, path: 'M 0 0', bends: [], labelX: 0, labelY: 0 },
  ],
  icons: { i0: '<path d="M1 1h22"/>', i1: '<circle cx="12" cy="12" r="10"/>' },
  bounds: { x: -100, y: 50, width: 1100, height: 300 },
}

function parse(xml: string, type: DOMParserSupportedType): Document {
  const doc = new DOMParser().parseFromString(xml, type)
  expect(doc.getElementsByTagName('parsererror')).toHaveLength(0)
  return doc
}

describe('writeDrawio', () => {
  const doc = parse(writeDrawio(model, { name: 'Topology & more' }), 'application/xml')
  const cells = [...doc.getElementsByTagName('mxCell')]
  const cellFor = (value: RegExp) => cells.find(c => value.test(c.getAttribute('value') ?? ''))!
  const geometry = (cell: Element) => cell.getElementsByTagName('mxGeometry')[0]

  it('escapes names so the file stays valid and the text survives', () => {
    expect(cellFor(/^web/).getAttribute('value')).toContain('web &lt;prod&gt; &amp; "blue"')
    expect(doc.getElementsByTagName('diagram')[0].getAttribute('name')).toBe('Topology & more')
  })

  it('makes groups containers and places children relative to them', () => {
    const group = cellFor(/^shop/)
    const web = cellFor(/^web/)

    expect(group.getAttribute('style')).toContain('container=1')
    expect(web.getAttribute('parent')).toBe(group.getAttribute('id'))
    // Child x is relative to the group; the icon is centered in the box React Flow drew.
    expect(Number(geometry(web).getAttribute('x'))).toBe(130 - 100 + (280 - 50) / 2)
    expect(Number(geometry(web).getAttribute('y'))).toBe(100)
  })

  it('uses the Kubernetes icon for known kinds and the custom resource icon for others', () => {
    expect(cellFor(/^web/).getAttribute('style')).toContain('prIcon=deploy')
    expect(cellFor(/^db/).getAttribute('style')).toContain('prIcon=crd')
  })

  it('parents an edge to the nearest common container and skips lines with a missing end', () => {
    const edges = cells.filter(c => c.getAttribute('edge') === '1')
    const group = cellFor(/^shop/).getAttribute('id')

    expect(edges).toHaveLength(2)
    expect(edges.find(e => e.getAttribute('value')?.startsWith('HTTP'))?.getAttribute('parent')).toBe(group)
    expect(edges.find(e => !e.getAttribute('value'))?.getAttribute('parent')).toBe('1')
    expect(edges.find(e => !e.getAttribute('value'))?.getAttribute('style')).toContain('endArrow=none')
  })

  it('keeps the on-screen corners of an edge, relative to its container', () => {
    const edge = cells.find(c => c.getAttribute('value')?.startsWith('HTTP'))!
    const point = edge.getElementsByTagName('mxPoint')[0]
    expect([Number(point.getAttribute('x')), Number(point.getAttribute('y'))]).toEqual([420 - 100, 192 - 50])
  })

  it('makes a collapsed group a container only when it holds cards', () => {
    const cards: GraphModel = {
      ...model,
      boxes: [
        { id: 'g', shape: 'collapsed-group', kind: 'Namespace', name: 'cards', x: 0, y: 0, w: 440, h: 130 },
        { id: 'g#0', parent: 'g', shape: 'resource', kind: 'Deployment', name: 'api', kindLabel: 'Deployment', x: 14, y: 48, w: 200, h: 68 },
      ],
      lines: [],
    }
    const cells = [...parse(writeDrawio(cards, { name: 'x' }), 'application/xml').getElementsByTagName('mxCell')]
    const group = cells.find(c => c.getAttribute('value')?.includes('cards'))!
    const card = cells.find(c => c.getAttribute('value')?.startsWith('api'))!
    expect(group.getAttribute('style')).toContain('container=1')
    expect(card.getAttribute('parent')).toBe(group.getAttribute('id'))
    // A short card gets a smaller icon that fits above its two label lines.
    expect(Number(card.getElementsByTagName('mxGeometry')[0].getAttribute('height'))).toBeLessThan(48)
  })

  it('draws a collapsed group without cards as one box', () => {
    const other = cellFor(/other/)
    expect(other.getAttribute('style')).not.toContain('container=1')
    expect(cells.filter(c => c.getAttribute('parent') === other.getAttribute('id'))).toHaveLength(0)
  })
})

describe('writeSvg', () => {
  const svg = writeSvg(model, { name: 'Topology', theme: 'dark', background: 'rgb(11, 15, 30)' })
  const doc = parse(svg, 'image/svg+xml')
  const texts = [...doc.getElementsByTagName('text')].map(t => t.textContent ?? '')

  it('is plain SVG shapes and text, without embedded HTML', () => {
    expect(svg).not.toContain('foreignObject')
    expect(doc.documentElement.getAttribute('viewBox')).toBe('-124 26 1148 348')
    expect(doc.getElementsByTagName('path').length).toBeGreaterThanOrEqual(3)
  })

  it('keeps escaped names, edge labels and the page background', () => {
    expect(texts).toContain('web <prod> & "blue"')
    expect(texts).toContain('HTTP < 5ms')
    expect(doc.querySelector('rect')?.getAttribute('fill')).toBe('rgb(11, 15, 30)')
  })

  it('shortens a long kind label to fit its box instead of wrapping', () => {
    const kind = texts.find(t => t.startsWith('SERVICE'))!
    expect(kind).toContain('…')
    expect(kind.length * 10 * 0.72).toBeLessThanOrEqual(220)
  })

  it('draws a traffic card name first, with rows that fit and the tint over the background', () => {
    const card: GraphModel = {
      ...model,
      boxes: [{
        id: 'shop/api', shape: 'resource', kind: 'External', name: 'api', kindLabel: 'External', detail: 'shop', nameFirst: true,
        tint: { color: '#3B82F6', opacity: 0.2 }, x: 0, y: 0, w: 180, h: 78, icon: 'i0',
        rows: [{ text: ':80', code: true, value: '230' }, { text: ':443', code: true, value: '12' }],
      }],
      lines: [],
    }
    const doc = parse(writeSvg(card, { name: 'Traffic', theme: 'light', background: null }), 'image/svg+xml')
    const shown = [...doc.getElementsByTagName('text')].map(t => t.textContent)

    // The 78px card fits one row; the second would spill past its bottom edge.
    expect(shown).toEqual(['api', 'shop', ':80', '230'])
    expect(doc.querySelector('rect[fill="#3B82F6"]')?.getAttribute('fill-opacity')).toBe('0.2')
  })

  it('drops characters XML does not allow, so the file still parses', () => {
    const bad = { ...model, boxes: [{ ...model.boxes[1], name: 'web\u0001\u001Fapi' }] }
    expect(parse(writeSvg(bad, { name: 'x\u0002', theme: 'light', background: null }), 'image/svg+xml').documentElement.textContent).toContain('webapi')
    parse(writeDrawio(bad, { name: 'x\u0002' }), 'application/xml')
  })

  it('omits the background when transparent and adds one arrowhead per color', () => {
    const clear = parse(writeSvg(model, { name: 'Topology', theme: 'light', background: null }), 'image/svg+xml')
    expect(clear.querySelector('svg > rect')).toBeNull()
    expect(clear.getElementsByTagName('marker')).toHaveLength(2)
  })
})
