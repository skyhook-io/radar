// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SetImageDialog, canConfirmGitOpsWrite, type WorkloadImageInventory } from '@skyhook-io/k8s-ui'
import { afterEach, expect, it, vi } from 'vitest'
import { useGitOpsWriteGuard, type GitOpsWriteGuardState } from './useGitOpsWriteGuard'

const mocks = vi.hoisted(() => ({
  applications: { data: undefined as unknown[] | undefined, isPending: true, isError: false },
  evidence: vi.fn(),
}))
vi.mock('../api/client', () => ({
  useResource: () => ({ data: undefined, isLoading: false, isError: false }),
  useResourceWithRelationships: () => ({ data: undefined, isPending: false, isError: false }),
  useResources: () => mocks.applications,
  useRadarFeature: () => ({ guard: (fn: () => unknown) => fn(), gatedKey: [] }),
  fetchGitOpsWriteEvidence: (...args: unknown[]) => mocks.evidence(...args),
}))

afterEach(() => {
  mocks.applications.data = undefined
  mocks.applications.isPending = true
  mocks.applications.isError = false
  mocks.evidence.mockReset()
})

it.each(['resolved', 'failed', 'empty', 'ambiguous'])('waits for Argo Application namespace lookup before handling %s ownership', async (outcome) => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  let state: GitOpsWriteGuardState
  const onLoad = vi.fn(async (): Promise<WorkloadImageInventory> => ({
    target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' },
    containers: [{ type: 'container', name: 'app', image: 'app:old' }],
    behavior: { type: 'rolling' },
  }))
  mocks.evidence.mockResolvedValue({ uid: 'deployment', resourceVersion: '1', owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }, policy: null, paths: [] })
  function Harness() {
    state = useGitOpsWriteGuard({
      target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' },
      resource: {},
      relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: '', name: 'web' }] },
      writes: [{ scope: 'spec', paths: ['spec.replicas'] }],
    })
    return outcome !== 'resolved' ? <SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={state.ownership.lookupError || !state.guard ? undefined : { guard: state.guard }} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} /> : null
  }
  const render = () => root.render(<QueryClientProvider client={queryClient}><Harness /></QueryClientProvider>)
  try {
    await act(async () => render())
    expect(state!.guard?.pending).toBe(true)
    expect(mocks.evidence).not.toHaveBeenCalled()
    mocks.applications.isPending = false
    if (outcome === 'resolved') mocks.applications.data = [{ metadata: { name: 'web', namespace: 'argocd' } }]
    else if (outcome === 'failed') mocks.applications.isError = true
    else mocks.applications.data = outcome === 'empty' ? [] : ['argocd-a', 'argocd-b'].map((namespace) => ({ metadata: { name: 'web', namespace } }))
    await act(async () => render())
    if (outcome === 'resolved') {
      await act(async () => vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(1)))
      expect(mocks.evidence.mock.calls[0][0].owner.namespace).toBe('argocd')
    } else {
      expect(mocks.evidence).not.toHaveBeenCalled()
      expect(state!.ownership.lookupError).toBe(false)
      expect(state!.guard?.pending).toBe(false)
      expect(state!.guard?.requiresAck).toBe(true)
      expect(state!.guard?.level).toBe('may-revert')
      expect(canConfirmGitOpsWrite(state!.guard, true)).toBe(true)
      expect(document.body.textContent).not.toContain('Management ownership unavailable')
      expect(document.body.textContent).toContain('Argo CD')
    }
  } finally {
    await act(async () => root.unmount())
    queryClient.clear()
  }
})
