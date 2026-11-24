// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ToastProvider, type AppRow, type AppWorkload, type SelectedResource } from '@skyhook-io/k8s-ui'
import { ApplicationsView } from './ApplicationsView'

let apps: AppRow[] = []
const idle = { data: undefined, isLoading: false, isError: false, error: null }

vi.mock('../../api/client', () => ({
  useApplications: () => ({ ...idle, data: { applications: apps }, isSuccess: true, dataUpdatedAt: 0, refetch: () => {} }),
  useApplicationHistory: () => idle,
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
