// @vitest-environment jsdom
import { act, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Edge, Node } from '@xyflow/react'
import { ToastProvider } from '@skyhook-io/k8s-ui'
import type { AggregatedFlow } from '../../types'
import { TrafficGraph } from './TrafficGraph'

vi.mock('./TrafficView', () => ({ isClusterAddon: () => false }))
vi.mock('elkjs/lib/elk.bundled.js', () => ({
  default: class {
    async layout(graph: { children: Node[] }) {
      return { ...graph, children: graph.children.map((node, index) => ({ ...node, x: index * 250, y: 0 })) }
    }
  },
}))
vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  Background: () => null,
  Controls: ({ children }: { children: ReactNode }) => <div data-testid="controls">{children}</div>,
  Handle: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  Position: { Left: 'left', Right: 'right' },
  useNodesState: (initial: Node[]) => [...useState(initial), vi.fn()],
  useEdgesState: (initial: Edge[]) => [...useState(initial), vi.fn()],
  useReactFlow: () => ({ fitView: vi.fn(), getNodes: () => [], getNodesBounds: vi.fn() }),
  useStore: (selector: (s: { domNode: null }) => unknown) => selector({ domNode: null }),
}))

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

const flow: AggregatedFlow = {
  source: { name: 'web', namespace: 'default', kind: 'Pod' }, destination: { name: 'api', namespace: 'default', kind: 'Service' },
  protocol: 'tcp', port: 80, connections: 3, bytesSent: 10, bytesRecv: 20, bytesByProtocol: { tcp: { bytesSent: 10, bytesRecv: 20 } }, flowCount: 1, lastSeen: '',
}

it('offers image export in the graph controls', async () => {
  await act(async () => root.render(
    <ToastProvider>
      <TrafficGraph flows={[flow]} onLayoutUnavailable={vi.fn()} windowSeconds={300} />
    </ToastProvider>,
  ))
  const controls = container.querySelector('[data-testid="controls"]')!
  expect(controls.querySelector('button[aria-label="Export graph"]')).not.toBeNull()
})
