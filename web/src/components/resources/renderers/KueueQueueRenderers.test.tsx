// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ClusterQueueRenderer } from './KueueQueueRenderers'

const queue = { apiVersion: 'kueue.x-k8s.io/v1beta2', kind: 'ClusterQueue', metadata: { name: 'cq', uid: 'cq-uid' }, spec: {} }

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const page = (names: string[], next?: string, uid = 'cq-uid') => ({
  queue: { kind: 'ClusterQueue', name: 'cq', uid },
  workloads: {
    items: names.map(name => ({ namespace: 'team-a', name, uid: name, apiVersion: 'kueue.x-k8s.io/v1beta2', createdAt: null, deleting: false, membership: ['reservation'] })),
    matching: 3, counts: { members: 3, submission: 0, reservation: 3 }, next,
    coverage: { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] },
  },
})

let requests: string[]
let host: HTMLDivElement
let root: Root

function serve(answer: (cursor: string | null) => Response) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    if (url.pathname.endsWith('/capabilities')) return json({ features: { kueueQueueWorkloads: true } })
    if (url.pathname.endsWith('/version')) return json({ currentVersion: 'v1.20.0' })
    requests.push(url.search)
    return answer(url.searchParams.get('cursor'))
  }))
}

const mount = async (client: QueryClient) => {
  root = createRoot(host)
  await act(async () => { root.render(<QueryClientProvider client={client}><ClusterQueueRenderer data={queue} /></QueryClientProvider>) })
}
const text = () => host.textContent ?? ''
const loadMore = async () => {
  const button = [...host.querySelectorAll('button')].find(b => b.textContent === 'Load more')
  expect(button).toBeDefined()
  await act(async () => { button!.click() })
}
const firstPageReads = () => requests.filter(search => !search.includes('cursor=')).length

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  requests = []
  host = document.createElement('div')
})
afterEach(() => {
  act(() => root?.unmount())
  vi.unstubAllGlobals()
})

it('reads the list again from its first page when the next page\'s cursor is refused', async () => {
  serve(cursor => cursor
    ? json({ error: 'the cursor belongs to another list', error_code: 'kueue_cursor_invalid' }, 400)
    : json(page(['wl-a', 'wl-b'], 'next-1')))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await mount(client)
  await vi.waitFor(() => expect(text()).toContain('wl-a'))
  const before = firstPageReads()
  await loadMore()
  await vi.waitFor(() => expect(text()).toContain('The list restarted from its first page.'))
  await vi.waitFor(() => expect(firstPageReads()).toBe(before + 1))
  expect(text()).toContain('wl-b')
  expect(text()).not.toContain('Could not read the next page')
  client.clear()
})

it('keeps reading rather than reporting an empty list while the answer is for another queue with the same name', async () => {
  serve(() => json(page([], undefined, 'earlier-uid')))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await mount(client)
  await vi.waitFor(() => expect(requests.length).toBeGreaterThan(0))
  await act(async () => {})
  expect(text()).toContain('Reading Workloads…')
  expect(text()).not.toMatch(/No Workloads/)
  client.clear()
})

it('reopening a paged list reads its first page once instead of replaying every page', async () => {
  serve(cursor => json(cursor ? page(['wl-c']) : page(['wl-a', 'wl-b'], 'next-1')))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await mount(client)
  await vi.waitFor(() => expect(text()).toContain('wl-a'))
  await loadMore()
  await vi.waitFor(() => expect(text()).toContain('wl-c'))
  act(() => root.unmount())
  const before = requests.length
  await mount(client)
  await vi.waitFor(() => expect(text()).toContain('wl-a'))
  await vi.waitFor(() => expect(text()).not.toContain('wl-c'))
  expect(requests.slice(before)).toEqual([''])
  client.clear()
})
