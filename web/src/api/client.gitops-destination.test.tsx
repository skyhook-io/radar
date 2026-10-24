// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useGitOpsDestination } from './client'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement
let client: QueryClient
const requests: string[] = []

function Destination({ group }: { group?: string }) {
  useGitOpsDestination('applications', 'shop', 'wordpress', true, group)
  return null
}

beforeEach(() => {
  requests.length = 0
  vi.stubGlobal('fetch', async (input: string | URL | Request) => {
    requests.push(String(input))
    return new Response(JSON.stringify({ server: 'https://destination.example.com', contexts: [] }), { status: 200 })
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div')
  root = createRoot(host)
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
})

async function render(group?: string) {
  await act(async () => root.render(<QueryClientProvider client={client}><Destination group={group} /></QueryClientProvider>))
}

it('binds destination requests and cached results to the API group', async () => {
  await render('argoproj.io')
  await vi.waitFor(() => expect(requests).toHaveLength(1))
  await render('app.k8s.io')
  await vi.waitFor(() => expect(requests).toHaveLength(2))
  expect(requests[0]).toBe('/api/gitops/destination/applications/shop/wordpress?group=argoproj.io')
  expect(requests[1]).toBe('/api/gitops/destination/applications/shop/wordpress?group=app.k8s.io')
  const entries = client.getQueryCache().findAll({ queryKey: ['gitops-destination'] })
  expect(entries.map((entry) => entry.queryKey.at(-1))).toEqual(['argoproj.io', 'app.k8s.io'])
})

it('preserves the unqualified request used by existing callers', async () => {
  await render()
  await vi.waitFor(() => expect(requests).toHaveLength(1))
  expect(requests[0]).toBe('/api/gitops/destination/applications/shop/wordpress')
})
