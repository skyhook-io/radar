// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => vi.fn())
vi.mock('../components/ui/Toast', () => ({ useToast: () => ({ showToast: toast }), showApiError: vi.fn(), showApiSuccess: vi.fn() }))
import { useBulkDeleteResources, useDeleteResource } from './client'
import { isShownInline } from './httpErrors'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
afterEach(() => {
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  toast.mockClear()
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
  it(`${mode}: a failed observation reports the deletion as accepted, not done, with its error`, async () => {
    await remove([new Response(JSON.stringify({ observationError: 'get forbidden' }))], bulk)
    expect(toast.mock.calls[0]?.[0]).toBe(bulk ? "Deletion accepted for 1 resource; couldn't read the current state of 1" : 'Deletion accepted; current state unavailable')
    expect(toast.mock.calls[0]?.[1].type).toBe('info')
    expect(toast.mock.calls[0]?.[1].detail).toContain('get forbidden')
  })
}

it('bulk: finalizers still lead the headline when another item is unreadable', async () => {
  await remove([
    new Response(JSON.stringify({ pendingFinalizers: ['example.com/cleanup'], deletionTimestamp: 'now' })),
    new Response(JSON.stringify({ observationError: 'get forbidden' })),
  ], true, 2)
  expect(toast.mock.calls[0]?.[0]).toBe("Deleting — 1 resource waiting on finalizers; couldn't read the current state of 1")
  expect(toast.mock.calls[0]?.[1].type).toBe('info')
})

async function removeWith(item: Parameters<ReturnType<typeof useDeleteResource>['mutateAsync']>[0], response: Response) {
  const fetchMock = vi.fn<typeof fetch>(async () => response)
  vi.stubGlobal('fetch', fetchMock)
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let run: (() => Promise<unknown>) | undefined
  function Probe() {
    const single = useDeleteResource()
    run = () => single.mutateAsync(item)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  await act(async () => { await run!() })
  act(() => root.unmount())
  client.clear()
  return new URL(String(fetchMock.mock.calls[0]?.[0]))
}

it('single: sends the shown UID and the finalizers the user agreed to remove', async () => {
  const url = await removeWith(
    { kind: 'nodes', namespace: '', name: 'n1', force: true, uid: 'uid-1', removeFinalizers: ['karpenter.sh/termination', 'example.com/x'] },
    new Response(JSON.stringify({ removedFinalizers: ['karpenter.sh/termination', 'example.com/x'] })),
  )
  expect(url.searchParams.get('uid')).toBe('uid-1')
  expect(url.searchParams.get('force')).toBe('true')
  expect(url.searchParams.getAll('removeFinalizers')).toEqual(['karpenter.sh/termination', 'example.com/x'])
  expect(toast.mock.calls[0]?.[0]).toBe('Deleted after removing finalizers: karpenter.sh/termination, example.com/x')
  expect(toast.mock.calls[0]?.[1].type).toBe('warning')
  expect(toast.mock.calls[0]?.[1].detail).toContain('outside Kubernetes')
})

it('single: never sends finalizer consent without force', async () => {
  const url = await removeWith(
    { kind: 'configmaps', namespace: 'shop', name: 'app', uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    new Response('{}'),
  )
  expect(url.searchParams.getAll('removeFinalizers')).toEqual([])
  expect(url.searchParams.get('force')).toBeNull()
})

for (const [label, body, message] of [
  ['still terminating', { removedFinalizers: ['example.com/x'], deletionTimestamp: 'now' }, 'Removed finalizers: example.com/x; still deleting'],
  ['held by a new finalizer', { removedFinalizers: ['example.com/x'], deletionTimestamp: 'now', pendingFinalizers: ['example.com/new'] }, 'Removed finalizers: example.com/x; still waiting on: example.com/new'],
  ['unreadable afterwards', { removedFinalizers: ['example.com/x'], observationError: 'get forbidden' }, "Removed finalizers: example.com/x; couldn't check whether it's gone"],
] as const) {
  it(`single: reports removed finalizers without claiming deletion when ${label}`, async () => {
    await removeWith(
      { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
      new Response(JSON.stringify(body)),
    )
    expect(toast.mock.calls[0]?.[0]).toBe(message)
    expect(toast.mock.calls[0]?.[1].type).toBe('warning')
  })
}

async function removeFailing(item: Parameters<ReturnType<typeof useDeleteResource>['mutateAsync']>[0], fetchImpl: typeof fetch) {
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(fetchImpl))
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  let run: (() => Promise<unknown>) | undefined
  function Probe() {
    const single = useDeleteResource()
    run = () => single.mutateAsync(item)
    return null
  }
  const root = createRoot(document.createElement('div'))
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
  let failure: unknown
  await act(async () => { try { await run!() } catch (err) { failure = err } })
  act(() => root.unmount())
  client.clear()
  return failure
}

it('single: with consent, a request that never answered says removal is unknown', async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => { throw new TypeError('Failed to fetch') },
  )
  expect(String(failure)).toContain("isn't known whether example.com/x was removed. If it was, its cleanup may have been skipped.")
})

it('single: with consent, a server rejection keeps its own message', async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response(JSON.stringify({ error: 'the finalizers on this resource changed since they were shown, so none were removed', error_code: 'finalizers_not_removed' }), { status: 409 }),
  )
  expect(String(failure)).toContain('none were removed')
  expect(String(failure)).not.toContain("isn't known")
})

