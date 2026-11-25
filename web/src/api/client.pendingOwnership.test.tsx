// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { OwnershipCoverage } from '@skyhook-io/k8s-ui/types'
import { useResourceWithRelationships } from './client'

const pending = 'kind_sync_pending'
const settled = { children: { complete: true } }
const loading = { children: { complete: false, notChecked: [{ kind: 'Pod', reason: pending }] } }

let client: QueryClient
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let ownership: OwnershipCoverage
let requests: number[]
let status: number

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(0)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  client = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } })
  host = document.createElement('div')
  root = createRoot(host)
  ownership = loading
  requests = []
  status = 200
  vi.stubGlobal('fetch', async () => {
    requests.push(Date.now())
    if (status !== 200) return Response.json({ error: 'read failed' }, { status })
    return Response.json({ resource: {}, relationships: { ownership } })
  })
})

afterEach(() => {
  act(() => root.unmount())
  client.clear()
  focusManager.setFocused(undefined)
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function Probe({ poll, scope }: { poll?: boolean; scope?: string }) {
  const result = useResourceWithRelationships('pods', 'shop', 'web', undefined, { pollPendingOwnership: poll, cacheScope: scope })
  return <span>{result.data?.relationships?.ownership?.children.complete ? 'complete' : 'partial'}</span>
}

async function observe(poll?: boolean, scope?: string) {
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Probe poll={poll} scope={scope} /></QueryClientProvider>)
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
  ['children', loading],
])('polls pending %s coverage and stops on an answer containing only other gaps', async (_, initial) => {
  ownership = initial
  await observe(true)
  ownership = { children: { complete: false, notChecked: [{ kind: 'Pod', reason: 'kind_sync_timed_out' }] } }
  await advance(240_000)
  expect(requests).toEqual([0, 5000])
})

it('backs off per answer, stops at three minutes, and gives another query its own budget', async () => {
  await observe(true, 'cluster-a')
  await advance(300_000)
  expect(requests).toEqual([0, 5000, 15_000, 35_000, 65_000, 95_000, 125_000, 155_000, 180_000])
  await observe(true, 'cluster-b')
  await advance(5000)
  expect(requests.slice(-2)).toEqual([300_000, 305_000])
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
})

it('pauses interval fetches while the tab is in the background', async () => {
  await observe(true)
  focusManager.setFocused(false)
  await advance(15_000)
  expect(requests).toEqual([0])
  focusManager.setFocused(true)
  await advance(5000)
  expect(requests).toEqual([0, 20_000])
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
  await advance(5001)
  expect(requests).toEqual(errorStatus === 404 ? [0, 5000, 6000, 66_000, 71_000] : [0, 5000, 6000])
})
