// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi, type MockInstance } from 'vitest'
import { useBulkDeleteResources, useDeleteResource } from './client'

let success: MockInstance
let notice: MockInstance
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
beforeEach(() => {
  success = vi.spyOn(console, 'log').mockImplementation(() => {})
  notice = vi.spyOn(console, 'warn').mockImplementation(() => {})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.restoreAllMocks()
})

async function remove(responses: Response[], bulk: boolean, count = 1) {
  const fetchMock = vi.fn<typeof fetch>(async () => responses.shift()!)
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let run: (() => Promise<unknown>) | undefined
  function Probe() {
    const single = useDeleteResource()
    const many = useBulkDeleteResources()
    const items = Array.from({ length: count }, (_, i) => ({ kind: 'configmaps', namespace: 'shop', name: i ? `app-${i}` : 'app' }))
    run = () => bulk ? many.mutateAsync({ items }) : single.mutateAsync(items[0])
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let failure: unknown
  await act(async () => { try { await run!() } catch (err) { failure = err } })
  act(() => root.unmount())
  client.clear()
  expect(fetchMock).toHaveBeenCalledTimes(count)
  expect(fetchMock.mock.calls[0]?.[0]).toContain('/api/resources/configmaps/shop/app')
  return failure
}

const legacy = 'resource is stuck in Terminating state due to finalizers — use force delete to remove it'
for (const bulk of [false, true]) {
  const mode = bulk ? 'bulk' : 'single'
  it(`${mode}: treats only the historical finalizer 409 as accepted pending deletion`, async () => {
    expect(await remove([new Response(JSON.stringify({ error: legacy }), { status: 409 })], bulk)).toBeUndefined()
    expect(notice).toHaveBeenCalledExactlyOnceWith(
      '[Toast]',
      bulk ? 'Deleting — 1 resource waiting on finalizers' : 'Deleting — waiting on finalizers',
      bulk ? 'shop/app: Deleting — waiting on finalizers' : undefined,
    )
    expect(success).not.toHaveBeenCalled()
    expect(JSON.stringify(notice.mock.calls)).not.toContain('force')
  })
  it(`${mode}: rejects other conflicts and the same text on other statuses`, async () => {
    expect(await remove([new Response(JSON.stringify({ error: 'operation in progress' }), { status: 409 })], bulk)).toBeInstanceOf(Error)
    expect(await remove([new Response(JSON.stringify({ error: legacy }), { status: 500 })], bulk)).toBeInstanceOf(Error)
    expect(success).not.toHaveBeenCalled()
    expect(notice).not.toHaveBeenCalled()
  })
  for (const body of ['{}', '', 'not JSON']) {
    it(`${mode}: accepted 200 ${JSON.stringify(body)} reports deletion success`, async () => {
      expect(await remove([new Response(body, { status: 200 })], bulk)).toBeUndefined()
      expect(success).toHaveBeenCalledExactlyOnceWith('[Toast]', bulk ? '1 resource deleted' : 'Resource deleted', undefined)
      expect(notice).not.toHaveBeenCalled()
    })
  }
  it(`${mode}: 204 reports deletion success without a ToastProvider`, async () => {
    expect(await remove([new Response(null, { status: 204 })], bulk)).toBeUndefined()
    expect(success).toHaveBeenCalledExactlyOnceWith('[Toast]', bulk ? '1 resource deleted' : 'Resource deleted', undefined)
    expect(notice).not.toHaveBeenCalled()
  })
  it(`${mode}: graceful termination stays neutral`, async () => {
    await remove([new Response(JSON.stringify({ deletionTimestamp: '2026-10-10T00:00:00Z' }))], bulk)
    expect(notice).toHaveBeenCalledExactlyOnceWith(
      '[Toast]',
      bulk ? 'Deleting 1 resource…' : 'Deleting…',
      bulk ? 'shop/app: Deleting…' : undefined,
    )
    expect(success).not.toHaveBeenCalled()
  })
  it(`${mode}: finalizers show progress with their names`, async () => {
    await remove([new Response(JSON.stringify({ pendingFinalizers: ['example.com/cleanup'], deletionTimestamp: 'now' }))], bulk)
    expect(notice).toHaveBeenCalledExactlyOnceWith(
      '[Toast]',
      bulk ? 'Deleting — 1 resource waiting on finalizers' : 'Deleting — waiting on finalizers: example.com/cleanup',
      bulk ? 'shop/app: Deleting — waiting on finalizers: example.com/cleanup' : undefined,
    )
    expect(success).not.toHaveBeenCalled()
  })
  it(`${mode}: a failed observation reports the deletion as accepted, not done, with its error`, async () => {
    await remove([new Response(JSON.stringify({ observationError: 'get forbidden' }))], bulk)
    expect(notice).toHaveBeenCalledExactlyOnceWith(
      '[Toast]',
      bulk ? "Deletion accepted for 1 resource; couldn't read the current state of 1" : 'Deletion accepted; current state unavailable',
      bulk ? 'shop/app: Deletion accepted; current state unavailable: get forbidden' : 'get forbidden',
    )
    expect(success).not.toHaveBeenCalled()
  })
}

it('bulk: finalizers still lead the headline when another item is unreadable', async () => {
  await remove([
    new Response(JSON.stringify({ pendingFinalizers: ['example.com/cleanup'], deletionTimestamp: 'now' })),
    new Response(JSON.stringify({ observationError: 'get forbidden' })),
  ], true, 2)
  expect(notice).toHaveBeenCalledExactlyOnceWith(
    '[Toast]',
    "Deleting — 1 resource waiting on finalizers; couldn't read the current state of 1",
    'shop/app: Deleting — waiting on finalizers: example.com/cleanup; shop/app-1: Deletion accepted; current state unavailable: get forbidden',
  )
  expect(success).not.toHaveBeenCalled()
})
