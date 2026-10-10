// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => vi.fn())
vi.mock('../components/ui/Toast', () => ({ useToast: () => ({ showToast: toast }), showApiError: vi.fn(), showApiSuccess: vi.fn() }))
import { useBulkDeleteResources, useDeleteResource } from './client'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  toast.mockClear()
})

async function remove(responses: Response[], bulk: boolean) {
  const fetchMock = vi.fn<typeof fetch>(async () => responses.shift()!)
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let run: (() => Promise<unknown>) | undefined
  function Probe() {
    const single = useDeleteResource()
    const many = useBulkDeleteResources()
    const item = { kind: 'configmaps', namespace: 'shop', name: 'app' }
    run = () => bulk ? many.mutateAsync({ items: [item] }) : single.mutateAsync(item)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let failure: unknown
  await act(async () => { try { await run!() } catch (err) { failure = err } })
  act(() => root.unmount())
  client.clear()
  expect(fetchMock).toHaveBeenCalledOnce()
  expect(fetchMock.mock.calls[0]?.[0]).toContain('/api/resources/configmaps/shop/app')
  return failure
}

const legacy = 'resource is stuck in Terminating state due to finalizers — use force delete to remove it'
for (const bulk of [false, true]) {
  const mode = bulk ? 'bulk' : 'single'
  it(`${mode}: treats only the historical finalizer 409 as accepted pending deletion`, async () => {
    expect(await remove([new Response(JSON.stringify({ error: legacy }), { status: 409 })], bulk)).toBeUndefined()
    expect(toast.mock.calls[0]?.[0]).toBe(bulk ? 'Deleting — 1 resource waiting on finalizers' : 'Deleting — waiting on finalizers')
    expect(toast.mock.calls[0]?.[1].type).toBe('info')
    expect(JSON.stringify(toast.mock.calls)).not.toContain('force')
  })
  it(`${mode}: rejects other conflicts and the same text on other statuses`, async () => {
    expect(await remove([new Response(JSON.stringify({ error: 'operation in progress' }), { status: 409 })], bulk)).toBeInstanceOf(Error)
    expect(await remove([new Response(JSON.stringify({ error: legacy }), { status: 500 })], bulk)).toBeInstanceOf(Error)
    expect(toast).not.toHaveBeenCalled()
  })
  for (const body of ['{}', '', 'not JSON']) {
    it(`${mode}: accepted 200 ${JSON.stringify(body)} reports deletion success`, async () => {
      expect(await remove([new Response(body, { status: 200 })], bulk)).toBeUndefined()
      expect(toast.mock.calls[0]?.[0]).toBe(bulk ? '1 resource deleted' : 'Resource deleted')
      expect(toast.mock.calls[0]?.[1].type).toBe('success')
    })
  }
  it(`${mode}: 204 reports deletion success`, async () => {
    expect(await remove([new Response(null, { status: 204 })], bulk)).toBeUndefined()
    expect(toast.mock.calls[0]?.[0]).toBe(bulk ? '1 resource deleted' : 'Resource deleted')
  })
  it(`${mode}: graceful termination stays neutral`, async () => {
    await remove([new Response(JSON.stringify({ deletionTimestamp: '2026-10-10T00:00:00Z' }))], bulk)
    expect(toast.mock.calls[0]?.[0]).toBe(bulk ? 'Deleting 1 resource…' : 'Deleting…')
    expect(toast.mock.calls[0]?.[1].type).toBe('info')
  })
  it(`${mode}: finalizers show progress with their names`, async () => {
    await remove([new Response(JSON.stringify({ pendingFinalizers: ['example.com/cleanup'], deletionTimestamp: 'now' }))], bulk)
    expect(JSON.stringify(toast.mock.calls)).toContain('example.com/cleanup')
    expect(toast.mock.calls[0]?.[1].type).toBe('info')
  })
  it(`${mode}: a failed observation retains deletion success and includes its error`, async () => {
    await remove([new Response(JSON.stringify({ observationError: 'get forbidden' }))], bulk)
    expect(toast.mock.calls[0]?.[0]).toBe(bulk ? "1 resource deleted; couldn't read the current state of 1" : "Deleted; couldn't read the current state")
    expect(toast.mock.calls[0]?.[1].type).toBe('success')
    expect(toast.mock.calls[0]?.[1].detail).toContain('get forbidden')
  })
}
