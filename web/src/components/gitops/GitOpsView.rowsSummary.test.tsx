// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { GitOpsView } from './GitOpsView'

// The table polls every row kind, so each list must ask the server for the
// summary shape: full Applications and Kustomizations carry status.resources
// and status.inventory, which the rows never read.

const lists = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  fetchResourceList: (kind: string, options: unknown) => {
    lists(kind, options)
    return Promise.resolve({ items: [], coverage: { status: 'complete' } })
  },
}))
vi.mock('../../api/apiResources', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/apiResources')>()),
  useAPIResources: () => ({
    data: [
      { name: 'applications', group: 'argoproj.io' },
      { name: 'kustomizations', group: 'kustomize.toolkit.fluxcd.io' },
      { name: 'helmreleases', group: 'helm.toolkit.fluxcd.io' },
      { name: 'gitrepositories', group: 'source.toolkit.fluxcd.io' },
    ],
    isLoading: false,
  }),
}))
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showToast: vi.fn() }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.mock('../useContextSwitchFlow', () => ({ useContextSwitchFlow: () => ({ confirmDialog: null, requestSwitch: vi.fn() }) }))
vi.mock('../../hooks/useKeyboardShortcuts', () => ({ useRegisterShortcut: () => {} }))
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({}), previousSettingsNote: () => undefined }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

afterEach(() => { document.body.innerHTML = '' })

it('lists every GitOps row kind in the summary shape', async () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  await act(async () => root.render(
    <MemoryRouter initialEntries={['/gitops']}>
      <QueryClientProvider client={client}><GitOpsView namespaces={[]} onOpenResource={() => {}} /></QueryClientProvider>
    </MemoryRouter>,
  ))
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  expect(lists.mock.calls.map(([kind]) => kind).sort()).toEqual(['applications', 'gitrepositories', 'helmreleases', 'kustomizations'])
  for (const [, options] of lists.mock.calls) expect(options).toMatchObject({ summary: true })
  await act(async () => root.unmount())
  client.clear()
})
