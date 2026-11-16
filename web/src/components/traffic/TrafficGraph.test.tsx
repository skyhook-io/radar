// @vitest-environment jsdom
import { act, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Edge, Node } from '@xyflow/react'
import type { AggregatedFlow } from '../../types'
import { TrafficGraph } from './TrafficGraph'
import { endpointPair, groupAddonFlows } from './trafficFilters'

const { fitView } = vi.hoisted(() => ({ fitView: vi.fn() }))
vi.mock('./TrafficView', () => ({ isClusterAddon: (_name: string, namespace: string) => namespace === 'monitoring' }))
vi.mock('elkjs/lib/elk.bundled.js', () => ({
  default: class {
    async layout(graph: { children: Node[] }) {
      return { ...graph, children: graph.children.map((node, index) => ({ ...node, x: index * 250, y: 0 })) }
    }
  },
}))
vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ nodes, edges, onNodeClick, onEdgeClick, children }: {
    nodes: Node[]; edges: Edge[]; onNodeClick: (event: unknown, node: Node) => void
    onEdgeClick: (event: unknown, edge: Edge) => void; children: ReactNode
  }) => (
    <div>
      {nodes.map(node => <button key={node.id} data-node={node.id} data-connections={node.data.connections}
        data-total-volume={node.data.totalConnections} data-ports={JSON.stringify(node.data.ports)}
        onClick={() => onNodeClick({}, node)}>{node.data.label as string}</button>)}
      {edges.map(edge => <button key={edge.id} data-edge={edge.id} onClick={() => onEdgeClick({}, edge)}>{edge.label as string}</button>)}
      {children}
    </div>
  ),
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  Position: { Left: 'left', Right: 'right' },
  useNodesState: (initial: Node[]) => [...useState(initial), vi.fn()],
  useEdgesState: (initial: Edge[]) => [...useState(initial), vi.fn()],
  useReactFlow: () => ({ fitView }),
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

const addon = { name: 'prometheus', namespace: 'monitoring', kind: 'Pod' }
const incoming: AggregatedFlow = {
  source: { name: 'Internet', namespace: '', kind: 'Internet' }, destination: addon,
  protocol: 'tcp', port: 443, connections: 0, bytesSent: 100, bytesRecv: 200, flowCount: 1, lastSeen: '',
}
const outgoing: AggregatedFlow = {
  ...incoming, source: addon, destination: { name: 'kubernetes', namespace: 'default', kind: 'Service' },
}

describe('TrafficGraph addon group edges', () => {
  for (const source of ['istio', 'beyla']) {
    for (const [flow, edgeId, port] of [
      [incoming, 'addon-internet->addon-group', 0],
      [outgoing, 'addon-group->default/kubernetes', 443],
    ] as const) {
      it(`shows Data without a rate when selecting ${edgeId} from ${source}`, async () => {
        const grouped = groupAddonFlows([{ ...flow, rawPairs: [endpointPair(flow)] }], (_name, ns) => ns === 'monitoring')
        const onSelectionChange = vi.fn()
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" onSelectionChange={onSelectionChange} />))
        const edge = container.querySelector<HTMLButtonElement>(`[data-edge="${edgeId}"]`)!
        expect(edge).not.toBeNull()
        expect(edge.textContent).toBe('')
        act(() => edge.click())
        expect(container.textContent).toContain('Data300 B')
        expect(container.textContent).not.toContain('Request Rate')
        expect(container.textContent).not.toContain('0/s')
        expect(onSelectionChange.mock.calls[0][0]).toMatchObject({ type: 'edge', port, directionUnknown: false })
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" selection={onSelectionChange.mock.calls[0][0]} />))
        expect(container.textContent).toContain('Data300 B')
      })
    }
  }
})


describe('TrafficGraph addon group node accounting', () => {
  for (const source of ['istio', 'beyla', 'hubble']) {
    for (const [flow, nodeId, direction] of [
      [incoming, 'addon-internet', 'Outgoing'],
      [outgoing, 'default/kubernetes', 'Incoming'],
    ] as const) {
      it(`counts Data once when selecting ${nodeId} from ${source}`, async () => {
        const grouped = groupAddonFlows([{ ...flow, rawPairs: [endpointPair(flow)] }], (_name, ns) => ns === 'monitoring')
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" />))
        const node = container.querySelector<HTMLButtonElement>(`[data-node="${nodeId}"]`)!
        expect(node).not.toBeNull()
        expect(node.dataset.connections).toBe('1')
        act(() => node.click())
        expect(container.textContent).toContain('Data transferred300 B')
        expect(container.textContent).not.toContain('600 B')
        expect(container.textContent).toContain(`${direction} (1)`)

        const addonNode = container.querySelector<HTMLButtonElement>('[data-node="monitoring/prometheus"]')!
        expect(addonNode.dataset.connections).toBe('1')
        act(() => addonNode.click())
        expect(container.textContent).toContain('Data transferred300 B')
      })

      it(`counts node volume and ports once at ${nodeId} from ${source}`, async () => {
        const measured = { ...flow, connections: 2, requestRate: 1.7 }
        const grouped = groupAddonFlows([{ ...measured, rawPairs: [endpointPair(measured)] }], (_name, ns) => ns === 'monitoring')
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" />))
        const volume = source === 'hubble' ? 2 : 1.7
        for (const id of [nodeId, 'monitoring/prometheus']) {
          const node = container.querySelector<HTMLButtonElement>(`[data-node="${id}"]`)!
          expect(node.dataset.connections).toBe('1')
          expect(Number(node.dataset.totalVolume)).toBe(volume)
          if (id === 'default/kubernetes' || (id === 'monitoring/prometheus' && direction === 'Outgoing')) {
            expect(JSON.parse(node.dataset.ports!)).toEqual([{ port: 443, connections: volume }])
          }
        }
      })
    }
  }
})
