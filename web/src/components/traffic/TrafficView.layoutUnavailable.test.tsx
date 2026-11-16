// @vitest-environment jsdom
import { act, useEffect, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AggregatedFlow, TrafficFlowsResponse } from '../../types'
import type { UseTrafficRecordsOptions } from '../../api/traffic'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

vi.mock('elkjs/lib/elk.bundled.js', () => {
  throw new Error('Failed to fetch dynamically imported module')
})

vi.mock('@xyflow/react', async importOriginal => ({
  ...(await importOriginal<typeof import('@xyflow/react')>()),
  ReactFlow: () => <div data-testid="flow" />,
}))

vi.mock('react-virtuoso', () => ({
  Virtuoso: ({ data, itemContent }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode }) => (
    <div>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div>
  ),
}))

const state = vi.hoisted(() => ({
  flows: null as unknown,
  recordsCalls: [] as unknown[],
}))

vi.mock('../../api/traffic', async importOriginal => {
  const sources = {
    cluster: {},
    active: 'hubble',
    detected: [{ name: 'hubble', status: 'available' }],
    notDetected: [],
  }
  return {
    ...(await importOriginal<typeof import('../../api/traffic')>()),
    useTrafficSources: () => ({ data: sources, isLoading: false, detectAgain: () => Promise.resolve(sources) }),
    useTrafficFlows: () => ({ data: state.flows, isFetching: false, dataUpdatedAt: 1, refetch: () => Promise.resolve() }),
    useTrafficRecords: (options: unknown) => {
      state.recordsCalls.push(options)
      return { data: undefined, supported: false, tooLarge: false, isError: false, error: null, isLoading: false, refetch: () => Promise.resolve() }
    },
    useTrafficConnect: () => ({ mutate: (_vars: unknown, opts?: { onSuccess?: (d: unknown) => void }) => opts?.onSuccess?.({ connected: true }) }),
    useSetTrafficSource: () => ({ mutate: () => {}, isPending: false }),
  }
})

vi.mock('../../api/client', async importOriginal => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useClusterInfo: () => ({ data: { context: 'test' } }),
  useNamespaceScope: () => ({ data: undefined }),
}))

vi.mock('../dock', async importOriginal => ({
  ...(await importOriginal<typeof import('../dock')>()),
  useDock: () => ({ setLeftOffset: () => {}, addTab: () => 'flows', removeTab: () => {} }),
}))

vi.mock('../../context/ConnectionContext', async importOriginal => ({
  ...(await importOriginal<typeof import('../../context/ConnectionContext')>()),
  useConnection: () => ({ connection: { state: 'connected' } }),
}))

const { TrafficView } = await import('./TrafficView')

const endpoint = (namespace: string, name: string, kind = 'Deployment') => ({ namespace, name, kind })
const flow = (source: AggregatedFlow['source'], destination: AggregatedFlow['destination'], port: number, connections: number): AggregatedFlow => ({
  source, destination, protocol: 'tcp', port, connections, lastSeen: '', flowCount: 1, bytesSent: 0, bytesRecv: 0,
})

// An addon talking to the API server is the edge grouping folds into the
// addon group; a plain workload edge is drawn as it is.
const aggregated = [
  flow(endpoint('shop', 'prometheus'), endpoint('default', 'kubernetes', 'Service'), 443, 17),
  flow(endpoint('shop', 'web'), endpoint('shop', 'api'), 80, 5),
]

function flowsResponse(warning?: string): TrafficFlowsResponse {
  return { source: 'hubble', timestamp: '', flows: [], aggregated, ...(warning && { warning }) } as TrafficFlowsResponse
}

const seen = { search: '' }
function LocationProbe() {
  const { search } = useLocation()
  useEffect(() => {
    seen.search = search
  }, [search])
  return null
}

let container: HTMLDivElement
let root: Root

async function render() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } })
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/traffic']}>
          <TrafficView namespaces={[]} />
          <LocationProbe />
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  await settle()
}

async function settle() {
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  }
}

async function click(el: Element | undefined | null) {
  expect(el).toBeTruthy()
  await act(async () => {
    (el as HTMLElement).click()
  })
  await settle()
}

