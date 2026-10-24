import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const destinationRequest = vi.hoisted(() => vi.fn())
const state = vi.hoisted(() => ({ apiVersion: 'app.k8s.io/v1beta1', loading: false, treeLoading: false, kind: 'Application', error: '', remote: false }))
vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    useResource: () => ({ isLoading: state.loading, error: state.error ? new Error(state.error) : null, data: state.loading || state.error ? undefined : {
      apiVersion: state.apiVersion, kind: state.kind, metadata: { name: 'wordpress', namespace: 'shop' },
      spec: { project: 'default', destination: { server: 'https://kubernetes.default.svc' } },
      status: { sync: { status: 'Synced' }, health: { status: 'Healthy' }, conditions: [{ type: 'Ready', status: 'True' }] },
    } }),
    useGitOpsTree: () => ({ isLoading: state.treeLoading, error: null, data: undefined }),
    useGitOpsInsights: () => ({ isLoading: false, error: null, data: state.remote ? { summary: { tool: 'argocd', remoteDestination: true }, issues: [], changes: [], history: [], plan: [], capabilities: {} } : undefined }),
  }
})
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showToast: vi.fn() }) }))
vi.mock('../useContextSwitchFlow', () => ({ useContextSwitchFlow: () => ({ confirmDialog: null }) }))
vi.mock('./useDestinationCluster', () => ({ useDestinationCluster: (...args: unknown[]) => { destinationRequest(...args); return {} } }))
vi.mock('../../hooks/useKeyboardShortcuts', () => ({ useRegisterShortcut: () => {} }))
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({}), previousSettingsNote: () => undefined }))

import { GitOpsView } from './GitOpsView'

function render(group = 'argoproj.io', kind = 'applications') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const html = renderToString(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/gitops/detail/${kind}/shop/wordpress?apiGroup=${group}`]}>
        <GitOpsView namespaces={[]} onOpenResource={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  client.clear()
  return html
}

describe('GitOps detail identity', () => {
  it.each([false, true])('refuses a foreign Application even while tree loading is %s', (treeLoading) => {
    Object.assign(state, { apiVersion: 'app.k8s.io/v1beta1', kind: 'Application', error: '', loading: false, treeLoading })
    const html = render()
    expect(html).toContain('Application.app.k8s.io shop/wordpress is not a supported Argo CD or Flux resource')
    expect(html).not.toContain('ArgoCD')
    expect(html).not.toContain('FluxCD')
    expect(html).not.toContain('Sync…')
    expect(html).not.toContain('Reconcile')
  })

  it('keeps actions hidden until the resource confirms its group', () => {
    Object.assign(state, { loading: true, treeLoading: true })
    const html = render()
    expect(html).not.toContain('ArgoCD')
    expect(html).not.toContain('Sync…')
    expect(html).not.toContain('is not a supported Argo CD or Flux resource')
  })

  it('keeps the Argo CD header and actions for a real Application', () => {
    Object.assign(state, { apiVersion: 'argoproj.io/v1alpha1', kind: 'Application', error: '', loading: false, treeLoading: true })
    const html = render()
    expect(html).toContain('ArgoCD')
    expect(html).toContain('Sync…')
    expect(html).toContain('Healthy')
    expect(html).not.toContain('is not a supported Argo CD or Flux resource')
  })
  it('preserves Flux reconcile controls', () => {
    Object.assign(state, { apiVersion: 'kustomize.toolkit.fluxcd.io/v1', kind: 'Kustomization', loading: false, treeLoading: true, error: '', remote: false })
    const html = render('kustomize.toolkit.fluxcd.io', 'kustomizations')
    expect(html).toContain('FluxCD')
    expect(html).toContain('Reconcile')
    expect(html).not.toContain('ArgoCD')
  })

  it('does not classify Argo Workflows as Argo CD', () => {
    Object.assign(state, { apiVersion: 'argoproj.io/v1alpha1', kind: 'Workflow', loading: false, treeLoading: false, error: '', remote: false })
    const html = render('argoproj.io', 'workflows')
    expect(html).toContain('Workflow.argoproj.io shop/wordpress is not a supported Argo CD or Flux resource')
    expect(html).not.toContain('ArgoCD')
  })

  it('shows the resource read error without claiming an identity', () => {
    Object.assign(state, { loading: false, error: 'access denied' })
    const html = render()
    expect(html).toContain('access denied')
    expect(html).not.toContain('is not a supported Argo CD or Flux resource')
    expect(html).not.toContain('ArgoCD')
    expect(html).not.toContain('Sync…')
    state.error = ''
  })

  it.each([
    ['applications', 'Application', 'source.toolkit.fluxcd.io'],
    ['kustomizations', 'Kustomization', 'source.toolkit.fluxcd.io'],
  ])('rejects %s in the wrong Flux group', (plural, kind, group) => {
    Object.assign(state, { apiVersion: `${group}/v1`, kind, loading: false, treeLoading: false, error: '', remote: false })
    const html = render(group, plural)
    expect(html).toContain('is not a supported Argo CD or Flux resource')
    expect(html).not.toContain('FluxCD')
    expect(html).not.toContain('Reconcile')
    expect(html).not.toContain('ArgoCD')
  })

  it('ignores a misleading remote destination from an older backend', () => {
    Object.assign(state, { apiVersion: 'app.k8s.io/v1beta1', kind: 'Application', loading: false, error: '', remote: true })
    render('app.k8s.io')
    expect(destinationRequest).toHaveBeenLastCalledWith(false, 'applications', 'shop', 'wordpress', 'app.k8s.io')
    state.remote = false
  })

})
