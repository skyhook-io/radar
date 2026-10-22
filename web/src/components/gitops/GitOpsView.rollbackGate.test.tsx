// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { GitOpsHistoryItem, GitOpsInsight } from '@skyhook-io/k8s-ui'
import { GitOpsView } from './GitOpsView'

const harness = vi.hoisted(() => ({
  rollback: vi.fn(),
  showError: vi.fn(),
  multiSource: undefined as boolean | undefined,
  source: undefined as string | undefined,
  confirm: undefined as undefined | ((opts: { prune: boolean; dryRun: boolean }) => void),
}))

const app = {
  apiVersion: 'argoproj.io/v1alpha1',
  kind: 'Application',
  metadata: { name: 'guestbook', namespace: 'argocd' },
  spec: { project: 'default', source: {}, destination: {}, syncPolicy: {} },
  status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } },
}
const insight = {
  summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'guestbook', autoSyncMode: 'Manual' },
  capabilities: { rollback: true },
  history: [{ id: '0', revision: 'aaa111' }],
} as unknown as GitOpsInsight

// The layout and the history view are replaced by stubs that expose just what
// the rollback path needs: the Activity body, the dialog, and the gate.
vi.mock('@skyhook-io/k8s-ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@skyhook-io/k8s-ui')>()
  return {
    ...actual,
    useToast: () => ({ showError: harness.showError, showSuccess: vi.fn(), showToast: vi.fn() }),
    GitOpsDetailLayout: (props: { renderTabBody: (ctx: { tab: 'activity'; fullscreen: boolean }) => React.ReactNode; children?: React.ReactNode }) => (
      <div>{props.renderTabBody({ tab: 'activity', fullscreen: false })}{props.children}</div>
    ),
    GitOpsActivityInsightView: (props: { onRollback?: (item: GitOpsHistoryItem) => void; rollbackUpgrade?: unknown }) => (
      <button type="button" disabled={!!props.rollbackUpgrade} onClick={() => props.onRollback?.({ id: '0', revision: 'aaa111', source: 'guestbook (+1 more)', multiSource: true })}>Rollback</button>
    ),
    RollbackDialog: (props: { open: boolean; source?: string; multiSource?: boolean; onConfirm: (opts: { prune: boolean; dryRun: boolean }) => void }) => {
      if (props.open) {
        harness.confirm = props.onConfirm
        harness.multiSource = props.multiSource
        harness.source = props.source
      }
      return props.open ? <div role="dialog">Roll back?</div> : null
    },
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
    useArgoRollback: () => ({ mutate: harness.rollback, isPending: false }),
    useArgoSync: idle, useArgoResourceValidation: idle, useArgoRefresh: idle, useArgoTerminate: idle,
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

beforeEach(async () => {
  harness.rollback.mockClear()
  harness.showError.mockClear()
  harness.confirm = undefined
  harness.multiSource = undefined
  harness.source = undefined
  // /api/capabilities and /version-check stay pending until a test answers.
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await act(async () => root.render(
    <MemoryRouter initialEntries={['/gitops/detail/applications/argocd/guestbook?apiGroup=argoproj.io']}>
      <QueryClientProvider client={client}><GitOpsView namespaces={[]} onOpenResource={() => {}} /></QueryClientProvider>
    </MemoryRouter>,
  ))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  host.remove()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

const settle = async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) }) }
const rollbackButton = () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Rollback')!

it('does not send a rollback confirmed after capabilities show an older Radar', async () => {
  expect(rollbackButton().disabled).toBe(false)
  await act(async () => rollbackButton().click())
  expect(host.querySelector('[role="dialog"]')).not.toBeNull()

  await act(async () => { client.setQueryData(['capabilities'], { features: { workloadHistory: true } }) })
  await settle()
  expect(rollbackButton().disabled).toBe(true)
  await act(async () => harness.confirm!({ prune: false, dryRun: false }))

  expect(harness.rollback).not.toHaveBeenCalled()
  expect(harness.showError).toHaveBeenCalledWith('Rollback unavailable', expect.stringContaining('Argo CD rollbacks need'))
  expect(host.querySelector('[role="dialog"]')).toBeNull()
})

it('sends history id 0 once capabilities advertise argoRollbackSync', async () => {
  await act(async () => { client.setQueryData(['capabilities'], { features: { argoRollbackSync: true } }) })
  await settle()
  expect(rollbackButton().disabled).toBe(false)
  await act(async () => rollbackButton().click())
  await act(async () => harness.confirm!({ prune: false, dryRun: false }))
  expect(harness.rollback).toHaveBeenCalledWith(expect.objectContaining({ namespace: 'argocd', name: 'guestbook', id: 0 }), expect.any(Object))
})

it('passes the selected multi-source history entry to the rollback confirmation', async () => {
  await act(async () => { client.setQueryData(['capabilities'], { features: { argoRollbackSync: true } }) })
  await settle()
  await act(async () => rollbackButton().click())
  expect(harness.multiSource).toBe(true)
  expect(harness.source).toBe('guestbook (+1 more)')
})
