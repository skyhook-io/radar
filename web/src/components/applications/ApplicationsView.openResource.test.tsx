// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ToastProvider, workloadKey, type AppHistory, type AppRow, type AppWorkload, type SelectedResource } from '@skyhook-io/k8s-ui'
import { ApplicationsView } from './ApplicationsView'

let apps: AppRow[] = []
const idle = { data: undefined, isLoading: false, isFetching: false, isPending: false, isSuccess: false, isError: false, error: null }
let history: Omit<typeof idle, 'data'> & { data?: AppHistory }

vi.mock('../../api/client', () => ({
  useApplications: () => ({ ...idle, data: { applications: apps }, isSuccess: true, dataUpdatedAt: 0, refetch: () => {} }),
  useApplicationHistory: () => history,
  useGitOpsTree: () => idle,
  useHelmRelease: () => idle,
  useIssues: () => idle,
  useTopology: () => idle,
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected', context: 'test' } }) }))
vi.mock('../../context/TimelineSource', () => ({
  useTimelineSource: () => ({ capabilities: { mode: 'local' }, useEvents: () => ({ data: [], isLoading: false, isError: false }) }),
}))
// The workload view's own content is out of scope here: the stub stands in for
// any resource the user clicks inside it (a config chip or a topology node).
vi.mock('../workload/WorkloadView', () => ({
  WorkloadView: ({ name, onNavigateToResource }: { name: string; onNavigateToResource: (r: SelectedResource) => void }) => (
    <div data-testid="workload-view" data-name={name}>
      <button data-testid="open-cm" onClick={() => onNavigateToResource({ kind: 'ConfigMap', namespace: 'shop', name: 'web-config' })} />
      <button data-testid="open-ing" onClick={() => onNavigateToResource({ kind: 'Ingress', namespace: 'shop', name: 'web', group: 'networking.k8s.io' })} />
    </div>
  ),
}))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

function deployment(name: string): AppWorkload {
  return { kind: 'Deployment', group: 'apps', namespace: 'shop', name, health: 'healthy', ready: 1, desired: 1, restarts: 0 }
}

function app(key: string, workloadNames: string[]): AppRow {
  return { key, name: key.split('/').pop() ?? key, namespace: 'shop', health: 'healthy', workloads: workloadNames.map(deployment) }
}

let root: ReturnType<typeof createRoot>
let container: HTMLDivElement
let client: QueryClient
let location = ''

function recordLocation(next: string) {
  location = next
}

function LocationProbe({ onChange }: { onChange: (location: string) => void }) {
  const loc = useLocation()
  useEffect(() => onChange(`${loc.pathname}${loc.search}`), [loc, onChange])
  return null
}

beforeEach(() => {
  history = idle
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }))
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  client.clear()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function renderAt(url: string, onOpenResource = vi.fn()) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={[url]}>
            <ApplicationsView namespaces={[]} onOpenResource={onOpenResource} />
            <LocationProbe onChange={recordLocation} />
          </MemoryRouter>
        </ToastProvider>
      </QueryClientProvider>,
    )
  })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  return onOpenResource
}

async function click(testId: string) {
  const button = container.querySelector(`[data-testid="${testId}"]`)
  if (!(button instanceof HTMLElement)) throw new Error(`no ${testId} in the workload view`)
  await act(async () => button.click())
}

it('opens a ConfigMap clicked in a single-workload app on its resource page', async () => {
  apps = [app('shop/Deployment/web', ['web'])]
  await renderAt('/applications?app=shop%2FDeployment%2Fweb&tab=topology')
  await click('open-cm')
  const url = new URL(location, 'http://radar.invalid')
  expect(url.pathname).toBe('/resources/configmaps')
  expect(url.searchParams.get('resource')).toBe('shop/web-config')
})

it('keeps the API group, namespace filter and open investigation when opening an Ingress from a single-workload app', async () => {
  apps = [app('shop/Deployment/web', ['web'])]
  await renderAt('/applications?app=shop%2FDeployment%2Fweb&namespaces=shop&ai-run=run-1')
  await click('open-ing')
  const url = new URL(location, 'http://radar.invalid')
  expect(url.pathname).toBe('/resources/ingresses')
  expect(url.searchParams.get('resource')).toBe('shop/web')
  expect(url.searchParams.get('apiGroup')).toBe('networking.k8s.io')
  expect(url.searchParams.get('namespaces')).toBe('shop')
  expect(url.searchParams.get('ai-run')).toBe('run-1')
})

