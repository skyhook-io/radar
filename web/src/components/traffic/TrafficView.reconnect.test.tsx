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

it.each([
  'success',
  'failure',
  'cancelled detection',
  'unmounted during detection',
  'cancelled manual Retry',
  'status stays disconnected',
  'initial status stays disconnected',
  'flows lose source without Settings invalidation',
  'Beyla unavailable until Metrics repair',
])('handles traffic connection status and source loss (%s)', async (scenario) => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { enabled: false, retryDelay: 0 } } })
  const container = document.body.appendChild(document.createElement('div'))
  const root = createRoot(container)
  const connectOrigins: (string | null)[] = []
  const connectionFailure = 'Could not connect to Hubble'
  const notConnected = 'Not connected to Hubble Relay. Call Connect() first or use the Traffic view to establish connection.'
  const source = scenario === 'Beyla unavailable until Metrics repair' ? 'beyla' : 'hubble'
  const manualRetry = scenario === 'cancelled manual Retry'
  const cancelledDetection = manualRetry ? 2 : scenario === 'cancelled detection' ? 1 : 0
  let connected = false
  let sourceAvailable = true
  let freshDetections = 0
  let resumeDetection: (() => void) | undefined
  let unmounted = false
  let warnings = 0
  let disconnectedStatusReads = 0
  let statusReadsAfterReconnect = 0
  queryClient.setQueryDefaults(['traffic-sources'], { enabled: true })
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  setAutomaticRequestMarking(true)
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const { pathname, search } = new URL(url, window.location.origin)
    switch (pathname) {
      case '/api/traffic/sources':
        if (!search) {
          freshDetections++
          if (freshDetections === cancelledDetection) {
            return new Promise<Response>((_, reject) => {
              init!.signal!.addEventListener('abort', () => reject(init!.signal!.reason), { once: true })
            })
          }
          if (scenario === 'unmounted during detection') {
            await new Promise<void>(resolve => { resumeDetection = resolve })
          }
        }
        return Response.json({
          cluster: {}, active: sourceAvailable ? source : '', notDetected: [],
          detected: [{ name: source, status: sourceAvailable ? 'available' : 'not_found' }],
        })
      case '/api/traffic/connection':
        if (connectOrigins.length > 1) statusReadsAfterReconnect++
        if (!connected) disconnectedStatusReads++
        return Response.json({ connected: connected &&
          scenario !== 'initial status stays disconnected' &&
          !((scenario === 'status stays disconnected' || manualRetry) && connectOrigins.length > 1) })
      case '/api/traffic/connect':
        expect(init?.method).toBe('POST')
        connectOrigins.push(new Headers(init?.headers).get('X-Radar-Request-Origin'))
        if ((connectOrigins.length > 1 && scenario === 'failure') || (connectOrigins.length === 2 && manualRetry)) {
          return Response.json({ connected: false, error: connectionFailure })
        }
        if (!sourceAvailable) {
          return Response.json({ error: 'No traffic source available' }, { status: 503 })
        }
        connected = true
        return Response.json({ connected })
      case '/api/traffic/flows':
        if (!connected && (scenario === 'flows lose source without Settings invalidation' || !sourceAvailable)) {
          return Response.json({ error: 'no traffic source available' }, { status: 503 })
        }
        if (!connected) warnings++
        return Response.json({ source, timestamp: '', flows: [], aggregated: [], warning: connected ? undefined : notConnected })
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
      expect(queryClient.getQueryData(['traffic-connection'])).toEqual({
        connected: scenario !== 'initial status stays disconnected',
      })
      expect(queryClient.getQueryState(['traffic-connection'])?.fetchStatus).toBe('idle')
    })
    await settle()
    expect(connectOrigins).toEqual(['auto'])
    expect(freshDetections).toBe(0)
    if (scenario === 'initial status stays disconnected') return

    connected = false
    const statusReadsBeforeLoss = disconnectedStatusReads
    if (scenario === 'flows lose source without Settings invalidation' || source === 'beyla') {
      sourceAvailable = source !== 'beyla'
      await act(async () => {
        await queryClient.refetchQueries({ queryKey: ['traffic-flows'] })
      })
    } else {
      const warningsBeforeSave = warnings
      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
      expect(warnings).toBeGreaterThan(warningsBeforeSave)
    }

    if (scenario === 'unmounted during detection') {
      await vi.waitFor(() => expect(resumeDetection).toBeDefined())
      expect(connectOrigins).toEqual(['auto'])
      act(() => root.unmount())
      unmounted = true
      resumeDetection!()
      await settle()
      expect(queryClient.getQueryState(['traffic-sources'])?.status).toBe('success')
      expect(connectOrigins).toEqual(['auto'])
      return
    }

    if (source === 'beyla') {
      await vi.waitFor(async () => {
        await settle()
        expect(container.textContent).toContain('Beyla is installed but not usable')
      })
      expect(freshDetections).toBe(1)
      expect(connectOrigins).toEqual(['auto'])
      sourceAvailable = true
      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    }

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
      if (scenario === 'failure' || manualRetry) {
        expect(container.textContent).toContain(connectionFailure)
        expect(container.textContent).toContain('Retry Connection')
      } else if (scenario === 'status stays disconnected') {
        expect(statusReadsAfterReconnect).toBeGreaterThan(0)
      } else {
        expect(queryClient.getQueryData(['traffic-connection'])).toEqual({ connected: true })
      }
    })
    if (manualRetry) {
      const retry = Array.from(container.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Retry Connection')
      expect(retry).toBeDefined()
      await act(async () => retry!.click())
      await vi.waitFor(() => expect(freshDetections).toBe(2))
      expect(connectOrigins).toEqual(['auto', 'auto'])

      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
      await vi.waitFor(async () => {
        await settle()
        expect(connectOrigins).toEqual(['auto', 'auto', 'auto'])
        expect(queryClient.getQueryData(['traffic-connection'])).toEqual({ connected: false })
        expect(queryClient.getQueryState(['traffic-connection'])?.fetchStatus).toBe('idle')
      })
      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
      await settle()
      expect(connectOrigins).toEqual(['auto', 'auto', 'auto'])
      expect(freshDetections).toBe(3)
      return
    }
    if (scenario === 'flows lose source without Settings invalidation') {
      expect(disconnectedStatusReads - statusReadsBeforeLoss).toBe(1)
    } else {
      await act(async () => invalidateIntegrationQueries(queryClient, ['metrics']))
    }
    await settle()
    expect(container.textContent).not.toContain('No traffic source available')
    expect(connectOrigins).toEqual(['auto', 'auto'])
    expect(freshDetections).toBe(scenario === 'cancelled detection' ? 2 : 1)
  } finally {
    if (!unmounted) act(() => root.unmount())
    queryClient.clear()
    container.remove()
    setAutomaticRequestMarking(false)
    vi.unstubAllGlobals()
  }
})
