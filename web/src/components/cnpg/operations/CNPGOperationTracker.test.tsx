// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { beforeEach, expect, it, vi } from 'vitest'
import { CNPG_OP_UNOBSERVABLE_MS, type CNPGTrackedOperation } from './model'

const state = vi.hoisted(() => ({
  operations: [] as CNPGTrackedOperation[],
  invalidateQueries: vi.fn(),
  caps: vi.fn(() => ({ data: undefined as { uid: string } | undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })),
  ha: vi.fn(() => ({ data: undefined as { cluster: { uid: string } } | undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })),
  workspace: vi.fn(() => ({ dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })),
  runtime: vi.fn(() => ({ data: undefined as { cluster: { uid: string } } | undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })),
}))
vi.mock('@tanstack/react-query', () => ({ useQueryClient: () => ({ invalidateQueries: state.invalidateQueries }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-db' } }) }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGClusterCapabilities: state.caps,
  useCNPGRuntime: state.runtime,
  useCNPGWorkspace: state.workspace,
}))
vi.mock('../../../api/cnpg-ha', () => ({ useCNPGClusterHA: state.ha }))
vi.mock('./store', () => ({
  useCNPGOperations: () => state.operations,
  updateCNPGOperations: (update: (all: CNPGTrackedOperation[]) => CNPGTrackedOperation[]) => { state.operations = update(state.operations) },
  dismissCNPGOperation: vi.fn(),
}))

const { CNPGOperationTracker } = await import('./CNPGOperationTracker')

beforeEach(() => {
  state.invalidateQueries.mockClear()
  state.workspace.mockReturnValue({ dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  state.ha.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  state.runtime.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
})

it('uses a fresh runtime identity to supersede a hidden predecessor when other reads fail', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now() - 1_000
  state.operations = [{ id: 'predecessor', kind: 'restart', label: 'Restart', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: { instances: ['pg-1'], podUIDs: { 'pg-1': 'old-pod' } } }]
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: true, isFetching: false, isFetchedAfterMount: true })
  state.ha.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: true, isFetching: false, isFetchedAfterMount: true })
  state.runtime.mockReturnValue({ data: { cluster: { uid: 'new-uid' } }, dataUpdatedAt: startedAt + 1, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].state).toBe('superseded')
    expect(state.operations[0].finishedAt).toBeDefined()
  } finally {
    act(() => root.unmount())
  }
})

it('observes a lone predecessor record even when identity filtering hides it', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const op: CNPGTrackedOperation = { id: 'old', kind: 'reload', label: 'Reload', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt: 1, lastProgressAt: 1, state: 'requested', baseline: {} }
  const completed = { ...op, id: 'finished', state: 'completed' as const, finishedAt: 2 }
  const otherContext = { ...op, id: 'other-context', context: 'prod' }
  const otherCluster = { ...op, id: 'other-cluster', cluster: 'other' }
  state.operations = [op, completed, otherContext, otherCluster]
  state.caps.mockReturnValue({ data: { uid: 'new-uid' }, dataUpdatedAt: Date.now(), errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(host.textContent).toBe('')
    expect(state.ha).toHaveBeenCalledWith('db', 'pg', { enabled: true, refetchInterval: 5_000 })
    expect(state.operations[0]).toMatchObject({ state: 'superseded', clusterUID: 'old-uid' })
    expect(state.operations[0].finishedAt).toBeGreaterThan(2)
    expect(state.operations.slice(1)).toEqual([completed, otherContext, otherCluster])
  } finally {
    act(() => root.unmount())
  }
})

it.each([true, false])('keeps the current operation when an old capability UID has failed=%s', (failed) => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now() - 1_000
  state.operations = [{ id: 'current', kind: 'switchover', label: 'Switch over', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'new-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: {} }]
  state.caps.mockReturnValue({ data: { uid: 'old-uid' }, dataUpdatedAt: startedAt - 1, errorUpdatedAt: 0, isError: failed, isFetching: false, isFetchedAfterMount: true })
  state.ha.mockReturnValue({ data: { cluster: { uid: 'new-uid' } }, dataUpdatedAt: startedAt + 1, errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].state).not.toBe('superseded')
    expect(state.operations[0].finishedAt).toBeUndefined()
    expect(state.operations[0].clusterUID).toBe('new-uid')
  } finally {
    act(() => root.unmount())
  }
})


