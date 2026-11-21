// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MutationCache, QueryClient, QueryClientProvider, onlineManager } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { useBulkRestartWorkloads, useRestartWorkload } from './client'
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

// A host's global onMutate runs before the request; if the tab moves to
// another connection meanwhile, the restart still names the one confirmed on.
it('sends a confirmed restart for the connection the user confirmed on, whatever happens before it runs', async () => {
  const BOOT = '2026-11-21T03:00:00.000000000Z'
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  const seen: (string | null)[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(new Headers(init?.headers).get('X-Radar-Connection'))
    return new Response('{"error":"changed","code":"context_changed"}', { status: 409, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'n-2' } })
  }))
  let release: () => void = () => {}
  const held = new Promise<void>((r) => { release = r })
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 } },
    mutationCache: new MutationCache({ onMutate: () => held }),
  })
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
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 2, bootAt: BOOT }, getApiBase())
    release()
    await new Promise((r) => setTimeout(r, 30))
  })
  // Sent once (never retried), naming the connection confirmed on.
  expect(seen).toEqual(['n-1'])
  act(() => root.unmount())
})

// A view mounted before the tab moved to another connection (a closing drawer,
// a dialog it portaled, a page that outlived the reset) can't write.
it('refuses a restart from a view mounted before the tab moved to another connection, without sending it', async () => {
  const BOOT = '2026-11-21T03:00:00.000000000Z'
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  const fetchMock = vi.fn(async () => new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let hook: ReturnType<typeof useRestartWorkload> | undefined
  function Probe() {
    hook = useRestartWorkload()
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
  })
  observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 2, bootAt: BOOT }, getApiBase())
  let error: unknown
  await act(async () => {
    await hook!.mutateAsync({ kind: 'deployments', namespace: 'default', name: 'web' }).catch((e) => { error = e })
  })
  expect(fetchMock).not.toHaveBeenCalled()
  expect(error).toMatchObject({ status: 409, message: expect.stringContaining('Nothing was changed') })
  act(() => root.unmount())
})

// Bulk actions are confirmed inside the resource table, which remounts on a
// new connection; the page around it may not, and must still be able to act.
it('sends a bulk restart for the current connection from a page that outlived a reset', async () => {
  const BOOT = '2026-11-21T03:00:00.000000000Z'
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  const seen: (string | null)[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(new Headers(init?.headers).get('X-Radar-Connection'))
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'n-2' } })
  }))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let hook: ReturnType<typeof useBulkRestartWorkloads> | undefined
  function Probe() {
    hook = useBulkRestartWorkloads()
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
  })
  observeConnectionStatus({ context: 'a', connectionId: 'n-2', seq: 2, bootAt: BOOT }, getApiBase())
  await act(async () => {
    await hook!.mutateAsync({ items: [{ kind: 'deployments', namespace: 'default', name: 'web' }] })
  })
  expect(seen).toEqual(['n-2'])
  act(() => root.unmount())
})
