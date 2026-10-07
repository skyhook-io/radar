// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { beforeEach, expect, it, vi } from 'vitest'
import type { CNPGTrackedOperation } from './model'

const state = vi.hoisted(() => ({
  operations: [] as CNPGTrackedOperation[],
  caps: vi.fn(() => ({ data: undefined as { uid: string } | undefined, dataUpdatedAt: 0, isError: false })),
  ha: vi.fn(() => ({ data: undefined as { cluster: { uid: string } } | undefined, dataUpdatedAt: 0, isError: false })),
  runtime: vi.fn(() => ({ data: undefined as { cluster: { uid: string } } | undefined, dataUpdatedAt: 0, isError: false })),
}))
vi.mock('@tanstack/react-query', () => ({ useQueryClient: () => ({ invalidateQueries: vi.fn() }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-db' } }) }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGClusterCapabilities: state.caps,
  useCNPGRuntime: state.runtime,
  useCNPGWorkspace: () => ({ dataUpdatedAt: 0, isError: false }),
}))
vi.mock('../../../api/cnpg-ha', () => ({ useCNPGClusterHA: state.ha }))
vi.mock('./store', () => ({
  useCNPGOperations: () => state.operations,
  updateCNPGOperations: (update: (all: CNPGTrackedOperation[]) => CNPGTrackedOperation[]) => { state.operations = update(state.operations) },
  dismissCNPGOperation: vi.fn(),
}))

const { CNPGOperationTracker } = await import('./CNPGOperationTracker')

beforeEach(() => {
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, isError: false })
  state.ha.mockReturnValue({ data: undefined, dataUpdatedAt: 0, isError: false })
  state.runtime.mockReturnValue({ data: undefined, dataUpdatedAt: 0, isError: false })
})

it('uses a fresh runtime identity to supersede a hidden predecessor when other reads fail', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const startedAt = Date.now() - 1_000
  state.operations = [{ id: 'predecessor', kind: 'restart', label: 'Restart', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt, lastProgressAt: startedAt, state: 'requested', baseline: { instances: ['pg-1'], podUIDs: { 'pg-1': 'old-pod' } } }]
  state.caps.mockReturnValue({ data: undefined, dataUpdatedAt: 0, isError: true })
  state.ha.mockReturnValue({ data: undefined, dataUpdatedAt: 0, isError: true })
  state.runtime.mockReturnValue({ data: { cluster: { uid: 'new-uid' } }, dataUpdatedAt: startedAt + 1, isError: false })
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
  state.caps.mockReturnValue({ data: { uid: 'new-uid' }, dataUpdatedAt: Date.now(), isError: false })
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
  state.caps.mockReturnValue({ data: { uid: 'old-uid' }, dataUpdatedAt: startedAt - 1, isError: failed })
  state.ha.mockReturnValue({ data: { cluster: { uid: 'new-uid' } }, dataUpdatedAt: startedAt + 1, isError: false })
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
