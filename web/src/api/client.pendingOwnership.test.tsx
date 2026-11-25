// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { OwnershipCoverage } from '@skyhook-io/k8s-ui/types'
import { useResourceWithRelationships } from './client'
import { getApiBase, setApiBase } from './config'

const pending = 'kind_sync_pending'
const settled = { children: { complete: true } }
const loading = { children: { complete: false, notChecked: [{ kind: 'Pod', reason: pending }] } }

let client: QueryClient
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let originalBase: string
let ownership: Partial<OwnershipCoverage>
let requests: number[]
let status: number

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(0)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  client = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } })
  host = document.createElement('div')
  root = createRoot(host)
  originalBase = getApiBase()
  ownership = loading
  requests = []
  status = 200
  vi.stubGlobal('fetch', vi.fn(async () => {
    requests.push(Date.now())
    if (status !== 200) return Response.json({ error: 'read failed' }, { status })
    return Response.json({ resource: {}, relationships: { ownership } })
  }))
})

afterEach(() => {
  act(() => root.unmount())
  client.clear()
  setApiBase(originalBase)
  focusManager.setFocused(undefined)
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

const key = ['resource', 'pods', 'shop', 'web', '']
const eightPolls = [5000, 15_000, 35_000, 65_000, 95_000, 125_000, 155_000, 185_000]

function Probe({ poll }: { poll?: boolean }) {
  const result = useResourceWithRelationships('pods', 'shop', 'web', '', { pollPendingOwnership: poll })
  return <span>{result.data?.relationships?.ownership?.children?.complete ? 'complete' : 'partial'}</span>
}

async function observe(poll?: boolean) {
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe poll={poll} /></QueryClientProvider>)
  })
}

async function advance(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

it.each<[string, OwnershipCoverage]>([
  ['ownership', { ...settled, unavailableReason: pending }],
  ['owner', { ...settled, ownerReason: pending }],
  ['manages', { ...settled, manages: { membership: 'unreadable', unavailableReason: pending } }],
  ['managers', { ...settled, managers: { notChecked: [{ kind: 'Application', reason: pending }] } }],
  ['manager chain stop', {
    ...settled,
    manager: {
      state: 'unknown',
      direct: { object: { kind: 'Pod', namespace: 'shop', name: 'web' } },
      chainEnd: 'unfollowed',
      stop: { at: { kind: 'ReplicaSet', namespace: 'shop', name: 'web-rs' }, state: 'unknown', reason: pending },
    },
  }],
  ['children', loading],
])('polls pending %s coverage and stops on an answer containing only other gaps', async (_, initial) => {
  ownership = initial
  await observe(true)
  ownership = { children: { complete: false, notChecked: [{ kind: 'Pod', reason: 'kind_sync_timed_out' }] } }
  await advance(240_000)
  expect(requests).toEqual([0, 5000])
})

it('stops after eight foreground polls and starts again when the cached resource reopens', async () => {
  await observe(true)
  const query = client.getQueryCache().find({ queryKey: key, exact: true })
  expect(query).toBeDefined()
  await advance(240_000)
  expect(requests).toEqual([0, ...eightPolls])

  await act(async () => root.render(<QueryClientProvider client={client}>{null}</QueryClientProvider>))
  await advance(1000)
  await observe(true)
  expect(client.getQueryCache().find({ queryKey: key, exact: true })).toBe(query)
  await advance(240_000)
  expect(requests).toEqual([0, ...eightPolls, 241_000, ...eightPolls.map(t => t + 241_000)])
})

it('gives a new cluster a fresh budget on the same mounted observer and query key', async () => {
  setApiBase('/c/first/api')
  await observe(true)
  const query = client.getQueryCache().find({ queryKey: key, exact: true })
  expect(query).toBeDefined()
  await advance(240_000)
  expect(requests).toEqual([0, ...eightPolls])

  expect(fetch).toHaveBeenLastCalledWith('/c/first/api/resources/pods/shop/web', expect.anything())
  setApiBase('/c/second/api')
  await observe(true)
  await act(async () => { await client.invalidateQueries({ queryKey: key, exact: true }) })
  expect(client.getQueryCache().find({ queryKey: key, exact: true })).toBe(query)
  await advance(240_000)
  expect(requests).toEqual([0, ...eightPolls, 240_000, ...eightPolls.map(t => t + 240_000)])
  expect(fetch).toHaveBeenLastCalledWith('/c/second/api/resources/pods/shop/web', expect.anything())
})

it('starts a new episode after the pending gap clears and reappears', async () => {
  await observe(true)
  await advance(240_000)
  ownership = settled
  await act(async () => { await client.invalidateQueries({ queryKey: key }) })
  await advance(60_000)
  expect(requests).toEqual([0, ...eightPolls, 240_000])
  ownership = loading
  await act(async () => { await client.invalidateQueries({ queryKey: key }) })
  await advance(240_000)
  expect(requests).toEqual([0, ...eightPolls, 240_000, 300_000, ...eightPolls.map(t => t + 300_000)])
})

it('accepts coverage without a children field', async () => {
  ownership = {}
  await observe(true)
  await advance(60_000)
  expect(requests).toEqual([0])
})

it('only polls while opted in', async () => {
  await observe()
  await advance(240_000)
  expect(requests).toEqual([0])
  await observe(true)
  await advance(5000)
  expect(requests).toEqual([0, 245_000])
  await observe(false)
  await advance(240_000)
  expect(requests).toEqual([0, 245_000])
  await observe(true)
  await advance(240_000)
  expect(requests).toEqual([0, 245_000, ...eightPolls.map(t => t + 485_000)])
})

it('keeps the remaining attempts across a long background pause and background refetches', async () => {
  await observe(true)
  await advance(5000)
  const beforeBlur = vi.getTimerCount()
  focusManager.setFocused(false)
  // Blur cancels the poll timer, and a hidden view arms none while it waits.
  expect(vi.getTimerCount()).toBe(beforeBlur - 1)
  await advance(300_000)
  expect(vi.getTimerCount()).toBe(0)
  expect(requests).toEqual([0, 5000])
  await act(async () => { await client.invalidateQueries({ queryKey: key }) })
  await observe(true)
  focusManager.setFocused(true)
  await advance(240_000)
  expect(requests).toEqual([0, 5000, 305_000, ...eightPolls.slice(1).map(t => t + 300_000)])
})

it('shares warmup answers with a passive observer of the same resource', async () => {
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe poll /><Probe /></QueryClientProvider>)
  })
  ownership = settled
  await advance(60_000)
  expect(requests).toEqual([0, 5000])
  expect([...host.querySelectorAll('span')].map(node => node.textContent)).toEqual(['complete', 'complete'])
})

it.each([403, 404])('drops the stale warmup interval after a %i response', async (errorStatus) => {
  await observe(true)
  status = errorStatus
  await advance(6000)
  expect(requests).toEqual([0, 5000, 6000])
  status = 200
  await advance(59_999)
  expect(requests).toEqual([0, 5000, 6000])
  await advance(1)
  expect(requests).toEqual(errorStatus === 404 ? [0, 5000, 6000, 66_000] : [0, 5000, 6000])
  await advance(5000)
  expect(requests).toEqual(errorStatus === 404 ? [0, 5000, 6000, 66_000, 71_000] : [0, 5000, 6000])
})
