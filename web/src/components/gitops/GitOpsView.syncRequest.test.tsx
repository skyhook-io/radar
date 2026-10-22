// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ArgoActionHandlers, GitOpsInsight, GitOpsInsightRef, GitOpsRow, GitOpsRowAction } from '@skyhook-io/k8s-ui'
import { GitOpsView } from './GitOpsView'

// The requests the Sync dialog builds, sent by the real useArgoSync and
// useArgoResourceValidation: only a Radar advertising argoSyncInheritance is
// trusted to read an omitted syncOptions as the Application's own.

const APP_OPTIONS = ['CreateNamespace=true', 'ServerSideApply=true']
const app = {
  apiVersion: 'argoproj.io/v1alpha1',
  kind: 'Application',
  metadata: { name: 'guestbook', namespace: 'argocd' },
  spec: { project: 'default', source: {}, sources: undefined as Array<{ repoURL: string; targetRevision: string }> | undefined, destination: {}, syncPolicy: { syncOptions: APP_OPTIONS } },
  status: { sync: { status: 'OutOfSync' }, health: { status: 'Healthy' } },
}
const insight = {
  summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'guestbook', autoSyncMode: 'Manual' },
} as unknown as GitOpsInsight
const deployment: GitOpsInsightRef = { kind: 'Deployment', group: 'apps', namespace: 'guestbook', name: 'web' }

vi.mock('@skyhook-io/k8s-ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@skyhook-io/k8s-ui')>()
  return {
    ...actual,
    useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showToast: vi.fn() }),
    GitOpsTableView: (props: { onRowAction: (row: GitOpsRow, action: GitOpsRowAction) => void }) => (
      <button type="button" onClick={() => props.onRowAction(actual.normalizeArgoApplication(app), 'sync')}>Open sync</button>
    ),
    GitOpsDetailLayout: (props: { argo?: ArgoActionHandlers; renderTabBody: (ctx: { tab: 'changes'; fullscreen: boolean }) => React.ReactNode; children?: React.ReactNode }) => (
      <div>
        <button type="button" onClick={() => props.argo?.onSyncRequested()}>Open sync</button>
        {props.renderTabBody({ tab: 'changes', fullscreen: false })}
        {props.children}
      </div>
    ),
    GitOpsChangesView: (props: { onSyncResource?: (ref: GitOpsInsightRef) => void }) => (
      <button type="button" onClick={() => props.onSyncResource?.(deployment)}>Open resource sync</button>
    ),
  }
})
vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  const idle = () => ({ mutate: vi.fn(), reset: vi.fn(), isPending: false })
  return {
    ...actual,
    useResource: () => ({ data: app, isLoading: false, error: null }),
    useGitOpsTree: () => ({ data: undefined, isLoading: false, error: null }),
    useGitOpsInsights: () => ({ data: insight, isLoading: false, error: null }),
    useArgoRollback: idle, useArgoRefresh: idle, useArgoTerminate: idle,
    useArgoSuspend: idle, useArgoResume: idle, useApplyResource: idle,
    useFluxReconcile: idle, useFluxSyncWithSource: idle, useFluxSuspend: idle, useFluxResume: idle,
  }
})
vi.mock('../../api/apiResources', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/apiResources')>()),
  useAPIResources: () => ({ data: [], isLoading: false }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.mock('../useContextSwitchFlow', () => ({ useContextSwitchFlow: () => ({ confirmDialog: null, requestSwitch: vi.fn() }) }))
vi.mock('./useDestinationCluster', () => ({ useDestinationCluster: () => ({}) }))
vi.mock('../../hooks/useKeyboardShortcuts', () => ({ useRegisterShortcut: () => {} }))
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({}), previousSettingsNote: () => undefined }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let client: QueryClient
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let requests: Array<{ path: string; body: Record<string, unknown> }>

beforeEach(async () => {
  requests = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (init?.method === 'POST') {
      requests.push({ path, body: JSON.parse(String(init.body)) })
      const answer = path.endsWith('/validate-resource') ? { outcome: 'succeeded', message: 'ok' } : { message: 'Sync initiated' }
      return Promise.resolve(new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    }
    // /api/capabilities and /version-check stay pending; tests set capabilities.
    return new Promise<Response>(() => {})
  }))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await renderView('/gitops/detail/applications/argocd/guestbook?apiGroup=argoproj.io')
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  host.remove()
  document.body.innerHTML = ''
  app.spec.sources = undefined
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function renderView(path: string) {
  await act(async () => root.render(
    <MemoryRouter key={path} initialEntries={[path]}>
      <QueryClientProvider client={client}><GitOpsView namespaces={[]} onOpenResource={() => {}} /></QueryClientProvider>
    </MemoryRouter>,
  ))
}

const settle = async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) }) }
const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)!
const toggle = (label: string) => [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))!.querySelector('input')!
const click = async (el: HTMLElement) => { await act(async () => el.click()); await settle() }
const advertise = async (features: Record<string, boolean>) => {
  await act(async () => { client.setQueryData(['capabilities'], { features }) })
  await settle()
}

