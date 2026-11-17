// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { ClusterConnectedContext } from '../context/clusterConnected'
import { useAudit, useAuditBadges } from './client'
import { setApiBase } from './config'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const badgeKey = ['audit', ['shop'], 'badges']
const fullKey = ['audit', ['shop']]
let roots: Root[] = []
let clients: QueryClient[] = []
afterEach(() => {
  act(() => { for (const root of roots) root.unmount() })
  for (const client of clients) client.clear()
  roots = []
  clients = []
  setApiBase('/api')
  vi.unstubAllGlobals()
})

// Holds each response until the test releases it, so the badge and full
// queries can finish in any order.
function setup() {
  const requests: { url: string; respond: (scan: string) => void; signal?: AbortSignal | null }[] = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((resolve) => {
    requests.push({ url: String(input), signal: init?.signal, respond(scan) {
      resolve(new Response(JSON.stringify({ findings: [{ checkID: 'test', name: scan }], checks: { test: { badgeWorthy: true } }, groups: [], scan }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    } })
  })))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  clients.push(client)
  const root = createRoot(document.createElement('div'))
  roots.push(root)
  function Full() { useAudit(['shop']); return null }
  function Badges({ enabled }: { enabled: boolean }) { useAuditBadges(['shop'], { enabled }); return null }
  async function render(full = true, enabled = true, key = 'A') {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><ClusterConnectedContext.Provider value={true}>
        <Badges key={`badges-${key}`} enabled={enabled} />
        {full && <Full key={`full-${key}`} />}
      </ClusterConnectedContext.Provider></QueryClientProvider>)
    })
  }
  async function respond(index: number, scan: string) {
    await act(async () => { requests[index].respond(scan); await new Promise(r => setTimeout(r, 0)) })
  }
  return { requests, client, root, render, respond }
}


// The badges and the full audit are separate queries over the same scan. A
// newer full scan must leave the badges on a scan at least as new, never an
// older one, and never one from before a context switch.
it('restarts the first active badges request, rejects its late result, and does not loop on rerender', async () => {
  const h = setup()
  await h.render()
  expect(h.requests.map(r => r.url)).toEqual(['/api/audit?include=badges&namespaces=shop', '/api/audit?namespaces=shop'])
  await h.respond(1, 'new-full')
  expect(h.requests).toHaveLength(3)
  expect(h.requests[0].signal?.aborted).toBe(true)
  expect(h.requests[2].signal?.aborted).toBe(false)
  expect(h.client.getQueryState(badgeKey)).toMatchObject({ data: undefined, status: 'pending', fetchStatus: 'fetching' })
  await h.respond(0, 'old-badges')
  expect(h.client.getQueryData(badgeKey)).toBeUndefined()
  await h.respond(2, 'new-badges')
  expect(h.client.getQueryData(badgeKey)).toMatchObject({ findings: [{ name: 'new-badges' }] })
  expect(h.client.getQueryData(fullKey)).toMatchObject({ scan: 'new-full' })
  for (let i = 0; i < 5; i++) await h.render()
  expect(h.requests).toHaveLength(3)
})

it('refreshes active badges after leaving Checks mid-fetch', async () => {
  const h = setup()
  await h.render()
  await h.render(false)
  await h.respond(1, 'full-after-leaving')
  expect(h.requests).toHaveLength(3)
  await h.respond(2, 'new-badges')
  await h.respond(0, 'old-badges')
  expect(h.client.getQueryData(badgeKey)).toMatchObject({ findings: [{ name: 'new-badges' }] })
  expect(h.client.getQueryData(fullKey)).toMatchObject({ scan: 'full-after-leaving' })
})

it('does not recreate removed queries when an old full response arrives', async () => {
  const h = setup()
  await h.render()
  act(() => h.root.render(null))
  h.client.removeQueries()
  await h.respond(1, 'old-cluster-full')
  await h.respond(0, 'old-cluster-badges')
  expect(h.client.getQueryCache().getAll()).toHaveLength(0)
  expect(h.requests).toHaveLength(2)
})

it('an old full response can restart current-cluster badges but cannot write old-cluster data', async () => {
  const h = setup()
  setApiBase('/c/A/api')
  await h.render()
  act(() => h.root.render(null))
  h.client.removeQueries()
  setApiBase('/c/B/api')
  await h.render(false, true, 'B')
  expect(h.requests).toHaveLength(3)
  expect(h.requests[2].url).toBe('/c/B/api/audit?include=badges&namespaces=shop')
  await h.respond(1, 'A-full')
  expect(h.requests).toHaveLength(4)
  expect(h.requests[3].url).toBe('/c/B/api/audit?include=badges&namespaces=shop')
  await h.respond(0, 'A-badges')
  await h.respond(2, 'B-cancelled-badges')
  expect(h.client.getQueryData(fullKey)).toBeUndefined()
  expect(h.client.getQueryData(badgeKey)).toBeUndefined()
  await h.respond(3, 'B-fresh-badges')
  expect(h.client.getQueryData(badgeKey)).toMatchObject({ findings: [{ name: 'B-fresh-badges' }] })
  expect(h.requests).toHaveLength(4)
})
