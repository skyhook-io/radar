// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import type { PrometheusStatus } from '../../api/client'
import { NavCustomizationProvider } from '../../context/NavCustomization'
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({ metrics: false }) }))

const { MetricsNotConnected, OpenObserveUnavailable } = await import('./MetricsNotConnected')

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

it('drops a discovery result once the metrics source changes', async () => {
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input) => String(input).includes('/prometheus/connect')
    ? new Response(JSON.stringify({ error: 'Prometheus connection failed' }), { status: 502 })
    : new Response(JSON.stringify({ detected: false }))))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } })
  const container = document.createElement('div')
  const root = createRoot(container)
  const render = (status: PrometheusStatus) => root.render(
    <QueryClientProvider client={client}><NavCustomizationProvider value={{}}>
      <MetricsNotConnected status={status} isWorkload={false} />
    </NavCustomizationProvider></QueryClientProvider>,
  )
  const before: PrometheusStatus = { available: false, connected: false, openobserve: 'local' }
  await act(async () => render(before))
  const discover = () => [...container.querySelectorAll('button')].find((b) => b.textContent === 'Discover metrics')
  await act(async () => discover()!.click())
  expect(container.querySelector('[role="status"]')?.textContent).toBe('No metrics source found.')
  expect(discover()).toBeUndefined()

  await act(async () => render({ ...before, source: { kind: 'openobserve', via: 'local', warning: 'Checking…', pending: true } }))
  expect(container.querySelector('[role="status"]')).toBeNull()
  expect(discover()).toBeDefined()
  act(() => root.unmount())
  vi.unstubAllGlobals()
})

it('says OpenObserve unavailable once', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const container = document.createElement('div')
  const root = createRoot(container)
  const status: PrometheusStatus = { available: false, connected: true, source: { kind: 'openobserve', via: 'hub', unavailable: true, error: 'OpenObserve unavailable: Radar Hub could not be reached' } }
  await act(async () => root.render(<NavCustomizationProvider value={{}}><OpenObserveUnavailable status={status} /></NavCustomizationProvider>))
  expect(container.textContent?.match(/OpenObserve unavailable/g)).toHaveLength(1)
  expect(container.textContent).toContain('Radar Hub could not be reached')
  act(() => root.unmount())
  vi.unstubAllGlobals()
})
