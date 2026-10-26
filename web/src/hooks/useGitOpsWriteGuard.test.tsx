// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SetImageDialog, canConfirmGitOpsWrite, setImageSelectionKey, type WorkloadImageInventory } from '@skyhook-io/k8s-ui'
import { afterEach, expect, it, vi } from 'vitest'
import { useGitOpsWriteGuard, type GitOpsWriteGuardState } from './useGitOpsWriteGuard'
import { findInheritedGitOpsLookupRef } from './useResolvedGitOpsOwner'

const mocks = vi.hoisted(() => ({
  applications: { data: undefined as unknown[] | undefined, isPending: true, isError: false },
  inherited: { data: undefined as unknown, isPending: false, isError: false },
  target: { data: undefined as unknown, relationships: undefined as unknown, isLoading: false, isError: false },
  ownerObject: undefined as unknown,
  resourceRead: vi.fn(),
  resourceList: vi.fn(),
  evidence: vi.fn(),
  evidenceUnsupported: false,
}))
vi.mock('../api/client', () => ({
  useResource: (...args: unknown[]) => {
    mocks.resourceRead(...args)
    return args[0] === 'deployments' ? mocks.target : { data: args[0] ? mocks.ownerObject : undefined, isLoading: false, isError: false }
  },
  useResourceWithRelationships: () => mocks.inherited,
  useResources: (...args: unknown[]) => { mocks.resourceList(...args); return mocks.applications },
  useRadarFeature: () => ({
    guard: async (fn: () => unknown) => {
      if (!mocks.evidenceUnsupported) return fn()
      const { RadarFeatureUnsupportedError } = await import('../api/radarFeatures')
      throw new RadarFeatureUnsupportedError('gitopsWriteEvidence', { currentVersion: 'v1.16.0' })
    },
    gatedKey: mocks.evidenceUnsupported ? ['radar-feature-unsupported'] : [],
  }),
  fetchGitOpsWriteEvidence: (...args: unknown[]) => mocks.evidence(...args),
}))

afterEach(() => {
  mocks.evidenceUnsupported = false
  mocks.applications.data = undefined
  mocks.applications.isPending = true
  mocks.applications.isError = false
  mocks.inherited = { data: undefined, isPending: false, isError: false }
  mocks.target = { data: undefined, relationships: undefined, isLoading: false, isError: false }
  mocks.evidence.mockReset()
  mocks.ownerObject = undefined
  mocks.resourceRead.mockReset()
  mocks.resourceList.mockReset()
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
    return outcome !== 'resolved' ? <SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={state.guard ? { guard: state.guard } : undefined} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} /> : null
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
      // The server resolves the namespace; the browser sends the name alone.
      await act(async () => vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(1)))
      expect(mocks.evidence.mock.calls[0][0].owner.namespace).toBe('')
      await act(async () => vi.waitFor(() => expect(state!.guard?.pending).toBe(false)))
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
    expect(h.ref.state!.guard?.ownershipError).toContain("hasn't mapped this resource's owners")
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

it('does not keep an exemption after a refetch fails', async () => {
  const exempt = {
    uid: 'u',
    resourceVersion: '1',
    owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' },
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null, respectIgnoreDifferences: true },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false, ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }],
  }
  mocks.evidence.mockResolvedValueOnce(exempt).mockRejectedValueOnce(new Error('503 cache not ready'))
  const h = await renderGuard({
    target: deployment,
    resource: {},
    relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }] },
    writes: replicas,
  })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.guard?.level).toBe('info')
    await act(async () => { await h.queryClient.invalidateQueries({ queryKey: ['gitops-write-evidence'] }) })
    await act(async () => vi.waitFor(() => {
      expect(h.ref.state!.guard?.pending).toBe(false)
      expect(h.ref.state!.guard?.level).toBe('may-revert')
    }))
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
  } finally {
    await h.cleanup()
  }
})