describe('a Radar that advertises argoSyncInheritance', () => {
  beforeEach(async () => { await advertise({ argoRollbackSync: true, argoSyncInheritance: true }) })

  it("omits the options for an unchanged sync and always sends prune", async () => {
    await click(button('Open sync'))
    await click(button('Sync now'))
    expect(requests).toHaveLength(1)
    expect(requests[0].path).toBe('/api/argo/applications/argocd/guestbook/sync')
    expect(requests[0].body.prune).toBe(false)
    expect(requests[0].body).not.toHaveProperty('syncOptions')
  })

  it('sends an edited list when a toggle changes', async () => {
    await click(button('Open sync'))
    await click(toggle('Server-side apply'))
    await click(button('Sync now'))
    expect(requests[0].body).toMatchObject({ prune: false, syncOptions: ['CreateNamespace=true'] })
  })

  it('validates and syncs one resource with the inherited options', async () => {
    await click(button('Open resource sync'))
    await click(button('Dry-run'))
    await click(button('Sync resource'))
    expect(requests.map((r) => r.path)).toEqual([
      '/api/argo/applications/argocd/guestbook/validate-resource',
      '/api/argo/applications/argocd/guestbook/sync',
    ])
    expect(requests[0].body).not.toHaveProperty('syncOptions')
    expect(requests[1].body).toMatchObject({ prune: false, resources: [{ group: 'apps', kind: 'Deployment', namespace: 'guestbook', name: 'web' }] })
    expect(requests[1].body).not.toHaveProperty('syncOptions')
  })
})

describe('an older Radar, without argoSyncInheritance', () => {
  beforeEach(async () => { await advertise({ argoRollbackSync: true }) })

  it("sends the Application's options, which it would otherwise drop, and prune false, which it would otherwise default to true", async () => {
    await click(button('Open sync'))
    await click(button('Sync now'))
    expect(requests[0].body).toMatchObject({ prune: false, syncOptions: APP_OPTIONS })
  })

  it('sends an edited list the same way', async () => {
    await click(button('Open sync'))
    await click(toggle('Server-side apply'))
    await click(button('Sync now'))
    expect(requests[0].body).toMatchObject({ prune: false, syncOptions: ['CreateNamespace=true'] })
  })

  it("validates and syncs one resource with the Application's options", async () => {
    await click(button('Open resource sync'))
    await click(button('Dry-run'))
    await click(button('Sync resource'))
    expect(requests[0].body.syncOptions).toEqual(APP_OPTIONS)
    expect(requests[1].body).toMatchObject({ prune: false, syncOptions: APP_OPTIONS })
  })
})

it('treats a Radar whose capabilities have not loaded as older', async () => {
  await click(button('Open sync'))
  await click(button('Sync now'))
  expect(requests[0].body).toMatchObject({ prune: false, syncOptions: APP_OPTIONS })
})

it.each([
  '/gitops',
  '/gitops/detail/applications/argocd/guestbook?apiGroup=argoproj.io',
])('uses each source target revision when syncing from %s', async (path) => {
  app.spec.sources = [
    { repoURL: 'https://example.com/app', targetRevision: 'main' },
    { repoURL: 'https://example.com/values', targetRevision: 'v1' },
  ]
  await renderView(path)
  await click(button('Open sync'))
  expect(document.body.textContent).not.toContain('Revision (optional)')
  expect(document.body.textContent).toContain('Each source uses its own targetRevision.')
  await click(button('Sync now'))
  expect(requests).toHaveLength(1)
  expect(requests[0].body).not.toHaveProperty('revision')
})
