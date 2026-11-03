// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ClusterQueueRenderer, LocalQueueRenderer, LocalQueueStatusBadge } from './KueueQueueRenderers'
import { KueueWorkloadRenderer } from './KueueWorkloadRenderer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const v = 'kueue.x-k8s.io/v1beta2'
const active = (status: string, reason: string) => ({ type: 'Active', status, reason, observedGeneration: 1 })
const cq = (name: string, cohortName: string | undefined, condition: any, spec: any = {}) => ({ apiVersion: v, kind: 'ClusterQueue', metadata: { name, generation: 1 }, spec: { cohortName, ...spec }, status: { conditions: [condition] } })

let root: Root
let element: HTMLElement
let client: QueryClient
let queues: any[]
const requests: string[] = []

beforeEach(() => {
  requests.length = 0
  queues = []
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  const complete = { status: 'complete', scope: { type: 'cluster' }, read: {}, gaps: [] }
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url.pathname)
    if (url.pathname === '/api/resources/clusterqueues') return new Response(JSON.stringify({ items: queues, coverage: complete }))
    if (url.pathname === '/api/resources/resourceflavors' || url.pathname === '/api/resources/admissionchecks') return new Response(JSON.stringify({ items: [], coverage: complete }))
    if (url.pathname === '/api/resources/localqueues') return new Response(JSON.stringify({ items: [{ apiVersion: v, metadata: { name: 'lq', namespace: 'team', generation: 1 }, spec: { clusterQueue: 'parent' }, status: { conditions: [active('True', 'Ready')] } }], coverage: complete }))
    if (url.pathname === '/api/resources/namespaces/_/team') return new Response(JSON.stringify({ resource: { metadata: { name: 'team', labels: { team: 'a' } } } }))
    if (url.pathname.startsWith('/api/kueue/provisioning/')) return new Response(JSON.stringify({ uid: 'u', installed: true, requests: [], total: 0, truncated: false }))
    throw new Error(`Unexpected request: ${input}`)
  }))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

async function render(node: React.ReactNode, until: () => void) {
  await act(async () => root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>))
  await vi.waitFor(until, { timeout: 4000 })
}

it('lists the other ClusterQueues naming the cohort, never the queue itself', async () => {
  queues = [cq('me', 'pool', active('True', 'Ready')), cq('lender', 'pool', active('True', 'Ready')), cq('stranger', 'other', active('True', 'Ready'))]
  await render(<ClusterQueueRenderer data={queues[0]} />, () => expect(element.textContent).toContain('Other ClusterQueues naming this cohort'))
  expect(element.textContent).toContain('lender')
  expect(element.textContent).not.toContain('stranger')
  const line = [...element.querySelectorAll('p')].find(p => p.textContent?.startsWith('Other ClusterQueues naming this cohort'))
  expect(line?.textContent).toBe('Other ClusterQueues naming this cohort: lender')
})

it('reads the namespace only when the ClusterQueue selector has terms', async () => {
  queues = [cq('parent', undefined, active('True', 'Ready'), { namespaceSelector: {} })]
  const lq = { apiVersion: v, metadata: { name: 'lq', namespace: 'team', generation: 1 }, spec: { clusterQueue: 'parent' }, status: { conditions: [active('True', 'Ready')] } }
  await render(<LocalQueueRenderer data={lq} />, () => expect(element.textContent).toContain('every namespace matches'))
  expect(requests).not.toContain('/api/resources/namespaces/_/team')
  await act(async () => root.unmount())
  root = createRoot(element)
  client.clear()
  queues = [cq('parent', undefined, active('True', 'Ready'), { namespaceSelector: { matchLabels: { team: 'a' } } })]
  await render(<LocalQueueRenderer data={lq} />, () => expect(element.textContent).toContain('labels match the selector'))
  expect(requests).toContain('/api/resources/namespaces/_/team')
})

it('tones the header badge like the detail when the ClusterQueue reports Stopped', async () => {
  queues = [cq('parent', undefined, { ...active('False', 'Stopped'), message: "Can't admit new workloads: is stopped." }, { stopPolicy: 'Hold' })]
  const lq = { apiVersion: v, metadata: { name: 'lq', namespace: 'team', generation: 1 }, spec: { clusterQueue: 'parent' }, status: { conditions: [active('False', 'ClusterQueueIsInactive')] } }
  await render(<LocalQueueStatusBadge data={lq} />, () => expect(element.querySelector('.badge')?.className).toContain('status-neutral'))
  expect(requests).toContain('/api/resources/clusterqueues')
  expect(element.textContent).toBe('ClusterQueueIsInactive')
})

it('keeps the header badge alert when the stopped ClusterQueue reports other causes too', async () => {
  queues = [cq('parent', undefined, { ...active('False', 'Stopped'), message: "Can't admit new workloads: is stopped, references missing ResourceFlavor(s): x." }, { stopPolicy: 'Hold' })]
  const lq = { apiVersion: v, metadata: { name: 'lq', namespace: 'team', generation: 1 }, spec: { clusterQueue: 'parent' }, status: { conditions: [active('False', 'ClusterQueueIsInactive')] } }
  await render(<LocalQueueStatusBadge data={lq} />, () => expect(element.querySelector('.badge')?.className).toContain('status-alert'))
  expect(requests).toContain('/api/resources/clusterqueues')
})

it('follows a Workload to its LocalQueue and that LocalQueue’s ClusterQueue through list reads', async () => {
  queues = [cq('parent', undefined, active('True', 'Ready'), { namespaceSelector: {} })]
  const workload = { apiVersion: v, kind: 'Workload', metadata: { name: 'w', namespace: 'team', generation: 1, uid: 'u' }, spec: { queueName: 'lq' }, status: {} }
  await render(<KueueWorkloadRenderer data={workload} />, () => expect(element.textContent).toContain('Configured by LocalQueue lq'))
  expect(requests).toEqual(expect.arrayContaining(['/api/resources/localqueues', '/api/resources/clusterqueues']))
  expect(requests.filter(path => path.startsWith('/api/resources/') && path.split('/').length > 4)).toEqual([])
})
