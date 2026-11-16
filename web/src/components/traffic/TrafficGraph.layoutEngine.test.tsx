// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AggregatedFlow } from '../../types'
import type { TrafficGraph as TrafficGraphType } from './TrafficGraph'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type ElkGraph = { children: Array<{ id: string }>; edges: unknown[] }

let pendingLayouts: Array<{ resolve: () => void }> = []

// The engine module is mocked per test: a failed import has to come from the
// module itself, and a resolved one stays cached in TrafficGraph.
function mockElk(fail: boolean) {
  vi.doMock('elkjs/lib/elk.bundled.js', () => {
    if (fail) throw new Error('Failed to fetch dynamically imported module')
    return {
      default: class {
        layout(graph: ElkGraph) {
          return new Promise(resolve => {
            pendingLayouts.push({
              resolve: () => resolve({ ...graph, children: graph.children.map((c, i) => ({ ...c, x: i * 300, y: 0 })) }),
            })
          })
        }
      },
    }
  })
}

// The canvas needs layout APIs jsdom lacks; what the graph hands it is what
// these tests check.
vi.mock('@xyflow/react', async importOriginal => ({
  ...(await importOriginal<typeof import('@xyflow/react')>()),
  ReactFlow: ({ nodes, edges }: { nodes: unknown[]; edges: unknown[] }) => (
    <div data-testid="flow" data-nodes={nodes.length} data-edges={edges.length} />
  ),
}))

const flow = (src: string, dst: string): AggregatedFlow => ({
  source: { name: src, namespace: 'shop', kind: 'Deployment' },
  destination: { name: dst, namespace: 'shop', kind: 'Deployment' },
  protocol: 'tcp',
  port: 80,
  lastSeen: '',
  flowCount: 1,
  bytesSent: 0,
  bytesRecv: 0,
  connections: 1,
})

let container: HTMLDivElement
let root: Root
let TrafficGraph: typeof TrafficGraphType
const onLayoutUnavailable = vi.fn()

const props = (flows: AggregatedFlow[]): ComponentProps<typeof TrafficGraphType> => ({
  flows,
  selection: null,
  onSelectionChange: vi.fn(),
  onFocus: vi.fn(),
  onLayoutUnavailable,
})

async function render(flows: AggregatedFlow[]) {
  await act(async () => root.render(<TrafficGraph {...props(flows)} />))
}

async function settle() {
  await act(async () => {
    await new Promise(resolve => setTimeout(resolve, 0))
  })
}

const canvas = () => container.querySelector<HTMLElement>('[data-testid="flow"]')

async function load({ failElk = false } = {}) {
  pendingLayouts = []
  onLayoutUnavailable.mockReset()
  vi.resetModules()
  mockElk(failElk)
  ;({ TrafficGraph } = await import('./TrafficGraph'))
}

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('TrafficGraph layout engine', () => {
  it('shows a loader until the first layout lands, then the graph', async () => {
    await load()
    await render([flow('web', 'api')])
    await settle()

    expect(container.textContent).toContain('Laying out traffic…')
    expect(canvas()?.dataset.nodes).toBe('0')

    await act(async () => pendingLayouts[0].resolve())
    await settle()

    expect(container.textContent).not.toContain('Laying out traffic…')
    expect(canvas()?.dataset.nodes).toBe('2')
    expect(canvas()?.dataset.edges).toBe('1')
  })

  it('keeps the drawn graph while a later layout runs', async () => {
    await load()
    await render([flow('web', 'api')])
    await settle()
    await act(async () => pendingLayouts[0].resolve())
    await settle()

    await render([flow('web', 'api'), flow('api', 'db')])
    await settle()

    expect(pendingLayouts).toHaveLength(2)
    expect(container.textContent).not.toContain('Laying out traffic…')
    expect(canvas()?.dataset.nodes).toBe('2')
  })

  it('does not let a replaced layout overwrite a newer one', async () => {
    await load()
    await render([flow('web', 'api')])
    await settle()
    await render([flow('web', 'api'), flow('api', 'db')])
    await settle()

    await act(async () => pendingLayouts[1].resolve())
    await settle()
    await act(async () => pendingLayouts[0].resolve())
    await settle()

    expect(canvas()?.dataset.nodes).toBe('3')
  })

  it('draws nothing and reports the engine unavailable instead of drawing a partial grid when ELK cannot load', async () => {
    await load({ failElk: true })
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    await render([flow('web', 'api')])
    await settle()

    expect(canvas()).toBeNull()
    expect(container.textContent).toBe('')
    expect(onLayoutUnavailable).toHaveBeenLastCalledWith(true)
    expect(errors).toHaveBeenCalledWith('ELK failed to load:', expect.any(Error))
    errors.mockRestore()

    act(() => root.unmount())
    expect(onLayoutUnavailable).toHaveBeenLastCalledWith(false)
    root = createRoot(container)
  })

  it('reports the engine available once a layout lands', async () => {
    await load()
    await render([flow('web', 'api')])
    await settle()
    await act(async () => pendingLayouts[0].resolve())
    await settle()

    expect(onLayoutUnavailable).toHaveBeenLastCalledWith(false)
    expect(onLayoutUnavailable).not.toHaveBeenCalledWith(true)
  })
})