it.each(['reload', 'restart'])('waits for the first identity response before finalizing an old hidden %s', (kind) => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now() - CNPG_OP_UNOBSERVABLE_MS - 1_000
  state.operations = [{ id: 'predecessor', kind, label: kind, context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: {} }]
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: false, isFetching: true, isFetchedAfterMount: false })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(host.textContent).toBe('')
    expect(state.operations[0].state).toBe('unobservable')
    expect(state.operations[0].finishedAt).toBeUndefined()
    state.caps.mockReturnValue({ data: { uid: 'new-uid' }, dataUpdatedAt: Date.now(), errorUpdatedAt: 0, isError: false, isFetching: false, isFetchedAfterMount: true })
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].state).toBe('superseded')
    expect(state.operations[0].finishedAt).toBeDefined()
  } finally {
    act(() => root.unmount())
  }
})


it('bounds old unobservable operations during background refetches after initial reads settled', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now() - CNPG_OP_UNOBSERVABLE_MS - 1_000
  state.operations = [{ id: 'predecessor', kind: 'reload', label: 'Reload', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: {} }]
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: true, isFetching: true, isFetchedAfterMount: true })
  state.ha.mockReturnValue({ data: undefined, dataUpdatedAt: 0, errorUpdatedAt: 0, isError: true, isFetching: true, isFetchedAfterMount: true })
  state.workspace.mockReturnValue({ dataUpdatedAt: 0, errorUpdatedAt: 0, isError: true, isFetching: true, isFetchedAfterMount: true })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].state).toBe('unobservable')
    expect(state.operations[0].detail).toContain('identity')
    expect(state.operations[0].finishedAt).toBeDefined()
  } finally {
    act(() => root.unmount())
  }
})


it('advances the follow-window clock when only repeated failure timestamps change', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now()
  const clock = vi.spyOn(Date, 'now').mockReturnValue(startedAt + 1_000)
  state.operations = [{ id: 'outage', kind: 'restart', label: 'Restart', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: {} }]
  const failed = { data: undefined, dataUpdatedAt: 0, errorUpdatedAt: startedAt + 1_000, isError: true, isFetching: false, isFetchedAfterMount: true }
  state.caps.mockReturnValue(failed)
  state.ha.mockReturnValue(failed)
  state.runtime.mockReturnValue(failed)
  state.workspace.mockReturnValue(failed)
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].finishedAt).toBeUndefined()
    const settledAt = startedAt + CNPG_OP_UNOBSERVABLE_MS + 1
    clock.mockReturnValue(settledAt)
    state.caps.mockReturnValue({ ...failed, errorUpdatedAt: settledAt })
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(state.operations[0].state).toBe('unobservable')
    expect(state.operations[0].finishedAt).toBe(settledAt)
    expect(state.operations[0].lastCheckedAt).toBe(settledAt)
  } finally {
    act(() => root.unmount())
    clock.mockRestore()
  }
})

it('preserves an in-flight initial identity read when its next poll is due', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.useFakeTimers()
  const startedAt = Date.now() - CNPG_OP_UNOBSERVABLE_MS - 1_000
  state.operations = [{ id: 'slow', kind: 'reload', label: 'Reload', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: {} }]
  state.caps.mockReturnValue({ data: { uid: 'old-uid' }, dataUpdatedAt: startedAt - 1, errorUpdatedAt: 0, isError: false, isFetching: true, isFetchedAfterMount: false })
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    act(() => vi.advanceTimersByTime(5_000))
    expect(state.operations[0].finishedAt).toBeUndefined()
    expect(state.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['cnpg', 'capabilities', 'clusters', 'db', 'pg'] }, { cancelRefetch: false })
    expect(state.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['cnpg', 'workspace', 'db'] }, { cancelRefetch: false })
  } finally {
    act(() => root.unmount())
    vi.useRealTimers()
  }
})
