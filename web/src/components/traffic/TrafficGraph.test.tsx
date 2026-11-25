// @vitest-environment jsdom
import { act, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Edge, Node } from '@xyflow/react'
import type { AggregatedFlow } from '../../types'
import { TrafficGraph, TrafficNode } from './TrafficGraph'
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
  protocol: 'tcp', port: 443, connections: 0, bytesSent: 100, bytesRecv: 200, bytesByProtocol: { tcp: { bytesSent: 100, bytesRecv: 200 } }, flowCount: 1, lastSeen: '',
}
const outgoing: AggregatedFlow = {
  ...incoming, source: addon, destination: { name: 'kubernetes', namespace: 'default', kind: 'Service' },
}

describe('TrafficGraph addon group edges', () => {
  for (const source of ['istio', 'beyla']) {
    for (const [flow, edgeId, port] of [
      [incoming, 'addon-internet->addon-group', 443],
      [outgoing, 'addon-group->default/kubernetes', 443],
    ] as const) {
      it(`shows Data without a rate when selecting ${edgeId} from ${source}`, async () => {
        const grouped = groupAddonFlows([{ ...flow, rawPairs: [endpointPair(flow)] }], (_name, ns) => ns === 'monitoring')
        const onSelectionChange = vi.fn()
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" onSelectionChange={onSelectionChange} onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
        const edge = container.querySelector<HTMLButtonElement>(`[data-edge="${edgeId}"]`)!
        expect(edge).not.toBeNull()
        // 300 bytes over the 300s window, with no request rate to show.
        expect(edge.textContent).toBe('TCP · 1 B/s')
        act(() => edge.click())
        expect(container.textContent).toContain('Data300 B')
        expect(container.textContent).not.toContain('Request Rate')
        expect(container.textContent).not.toContain('0/s')
        expect(onSelectionChange.mock.calls[0][0]).toMatchObject({ type: 'edge', port, directionUnknown: false })
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" selection={onSelectionChange.mock.calls[0][0]} onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
        expect(container.textContent).toContain('Data300 B')
      })
    }
  }

  for (const [flow, edgeId, nodeId] of [
    [incoming, 'addon-internet->addon-group', 'addon-internet'],
    [outgoing, 'addon-group->default/kubernetes', 'default/kubernetes'],
  ] as const) {
    it(`shows HTTP metadata without an invented port in ${edgeId} details`, async () => {
      const http = { ...flow, protocol: 'http', l7Protocol: 'HTTP', bytesByProtocol: { http: { bytesSent: 100, bytesRecv: 200 } }, port: 0, connections: 2, requestRate: 1.7 }
      const grouped = groupAddonFlows([{ ...http, rawPairs: [endpointPair(http)] }], (_name, ns) => ns === 'monitoring')
      const onSelectionChange = vi.fn()
      await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource="istio" addonMode="group" onSelectionChange={onSelectionChange} onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
      act(() => container.querySelector<HTMLButtonElement>(`[data-edge="${edgeId}"]`)!.click())
      expect(container.textContent).toContain('ProtocolHTTP')
      expect(container.textContent).not.toContain('Port')
      expect(container.textContent).not.toContain('tcp')
      expect(onSelectionChange.mock.calls[0][0]).toMatchObject({ type: 'edge', port: 0 })
      act(() => container.querySelector<HTMLButtonElement>(`[data-node="${nodeId}"]`)!.click())
      const protocols = Array.from(container.querySelectorAll('div')).find(el => el.textContent === 'Protocols')!.parentElement!
      expect(protocols.textContent).toBe('ProtocolsHTTPHTTP1.7/s')
      expect(container.textContent).toContain('Data transferred300 B')
    })

    it(`shows mixed metadata in ${edgeId} details`, async () => {
      const http = { ...flow, protocol: 'http', l7Protocol: 'HTTP', bytesByProtocol: { http: { bytesSent: 100, bytesRecv: 200 } }, port: 8080 }
      const grouped = groupAddonFlows([http, flow].map(f => ({ ...f, rawPairs: [endpointPair(f)] })), (_name, ns) => ns === 'monitoring')
      await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource="istio" addonMode="group" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
      act(() => container.querySelector<HTMLButtonElement>(`[data-edge="${edgeId}"]`)!.click())
      expect(container.textContent).toContain('Port443')
      expect(container.textContent).toContain('ProtocolHTTP + TCP')
      expect(container.textContent).toContain('Data by protocolHTTP 100 B sent · 200 B receivedTCP 100 B sent · 200 B received')
      expect(container.textContent).not.toContain('600 B')
    })
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
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
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
        await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource={source} addonMode="group" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
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

describe('TrafficGraph protocol data', () => {
  const buckets = { http: { bytesSent: 5969, bytesRecv: 260965 }, tcp: { bytesSent: 32699, bytesRecv: 391439 } }
  const sample = { bytesSent: 38668, bytesRecv: 652404, bytesByProtocol: buckets }

  for (const [flow, edgeId, endpointId, direction] of [
    [incoming, 'addon-internet->addon-group', 'addon-internet', 'Outgoing'],
    [outgoing, 'addon-group->default/kubernetes', 'default/kubernetes', 'Incoming'],
  ] as const) {
    it(`keeps both protocols at the virtual edge and both ends of ${edgeId}`, async () => {
      const grouped = groupAddonFlows([{ ...flow, ...sample, rawPairs: [endpointPair(flow)] }], (_name, ns) => ns === 'monitoring')
      await act(async () => root.render(<TrafficGraph flows={grouped} trafficSource="istio" addonMode="group" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
      const groupEdge = container.querySelector<HTMLButtonElement>(`[data-edge="${edgeId}"]`)!
      expect(groupEdge.textContent).toBe('HTTP · 889.8 B/s · TCP · 1.4 KB/s')
      act(() => groupEdge.click())
      expect(container.textContent).toContain('ProtocolHTTP + TCP')
      expect(container.textContent).toContain('Data by protocolHTTP 5.8 KB sent · 254.8 KB receivedTCP 31.9 KB sent · 382.3 KB received')
      expect(container.textContent).toContain('HTTP body bytes and TCP connection bytes')
      expect(container.textContent).not.toMatch(/674\.9 KB|691,?072/)
      for (const id of [endpointId, 'monitoring/prometheus']) {
        act(() => container.querySelector<HTMLButtonElement>(`[data-node="${id}"]`)!.click())
        expect(container.textContent).toContain('Data transferredHTTP 260.7 KBTCP 414.2 KB')
        expect(container.textContent).toContain('HTTP 260.7 KB · TCP 414.2 KB')
        expect(container.textContent).not.toMatch(/674\.9 KB|691,?072/)
      }
      act(() => container.querySelector<HTMLButtonElement>(`[data-node="${endpointId}"]`)!.click())
      expect(container.textContent).toContain(`${direction} (1)`)
    })
  }

  it('folds protocols across ordinary incoming and outgoing edges without changing labels', async () => {
    const client = { name: 'client', namespace: 'demo', kind: 'Pod' }
    const server = { name: 'server', namespace: 'demo', kind: 'Pod' }
    const flows = [
      { ...incoming, ...sample, source: client, destination: server, port: 80 },
      { ...incoming, ...sample, source: server, destination: addon, port: 443 },
    ]
    await act(async () => root.render(<TrafficGraph flows={flows} trafficSource="istio" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
    act(() => container.querySelector<HTMLButtonElement>('[data-node="demo/server"]')!.click())
    expect(container.textContent).toContain('Data transferredHTTP 521.4 KBTCP 828.4 KB')
    expect(container.textContent?.match(/HTTP 260\.7 KB · TCP 414\.2 KB/g)).toHaveLength(2)
    expect(container.textContent).toContain('Incoming (1)')
    expect(container.textContent).toContain('Outgoing (1)')
    expect(container.textContent).not.toMatch(/674\.9 KB|691,?072|1\.3 MB/)
  })
})


it('keeps a released HTTP/gRPC aggregate scalar unlabelled in node details', async () => {
  const legacy = { ...incoming, bytesByProtocol: undefined, protocol: 'http', l7Protocol: 'HTTP' }
  await act(async () => root.render(<TrafficGraph flows={[legacy]} trafficSource="istio" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
  act(() => container.querySelector<HTMLButtonElement>('[data-node="monitoring/prometheus"]')!.click())
  expect(container.textContent).toContain('Data transferred300 B')
  expect(container.textContent).not.toContain('HTTP 300 B')
})


it('retains the application protocol over multiple transports in edge details', async () => {
  const dns = { ...outgoing, port: 53, protocol: 'udp', l7Protocol: 'DNS', bytesByProtocol: { udp: { bytesSent: 100, bytesRecv: 100 }, tcp: { bytesSent: 0, bytesRecv: 100 } } }
  await act(async () => root.render(<TrafficGraph flows={[dns]} trafficSource="hubble" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
  act(() => container.querySelector<HTMLButtonElement>('[data-edge]')!.click())
  expect(container.textContent).toContain('ProtocolDNS / TCP + UDP')
})

describe('TrafficGraph edge labels for traffic measured only in bytes', () => {
  const client = { name: 'curl', namespace: 'demo', kind: 'Pod' }
  const server = { name: 'httpbin', namespace: 'demo', kind: 'Pod' }
  const tcpOnly: AggregatedFlow = {
    ...incoming, source: server, destination: client, port: 0, protocol: 'tcp', connections: 0,
    bytesSent: 29999, bytesRecv: 119999, bytesByProtocol: { tcp: { bytesSent: 29999, bytesRecv: 119999 } },
  }
  const http: AggregatedFlow = {
    ...incoming, source: client, destination: server, port: 0, protocol: 'http', l7Protocol: 'HTTP', connections: 2, requestRate: 1.9,
    bytesSent: 38668, bytesRecv: 652404, bytesByProtocol: { http: { bytesSent: 5969, bytesRecv: 260965 }, tcp: { bytesSent: 32699, bytesRecv: 391439 } },
  }

  it('labels a TCP-only edge with its throughput and leaves an edge with a request rate as it was', async () => {
    await act(async () => root.render(<TrafficGraph flows={[tcpOnly, http]} trafficSource="istio" onLayoutUnavailable={vi.fn()} windowSeconds={60} />))
    expect(container.querySelector('[data-edge="demo/httpbin->demo/curl:0"]')!.textContent).toBe('TCP · 2.4 KB/s')
    const httpLabel = container.querySelector('[data-edge="demo/curl->demo/httpbin:0"]')!.textContent!
    expect(httpLabel).toBe('HTTP · 1.9/s')
  })

  it('claims no throughput for an older Radar whose byte sum can mix TCP and UDP', async () => {
    const legacyTcp = { ...incoming, bytesByProtocol: undefined, bytesSent: 300, bytesRecv: 0 }
    const legacyUdp = { ...incoming, protocol: 'udp', bytesByProtocol: undefined, bytesSent: 600, bytesRecv: 0 }
    const grouped = groupAddonFlows([legacyTcp, legacyUdp].map(f => ({ ...f, rawPairs: [endpointPair(f)] })), (_name, ns) => ns === 'monitoring')
    await act(async () => root.render(<TrafficGraph flows={[...grouped, { ...tcpOnly, bytesByProtocol: undefined }]} trafficSource="beyla" addonMode="group" onLayoutUnavailable={vi.fn()} windowSeconds={300} />))
    expect(container.querySelector('[data-edge="addon-internet->addon-group"]')!.textContent).toBe('')
    expect(container.querySelector('[data-edge="demo/httpbin->demo/curl:0"]')!.textContent).toBe('')
  })

  it('lists a protocol whose bytes were not measured without printing 0 B for it', async () => {
    const unmeasured = { ...http, bytesSent: 300, bytesRecv: 0, bytesByProtocol: { http: { bytesSent: 0, bytesRecv: 0 }, tcp: { bytesSent: 300, bytesRecv: 0 } } }
    await act(async () => root.render(<TrafficGraph flows={[unmeasured]} trafficSource="istio" onLayoutUnavailable={vi.fn()} windowSeconds={60} />))
    act(() => container.querySelector<HTMLButtonElement>('[data-edge]')!.click())
    expect(container.textContent).toContain('ProtocolHTTP + TCP')
    expect(container.textContent).toContain('Data300 B')
    expect(container.textContent).not.toMatch(/0 B sent|HTTP 0 B|body bytes/)
  })

  it('gives each transport its own throughput rather than their sum', async () => {
    const both = { ...tcpOnly, bytesByProtocol: { tcp: { bytesSent: 29999, bytesRecv: 119999 }, udp: { bytesSent: 30, bytesRecv: 0 } } }
    await act(async () => root.render(<TrafficGraph flows={[both]} trafficSource="beyla" onLayoutUnavailable={vi.fn()} windowSeconds={60} />))
    expect(container.querySelector('[data-edge]')!.textContent).toBe('TCP · 2.4 KB/s · UDP · <1 B/s')
  })
})

describe('TrafficNode title', () => {
  // jsdom has no layout or canvas: give the title 100px and every glyph 7px.
  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
      { font: '', measureText: (t: string) => ({ width: t.length * 7 }) } as unknown as CanvasRenderingContext2D)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 100 } as DOMRect)
  })
  afterEach(() => vi.restoreAllMocks())

  it('keeps the end of a long name, so names sharing a prefix stay apart', () => {
    const titles = ['checkout-platform-release-engine', 'checkout-platform-release-redis'].map(label => {
      act(() => root.render(<TrafficNode data={{ label, namespace: 'demo', kind: 'Deployment' }} />))
      const title = container.querySelector(`[title="${label}"]`)!
      return title.lastElementChild!.textContent!
    })
    expect(titles[0]).toBe('checko…engine')
    expect(titles[1]).toMatch(/redis$/)
    expect(titles[0]).not.toBe(titles[1])
  })

  it('gives screen readers the full name, not the cut one', () => {
    const label = 'checkout-platform-release-engine'
    act(() => root.render(<TrafficNode data={{ label, namespace: 'demo', kind: 'Deployment' }} />))
    const srOnly = container.querySelector('.sr-only')
    expect(srOnly?.textContent).toBe(label)
    expect(container.querySelector(`[title="${label}"]`)!.closest('[aria-hidden="true"]')).not.toBeNull()
  })
})
