import { afterEach, expect, it, vi } from 'vitest'
import { ApiError, apiFetch, fetchJSON } from './client'
import { getApiBase } from './config'
import { observeConnectionStatus, resetConnectionBindingForTest, setConnectionMismatchHandler } from './connectionBinding'

const BOOT = '2026-11-21T03:00:00.000000000Z'

afterEach(() => {
  resetConnectionBindingForTest()
  vi.unstubAllGlobals()
})

function serve(stamp: string | null) {
  const seen: (string | null)[] = []
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    seen.push(new Headers(init?.headers).get('X-Radar-Connection'))
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (stamp !== null) headers['X-Radar-Connection'] = stamp
    return new Response('{"items":[]}', { status: 200, headers })
  }))
  return seen
}

it('names the connection the tab shows and drops a response from another one', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  const seen = serve('n-2')
  const response = await apiFetch(`${getApiBase()}/resources/pods`)
  expect(seen).toEqual(['n-1'])
  expect(response.status).toBe(409)
  expect(await response.json()).toMatchObject({ code: 'context_changed' })
  await expect(fetchJSON('/resources/pods')).rejects.toBeInstanceOf(ApiError)
})

it('passes unlabelled responses and responses from the tab\'s own connection', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  serve(null)
  await expect(fetchJSON('/settings')).resolves.toEqual({ items: [] })
  serve('n-1')
  await expect(fetchJSON('/resources/pods')).resolves.toEqual({ items: [] })
})

it('sends no header to a server that does not bind connections', async () => {
  const seen = serve(null)
  await apiFetch(`${getApiBase()}/resources/pods`)
  expect(seen).toEqual([null])
})

it('keeps a mid-switch refusal\'s own code instead of calling it a cluster change', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"switching","code":"cluster_changing"}', {
    status: 503, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'none' },
  })))
  const refresh = vi.fn()
  setConnectionMismatchHandler(refresh)
  const response = await apiFetch(`${getApiBase()}/workloads/deployments/default/web/restart`, { method: 'POST' })
  expect(response.status).toBe(503)
  await Promise.resolve()
  expect(refresh).toHaveBeenCalledTimes(1) // and the tab re-reads its status
})

it('re-reads the status when the server refuses a write for another connection', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  const refresh = vi.fn()
  setConnectionMismatchHandler(refresh)
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"Nothing was changed: the cluster changed","code":"context_changed"}', {
    status: 409, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'n-2' },
  })))
  const response = await apiFetch(`${getApiBase()}/workloads/deployments/default/web/restart`, { method: 'POST' })
  expect(response.status).toBe(409)
  // The server's own refusal reaches the user: it says nothing was changed.
  expect(await response.json()).toMatchObject({ code: 'context_changed', error: 'Nothing was changed: the cluster changed' })
  await Promise.resolve()
  expect(refresh).toHaveBeenCalledTimes(1)
})

it('drops a result whose connection the tab left while it was in flight', async () => {
  observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, getApiBase())
  vi.stubGlobal('fetch', vi.fn(async () => {
    const r = new Response('{"items":[]}', { status: 200, headers: { 'Content-Type': 'application/json', 'X-Radar-Connection': 'n-1' } })
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 2, bootAt: BOOT }, getApiBase())
    return r
  }))
  await expect(fetchJSON('/resources/pods')).rejects.toMatchObject({ status: 409 })
})
