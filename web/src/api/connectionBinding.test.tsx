// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { apiFetch, useRestartWorkload } from './client'
import { getApiBase } from './config'
import { connectionHeaderValue, observeConnectionStatus, resetConnectionBindingForTest } from './connectionBinding'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  resetConnectionBindingForTest()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

function recordHeaders(status = 200, body = '{}') {
  const seen: (string | null)[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(new Headers(init?.headers).get('X-Radar-Connection'))
    return new Response(body, { status, headers: { 'Content-Type': 'application/json' } })
  }))
  return seen
}

it('follows the latest connection a status names, and keeps it while a switch has none', () => {
  const base = getApiBase()
  expect(connectionHeaderValue(base)).toBeUndefined()
  observeConnectionStatus({ connectionId: 'n-1' }, base)
  observeConnectionStatus({ connectionId: '' }, base) // mid-switch: nothing serving
  expect(connectionHeaderValue(base)).toBe('n-1')
  observeConnectionStatus({ connectionId: 'n-2' }, base)
  expect(connectionHeaderValue(base)).toBe('n-2')
  // Another API base (Radar Hub) keeps its own.
  expect(connectionHeaderValue('/other/api')).toBeUndefined()
})

it('names the connection on requests only once the server published one', async () => {
  const seen = recordHeaders()
  await apiFetch(`${getApiBase()}/connection`)
  observeConnectionStatus({ connectionId: 'n-1' })
  await apiFetch(`${getApiBase()}/workloads/deployments/default/web/restart`, { method: 'POST' })
  expect(seen).toEqual([null, 'n-1'])
})

it("shows the server's refusal when a restart names a connection it no longer serves", async () => {
  observeConnectionStatus({ connectionId: 'n-1' })
  const message = 'Nothing was changed: the cluster changed since this view loaded. Check the current cluster and try again.'
  const seen = recordHeaders(409, JSON.stringify({ error: message, code: 'context_changed', error_code: 'context_changed' }))
  const client = new QueryClient()
  let restart: ReturnType<typeof useRestartWorkload> | undefined
  function Probe() {
    restart = useRestartWorkload()
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
  })
  let error: unknown
  await act(async () => {
    await restart!.mutateAsync({ kind: 'deployments', namespace: 'default', name: 'web' }).catch((e) => { error = e })
  })
  expect(seen).toEqual(['n-1'])
  expect((error as Error).message).toBe(message)
  act(() => root.unmount())
})
