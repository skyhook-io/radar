// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { unreadReason } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { SecondaryNetworksSection } from '@skyhook-io/k8s-ui/components/resources/renderers/SecondaryNetworksSection'
import { getPodNetworks, uniqueNetworkAttachmentRefs, type NetworkAttachmentLookups, type NetworkAttachmentRef } from '@skyhook-io/k8s-ui/utils/multus'
import { useNetworkAttachmentLookups } from './networkAttachments'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let container: HTMLDivElement
let client: QueryClient
let lookups: NetworkAttachmentLookups
const requests: string[] = []
// Responses per definition name, served in order; the last one repeats.
let responses: Record<string, { status: number; body: unknown }[]>

const found = { status: 200, body: { resource: { metadata: { name: 'x' } } } }
const fail = (status: number, error_code?: string) => ({ status, body: { error: 'nope', ...(error_code ? { error_code } : {}) } })

function Lookups({ refs }: { refs: NetworkAttachmentRef[] }) {
  const result = useNetworkAttachmentLookups(refs)
  useEffect(() => { lookups = result }, [result])
  return null
}

function Section({ names }: { names: string }) {
  const pod = { metadata: { name: 'worker', namespace: 'apps', annotations: { 'k8s.v1.cni.cncf.io/networks': names } } }
  const attachmentLookups = useNetworkAttachmentLookups(uniqueNetworkAttachmentRefs(getPodNetworks(pod).rows))
  return <SecondaryNetworksSection pod={pod} attachmentLookups={attachmentLookups} onNavigate={() => undefined} />
}

const ref = (name: string, namespace = 'apps'): NetworkAttachmentRef => ({ namespace, name })
const linked = (name: string) => [...container.querySelectorAll('button')].some(button => button.textContent === name)
const requestsFor = (name: string) => requests.filter(url => url.includes(`/${name}?`)).length

beforeEach(() => {
  requests.length = 0
  lookups = {}
  responses = {}
  vi.stubGlobal('fetch', async (input: string | URL | Request) => {
    const url = String(input)
    requests.push(url)
    const name = new URL(url, 'http://localhost').pathname.split('/').pop() ?? ''
    const queue = responses[name] ?? [found]
    const next = queue.length > 1 ? queue.shift()! : queue[0]
    return new Response(JSON.stringify(next.body), { status: next.status })
  })
  client = new QueryClient()
  container = document.createElement('div')
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
})

async function renderLookups(refs: NetworkAttachmentRef[]) {
  await act(async () => root.render(<QueryClientProvider client={client}><Lookups refs={refs} /></QueryClientProvider>))
}

async function renderSection(names: string) {
  await act(async () => root.render(<QueryClientProvider client={client}><Section names={names} /></QueryClientProvider>))
}

it('says whose access refused the read, and does not re-read a viewer denial', async () => {
  const viewer = { state: 'unavailable', code: 'user_forbidden' } as const
  const radar = { state: 'unavailable', code: 'radar_forbidden' } as const
  responses = { hidden: [fail(403, 'user_forbidden')], blocked: [fail(503, 'radar_forbidden')] }
  await renderSection('hidden,blocked')
  await vi.waitFor(() => {
    expect(container.textContent).toContain(unreadReason(viewer))
    expect(container.textContent).toContain(unreadReason(radar))
  }, { timeout: 6000 })
  expect(linked('hidden') || linked('blocked')).toBe(false)
  expect(requestsFor('hidden')).toBe(1)
})

it('keeps reading while Radar is still connecting and links the definition once it answers', async () => {
  responses = { later: [fail(503, 'cluster_connecting'), found] }
  await renderSection('later')
  await vi.waitFor(() => expect(linked('later')).toBe(true), { timeout: 6000 })
  expect(requestsFor('later')).toBe(2)
})

it('reads a definition as missing only when the server proved it absent', async () => {
  responses = { gone: [fail(404, 'object_not_found')], vague: [fail(404)] }
  await renderSection('gone,vague')
  const unconfirmed = unreadReason({ state: 'unconfirmed' })
  await vi.waitFor(() => expect(container.textContent).toContain(unconfirmed), { timeout: 6000 })
  const cards = [...container.querySelectorAll('.card-inner')].map(card => card.textContent ?? '')
  const missing = 'No NetworkAttachmentDefinition with this name'
  expect(cards.find(card => card.startsWith('gone'))).toContain(missing)
  expect(cards.find(card => card.startsWith('vague'))).not.toContain(missing)
  expect(requestsFor('gone')).toBe(1)
})

it('reads the definition by its exact group and hyphenated plural', async () => {
  await renderLookups([ref('present', 'shared')])
  await vi.waitFor(() => expect(lookups['shared/present']?.state).toBe('found'))
  expect(requests[0]).toBe('/api/resources/network-attachment-definitions/shared/present?group=k8s.cni.cncf.io')
})

it('makes no request for a Pod that names no definitions', async () => {
  await renderLookups([])
  expect(requests).toEqual([])
  expect(lookups).toEqual({})
})

it('bounds how many definitions one Pod can make Radar read and marks the rest unchecked', async () => {
  await renderLookups(Array.from({ length: 30 }, (_, index) => ref(`net-${index}`)))
  await vi.waitFor(() => expect(Object.values(lookups).filter(lookup => lookup.state === 'found')).toHaveLength(20))
  expect(new Set(requests).size).toBe(20)
  const unchecked = Object.entries(lookups).filter(([, lookup]) => lookup.state === 'unchecked').map(([key]) => key)
  expect(unchecked).toEqual(Array.from({ length: 10 }, (_, index) => `apps/net-${index + 20}`))
})
