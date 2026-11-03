// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getApiBase } from '../../api/config'
import { RELATED_GET_RECHECK_MS, useRelatedObjects, type RelatedObjects } from './useRelatedReads'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let client: QueryClient
let listStatus: number
const requests: string[] = []
let latest: RelatedObjects | undefined

beforeEach(() => {
  listStatus = 200
  requests.length = 0
  latest = undefined
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url.pathname)
    if (url.pathname === '/api/resources/clusterqueues') {
      return listStatus === 200
        ? new Response(JSON.stringify({ items: [{ metadata: { name: 'present' } }], coverage: { status: 'complete', scope: { type: 'cluster' }, read: {}, gaps: [] } }), { status: 200 })
        : new Response(JSON.stringify(listStatus === 4030 ? { error: 'insufficient permissions' } : { error: 'no list', error_code: 'user_forbidden' }), { status: 403 })
    }
    if (url.pathname === '/api/resources/clusterqueues/_/present') return new Response(JSON.stringify({ resource: { metadata: { name: 'present' } } }), { status: 200 })
    if (url.pathname === '/api/resources/clusterqueues/_/gone') return new Response(JSON.stringify({ error: 'gone', error_code: 'object_not_found' }), { status: 404 })
    throw new Error(`Unexpected request: ${input}`)
  }))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

function Probe({ onRead, version, names = ['present', 'gone'] }: { onRead: (read: RelatedObjects) => void; version?: string; names?: string[] }) {
  const read = useRelatedObjects('clusterqueues', 'kueue.x-k8s.io', undefined, names, true, version)
  useEffect(() => onRead(read))
  return null
}

async function render() {
  await act(async () => root.render(<QueryClientProvider client={client}><Probe onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.gone.state).not.toBe('loading'), { timeout: 6000 })
}

it('answers every name from one list read when the viewer may list', async () => {
  await render()
  expect(latest?.byName.present.state).toBe('found')
  expect(latest?.byName.gone).toEqual({ state: 'not_found' })
  expect(requests).toEqual(['/api/resources/clusterqueues'])
})

it('reads each name with a GET when the viewer may get but not list the kind', async () => {
  listStatus = 403
  await render()
  await vi.waitFor(() => expect(latest?.byName.present.state).toBe('found'), { timeout: 6000 })
  expect(latest?.byName.gone).toEqual({ state: 'not_found' })
  expect(latest?.all).toMatchObject({ state: 'unavailable', code: 'user_forbidden' })
  expect(requests).toEqual(expect.arrayContaining(['/api/resources/clusterqueues/_/present', '/api/resources/clusterqueues/_/gone']))
  // No watch events reach a viewer who can't list the kind, so the GETs re-read on their own.
  const present = client.getQueryCache().findAll({ queryKey: ['resource', 'clusterqueues', '', 'present'] })[0]
  const interval = (present?.options as { refetchInterval?: unknown } | undefined)?.refetchInterval
  expect(typeof interval === 'function' ? interval(present) : interval).toBe(RELATED_GET_RECHECK_MS)
})

it('also reads by GET when an older Radar denies the list with an uncoded 403', async () => {
  listStatus = 4030
  await render()
  await vi.waitFor(() => expect(latest?.byName.present.state).toBe('found'), { timeout: 6000 })
  expect(latest?.all).toMatchObject({ state: 'unavailable', code: undefined })
})

it('doesn’t call a name missing from a list cached before the hook mounted, until the re-read lands', async () => {
  // Another view read the list a moment ago, before the object existed.
  client.setQueryData(['resources', 'clusterqueues', 'kueue.x-k8s.io', undefined, getApiBase()], { items: [{ metadata: { name: 'present' } }], coverage: { status: 'complete', scope: { type: 'cluster' }, read: {}, gaps: [] } }, { updatedAt: Date.now() - 5000 })
  let release: (() => void) | undefined
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => {
    release = () => resolve(new Response(JSON.stringify({ items: [{ metadata: { name: 'present' } }, { metadata: { name: 'gone' } }], coverage: { status: 'complete', scope: { type: 'cluster' }, read: {}, gaps: [] } })))
  })))
  await act(async () => root.render(<QueryClientProvider client={client}><Probe onRead={read => { latest = read }} /></QueryClientProvider>))
  expect(latest?.byName.gone).toEqual({ state: 'loading' })
  // Nor does an object found in that old list lend its state before the re-read lands.
  expect(latest?.byName.present).toEqual({ state: 'loading' })
  await act(async () => release?.())
  await vi.waitFor(() => expect(latest?.byName.gone.state).toBe('found'))
})

