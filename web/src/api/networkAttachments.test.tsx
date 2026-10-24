// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { NetworkAttachmentLookups, NetworkAttachmentRef } from '@skyhook-io/k8s-ui/utils/multus'
import { ApiError } from './client'
import { networkAttachmentLookupFromError, useNetworkAttachmentLookups } from './networkAttachments'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let client: QueryClient
let lookups: NetworkAttachmentLookups
const requests: string[] = []

const capture = (value: NetworkAttachmentLookups) => {
  lookups = value
}

function Lookups({ refs }: { refs: NetworkAttachmentRef[] }) {
  const result = useNetworkAttachmentLookups(refs)
  useEffect(() => capture(result), [result])
  return null
}

const ref = (name: string, namespace = 'apps'): NetworkAttachmentRef => ({ namespace, name })

beforeEach(() => {
  requests.length = 0
  lookups = {}
  vi.stubGlobal('fetch', async (input: string | URL | Request) => {
    const url = String(input)
    requests.push(url)
    const status = url.includes('/missing') ? 404 : url.includes('/hidden') ? 403 : url.includes('/broken') ? 500 : 200
    return new Response(JSON.stringify(status === 200 ? { resource: { metadata: { name: 'x' } } } : { error: 'nope' }), { status })
  })
  client = new QueryClient()
  root = createRoot(document.createElement('div'))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
})

async function render(refs: NetworkAttachmentRef[]) {
  await act(async () => root.render(<QueryClientProvider client={client}><Lookups refs={refs} /></QueryClientProvider>))
}

it('classifies each definition by what the read answered', async () => {
  await render([ref('present'), ref('missing'), ref('hidden'), ref('broken')])
  await vi.waitFor(() => expect(Object.keys(lookups)).toHaveLength(4))
  expect(lookups).toEqual({
    'apps/present': 'found',
    'apps/missing': 'missing',
    'apps/hidden': 'forbidden',
    'apps/broken': 'unavailable',
  })
})

it('reads the definition by its exact group and hyphenated plural', async () => {
  await render([ref('present', 'shared')])
  await vi.waitFor(() => expect(requests).toHaveLength(1))
  expect(requests[0]).toBe('/api/resources/network-attachment-definitions/shared/present?group=k8s.cni.cncf.io')
})

it('makes no request for a Pod that names no definitions', async () => {
  await render([])
  expect(requests).toEqual([])
  expect(lookups).toEqual({})
})

it('bounds how many definitions one Pod can make Radar read and marks the rest unchecked', async () => {
  await render(Array.from({ length: 30 }, (_, index) => ref(`net-${index}`)))
  await vi.waitFor(() => expect(Object.keys(lookups)).toHaveLength(30))
  expect(requests).toHaveLength(20)
  const unchecked = Object.entries(lookups).filter(([, state]) => state === 'unchecked').map(([key]) => key)
  expect(unchecked).toEqual(Array.from({ length: 10 }, (_, index) => `apps/net-${index + 20}`))
})

it('treats anything but a 404 or 403 as unavailable rather than missing', () => {
  expect(networkAttachmentLookupFromError(new ApiError('gone', 404))).toBe('missing')
  expect(networkAttachmentLookupFromError(new ApiError('denied', 403))).toBe('forbidden')
  expect(networkAttachmentLookupFromError(new ApiError('unknown resource kind', 400))).toBe('unavailable')
  expect(networkAttachmentLookupFromError(new ApiError('starting', 503))).toBe('unavailable')
  expect(networkAttachmentLookupFromError(new Error('network'))).toBe('unavailable')
})
