// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Topology } from '../../types'

// The canvas stands in for React Flow: it prints each node's drawn status.
vi.mock('@xyflow/react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@xyflow/react')>()
  return {
    ...actual,
    ReactFlow: ({ nodes }: { nodes: Node[] }) => (
      <div>{nodes.map((n) => <span key={n.id} data-node={n.id} data-status={String(n.data?.status)} />)}</div>
    ),
    Background: () => null,
    Panel: () => null,
  }
})

// Layouts resolve only when the test says so, carrying the frame they started
// from, as ELK's result does.
const pending: Array<() => void> = []
vi.mock('./layout', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./layout')>()
  return {
    ...actual,
    applyHierarchicalLayout: (_graph: unknown, workingNodes: Array<{ id: string; kind: string; name: string; status: string; data: unknown }>) =>
      new Promise((resolve) =>
        pending.push(() =>
          resolve({
            nodes: workingNodes.map((n) => ({ id: n.id, type: 'k8sResource', position: { x: 0, y: 0 }, data: { kind: n.kind, name: n.name, status: n.status, nodeData: n.data } })),
          }),
        ),
      ),
  }
})

const { TopologyGraph } = await import('./TopologyGraph')

function topo(status: string): Topology {
  return {
    nodes: [
      { id: 'deployment/shop/web', kind: 'Deployment', name: 'web', status, data: { namespace: 'shop' } },
      { id: 'service/shop/web', kind: 'Service', name: 'web', status: 'healthy', data: { namespace: 'shop' } },
    ],
    edges: [{ id: 'e1', source: 'service/shop/web', target: 'deployment/shop/web', type: 'exposes' }],
  } as unknown as Topology
}

afterEach(() => {
  pending.length = 0
})

// The server sends a topology frame only when the graph changed, so no later
// identical frame repaints the canvas. A layout that started on an older frame
// and lands after a newer one must not leave the older frame's data drawn.
describe('TopologyGraph', () => {
  it('draws the latest frame when a slower layout lands after a data-only update', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    const container = document.createElement('div')
    const root = createRoot(container)
    const render = (t: Topology) =>
      act(() => root.render(<TopologyGraph topology={t} viewMode="resources" groupingMode="namespace" hideGroupHeader onNodeClick={() => {}} />))

    await render(topo('healthy'))
    expect(pending).toHaveLength(1)
    await render(topo('unhealthy')) // same structure: no new layout
    expect(pending).toHaveLength(1)
    await act(async () => {
      pending[0]()
      await new Promise((r) => setTimeout(r, 50))
    })

    const drawn = container.querySelector('[data-node="deployment/shop/web"]')
    expect(drawn?.getAttribute('data-status')).toBe('unhealthy')
    act(() => root.unmount())
    vi.unstubAllGlobals()
  })
})
