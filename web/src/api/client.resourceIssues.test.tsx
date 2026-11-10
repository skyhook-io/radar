// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useResourceIssues } from './client'
import { setApiBase } from './config'

interface Props { instance?: number; namespace?: string; observation?: string; enabled?: boolean; name?: string }
function Probe({ observation, enabled = true, name = 'collector', namespace = 'test' }: Props) {
  const query = useResourceIssues('opentelemetrycollectors', 'opentelemetry.io', namespace, name, enabled, observation)
  return <span>{query.data?.length ?? 'pending'}</span>
}

const clients: QueryClient[] = []
const roots: ReturnType<typeof createRoot>[] = []
const hosts: HTMLElement[] = []

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  focusManager.setFocused(true)
  setApiBase('/api')
})

afterEach(() => {
  roots.splice(0).forEach(root => act(() => root.unmount()))
  clients.splice(0).forEach(client => client.clear())
  hosts.splice(0).forEach(host => host.remove())
  focusManager.setFocused(undefined)
  setApiBase('/api')
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function setup(props: Props = {}, supported: boolean | 'unknown' = true) {
  let endpointSupported = supported === true
  let count = 1
  let heldReply: Promise<Response> | undefined
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const path = new URL(input, 'http://localhost').pathname
    requests.push(path)
    if (path.includes('/issues/resource/') && !endpointSupported) return new Response('404 page not found\n', { status: 404, headers: { 'Content-Type': 'text/plain; charset=utf-8' } })
    if (path.includes('/issues/resource/') && heldReply) {
      const reply = heldReply
      heldReply = undefined
      return reply
    }
    const body = path.endsWith('/capabilities')
      ? supported === 'unknown' ? { version: 'dev', features: {} } : { version: supported ? 'v1.18.2' : 'v1.7.0', features: { resourceIssues: supported } }
      : Array.from({ length: count }, (_, index) => ({ id: String(index), message: 'Recorded failure' }))
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
  if (supported === false) client.setQueryData(['version-check', '/api'], { currentVersion: 'v1.7.0' })
  clients.push(client)
  const host = document.createElement('div')
  document.body.append(host)
  hosts.push(host)
  const root = createRoot(host)
  roots.push(root)
  async function render(next: Props) {
    await act(async () => { root.render(<QueryClientProvider client={client}><Probe key={next.instance} {...next} /></QueryClientProvider>) })
  }
  await render(props)
  if (supported === true && props.enabled !== false) await vi.waitFor(() => expect(host.textContent).toBe('1'))
  const issueRequests = () => requests.filter(path => path.includes('/issues/resource/'))
  const advance = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
  const holdNextReply = () => {
    let release!: () => void
    const body = JSON.stringify(Array.from({ length: count }, (_, index) => ({ id: String(index) })))
    heldReply = new Promise(resolve => { release = () => resolve(new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })) })
    return release
  }
  return { client, host, root, render, advance, setEndpointSupported: (value: boolean) => { endpointSupported = value }, issueRequests, requests, holdNextReply, setCount: (value: number) => { count = value } }
}

it('clears an old Issue after a watched root changes without reopening', async () => {
  const h = await setup({ observation: 'managed-rv' })
  h.setCount(0)
  await h.render({ observation: 'unmanaged-rv' })
  await h.advance(10001)
  expect(h.host.textContent).toBe('0')
  expect(h.issueRequests()).toHaveLength(2)
})

it('coalesces watch churn and keeps one cache entry for the resource', async () => {
  const h = await setup({ observation: '0' })
  for (let index = 1; index <= 20; index++) {
    await h.render({ observation: String(index) })
    await h.advance(200)
  }
  expect(h.issueRequests()).toHaveLength(1)
  await h.advance(6001)
  expect(h.issueRequests()).toHaveLength(2)
  expect(h.client.getQueryCache().findAll({ queryKey: ['issues', 'resource'] })).toHaveLength(1)
})

