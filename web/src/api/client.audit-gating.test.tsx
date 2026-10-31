// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ClusterConnectedContext } from '../context/clusterConnected'
import { useAudit } from './client'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function AuditProbe() {
  useAudit([])
  return null
}

afterEach(() => {
  vi.unstubAllGlobals()
})

async function renderWith(connected: boolean) {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL) => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  const container = document.createElement('div')
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ClusterConnectedContext.Provider value={connected}>
          <AuditProbe />
        </ClusterConnectedContext.Provider>
      </QueryClientProvider>,
    )
  })
  await act(async () => { await Promise.resolve() })
  const auditCalls = fetchMock.mock.calls.filter(([input]) => String(input).includes('/audit')).length
  act(() => root.unmount())
  return auditCalls
}

describe('useAudit', () => {
  it('does not ask a disconnected cluster for checks', async () => {
    expect(await renderWith(false)).toBe(0)
  })

  it('fetches checks while connected', async () => {
    expect(await renderWith(true)).toBe(1)
  })
})
