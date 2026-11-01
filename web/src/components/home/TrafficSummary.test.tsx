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
    expect(container.textContent).toContain('The busiest paths Hubble saw in the last 5 minutes.')
    expect(container.querySelectorAll('[data-testid="other-traffic"]').length).toBeGreaterThan(0)
  })

  it('truncates a long endpoint name and keeps the full name in its title', () => {
    const long = 'payments-reconciliation-worker-canary-v2'
    render({ source: 'istio', flowCount: 1, topFlows: [{ src: long, srcNamespace: 'billing', dst: 'world', connections: 3 }] })
    const shown = labels().find(l => l?.startsWith('payments'))!
    expect(shown.endsWith('…')).toBe(true)
    expect(shown.length).toBeLessThan(long.length)
    expect([...container.querySelectorAll('svg title')].map(t => t.textContent)).toContain(`billing/${long}`)
    expect(labels()).toContain('world')
    // Every flow is drawn, so nothing stands for traffic left out.
    expect(container.querySelector('[data-testid="other-traffic"]')).toBeNull()
  })

  it('does not animate a flow whose direction is unknown', () => {
    render({ source: 'caretta', flowCount: 1, topFlows: [{ src: 'a', dst: 'b', connections: 1, directionUnknown: true }] })
    expect(container.querySelector('[data-testid="traffic-dash"]')).toBeNull()
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

  it('draws no line for a flow from an endpoint to itself', () => {
    const { nodes, edges } = layoutTopFlows([{ src: 'a', dst: 'a', connections: 1 }])
    expect(nodes).toHaveLength(1)
    expect(edges).toHaveLength(0)
  })
})

describe('labels', () => {
  it('fits neighbouring labels inside their half of the gap', () => {
    const name = 'x'.repeat(60)
    expect(fitLabel(name, { column: 0, columns: 2 }).length).toBeLessThan(25)
    expect(fitLabel(name, { column: 1, columns: 3 }).length).toBeLessThan(25)
    expect(fitLabel('cart', { column: 0, columns: 4 })).toBe('cart')
  })

  it('capitalizes source names', () => {
    expect(trafficSourceName('hubble')).toBe('Hubble')
    expect(trafficSourceName('newsource')).toBe('Newsource')
  })
})
