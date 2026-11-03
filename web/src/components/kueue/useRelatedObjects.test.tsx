// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
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
        ? new Response(JSON.stringify({ items: [{ metadata: { name: 'present' } }], coverage: { status: 'complete', scope: {}, read: {}, gaps: [] } }), { status: 200 })
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

function Probe() {
  latest = useRelatedObjects('clusterqueues', 'kueue.x-k8s.io', undefined, ['present', 'gone'])
  return null
}

async function render() {
  await act(async () => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>))
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