const button = (text: string) => Array.from(container.querySelectorAll('button')).find(b => b.textContent?.trim() === text)
const toggle = (label: string) => Array.from(container.querySelectorAll('button')).find(b => b.textContent?.startsWith(label))
const rows = () => Array.from(container.querySelectorAll('[role="row"]'))
const lastMatch = () => (state.recordsCalls.at(-1) as UseTrafficRecordsOptions).match

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
  state.recordsCalls = []
  state.flows = flowsResponse()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
})

describe('TrafficView when the layout engine cannot load', () => {
  it('lists each connection as it is, with grouped addons, and selects and focuses real endpoints', async () => {
    await render()
    await click(toggle('Hide System'))
    await click(button('group'))

    expect(container.querySelector('[data-testid="flow"]')).toBeNull()
    expect(container.textContent).toContain('The traffic map could not load its layout engine')
    expect(button('Reload')).toBeDefined()

    expect(rows()).toHaveLength(2)
    expect(container.textContent).not.toMatch(/addon-group|addon-internet/)
    const prometheus = rows().find(r => r.textContent?.includes('prometheus'))!
    expect(prometheus.textContent).toContain('kubernetes')
    expect(prometheus.textContent).toContain('17')
    expect(rows().find(r => r.textContent?.includes('web'))?.textContent).toContain('5')

    await click(prometheus)
    expect(rows().find(r => r.textContent?.includes('prometheus'))?.getAttribute('aria-selected')).toBe('true')
    expect(lastMatch()).toEqual({
      pairs: [expect.objectContaining({
        source: expect.objectContaining({ namespace: 'shop', name: 'prometheus' }),
        destination: expect.objectContaining({ namespace: 'default', name: 'kubernetes' }),
        port: 443,
      })],
    })

    const focusPrometheus = Array.from(rows()[0].querySelectorAll('button')).find(b => b.textContent?.includes('prometheus'))
    await click(focusPrometheus)
    expect(new URLSearchParams(seen.search).get('focus')).toBe('shop/prometheus')
  })

  it("puts the source's warning above the explanation instead of over it", async () => {
    state.flows = flowsResponse('Some nodes could not be reached.')
    await render()

    const warning = Array.from(container.querySelectorAll('[role="status"]')).find(el => el.textContent?.includes('Some nodes could not be reached.'))
    expect(warning).toBeDefined()
    expect(container.querySelectorAll('.top-14')).toHaveLength(0)
    const panel = warning!.parentElement!
    expect(panel.textContent).toContain('The traffic map could not load its layout engine')
    expect(panel.textContent!.indexOf('Some nodes could not be reached.')).toBeLessThan(panel.textContent!.indexOf('The traffic map could not load its layout engine'))
  })
})


describe('TrafficView protocol byte merges', () => {
  for (const external of [false, true]) {
    it(`keeps protocol buckets through ${external ? 'external service and Internet' : 'internal service'} collapse`, async () => {
      const source = endpoint(external ? '' : 'demo', external ? '1.2.3.4' : 'client', external ? 'External' : 'Pod')
      const destination = endpoint('demo', 'api', 'Pod')
      const http = { ...flow(source, destination, 0, 1), protocol: 'http', bytesSent: 5969, bytesRecv: 260965, bytesByProtocol: { http: { bytesSent: 5969, bytesRecv: 260965 } } }
      const tcp = { ...flow({ ...source, name: external ? '5.6.7.8' : source.name }, destination, 0, 1), bytesSent: 32699, bytesRecv: 391439, bytesByProtocol: { tcp: { bytesSent: 32699, bytesRecv: 391439 } } }
      state.flows = { ...flowsResponse(), aggregated: [http, tcp] }
      const snapshot = structuredClone(state.flows)
      await render()
      expect(rows()).toHaveLength(1)
      expect(Array.from(rows()[0].querySelectorAll('div.whitespace-nowrap')).map(line => line.textContent)).toEqual(['HTTP 260.7 KB', 'TCP 414.2 KB'])
      expect(rows()[0].textContent).not.toMatch(/674\.9 KB|691,?072/)
      expect(state.flows).toEqual(snapshot)
    })
  }
})
