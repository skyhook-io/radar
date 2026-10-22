// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { CNPGPoolerActions } from './CNPGPoolerActions'

const mocks = vi.hoisted(() => ({ mutate: vi.fn(), success: vi.fn() }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: mocks.success }) }))
vi.mock('./useCNPGWriteGuard', () => ({ useCNPGWriteGuard: () => ({ node: null, satisfied: true }) }))
vi.mock('../../../api/cnpg', () => ({ useCNPGAction: () => ({ mutate: mocks.mutate, isPending: false }) }))
vi.mock('../../../api/cnpg-sessions', () => ({
  useCNPGPoolerCapabilities: () => ({ data: {
    uid: 'pooler-uid', resourceVersion: '9', context: 'kind-test',
    facts: { generation: 4, cluster: 'payments', type: 'rw', paused: true, parameters: {}, terminating: false, deployment: { name: 'pooler', state: 'ok', readyReplicas: 2 }, service: { name: 'pooler', state: 'ok' } },
    actions: { pause: { allowed: true }, resume: { allowed: true }, observeState: { allowed: true } },
  } }),
}))

it('binds the reviewed generation along with the paused state', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  try {
    await act(async () => root.render(<CNPGPoolerActions namespace="prod" name="pooler" />))
    await act(async () => [...host.querySelectorAll('button')].find((button) => button.textContent === 'Resume')!.click())
    const confirm = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === 'Resume')!
    expect(confirm).toBeDefined()
    await act(async () => confirm.click())
    expect(mocks.mutate.mock.calls[0][0]).toMatchObject({ action: 'resume', request: { reviewedContext: 'kind-test', uid: 'pooler-uid', facts: { paused: true, generation: 4 } } })
  } finally {
    await act(async () => root.unmount())
    host.remove()
  }
})