it('single: without consent, a network failure is reported as is', async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true },
    async () => { throw new TypeError('Failed to fetch') },
  )
  expect(String(failure)).toContain('Failed to fetch')
})

for (const status of [502, 504]) {
  it(`single: with consent, a ${status} from a proxy says removal is unknown`, async () => {
    const failure = await removeFailing(
      { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
      async () => new Response('<html>Gateway Timeout</html>', { status }),
    )
    expect(String(failure)).toContain("isn't known whether example.com/x was removed")
  })
}

for (const [label, body] of [['empty', ''], ['malformed', '{"removedFin']] as const) {
  it(`single: with consent, an ${label} 200 says removal is unknown`, async () => {
    const failure = await removeFailing(
      { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
      async () => new Response(body, { status: 200 }),
    )
    expect(String(failure)).toContain("isn't known whether example.com/x was removed")
  })
}

it('single: with consent, an older Radar answering 204 is read as before', async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response(null, { status: 204 }),
  )
  expect(failure).toBeUndefined()
  expect(toast.mock.calls.at(-1)?.[0]).toBe('Resource deleted')
})

it("single: with consent, Radar's own 503 keeps its message", async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response(JSON.stringify({ error: 'cluster client not available', error_code: 'finalizers_not_removed' }), { status: 503, headers: { 'Content-Type': 'application/json' } }),
  )
  expect(String(failure)).toContain('cluster client not available')
  expect(String(failure)).not.toContain("isn't known")
})

it('single: with consent, a proxy 503 says removal is unknown', async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response('upstream connect error', { status: 503, headers: { 'Content-Type': 'text/plain' } }),
  )
  expect(String(failure)).toContain("isn't known whether example.com/x was removed")
})

// Radar answers every failure with a JSON {"error": "..."} body. Any other
// body did not come from Radar, or was cut off, so it doesn't say what Radar did.
for (const [label, status, body, contentType] of [
  ['a proxy 500 page', 500, '<html>Internal Server Error</html>', 'text/html'],
  ['a 500 whose body was cut off', 500, '{"error":"the finalizer pat', 'application/json'],
  ['a JSON 503 from a gateway', 503, '{"message":"no healthy upstream"}', 'application/json'],
  ['a gateway 500 whose JSON looks like Radar\'s', 500, '{"error":"upstream connection closed"}', 'application/json'],
] as const) {
  it(`single: with consent, ${label} says removal is unknown`, async () => {
    const failure = await removeFailing(
      { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
      async () => new Response(body, { status, headers: { 'Content-Type': contentType } }),
    )
    expect(String(failure)).toContain("isn't known whether example.com/x was removed")
  })
}

it("single: with consent, Radar's own 500 keeps its message", async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response(JSON.stringify({ error: 'a webhook denied the patch, so none were removed', error_code: 'finalizers_not_removed' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
  )
  expect(String(failure)).toContain('none were removed')
  expect(String(failure)).not.toContain("isn't known")
})

it("single: with consent, an unknown outcome gets its own warning, not a failure headline", async () => {
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x', 'example.com/y'] },
    async () => new Response('<html>Gateway Timeout</html>', { status: 504, headers: { 'Content-Type': 'text/html' } }),
  )
  expect(isShownInline(failure)).toBe(true)
  expect(toast.mock.calls.at(-1)?.[0]).toBe("Couldn't confirm the force delete")
  expect(toast.mock.calls.at(-1)?.[1].type).toBe('warning')
  expect(toast.mock.calls.at(-1)?.[1].detail).toMatch(/^Radar didn't confirm the result, so it isn't known whether example\.com\/x, example\.com\/y were removed\. If they were, their cleanup may have been skipped\. Check the resource before trying again\. Details: HTTP 504/)
})

it("single: with consent, Radar's own unknown-outcome answer gets the same warning, with Radar's reason", async () => {
  const reason = "couldn't confirm whether the finalizers were removed. Check the resource before trying again: the server was unable to return a response in the time allotted"
  const failure = await removeFailing(
    { kind: 'configmaps', namespace: 'shop', name: 'app', force: true, uid: 'uid-1', removeFinalizers: ['example.com/x'] },
    async () => new Response(JSON.stringify({ error: reason, error_code: 'finalizer_removal_unknown' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
  )
  expect(isShownInline(failure)).toBe(true)
  expect(toast.mock.calls.at(-1)?.[0]).toBe("Couldn't confirm the force delete")
  expect(toast.mock.calls.at(-1)?.[1].type).toBe('warning')
  expect(toast.mock.calls.at(-1)?.[1].detail).toBe(`Kubernetes didn't confirm the result, so it isn't known whether example.com/x was removed. If it was, its cleanup may have been skipped. Check the resource before trying again. Details: the server was unable to return a response in the time allotted`)
})
