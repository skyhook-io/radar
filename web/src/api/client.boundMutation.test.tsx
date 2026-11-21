// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, onlineManager } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useRestartWorkload } from './client'
import { getApiBase } from './config'
import { observeConnectionStatus, resetConnectionBindingForTest } from './connectionBinding'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  onlineManager.setOnline(true)
  resetConnectionBindingForTest()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

// An offline-paused mutation would resume after the tab adopted another
// connection and name that one; bound mutations run when confirmed.
it('sends a workload restart at once, naming the connection, even while the browser reports offline', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: '2026-11-21T03:00:00.000000000Z' }, getApiBase())
  const seen: (string | null)[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(new Headers(init?.headers).get('X-Radar-Connection'))
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'n-1' } })
  }))
  onlineManager.setOnline(false)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let mutate: ReturnType<typeof useRestartWorkload>['mutate'] | undefined
  function Probe() {
    mutate = useRestartWorkload().mutate
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
  })
  await act(async () => {
    mutate!({ kind: 'deployments', namespace: 'default', name: 'web' })
    await new Promise((r) => setTimeout(r, 20))
  })
  expect(seen).toEqual(['n-1'])
  act(() => root.unmount())
})
