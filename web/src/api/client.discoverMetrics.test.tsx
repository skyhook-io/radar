// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useDiscoverMetrics } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function discover(connect: Response, collector: Response) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input)
    calls.push(`${init?.method ?? 'GET'} ${url}`)
    return url.includes('/prometheus/connect') ? connect : collector
  }))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let mutate: (() => Promise<unknown>) | undefined
  function Probe() {
    const m = useDiscoverMetrics()
    mutate = () => m.mutateAsync()
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let outcome: unknown
  await act(async () => { outcome = await mutate!() })
  act(() => root.unmount())
  return { outcome, calls }
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

it('stops when the connect call finds a source', async () => {
  const { outcome, calls } = await discover(json({ available: true, connected: true }), json({ detected: true }))
  expect(outcome).toBe('connected')
  expect(calls).toHaveLength(1)
  expect(calls[0]).toMatch(/^POST .*\/prometheus\/connect$/)
})

it('reports a running OpenObserve collector when nothing connected', async () => {
  const { outcome, calls } = await discover(json({ error: 'Prometheus connection failed' }, 502), json({ detected: true }))
  expect(outcome).toBe('collector')
  expect(calls[1]).toMatch(/^GET .*\/metrics\/openobserve\/collector$/)
})

it.each([
  ['no collector', json({ detected: false })],
  ['a Radar without the presence check', json({ error: 'not found' }, 404)],
])('reports nothing found with %s', async (_, collector) => {
  const { outcome } = await discover(json({ available: true, connected: false }), collector)
  expect(outcome).toBe('none')
})
