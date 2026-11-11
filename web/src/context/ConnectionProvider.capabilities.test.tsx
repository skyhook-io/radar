// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { ConnectionProvider } from './ConnectionContext'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function CapabilitiesUser({ fetchCaps }: { fetchCaps: () => Promise<{ exec: boolean }> }) {
  useQuery({ queryKey: ['capabilities'], queryFn: fetchCaps, staleTime: 60_000 })
  return null
}

// The server answers capabilities with a restricted placeholder until the
// cluster connects, so the first connect must fetch them again.
it('refetches capabilities on the first connect', async () => {
  vi.useFakeTimers()
  let state = 'connecting'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(
    JSON.stringify({ state, context: 'prod' }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )))
  const fetchCaps = vi.fn(async () => ({ exec: state === 'connected' }))
  const container = document.createElement('div')
  const root = createRoot(container)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <ConnectionProvider>
          <CapabilitiesUser fetchCaps={fetchCaps} />
        </ConnectionProvider>
      </QueryClientProvider>,
    )
    await vi.advanceTimersByTimeAsync(100)
  })
  expect(fetchCaps).toHaveBeenCalledTimes(1)

  state = 'connected'
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000)
  })
  expect(client.getQueryData(['capabilities'])).toEqual({ exec: true })
  act(() => root.unmount())
})