it('asks for an acknowledgment, not a block, on a Radar without write evidence', async () => {
  mocks.evidenceUnsupported = true
  const h = await renderGuard({
    target: deployment,
    resource: {},
    relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }] },
    writes: replicas,
  })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).not.toHaveBeenCalled()
    const guard = h.ref.state!.guard
    expect(guard?.level).toBe('may-revert')
    expect(guard?.perWrite[0].reason).toContain('need a newer Radar')
    expect(canConfirmGitOpsWrite(guard, false)).toBe(false)
    expect(canConfirmGitOpsWrite(guard, true)).toBe(true)
  } finally {
    await h.cleanup()
  }
})

const pod ={ kind: 'Pod', group: '', namespace: 'prod', name: 'web-1-abc' }
const podRelationships = {
  owner: { kind: 'ReplicaSet', group: 'apps', namespace: 'prod', name: 'web-1' },
  deployment: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' },
}

it('reads a parent without relationships as unknown ownership, not as unmanaged', async () => {
  mocks.inherited = { data: { resource: { metadata: { labels: { 'kustomize.toolkit.fluxcd.io/name': 'apps' } } } }, isPending: false, isError: false }
  const h = await renderGuard({ target: pod, resource: {}, relationships: podRelationships, writes: replicas })
  try {
    expect(mocks.evidence).not.toHaveBeenCalled()
    expect(h.ref.state!.guard?.level).toBe('may-revert')
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
    expect(h.ref.state!.guard?.ownershipError).toContain("hasn't mapped this resource's owners")
  } finally {
    await h.cleanup()
  }
})

it('does not keep an exemption after the parent lookup fails', async () => {
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }
  mocks.inherited = { data: { resource: {}, relationships: { managedBy: [app] } }, isPending: false, isError: false }
  mocks.evidence.mockResolvedValue({
    uid: 'u',
    resourceVersion: '1',
    owner: app,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null, respectIgnoreDifferences: true },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false, ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }],
  })
  const h = await renderGuard({ target: pod, resource: {}, relationships: podRelationships, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.guard?.level).toBe('info')
    // React Query keeps the parent's data after a failed refetch.
    mocks.inherited = { ...mocks.inherited, isError: true }
    await h.rerender()
    expect(h.ref.state!.guard?.pending).toBe(false)
    expect(h.ref.state!.guard?.level).toBe('may-revert')
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
  } finally {
    await h.cleanup()
  }
})

it('does not keep an exemption after the target lookup fails', async () => {
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }
  mocks.target = { data: {}, relationships: { managedBy: [app] }, isLoading: false, isError: false }
  mocks.evidence.mockResolvedValue({
    uid: 'u',
    resourceVersion: '1',
    owner: app,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null, respectIgnoreDifferences: true },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false, ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }],
  })
  const h = await renderGuard({ target: deployment, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.guard?.level).toBe('info')
    // React Query keeps the target's data after a failed refetch.
    mocks.target = { ...mocks.target, isError: true }
    await h.rerender()
    expect(h.ref.state!.guard?.pending).toBe(false)
    expect(h.ref.state!.guard?.level).toBe('may-revert')
    expect(canConfirmGitOpsWrite(h.ref.state!.guard, false)).toBe(false)
  } finally {
    await h.cleanup()
  }
})

it('asks for an acknowledgment, not a block, when the parent cannot be read', async () => {
  mocks.inherited = { data: undefined, isPending: false, isError: true }
  const h = await renderGuard({ target: pod, resource: {}, relationships: podRelationships, writes: replicas })
  try {
    expect(mocks.evidence).not.toHaveBeenCalled()
    expect(h.ref.state!.guard?.level).toBe('may-revert')
    expect(h.ref.state!.guard?.ownershipError).toContain("couldn't read the resource that owns this one")
    expect(canConfirmGitOpsWrite(h.ref.state!.guard, false)).toBe(false)
    expect(canConfirmGitOpsWrite(h.ref.state!.guard, true)).toBe(true)
  } finally {
    await h.cleanup()
  }
})

