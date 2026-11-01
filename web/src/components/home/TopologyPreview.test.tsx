// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Topology } from '../../types'
import { TopologyPreview } from './TopologyPreview'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const largeTopology = { nodes: [], edges: [], requiresNamespaceFilter: true } as unknown as Topology
const smallTopology = {
  nodes: [
    { id: 'deployment/default/web', kind: 'Deployment', name: 'web', status: 'healthy', data: {} },
    { id: 'service/default/web', kind: 'Service', name: 'web', status: 'healthy', data: {} },
  ],
  edges: [{ id: 'e1', source: 'service/default/web', target: 'deployment/default/web', type: 'exposes' }],
} as unknown as Topology

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

function render(topology: Topology | null, namespaceSelected = false, onNavigate = vi.fn()) {
  act(() => root.render(<TopologyPreview topology={topology} namespaceSelected={namespaceSelected} onNavigate={onNavigate} />))
  return onNavigate
}

const header = () => container.querySelector('button > div > div')!.textContent

describe('TopologyPreview', () => {
  it('states the graph counts in the header', () => {
    render(smallTopology)
    expect(header()).toContain('2 resources · 1 relationships')
    expect(container.textContent).toContain('How ingresses, services and workloads connect, grouped by namespace.')
  })

  it('draws the same picture on a cluster too large to graph, with no counts and no namespaces', () => {
    render(smallTopology)
    const smallDrawing = container.querySelector('svg')!.innerHTML.replace(/_r_\w+_/g, '')
    render(largeTopology)
    expect(header()).toBe('Topology')
    expect(container.querySelector('[data-testid="topology-counts-loading"]')).toBeNull()
    expect(container.querySelector('svg')!.innerHTML.replace(/_r_\w+_/g, '')).toBe(smallDrawing)
  })

  it('keeps loading while a picked namespace is on its way to the server', () => {
    render(largeTopology, true)
    expect(container.querySelector('[data-testid="topology-counts-loading"]')).not.toBeNull()
  })

  it('shows an empty scope as an honest zero, and loading before any graph', () => {
    render({ nodes: [], edges: [] } as unknown as Topology)
    expect(header()).toContain('0 resources · 0 relationships')
    render(null)
    expect(container.querySelector('[data-testid="topology-counts-loading"]')).not.toBeNull()
  })

  it('opens Topology from anywhere on the card', () => {
    const onNavigate = render(largeTopology)
    act(() => container.querySelector('svg')!.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })
})
