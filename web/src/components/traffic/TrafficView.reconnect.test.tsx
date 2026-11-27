// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { setAutomaticRequestMarking } from '../../api/config'
import { invalidateIntegrationQueries } from '../settings/integrationQueries'
import { TrafficView } from './TrafficView'

vi.mock('../../hooks/useDesktopDownload', () => ({ useDesktopBlobDownload: () => undefined }))
vi.mock('./TrafficGraph', () => ({ TrafficGraph: () => null }))

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

const settle = () => act(async () => {
  await new Promise(resolve => setTimeout(resolve, 0))
})

it('redetects and reconnects once when a Metrics save loses the traffic source', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { enabled: false, retryDelay: 0 } } })
  const container = document.body.appendChild(document.createElement('div'))
  const root = createRoot(container)
  const connectOrigins: (string | null)[] = []
  const connectionFailure = 'Could not connect to Hubble'
  const flowsQuery = () => queryClient.getQueryCache().find({
    queryKey: ['traffic-flows'], exact: false, predicate: ({ queryKey }) => queryKey[1] !== 'records',
  })
  let sourceLost = false
  let freshDetections = 0
  let failedFlowReads = 0
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setAutomaticRequestMarking(true)
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const { pathname, search } = new URL(url, window.location.origin)
    switch (pathname) {
      case '/api/traffic/sources':
        if (!search) freshDetections++
        return Response.json({ cluster: {}, active: 'hubble', detected: [{ name: 'hubble', status: 'available' }], notDetected: [] })
      case '/api/traffic/connect':
        expect(init?.method).toBe('POST')
        connectOrigins.push(new Headers(init?.headers).get('X-Radar-Request-Origin'))
        return Response.json(sourceLost ? { connected: false, error: connectionFailure } : { connected: true })
      case '/api/traffic/flows':
        if (sourceLost) {
          failedFlowReads++
          return Response.json({ error: 'no traffic source available' }, { status: 503 })
        }
        return Response.json({ source: 'hubble', timestamp: '', flows: [], aggregated: [] })
      default:
        throw new Error(`Unexpected request to ${url}`)
    }
  }))
  queryClient.setQueryDefaults(['traffic-sources'], { enabled: true })

  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={['/traffic']}>
            <TrafficView namespaces={[]} />
          </MemoryRouter>
        </QueryClientProvider>,
      )
    })
    await vi.waitFor(async () => {
      await settle()
      expect(flowsQuery()?.state.status).toBe('success')
    })
    expect(connectOrigins).toEqual(['auto'])
    expect(freshDetections).toBe(0)

    sourceLost = true
    await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    await vi.waitFor(async () => {
      await settle()
      expect(container.textContent).toContain(connectionFailure)
    })
    expect(container.textContent).toContain('Retry Connection')
    expect(connectOrigins).toEqual(['auto', 'auto'])
    expect(freshDetections).toBe(1)

    const readsBeforeRefresh = failedFlowReads
    act(() => container.querySelector<HTMLButtonElement>('button[aria-label="Refresh now"]')!.click())
    await vi.waitFor(async () => {
      await settle()
      expect(failedFlowReads).toBe(readsBeforeRefresh + 2)
      expect(flowsQuery()?.state.status).toBe('error')
    })
    await settle()
    expect(connectOrigins).toEqual(['auto', 'auto'])
    expect(freshDetections).toBe(1)
  } finally {
    act(() => root.unmount())
    queryClient.clear()
    container.remove()
    setAutomaticRequestMarking(false)
    vi.unstubAllGlobals()
  }
})