it('lets an ignore rule waive the acknowledgment once the server confirms a name-only Argo match', async () => {
  mocks.applications.isPending = false
  mocks.applications.data = [{ metadata: { name: 'web', namespace: 'argocd' } }]
  const exempt = (ownerTracksTarget: boolean) => ({
    uid: 'u',
    resourceVersion: '1',
    owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' },
    ownerTracksTarget,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null, respectIgnoreDifferences: true },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false, ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }],
  })
  for (const [tracks, level] of [[true, 'info'], [false, 'may-revert']] as const) {
    mocks.evidence.mockResolvedValueOnce(exempt(tracks))
    const h = await renderGuard({
      target: deployment,
      resource: {},
      // Argo CD 3's tracking id omits the namespace of Applications in its own namespace.
      relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: '', name: 'web' }] },
      writes: replicas,
    })
    try {
      await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
      expect(h.ref.state!.guard?.level).toBe(level)
    } finally {
      await h.cleanup()
    }
  }
})

it("takes the Application the server confirmed when the browser can't list Applications", async () => {
  mocks.applications.isPending = false
  mocks.applications.data = []
  mocks.evidence.mockResolvedValueOnce({
    uid: 'u',
    resourceVersion: '1',
    owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' },
    ownerTracksTarget: true,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null, respectIgnoreDifferences: true },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false, ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }],
  })
  const h = await renderGuard({
    target: deployment,
    resource: {},
    relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: '', name: 'web' }] },
    writes: replicas,
  })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.evidence.mock.calls[0][0].owner.namespace).toBe('')
    expect(h.ref.state!.guard?.owner?.namespace).toBe('argocd')
    expect(h.ref.state!.guard?.level).toBe('info')
    expect(h.ref.state!.ownership.ownerVerified).toBe(true)
  } finally {
    await h.cleanup()
  }
})

it('reports the listed and changed containers so the host can judge exactly those', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const root = createRoot(document.createElement('div'))
  const onSelectionChange = vi.fn()
  const guard = { owner: null, helmRelease: null, pending: false, level: 'none', perWrite: [], summary: '', syncPolicy: null, requiresAck: false, ownershipError: null } as never
  const onLoad = vi.fn(async (): Promise<WorkloadImageInventory> => ({
    target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' },
    containers: [{ type: 'container', name: 'app', image: 'app:old' }, { type: 'initContainer', name: 'migrate', image: 'migrate:old' }],
    behavior: { type: 'rolling' },
  }))
  try {
    await act(async () => root.render(<SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={{ guard, onSelectionChange }} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} />))
    await act(async () => vi.waitFor(() => expect(onSelectionChange).toHaveBeenLastCalledWith({
      containers: [{ type: 'container', name: 'app' }, { type: 'initContainer', name: 'migrate' }],
      changed: [],
    })))
  } finally {
    await act(async () => root.unmount())
  }
})

it('keeps Update disabled until the host has judged the containers being changed', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const unmanaged = { owner: null, helmRelease: null, pending: false, level: 'none', perWrite: [], summary: '', syncPolicy: null, requiresAck: false, ownershipError: null } as never
  const onLoad = vi.fn(async (): Promise<WorkloadImageInventory> => ({
    target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' },
    containers: [{ type: 'container', name: 'app', image: 'app:old' }],
    behavior: { type: 'rolling' },
  }))
  const changed = setImageSelectionKey({ containers: [{ type: 'container', name: 'app' }], changed: [{ type: 'container', name: 'app' }] })
  const render = (selectionKey: string) =>
    root.render(<SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={{ guard: unmanaged, onSelectionChange: () => {}, selectionKey }} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} />)
  const update = () => [...document.querySelectorAll('button')].find((b) => /^Update \d+ image/.test(b.textContent ?? '')) as HTMLButtonElement | undefined
  try {
    await act(async () => render(''))
    const input = await vi.waitFor(() => {
      const el = document.querySelector('input[aria-label^="New image"]') as HTMLInputElement | null
      expect(el).not.toBeNull()
      return el!
    })
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'app:new')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    // The guard on screen was judged before the edit.
    expect(update()?.disabled).toBe(true)
    await act(async () => render(changed))
    expect(update()?.disabled).toBe(false)
  } finally {
    await act(async () => root.unmount())
    host.remove()
  }
})

