// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useRBACSubject } from './rbac'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function Probe() {
  useRBACSubject('ServiceAccount', 'shop', 'default')
  return null
}

afterEach(() => vi.unstubAllGlobals())

// Opens the "drawer" twice on one query client and counts RBAC requests.
async function requestsOverTwoOpens(status: number, error: string) {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL) =>
    new Response(JSON.stringify({ error }), { status, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  for (let open = 0; open < 2; open++) {
    const root = createRoot(document.createElement('div'))
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
    })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
    act(() => root.unmount())
  }
  return fetchMock.mock.calls.filter(([input]) => String(input).includes('/rbac/subject')).length
}

describe('useRBACSubject', () => {
  it('asks once when Radar cannot read RBAC', async () => {
    expect(await requestsOverTwoOpens(503, 'RBAC cache not available (informers not synced)')).toBe(1)
  })

  it('asks again on the next open after any other failure', async () => {
    expect(await requestsOverTwoOpens(404, 'not found')).toBe(2)
  })
})
