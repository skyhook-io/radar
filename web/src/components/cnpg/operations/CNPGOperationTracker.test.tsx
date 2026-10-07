// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import type { CNPGTrackedOperation } from './model'

const state = vi.hoisted(() => ({ operations: [] as CNPGTrackedOperation[], ha: vi.fn(() => ({ dataUpdatedAt: 0, isError: false })) }))
vi.mock('@tanstack/react-query', () => ({ useQueryClient: () => ({ invalidateQueries: vi.fn() }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-db' } }) }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGClusterCapabilities: () => ({ dataUpdatedAt: 0, isError: false }),
  useCNPGRuntime: () => ({ dataUpdatedAt: 0, isError: false }),
  useCNPGWorkspace: () => ({ dataUpdatedAt: 0, isError: false }),
}))
vi.mock('../../../api/cnpg-ha', () => ({ useCNPGClusterHA: state.ha }))
vi.mock('./store', () => ({
  useCNPGOperations: () => state.operations,
  updateCNPGOperations: (update: (all: CNPGTrackedOperation[]) => CNPGTrackedOperation[]) => { state.operations = update(state.operations) },
  dismissCNPGOperation: vi.fn(),
}))

const { CNPGOperationTracker } = await import('./CNPGOperationTracker')

it('finishes a lone predecessor record even when identity filtering disables all polling', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const op: CNPGTrackedOperation = { id: 'old', kind: 'reload', label: 'Reload', context: 'kind-db', namespace: 'db', cluster: 'pg', clusterUID: 'old-uid', startedAt: 1, lastProgressAt: 1, state: 'requested', baseline: {} }
  const completed = { ...op, id: 'finished', state: 'completed' as const, finishedAt: 2 }
  const otherContext = { ...op, id: 'other-context', context: 'prod' }
  const otherCluster = { ...op, id: 'other-cluster', cluster: 'other' }
  state.operations = [op, completed, otherContext, otherCluster]
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<CNPGOperationTracker namespace="db" name="pg" uid="new-uid" />))
    expect(host.textContent).toBe('')
    expect(state.ha).toHaveBeenCalledWith('db', 'pg', { enabled: false, refetchInterval: false })
    expect(state.operations[0]).toMatchObject({ state: 'superseded', clusterUID: 'old-uid' })
    expect(state.operations[0].finishedAt).toBeGreaterThan(2)
    expect(state.operations.slice(1)).toEqual([completed, otherContext, otherCluster])
  } finally {
    act(() => root.unmount())
  }
})