it('re-reads, and stops claiming absence, when the object on screen changes', async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="1" onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.gone).toEqual({ state: 'not_found' }))
  let release: (() => void) | undefined
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => {
    release = () => resolve(new Response(JSON.stringify({ items: [{ metadata: { name: 'present' } }, { metadata: { name: 'gone' } }], coverage: { status: 'complete', scope: { type: 'cluster' }, read: {}, gaps: [] } })))
  })))
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="2" onRead={read => { latest = read }} /></QueryClientProvider>))
  expect(latest?.byName.gone).toEqual({ state: 'loading' })
  await act(async () => release?.())
  await vi.waitFor(() => expect(latest?.byName.gone.state).toBe('found'))
})

it('makes no new request when the subject key stays the same', async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="a" onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.gone).toEqual({ state: 'not_found' }))
  const before = requests.length
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="a" onRead={read => { latest = read }} /></QueryClientProvider>))
  expect(requests.length).toBe(before)
})

it('neither retries nor polls a denied GET', async () => {
  listStatus = 403
  vi.mocked(fetch).mockImplementation(async (input: any) => {
    const url = new URL(String(input), 'http://localhost')
    requests.push(url.pathname)
    return new Response(JSON.stringify({ error: 'no', error_code: 'user_forbidden' }), { status: 403 })
  })
  await act(async () => root.render(<QueryClientProvider client={client}><Probe names={['denied']} onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.denied).toMatchObject({ state: 'unavailable', code: 'user_forbidden' }))
  const query = client.getQueryCache().findAll({ queryKey: ['resource', 'clusterqueues', '', 'denied'] })[0]
  const interval = (query?.options as { refetchInterval?: unknown }).refetchInterval
  expect(typeof interval === 'function' ? interval(query) : interval).toBe(false)
  expect(requests.filter(path => path === '/api/resources/clusterqueues/_/denied')).toHaveLength(1)
})

it('re-reads fallback GETs when the subject changes, and holds absence until they land', async () => {
  listStatus = 403
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="1" onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.gone).toEqual({ state: 'not_found' }), { timeout: 6000 })
  let release: (() => void) | undefined
  vi.mocked(fetch).mockImplementation((input: any) => {
    const path = new URL(String(input), 'http://localhost').pathname
    requests.push(path)
    if (path === '/api/resources/clusterqueues') return Promise.resolve(new Response(JSON.stringify({ error: 'no', error_code: 'user_forbidden' }), { status: 403 }))
    if (path === '/api/resources/clusterqueues/_/gone') return new Promise<Response>(resolve => { release = () => resolve(new Response(JSON.stringify({ resource: { metadata: { name: 'gone' } } }))) })
    return Promise.resolve(new Response(JSON.stringify({ resource: { metadata: { name: 'present' } } })))
  })
  await act(async () => root.render(<QueryClientProvider client={client}><Probe version="2" onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.byName.gone).toEqual({ state: 'loading' }))
  await act(async () => release?.())
  await vi.waitFor(() => expect(latest?.byName.gone.state).toBe('found'))
})

it('doesn’t serve a GET result cached before mount as current, found or not', async () => {
  listStatus = 403
  client.setQueryData(['resource', 'clusterqueues', '', 'present', 'kueue.x-k8s.io', getApiBase()], { resource: { metadata: { name: 'present', stale: true } } }, { updatedAt: Date.now() - 5000 })
  let release: (() => void) | undefined
  vi.mocked(fetch).mockImplementation((input: any) => {
    const path = new URL(String(input), 'http://localhost').pathname
    requests.push(path)
    if (path === '/api/resources/clusterqueues') return Promise.resolve(new Response(JSON.stringify({ error: 'no', error_code: 'user_forbidden' }), { status: 403 }))
    if (path === '/api/resources/clusterqueues/_/present') return new Promise<Response>(resolve => { release = () => resolve(new Response(JSON.stringify({ resource: { metadata: { name: 'present' } } }))) })
    return Promise.resolve(new Response(JSON.stringify({ error: 'gone', error_code: 'object_not_found' }), { status: 404 }))
  })
  await act(async () => root.render(<QueryClientProvider client={client}><Probe onRead={read => { latest = read }} /></QueryClientProvider>))
  await vi.waitFor(() => expect(latest?.all).toMatchObject({ state: 'unavailable' }))
  expect(latest?.byName.present).toEqual({ state: 'loading' })
  await act(async () => release?.())
  await vi.waitFor(() => expect(latest?.byName.present).toEqual({ state: 'found', object: { metadata: { name: 'present' } } }))
})