it("doesn't hold Update for a host that reports the selection without judging it", async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const unmanaged = { owner: null, helmRelease: null, pending: false, level: 'none', perWrite: [], summary: '', syncPolicy: null, requiresAck: false, ownershipError: null } as never
  const onLoad = vi.fn(async (): Promise<WorkloadImageInventory> => ({
    target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' },
    containers: [{ type: 'container', name: 'app', image: 'app:old' }],
    behavior: { type: 'rolling' },
  }))
  try {
    await act(async () => root.render(<SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={{ guard: unmanaged, onSelectionChange: () => {} }} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} />))
    const input = await vi.waitFor(() => {
      const el = document.querySelector('input[aria-label^="New image"]') as HTMLInputElement | null
      expect(el).not.toBeNull()
      return el!
    })
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'app:new')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const update = [...document.querySelectorAll('button')].find((b) => /^Update \d+ image/.test(b.textContent ?? '')) as HTMLButtonElement | undefined
    expect(update?.disabled).toBe(false)
  } finally {
    await act(async () => root.unmount())
    host.remove()
  }
})

const managerApp = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'server-app' }
const managerAnswer: import('@skyhook-io/k8s-ui').ManagerAnswer = { state: 'listed', lead: managerApp, direct: { object: deployment, recorders: [managerApp] } }

it.each(['disagree', 'shared', 'claimed'] as const)('fetches evidence for %s without a lead', async state => {
  const manager = { ...managerAnswer, state, lead: undefined, direct: { object: deployment, recorders: state === 'claimed' ? [] : state === 'shared' ? [managerApp, { ...managerApp, name: 'second-recorder' }] : [managerApp], claims: [{ manager: { ...managerApp, name: 'claim' }, source: 'argo-instance-label' as const, check: 'omitted' as const }, ...(state === 'claimed' ? [{ manager: managerApp, source: 'argo-instance-label' as const, check: 'omitted' as const }] : [])] } }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: null, manager, policy: null, paths: [] })
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).toHaveBeenCalledTimes(1)
    expect(mocks.evidence.mock.calls[0][0].owner).toBeUndefined()
    expect(h.ref.state!.guard?.requiresAck).toBe(true)
  } finally { await h.cleanup() }
})

it('fetches via evidence and names the parent without acknowledgment', async () => {
  const manager = { ...managerAnswer, direct: { object: pod }, via: managerAnswer.direct }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: managerApp, manager, policy: null, paths: [] })
  const h = await renderGuard({ target: pod, resource: {}, relationships: { ownership: { children: { complete: true }, manager } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).toHaveBeenCalledTimes(1)
    expect(h.ref.state!.ownership.inheritedLookupRef).toBeNull()
    expect(h.ref.state!.guard?.level).toBe('info')
    expect(h.ref.state!.guard?.requiresAck).toBe(false)
    expect(h.ref.state!.guard?.summary).toContain('Deployment prod/web')
  } finally { await h.cleanup() }
})

it.each(['none', 'unknown', 'disagree', 'shared', 'claimed', 'listed'] as const)('adopts the server %s answer over a conflicting client lead', async state => {
  const serverManager = { ...managerAnswer, state, lead: state === 'listed' ? managerApp : undefined, direct: { object: deployment } }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: managerApp, manager: serverManager, policy: null, paths: [] })
  const clientManager = { ...managerAnswer, lead: { ...managerApp, name: 'stale-client' } }
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager: clientManager } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.ownership.manager).toEqual(serverManager)
    expect(h.ref.state!.guard?.owner?.name ?? null).toBe(state === 'listed' ? 'server-app' : null)
    expect(h.ref.state!.guard?.summary).not.toContain('stale-client')
    if (state === 'none' || state === 'unknown') expect(h.ref.state!.guard?.level).toBe('none')
  } finally { await h.cleanup() }
})

