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

async function renderGuard(options: Parameters<typeof useGitOpsWriteGuard>[0]) {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  const ref: { state?: GitOpsWriteGuardState } = {}
  function Harness() {
    ref.state = useGitOpsWriteGuard(options)
    return null
  }
  await act(async () => root.render(<QueryClientProvider client={queryClient}><Harness /></QueryClientProvider>))
  return {
    ref,
    queryClient,
    rerender: () => act(async () => root.render(<QueryClientProvider client={queryClient}><Harness /></QueryClientProvider>)),
    cleanup: async () => {
      await act(async () => root.unmount())
      queryClient.clear()
    },
  }
}

const deployment = { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' }
const replicas = [{ scope: 'spec' as const, paths: ['spec.replicas'] }]

it('reads missing relationships as unknown ownership, not as unmanaged', async () => {
  const h = await renderGuard({ target: deployment, resource: {}, relationshipsUnavailable: true, writes: replicas })
  try {
    expect(mocks.evidence).not.toHaveBeenCalled()
    expect(h.ref.state!.guard?.pending).toBe(false)
    expect(h.ref.state!.guard?.level).toBe('may-revert')
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
    expect(h.ref.state!.guard?.ownershipError).toContain('relationships')
    expect(canConfirmGitOpsWrite(h.ref.state!.guard, false)).toBe(false)
  } finally {
    await h.cleanup()
  }
})

it('asks for an acknowledgment when the server saw a manager the browser could not resolve', async () => {
  const h = await renderGuard({ target: deployment, resource: {}, relationships: {}, declaredManager: 'Argo CD', writes: replicas })
  try {
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
    expect(h.ref.state!.guard?.ownershipError).toContain('Argo CD')
  } finally {
    await h.cleanup()
  }
})

it('ignores the declared manager once an owner resolves', async () => {
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }, policy: null, paths: [] })
  const h = await renderGuard({
    target: deployment,
    resource: {},
    relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }] },
    declaredManager: 'Argo CD',
    writes: replicas,
  })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.guard?.ownershipError).toBeNull()
    expect(h.ref.state!.guard?.owner?.name).toBe('web')
  } finally {
    await h.cleanup()
  }
})

it('treats a refetch of the evidence as pending', async () => {
  let resolveSecond: (v: unknown) => void = () => {}
  const evidence = { uid: 'u', resourceVersion: '1', owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }, policy: null, paths: [] }
  mocks.evidence.mockResolvedValueOnce(evidence).mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve }))
  const h = await renderGuard({
    target: deployment,
    resource: {},
    relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }] },
    writes: replicas,
  })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    await act(async () => { void h.queryClient.invalidateQueries({ queryKey: ['gitops-write-evidence'] }) })
    await h.rerender()
    expect(h.ref.state!.guard?.pending).toBe(true)
    expect(canConfirmGitOpsWrite(h.ref.state!.guard, true)).toBe(false)
    await act(async () => resolveSecond(evidence))
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
  } finally {
    await h.cleanup()
  }
})
