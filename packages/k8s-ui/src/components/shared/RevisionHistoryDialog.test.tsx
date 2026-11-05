// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RevisionHistoryDialog, type RollbackGuard } from './ResourceActionsBar'
import type { WorkloadRevision } from '../../types'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
vi.mock('../ui/DialogPortal', () => ({
  DialogPortal: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

const revisions: WorkloadRevision[] = [
  { number: 2, createdAt: '2026-11-02T10:00:00Z', image: 'web:v2', isCurrent: true, replicas: 1 },
  { number: 1, createdAt: '2026-11-01T10:00:00Z', image: 'web:v1', isCurrent: false, replicas: 0 },
]

describe('RevisionHistoryDialog rollback guard', () => {
  let element: HTMLDivElement

  beforeEach(() => {
    element = document.createElement('div')
    document.body.append(element)
  })

  afterEach(() => element.remove())

  async function open(rollbackGuard: RollbackGuard | undefined, onRollback = vi.fn()) {
    const root = createRoot(element)
    await act(async () =>
      root.render(
        <RevisionHistoryDialog
          kind="deployments"
          namespace="prod"
          name="web"
          open
          onClose={() => {}}
          revisions={revisions}
          onRollback={onRollback}
          rollbackGuard={rollbackGuard}
        />,
      ),
    )
    const rollback = [...element.querySelectorAll('button')].find((b) => b.textContent === 'Rollback')!
    await act(async () => rollback.click())
    return [...element.querySelectorAll('button')].find((b) => b.textContent === 'Confirm')!
  }

  it('confirms as before with no guard', async () => {
    const confirm = await open(undefined)

    expect(confirm.disabled).toBe(false)
  })

  it('reads the guard only when a rollback is being confirmed, and shows its warning', async () => {
    const onRequest = vi.fn()
    await open({ onRequest, warning: <div>Argo CD will revert this</div>, satisfied: true })

    expect(onRequest).toHaveBeenCalledOnce()
    expect(element.textContent).toContain('Argo CD will revert this')
  })

  it('blocks confirming until the guard is satisfied', async () => {
    const onRollback = vi.fn()
    const confirm = await open({ onRequest: () => {}, warning: null, satisfied: false }, onRollback)

    expect(confirm.disabled).toBe(true)
  })
})