// A live answer naming another manager doesn't inherit the cached one's
// verification or status: a link to it would rest on a check of the old one.
it("doesn't carry the cached manager's verification to a different live manager", async () => {
  mocks.ownerObject = { status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } } }
  const other = { ...managerApp, name: 'live-app' }
  const live = { state: 'claimed' as const, lead: other, direct: { object: deployment, claims: [{ manager: other, source: 'argo-tracking-id' as const, check: 'missing' as const }] } }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: other, manager: live, policy: null, paths: [] })
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager: managerAnswer } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.ownership.owner?.name).toBe('live-app')))
    expect(h.ref.state!.ownership.ownerVerified).toBe(false)
    expect(h.ref.state!.ownership.ownerStatus).toBeNull()
  } finally { await h.cleanup() }
})

// An object adopted since the cache saw it has a manager the cache doesn't.
it('checks a cached none against the live object', async () => {
  const cachedNone = { state: 'none' as const, direct: { object: deployment } }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: managerApp, manager: managerAnswer, policy: null, paths: [] })
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager: cachedNone } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalled()))
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.owner?.name).toBe('server-app')))
  } finally { await h.cleanup() }
})

it('does not guess an Argo namespace or inherit through the browser with an answer', async () => {
  const unresolved = { ...managerAnswer, state: 'claimed' as const, lead: { ...managerApp, namespace: '' } }
  mocks.applications.data = [{ metadata: { name: managerApp.name, namespace: 'guessed' } }]
  mocks.inherited = { data: { resource: {}, relationships: { managedBy: [managerApp] } }, isPending: false, isError: false }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: unresolved.lead, policy: null, paths: [] })
  const h = await renderGuard({ target: pod, resource: {}, relationships: { ...podRelationships, ownership: { children: { complete: true }, manager: unresolved } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(h.ref.state!.ownership.owner?.namespace).toBe('')
    expect(h.ref.state!.ownership.ownerMatchedByName).toBe(false)
    expect(h.ref.state!.ownership.inheritedLookupRef).toBeNull()
    expect(mocks.evidence.mock.calls[0][0].owner.namespace).toBe('')
  } finally { await h.cleanup() }
})


it('still fetches the lead for sync and health without listing namesakes', async () => {
  mocks.ownerObject = { status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } } }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: managerApp, manager: managerAnswer, policy: null, paths: [] })
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager: managerAnswer } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.resourceRead).toHaveBeenCalledWith('applications', 'argocd', 'server-app', 'argoproj.io', expect.objectContaining({ enabled: true }))
    expect(mocks.resourceList).toHaveBeenCalledWith('applications', undefined, 'argoproj.io', expect.objectContaining({ enabled: false }))
    expect(h.ref.state!.ownership.ownerStatus).toMatchObject({ sync: 'Synced', health: 'Healthy' })
  } finally { await h.cleanup() }
})


it.each(['agree', 'listed', 'claimed', 'disagree', 'shared', 'unknown', 'none'] as const)('image prefetch skips browser inheritance for %s answers', state => {
  expect(findInheritedGitOpsLookupRef({ ...podRelationships, ownership: { children: { complete: true }, manager: { state, direct: { object: pod } } } }, null, pod)).toBeNull()
})


it.each(['cycle', 'truncated'] as const)('fetches evidence for a %s chain without a stop object', async chainEnd => {
  const manager = { state: 'unknown' as const, direct: { object: deployment }, chainEnd }
  mocks.evidence.mockResolvedValue({ uid: 'u', resourceVersion: '1', owner: null, manager, policy: null, paths: [] })
  const h = await renderGuard({ target: deployment, resource: {}, relationships: { ownership: { children: { complete: true }, manager } }, writes: replicas })
  try {
    await act(async () => vi.waitFor(() => expect(h.ref.state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).toHaveBeenCalledTimes(1)
    expect(h.ref.state!.guard?.level).toBe('may-revert')
  } finally { await h.cleanup() }
})