it('does not refresh when the observation is unchanged', async () => {
  const observation = 'root-rv'
  const h = await setup({ observation })
  await h.render({ observation })
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('updates Pod-only and timer-driven findings even when the root does not change', async () => {
  const h = await setup({ observation: 'same-rv' })
  h.setCount(0)
  await h.advance(60001)
  expect(h.host.textContent).toBe('0')
  expect(h.issueRequests()).toHaveLength(2)
})

it('does not poll a hidden tab', async () => {
  const h = await setup({ observation: 'same-rv' })
  focusManager.setFocused(false)
  await h.advance(120001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('refreshes a hidden observation when focus returns with the production focus default off', async () => {
  const h = await setup({ observation: 'first' })
  focusManager.setFocused(false)
  h.setCount(0)
  await h.render({ observation: 'changed' })
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(1)
  await act(async () => { focusManager.setFocused(true) })
  await h.advance(1)
  expect(h.host.textContent).toBe('0')
  expect(h.issueRequests()).toHaveLength(2)
})

it('does not duplicate the initial request when the root first arrives', async () => {
  const h = await setup()
  await h.render({ observation: 'first' })
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('does not refresh for a drawer transition with an unchanged root', async () => {
  const h = await setup({ observation: 'first' })
  await h.render({ observation: 'first', enabled: false })
  await h.advance(300)
  await h.render({ observation: 'first' })
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('does not let an older in-flight poll swallow a newer watched state', async () => {
  const h = await setup({ observation: 'first' })
  const release = h.holdNextReply()
  await h.advance(60001)
  expect(h.issueRequests()).toHaveLength(2)
  h.setCount(0)
  await h.render({ observation: 'changed' })
  await h.advance(1)
  expect(h.host.textContent).toBe('0')
  await act(async () => { release() })
  await h.advance(1)
  expect(h.host.textContent).toBe('0')
})

it('stops pending refreshes for an inactive drawer and refreshes on reactivation', async () => {
  const h = await setup({ observation: 'first' })
  await h.render({ observation: 'changed' })
  await h.render({ observation: 'changed', enabled: false })
  await h.advance(60001)
  expect(h.issueRequests()).toHaveLength(1)
  h.setCount(0)
  await h.render({ observation: 'changed', enabled: true })
  await h.advance(10001)
  expect(h.host.textContent).toBe('0')
})

it('cancels a pending refresh when the inspected resource changes', async () => {
  const h = await setup({ observation: 'first' })
  await h.render({ observation: 'changed' })
  await h.render({ observation: 'other', name: 'other-collector' })
  await h.advance(10001)
  expect(h.issueRequests().filter(path => path.endsWith('/collector'))).toHaveLength(1)
  expect(h.issueRequests().filter(path => path.endsWith('/other-collector'))).toHaveLength(1)
})

it('does not poll or call an unsupported resource-Issues endpoint', async () => {
  const h = await setup({ observation: 'first' }, false)
  await h.advance(120001)
  await h.render({ observation: 'changed' })
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(0)
})

// Cluster-scoped subjects require a cluster-wide Issues scan, so resource
// version churn must not turn them into automatic repeated scans.
it('does not automatically rescan cluster-scoped drawers', async () => {
  const h = await setup({ namespace: '', observation: 'first' })
  await h.render({ namespace: '', observation: 'changed' })
  await h.advance(180001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('does not duplicate an in-flight reopen when a newer root arrives', async () => {
  const h = await setup({ observation: 'first' })
  await h.advance(31000)
  const release = h.holdNextReply()
  await h.render({ observation: 'first', instance: 1 })
  const reopening = h.issueRequests().length
  await h.render({ observation: 'changed', instance: 1 })
  await h.advance(1000)
  expect(h.issueRequests()).toHaveLength(reopening)
  release()
  await h.advance(10001)
  expect(h.issueRequests()).toHaveLength(reopening + 1)
})

// An unknown-version host can initially probe an endpoint before capabilities settle.
it('does not retry an unknown-route error on focus or watch updates', async () => {
  const h = await setup({ observation: 'first' }, 'unknown')
  await h.advance(100)
  expect(h.issueRequests()).toHaveLength(1)
  focusManager.setFocused(false)
  await act(async () => { focusManager.setFocused(true) })
  await h.render({ observation: 'changed' })
  await h.advance(120001)
  expect(h.issueRequests()).toHaveLength(1)
})

it('recovers an open unsupported query when capabilities confirm support', async () => {
  const h = await setup({ observation: 'first' }, 'unknown')
  await h.advance(100)
  expect(h.issueRequests()).toHaveLength(1)
  h.setEndpointSupported(true)
  await act(async () => { h.client.setQueryData(['capabilities'], { features: { resourceIssues: true } }) })
  await h.advance(1)
  expect(h.host.textContent).toBe('1')
  expect(h.issueRequests()).toHaveLength(2)
})
