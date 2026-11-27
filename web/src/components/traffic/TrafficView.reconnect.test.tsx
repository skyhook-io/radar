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

it.each(['success', 'failure', 'cancelled detection', 'status stays disconnected'])('handles traffic connection loss after a Metrics save (%s)', async (scenario) => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { enabled: false, retryDelay: 0 } } })
  const container = document.body.appendChild(document.createElement('div'))
  const root = createRoot(container)
  const connectOrigins: (string | null)[] = []
  const connectionFailure = 'Could not connect to Hubble'
  const notConnected = 'Not connected to Hubble Relay. Call Connect() first or use the Traffic view to establish connection.'
  let connected = false
  let freshDetections = 0
  let warnings = 0
  let statusReadsAfterReconnect = 0
  queryClient.setQueryDefaults(['traffic-sources'], { enabled: true })
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setAutomaticRequestMarking(true)
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const { pathname, search } = new URL(url, window.location.origin)
    switch (pathname) {
      case '/api/traffic/sources':
        if (!search && ++freshDetections === 1 && scenario === 'cancelled detection') {
          return new Promise<Response>((_, reject) => {
            init!.signal!.addEventListener('abort', () => reject(init!.signal!.reason), { once: true })
          })
        }
        return Response.json({ cluster: {}, active: 'hubble', detected: [{ name: 'hubble', status: 'available' }], notDetected: [] })
      case '/api/traffic/connection':
        if (connectOrigins.length > 1) statusReadsAfterReconnect++
        return Response.json({ connected: connected &&
          !(scenario === 'status stays disconnected' && connectOrigins.length > 1) })
      case '/api/traffic/connect':
        expect(init?.method).toBe('POST')
        connectOrigins.push(new Headers(init?.headers).get('X-Radar-Request-Origin'))
        if (connectOrigins.length > 1 && scenario === 'failure') {
          return Response.json({ connected: false, error: connectionFailure })
        }
        connected = true
        return Response.json({ connected })
      case '/api/traffic/flows':
        if (!connected) warnings++
        return Response.json({ source: 'hubble', timestamp: '', flows: [], aggregated: [], warning: connected ? undefined : notConnected })
      default:
        throw new Error(`Unexpected request to ${url}`)
    }
  }))

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
      expect(connected).toBe(true)
      expect(queryClient.getQueryData(['traffic-connection'])).toEqual({ connected: true })
    })
    expect(connectOrigins).toEqual(['auto'])
    expect(freshDetections).toBe(0)

    connected = false
    const warningsBeforeSave = warnings
    await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    expect(warnings).toBeGreaterThan(warningsBeforeSave)

    if (scenario === 'cancelled detection') {
      await vi.waitFor(async () => {
        await settle()
        expect(freshDetections).toBe(1)
      })
      expect(connectOrigins).toEqual(['auto'])
      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    }
    await vi.waitFor(async () => {
      await settle()
      if (scenario === 'failure') {
        expect(container.textContent).toContain(connectionFailure)
        expect(container.textContent).toContain('Retry Connection')
      } else if (scenario === 'status stays disconnected') {
        expect(statusReadsAfterReconnect).toBeGreaterThan(0)
      } else {
        expect(queryClient.getQueryData(['traffic-connection'])).toEqual({ connected: true })
      }
    })
    await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    await settle()
    expect(connectOrigins).toEqual(['auto', 'auto'])
    expect(freshDetections).toBe(scenario === 'cancelled detection' ? 2 : 1)
  } finally {
    act(() => root.unmount())
    queryClient.clear()
    container.remove()
    setAutomaticRequestMarking(false)
    vi.unstubAllGlobals()
  }
})
