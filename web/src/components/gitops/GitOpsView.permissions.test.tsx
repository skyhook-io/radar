// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { GitOpsRow } from '@skyhook-io/k8s-ui'
import type { ArgoSyncOpts } from '@skyhook-io/k8s-ui/components/gitops/SyncOptionsDialog'
import { GitOpsView } from './GitOpsView'

const harness = vi.hoisted(() => ({ sync: vi.fn(), confirm: undefined as undefined | ((opts: ArgoSyncOpts) => void) }))
const row = { id: 'demo', kindName: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'demo', tool: 'argo' } as GitOpsRow

vi.mock('@skyhook-io/k8s-ui', async importOriginal => {
  const actual = await importOriginal<typeof import('@skyhook-io/k8s-ui')>()
  return {
    ...actual,
    GitOpsTableView: ({ onRowAction }: { onRowAction: (row: GitOpsRow, action: 'sync') => void }) => <button onClick={() => onRowAction(row, 'sync')}>Open sync</button>,
    SyncOptionsDialog: (props: Parameters<typeof actual.SyncOptionsDialog>[0]) => {
      harness.confirm = props.onConfirm
      return <actual.SyncOptionsDialog {...props} />
    },
  }
})
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  const idle = () => ({ mutate: vi.fn(), isPending: false })
  return {
    ...actual,
    useArgoSync: () => ({ mutate: harness.sync, isPending: false }),
    useArgoRefresh: idle, useArgoTerminate: idle, useArgoSuspend: idle, useArgoResume: idle,
    useFluxReconcile: idle, useFluxSyncWithSource: idle, useFluxSuspend: idle, useFluxResume: idle,
  }
})
vi.mock('../../api/apiResources', () => ({ useAPIResources: () => ({ data: [], isLoading: false }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../../context/RadarUpgradeHost', () => ({ useRadarUpgradeHost: () => ({}) }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let client: QueryClient
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
let allowed: boolean
const requests = vi.fn(async (input: RequestInfo | URL) => {
  if (String(input).includes('/gitops/capabilities/')) {
    return Response.json({ actions: { sync: { allowed, verb: 'patch', resource: 'applications', group: 'argoproj.io', namespace: 'argocd' } } })
  }
  return Response.json({ counts: {} })
})
const flush = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }) }
beforeEach(async () => {
  allowed = true
  harness.sync.mockClear()
  requests.mockClear()
  vi.stubGlobal('fetch', requests)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['capabilities'], { features: { gitOpsActionCapabilities: true } })
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><GitOpsView namespaces={[]} onOpenResource={() => {}} /></QueryClientProvider></MemoryRouter>))
  await flush()
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  host.remove()
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})
it('keeps capabilities active while table Sync is open and blocks a later denial at the button and callback', async () => {
  const count = () => requests.mock.calls.filter(([url]) => String(url).includes('/gitops/capabilities/')).length
  expect(count()).toBe(0)
  await act(async () => host.querySelector<HTMLButtonElement>('button')!.click())
  await flush()
  expect(count()).toBe(1)
  const confirm = () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(button => button.textContent === 'Sync now')!
  expect(confirm().disabled).toBe(false)
  allowed = false
  await act(async () => { await client.refetchQueries({ queryKey: ['gitops-action-capabilities'], type: 'active' }) })
  await flush()
  expect(count()).toBe(2)
  expect(confirm().disabled).toBe(true)
  expect(document.querySelector('[role="dialog"]')!.textContent).toContain("Your role can't patch Argo CD Applications in argocd.")
  await act(async () => harness.confirm!({ prune: false, dryRun: false, force: false, applyOnly: false, syncOptions: [] }))
  expect(harness.sync).not.toHaveBeenCalled()
  const cancel = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(button => button.textContent === 'Cancel')!
  expect(cancel.disabled).toBe(false)
  await act(async () => cancel.click())
  await act(async () => { await client.refetchQueries({ queryKey: ['gitops-action-capabilities'], type: 'active' }) })
  expect(count()).toBe(2)
})
it('submits an allowed table Sync with the selected object', async () => {
  await act(async () => host.querySelector<HTMLButtonElement>('button')!.click())
  await flush()
  const confirm = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(button => button.textContent === 'Sync now')!
  await act(async () => confirm.click())
  expect(harness.sync).toHaveBeenCalledWith(expect.objectContaining({ namespace: 'argocd', name: 'demo' }), expect.any(Object))
})