it.each(['initial fetch', 'background refresh'])('keeps a single-workload Deploys link during %s', async (phase) => {
  apps = [app('shop/Deployment/web', ['web'])]
  const initial = phase === 'initial fetch'
  history = { ...idle, isFetching: true, isLoading: initial, isPending: initial, isSuccess: !initial, data: initial ? undefined : { appKey: apps[0].key } }
  const url = '/applications?app=shop%2FDeployment%2Fweb&view=deploys&namespaces=shop'
  await renderAt(url)
  expect(new URLSearchParams(location.split('?')[1]).get('view')).toBe('deploys')
  expect(container.querySelector('[data-testid="workload-view"]')).toBeNull()

  history = { ...idle, isSuccess: true, data: { appKey: apps[0].key, sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web' } } }
  await renderAt(url)
  const params = new URLSearchParams(location.split('?')[1])
  expect(params.get('view')).toBeNull()
  expect(params.get('tab')).toBe('deploys')
  expect(params.get('workload')).toBe(workloadKey(deployment('web')))
  expect(params.get('namespaces')).toBe('shop')
  expect(container.querySelector('[data-testid="workload-view"]')).not.toBeNull()
})

it('keeps app Deploys after clearing a stale workload, and drops Deploys from a valid child workload', async () => {
  apps = [{ ...app('shop/web', ['web', 'worker']), sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web' } }]
  await renderAt('/applications?app=shop%2Fweb&view=&tab=deploys&workload=stale')
  let params = new URLSearchParams(location.split('?')[1])
  expect(params.get('workload')).toBeNull()
  expect(params.get('tab')).toBeNull()
  expect(params.get('view')).toBe('deploys')
  expect(container.textContent).toContain('Recorded deliveries')

  await act(async () => root.unmount())
  root = createRoot(container)
  const child = workloadKey(deployment('worker'))
  await renderAt(`/applications?app=shop%2Fweb&view=deploys&workload=${encodeURIComponent(child)}`)
  params = new URLSearchParams(location.split('?')[1])
  expect(params.get('workload')).toBe(child)
  expect(params.get('view')).toBeNull()
  expect(container.textContent).not.toContain('Recorded deliveries')
})

it.each([false, true])('resolves a group-less Deploys workload link (ambiguous: %s)', async (ambiguous) => {
  const rollout = { ...deployment('api'), kind: 'Rollout', group: 'argoproj.io' }
  apps = [{
    ...app('shop/web', []),
    workloads: [rollout, ambiguous ? { ...rollout, group: 'example.io' } : deployment('worker')],
    sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web' },
  }]
  const child = 'Rollout/shop/api'
  await renderAt(`/applications?app=shop%2Fweb&tab=deploys&workload=${encodeURIComponent(child)}`)
  const params = new URLSearchParams(location.split('?')[1])
  expect(params.has('tab')).toBe(false)
  expect(params.get('workload')).toBe(ambiguous ? null : child)
  expect(params.get('view')).toBe(ambiguous ? 'deploys' : null)
  expect(container.querySelector('[data-testid="workload-view"]')?.getAttribute('data-name')).toBe(ambiguous ? undefined : 'api')
})

it('keeps Deploys on a failed history refresh until a successful response rules out a source', async () => {
  apps = [app('shop/web', ['web', 'worker'])]
  const data: AppHistory = {
    appKey: 'shop/web',
    sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web' },
    anchors: [{ type: 'helm', title: 'Release promoted', revision: '3', timestamp: '2026-11-23T12:00:00Z' }],
  }
  const url = '/applications?app=shop%2Fweb&view=deploys'
  history = { ...idle, isSuccess: true, data }
  await renderAt(url)
  expect(container.textContent).toContain('Release promoted')

  history = { ...idle, isError: true, data }
  await renderAt(url)
  expect(new URLSearchParams(location.split('?')[1]).get('view')).toBe('deploys')
  expect(container.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('Deploys')
  expect(container.textContent).toContain('Delivery records could not be refreshed')
  expect(container.textContent).not.toContain('Release promoted')

  history = { ...idle, isSuccess: true, data: { appKey: data.appKey } }
  await renderAt(url)
  expect(new URLSearchParams(location.split('?')[1]).has('view')).toBe(false)
  expect(container.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('Overview')
})

it('keeps Deploys and explains a partial source lookup until a complete response rules out a source', async () => {
  apps = [app('shop/web', ['web', 'worker'])]
  const appKey = apps[0].key
  const url = '/applications?app=shop%2Fweb&view=deploys'
  const reason = "Deployment-source history is unavailable because you don't have access to this app's deployment source."
  history = { ...idle, isSuccess: true, data: {
    appKey,
    partialSources: [reason],
    anchors: [{ type: 'helm', title: 'Release promoted', revision: '3', timestamp: '2026-11-23T12:00:00Z' }],
  } }
  await renderAt(url)
  expect(location).toBe(url)
  expect(container.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('Deploys')
  expect(container.textContent).toContain(reason)
  expect(container.textContent).not.toContain('Release promoted')

  history = { ...idle, isSuccess: true, data: { appKey } }
  await renderAt(url)
  expect(new URLSearchParams(location.split('?')[1]).has('view')).toBe(false)
  expect(container.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('Overview')
})
