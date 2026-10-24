// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { KyvernoPolicyCoverage } from './KyvernoPolicyCoverage'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const requested: URL[] = []
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  requested.length = 0
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    requested.push(new URL(String(input), 'http://radar.invalid'))
    return new Promise<Response>(() => {})
  }))
  root = createRoot(document.createElement('div'))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function coverageRequest(policy: object, url: string) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[url]}>
          <KyvernoPolicyCoverage data={policy} />
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  const coverage = requested.find((u) => u.pathname.includes('/policy/policies/'))
  if (!coverage) throw new Error(`no coverage request: ${requested.map(String).join(', ')}`)
  return { namespace: coverage.searchParams.get('namespace'), namespaces: coverage.searchParams.get('namespaces') }
}

it("narrows a ClusterPolicy's subjects to the namespaces in view", async () => {
  const clusterPolicy = { kind: 'ClusterPolicy', metadata: { name: 'require-limits' } }
  expect(await coverageRequest(clusterPolicy, '/resources/clusterpolicies?namespaces=team-a,team-b'))
    .toEqual({ namespace: null, namespaces: 'team-a,team-b' })
})

it('lists every namespace when the view covers all', async () => {
  const clusterPolicy = { kind: 'ClusterPolicy', metadata: { name: 'require-limits' } }
  expect(await coverageRequest(clusterPolicy, '/resources/clusterpolicies'))
    .toEqual({ namespace: null, namespaces: null })
})

it("keeps a namespaced Policy on its own namespace", async () => {
  const policy = { kind: 'Policy', metadata: { name: 'require-limits', namespace: 'team-a' } }
  expect(await coverageRequest(policy, '/resources/policies?namespaces=team-b'))
    .toEqual({ namespace: 'team-a', namespaces: null })
})
