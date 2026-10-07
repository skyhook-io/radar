// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { CNPGScheduleActions } from './CNPGScheduleActions'

const mocks = vi.hoisted(() => ({ track: vi.fn(), mutate: vi.fn(), success: vi.fn() }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: mocks.success }) }))
vi.mock('./useCNPGWriteGuard', () => ({ useCNPGWriteGuard: () => ({ node: null, satisfied: true }) }))
vi.mock('../operations/store', () => ({ trackCNPGOperation: mocks.track }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGAction: () => ({ mutate: mocks.mutate, isPending: false }),
  useCNPGScheduleCapabilities: () => ({ data: {
    uid: 'schedule-uid', context: 'kind-test',
    facts: { cluster: 'payments', clusterUID: 'source-uid', clusterGeneration: 7, generation: 3, clusterState: 'ok', suspended: false },
    actions: { run: { allowed: true }, suspend: { allowed: true }, setSchedule: { allowed: true } },
  } }),
}))

it('binds the manual run and its follow-through to the reviewed source Cluster', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  mocks.mutate.mockImplementation((_input, callbacks) => callbacks.onSuccess({ backup: 'manual-backup' }))
  try {
    await act(async () => root.render(<CNPGScheduleActions namespace="prod" name="nightly" />))
    await act(async () => [...host.querySelectorAll('button')].find((button) => button.textContent === 'Run now')!.click())
    const confirm = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === 'Create Backup')!
    expect(confirm).toBeDefined()
    await act(async () => confirm.click())
    expect(mocks.mutate.mock.calls[0][0].request.facts).toMatchObject({ clusterUID: 'source-uid', clusterGeneration: 7 })
    expect(mocks.track).toHaveBeenCalledWith(expect.objectContaining({ kind: 'run', cluster: 'payments', clusterUID: 'source-uid', target: { name: 'manual-backup' } }))
  } finally {
    await act(async () => root.unmount())
    host.remove()
  }
})
