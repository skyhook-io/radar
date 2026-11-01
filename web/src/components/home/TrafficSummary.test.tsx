// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DashboardTrafficSummary } from '../../api/client'
import { TrafficSummary, fitLabel, layoutTopFlows, trafficSourceName } from './TrafficSummary'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function render(data: DashboardTrafficSummary | null, onNavigate = vi.fn()) {
  act(() => root.render(<TrafficSummary data={data} onNavigate={onNavigate} />))
  return onNavigate
}

const header = () => container.querySelector('button > div > div')!.textContent
const labels = () => [...container.querySelectorAll('svg text')].map(t => t.textContent)

describe('TrafficSummary', () => {
  it('without a traffic source, names the sources it could use and moves nothing', () => {
    render(null)
    expect(header()).toBe('Live Traffic')
    expect(container.textContent).toContain('Needs a traffic source such as Hubble, Beyla, Caretta or Istio.')
    expect(container.querySelector('.animate-traffic-dash, .animate-ping')).toBeNull()
  })

  it('with a source but no flows, names the source and claims nothing about the cluster', () => {
    render({ source: 'beyla', flowCount: 0, topFlows: [] })
    expect(header()).toBe('Live TrafficBeyla')
    expect(container.textContent).toContain('from Beyla. Nothing to show right now.')
    expect(container.textContent).not.toContain('Live ·')
    expect(container.querySelector('.animate-traffic-dash, .animate-ping')).toBeNull()
  })

  it('with flows, shows the live source and count and draws each endpoint once', () => {
    render({
      source: 'hubble',
      flowCount: 312,
      topFlows: [
        { src: 'frontend', srcNamespace: 'shop', dst: 'cart', dstNamespace: 'shop', connections: 40 },
        { src: 'frontend', srcNamespace: 'shop', dst: 'catalog', dstNamespace: 'shop', connections: 20 },
        { src: 'cart', srcNamespace: 'shop', dst: 'redis', dstNamespace: 'shop', connections: 5 },
      ],
    })
    expect(header()).toContain('Live· Hubble · 312 flows')
    expect(container.querySelector('.animate-ping')).not.toBeNull()
    expect(labels().sort()).toEqual(['cart', 'catalog', 'frontend', 'redis'])
    expect(container.querySelectorAll('[data-testid="traffic-dash"]')).toHaveLength(3)
    expect(container.textContent).toContain('The busiest paths Hubble reports right now.')
  })

  it('truncates a long endpoint name and keeps the full name in its title', () => {
    const long = 'payments-reconciliation-worker-canary-v2'
    render({ source: 'istio', flowCount: 1, topFlows: [{ src: long, srcNamespace: 'billing', dst: 'world', connections: 3 }] })
    const shown = labels().find(l => l?.startsWith('payments'))!
    expect(shown.endsWith('…')).toBe(true)
    expect(shown.length).toBeLessThan(long.length)
    expect([...container.querySelectorAll('svg title')].map(t => t.textContent)).toContain(`billing/${long}`)
    expect(labels()).toContain('world')
  })

  it('does not animate or announce a direction it does not know', () => {
    render({ source: 'caretta', flowCount: 1, topFlows: [{ src: 'a', dst: 'b', connections: 1, directionUnknown: true }] })
    expect(container.querySelector('[data-testid="traffic-dash"]')).toBeNull()
    expect(container.querySelector('svg[role="img"]')!.getAttribute('aria-label')).toBe('between a and b, direction unknown')
  })

  it('qualifies a name shared by two namespaces', () => {
    render({ source: 'hubble', flowCount: 2, topFlows: [
      { src: 'web', srcNamespace: 'a', dst: 'db', dstNamespace: 'a', connections: 2 },
      { src: 'web', srcNamespace: 'b', dst: 'db', dstNamespace: 'a', connections: 1 },
    ] })
    expect(labels().sort()).toEqual(['a/web', 'b/web', 'db'])
    expect(container.querySelector('svg[role="img"]')!.getAttribute('aria-label')).toBe('a/web to a/db; b/web to a/db')
  })

  it('opens Live Traffic from anywhere on the card', () => {
    const onNavigate = render(null)
    act(() => container.querySelector('svg')!.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })
})

describe('layoutTopFlows', () => {
  it('keeps the same name in two namespaces as two endpoints', () => {
    const { nodes } = layoutTopFlows([
      { src: 'web', srcNamespace: 'a', dst: 'api', dstNamespace: 'a', connections: 1 },
      { src: 'web', srcNamespace: 'b', dst: 'api', dstNamespace: 'b', connections: 1 },
    ])
    expect(nodes).toHaveLength(4)
  })

  it('places a chain left to right and survives a cycle', () => {
    const chain = layoutTopFlows([
      { src: 'a', dst: 'b', connections: 3 },
      { src: 'b', dst: 'c', connections: 2 },
      { src: 'c', dst: 'd', connections: 1 },
    ])
    expect(chain.nodes.map(n => [n.name, n.column])).toEqual([['a', 0], ['b', 1], ['c', 2], ['d', 3]])
    const cycle = layoutTopFlows([
      { src: 'a', dst: 'b', connections: 3 },
      { src: 'b', dst: 'a', connections: 2 },
    ])
    expect(cycle.nodes.map(n => n.column)).toEqual([0, 1])
    expect(cycle.edges).toHaveLength(2)
  })

  it('keeps a flow from an endpoint to itself', () => {
    const { nodes, edges } = layoutTopFlows([{ src: 'a', dst: 'a', connections: 1 }])
    expect(nodes).toHaveLength(1)
    expect(edges).toHaveLength(1)
    expect(edges[0].from).toBe(edges[0].to)
  })

  it('sums a pair an older server names twice', () => {
    const { edges } = layoutTopFlows([
      { src: 'a', dst: 'b', connections: 3 },
      { src: 'a', dst: 'b', connections: 2 },
    ])
    expect(edges).toHaveLength(1)
    expect(edges[0].connections).toBe(5)
  })
})

describe('labels', () => {
  it('gives labels more room on a wider card and never lets same-line labels overlap', () => {
    const long = 'x'.repeat(60)
    const flows = [
      { src: 'frontend', dst: 'cart', connections: 3 },
      { src: 'frontend', dst: long, connections: 2 },
      { src: 'cart', dst: 'redis-master', connections: 1 },
    ]
    const narrow = layoutTopFlows(flows, { width: 270, height: 130 })
    const wide = layoutTopFlows(flows, { width: 430, height: 150 })
    const label = (l: typeof narrow, name: string) => l.nodes.find(n => n.name === name)!.label
    expect(label(narrow, 'redis-master')).toBe('redis-master')
    expect(label(wide, long).length).toBeGreaterThan(label(narrow, long).length)
    for (const l of [narrow, wide]) {
      const extents = l.nodes.map(n => {
        const w = n.label.length * l.labelSize * 0.6
        const left = n.labelAnchor === 'start' ? n.labelX : n.labelAnchor === 'end' ? n.labelX - w : n.labelX - w / 2
        return { y: n.labelY, left, right: left + w }
      })
      for (const a of extents) for (const b of extents) {
        if (a !== b && Math.abs(a.y - b.y) < l.labelSize) expect(a.right <= b.left || b.right <= a.left).toBe(true)
      }
    }
  })

  it('keeps labels clear of each other and of circles in crowded cases', () => {
    const name = (c: string) => c.repeat(40)
    const cases: [Parameters<typeof layoutTopFlows>[0], { width: number; height: number }][] = [
      [[{ src: name('a'), dst: name('b'), connections: 1 }], { width: 430, height: 150 }],
      [[{ src: 'payments-reconciliation-worker-prod', dst: 'backend', connections: 2 },
        { src: 'payments-reconciliation-worker-prod', dst: 'cache', connections: 1 }], { width: 270, height: 130 }],
      [[{ src: 'a', dst: 'b', connections: 1 }, { src: 'b', dst: 'c', connections: 1 }, { src: 'c', dst: name('d'), connections: 1 }],
        { width: 270, height: 130 }],
    ]
    for (const [flows, size] of cases) {
      const l = layoutTopFlows(flows, size)
      const boxes = l.nodes.map(n => {
        const w = n.label.length * l.labelSize * 0.6
        const left = n.labelAnchor === 'start' ? n.labelX : n.labelAnchor === 'end' ? n.labelX - w : n.labelX - w / 2
        return { n, left, right: left + w, top: n.labelY - l.labelSize, bottom: n.labelY + 3 }
      })
      for (const a of boxes) {
        expect(a.left).toBeGreaterThanOrEqual(0)
        expect(a.right).toBeLessThanOrEqual(size.width)
        for (const b of boxes) {
          if (a === b || !a.n.label || !b.n.label) continue
          const sameLine = a.top < b.bottom && b.top < a.bottom
          if (sameLine) expect(a.right <= b.left || b.right <= a.left).toBe(true)
        }
        for (const o of l.nodes) {
          if (o === a.n || !a.n.label) continue
          const crossesBand = o.y + l.radius > a.top && o.y - l.radius < a.bottom
          if (crossesBand) expect(a.right <= o.x - l.radius || a.left >= o.x + l.radius).toBe(true)
        }
      }
    }
  })

  it('keeps loops over circles inside the drawing', () => {
    const l = layoutTopFlows([
      { src: 'a', dst: 'a', connections: 3 }, { src: 'b', dst: 'b', connections: 2 },
    ], { width: 270, height: 130 })
    for (const n of l.nodes) expect(n.y - l.radius * 4.2).toBeGreaterThanOrEqual(0)
  })

  it('draws fewer endpoints larger and centres them', () => {
    const one = layoutTopFlows([{ src: 'world', dst: 'api', connections: 1 }], { width: 300, height: 130 })
    const three = layoutTopFlows([
      { src: 'a', dst: 'b', connections: 1 }, { src: 'a', dst: 'c', connections: 1 }, { src: 'b', dst: 'd', connections: 1 },
    ], { width: 300, height: 130 })
    expect(one.radius).toBeGreaterThan(three.radius)
    expect(one.labelSize).toBeGreaterThan(three.labelSize)
    const top = Math.min(...one.nodes.map(n => n.y - one.radius))
    const bottom = Math.max(...one.nodes.map(n => n.labelY + 3))
    expect(Math.abs((top + bottom) / 2 - 65)).toBeLessThan(1)
  })

  it('cuts a label to its room', () => {
    expect(fitLabel('cart', 100, 12)).toBe('cart')
    expect(fitLabel('x'.repeat(40), 60, 10)).toBe(`${'x'.repeat(9)}…`)
    expect(fitLabel('x'.repeat(40), 15, 10)).toBe('')
  })

  it('capitalizes source names', () => {
    expect(trafficSourceName('hubble')).toBe('Hubble')
    expect(trafficSourceName('newsource')).toBe('Newsource')
  })
})
