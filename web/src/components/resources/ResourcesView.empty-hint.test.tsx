// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected', context: 'test' } }) }))
vi.mock('../dock', () => ({ useOpenLogs: () => () => {}, useOpenWorkloadLogs: () => () => {} }))
vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const completeAll = { status: 'complete', scope: { type: 'all_namespaces' }, gaps: [] }
const narrowedAll = { status: 'complete', scope: { type: 'all_namespaces' }, narrowedBy: ['user_access'], gaps: [] }
const completeScoped = (ns: string) => ({ status: 'complete', scope: { type: 'namespaces', namespaces: { names: [ns], count: 1 } }, gaps: [] })

const apiResourcesFixture = [
  { name: 'pods', kind: 'Pod', group: '', version: 'v1', namespaced: true, verbs: ['get', 'list'], isCrd: false },
  { name: 'certificates', kind: 'Certificate', group: 'cert-manager.io', version: 'v1', namespaced: true, verbs: ['get', 'list'], isCrd: true },
]

function jsonResponse(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
}

let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let client: QueryClient

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }))
  Element.prototype.scrollIntoView = vi.fn()
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function flush() {
  for (let i = 0; i < 6; i++) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)) })
  }
}

function setupFetch(options: {
  scope?: 'pending' | Record<string, unknown>
  listCoverage?: Record<string, unknown>
  allCounts?: Record<string, unknown>
}) {
  const requested: URL[] = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://radar.invalid')
    requested.push(url)
    if (url.pathname.endsWith('/api-resources')) return jsonResponse(apiResourcesFixture)
    if (url.pathname.endsWith('/capabilities')) return jsonResponse({ workloadWrites: {} })
    if (url.pathname.endsWith('/cluster/namespace-scope')) {
      if (options.scope === 'pending') return new Promise<Response>(() => {})
      return jsonResponse(options.scope)
    }
    if (url.pathname.endsWith('/resource-counts')) {
      const ns = url.searchParams.get('namespaces')
      if (!ns) return jsonResponse(options.allCounts ?? { counts: { Pod: 15, 'cert-manager.io/Certificate': 12 }, coverage: { Pod: completeAll, 'cert-manager.io/Certificate': completeAll } })
      return jsonResponse({ counts: { Pod: 0, 'cert-manager.io/Certificate': 0 }, coverage: { Pod: completeScoped(ns), 'cert-manager.io/Certificate': completeScoped(ns) } })
    }
    if (url.pathname.includes('/metrics/')) return jsonResponse([])
    if (url.pathname.includes('/resources/')) {
      return jsonResponse({ items: [], coverage: options.listCoverage ?? completeScoped('staging') })
    }
    return jsonResponse({})
  }))
  return requested
}

async function renderView(initialEntry: string, namespaces: string[]) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <ResourcesView namespaces={namespaces} selectedResource={null} onResourceClick={() => {}} onClearNamespaces={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  await flush()
}

it('resolves canonical CRD count keys on direct URL load', async () => {
  setupFetch({
    scope: { actives: ['staging'], context: 'test', accessChecked: true, kubeconfigNamespace: '', mode: 'namespace', accessibleNamespaces: ['default', 'staging'], deniedNamespaces: [], authoritative: true, canClearNamespace: true, cacheScoped: false, namespaceRescope: false },
  })
  await renderView('/resources/certificates?apiGroup=cert-manager.io', ['staging'])
  expect(host.textContent).toContain('Filtered to staging. 12 Certificates are in other namespaces.')
  expect(host.textContent).toContain('Show all namespaces')
})

it('waits for namespaceScope before requesting unfiltered counts (prevents --namespace-scope load race)', async () => {
  const requested = setupFetch({ scope: 'pending' })
  await renderView('/resources/pods', ['other-ns'])
  const unfilteredCounts = requested.filter(u => u.pathname.endsWith('/resource-counts') && !u.searchParams.get('namespaces'))
  expect(unfilteredCounts).toHaveLength(0)
  expect(host.textContent).not.toContain('are in other namespaces')
})

it('hides Show all namespaces when canClearNamespace is false', async () => {
  setupFetch({
    scope: { actives: ['team-a'], context: 'test', accessChecked: true, kubeconfigNamespace: '', mode: 'namespace', accessibleNamespaces: ['team-a'], deniedNamespaces: [], authoritative: false, canClearNamespace: false, cacheScoped: false, namespaceRescope: false },
    allCounts: { counts: { Pod: 8 }, coverage: { Pod: narrowedAll } },
  })
  await renderView('/resources/pods', ['team-a'])
  expect(host.textContent).toContain('Filtered to team-a. At least 8 Pods are in other namespaces.')
  expect(host.textContent).not.toContain('Show all namespaces')
})

it('skips unfiltered counts while a selected namespace is still syncing', async () => {
  const requested = setupFetch({
    scope: { actives: ['staging', 'syncing'], context: 'test', accessChecked: true, kubeconfigNamespace: '', mode: 'namespace', accessibleNamespaces: ['staging', 'syncing', 'prod'], deniedNamespaces: [], authoritative: true, canClearNamespace: true, cacheScoped: false, namespaceRescope: false },
    listCoverage: { status: 'partial', scope: { type: 'namespaces', namespaces: { names: ['staging', 'syncing'], count: 2 } }, gaps: [{ code: 'kind_sync_pending', namespaces: { names: ['syncing'], count: 1 } }] },
  })
  await renderView('/resources/pods', ['staging', 'syncing'])
  const unfilteredCounts = requested.filter(u => u.pathname.endsWith('/resource-counts') && !u.searchParams.get('namespaces'))
  expect(unfilteredCounts).toHaveLength(0)
  expect(host.textContent).not.toContain('are in other namespaces')
})
