// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { WorkloadRevisionsButton } from './WorkloadRevisionsButton'
import { useWorkloadRevisions } from '../../api/client'

vi.mock('../../api/client', () => ({ useWorkloadRevisions: vi.fn() }))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
afterEach(() => { document.body.replaceChildren(); vi.clearAllMocks() })

it('reads only the chosen workload, compares retained templates, and has no mutation actions', async () => {
  const data = [
    { number: 8, image: 'worker:v1', isCurrent: true, replicas: 2, readyReplicas: 0, matchesDesired: true, template: 'path: /healthz' },
    { number: 7, image: 'worker:v1', isCurrent: false, replicas: 2, readyReplicas: 2, template: 'path: /ready' },
  ]
  vi.mocked(useWorkloadRevisions).mockReturnValue({ data, isError: false, isLoading: false, error: null } as ReturnType<typeof useWorkloadRevisions>)
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  await act(() => root.render(<WorkloadRevisionsButton kind="Deployment" group="apps" namespace="prod" name="worker" />))
  expect(useWorkloadRevisions).not.toHaveBeenCalled()
  await act(() => container.querySelector('button')!.click())
  expect(useWorkloadRevisions).toHaveBeenCalledWith('deployments', 'prod', 'worker')
  const dialog = document.querySelector('[role=dialog]')!
  expect(dialog.textContent).toContain('0/2 ready')
  expect(dialog.textContent).toContain('2/2 ready')
  expect(dialog.textContent).not.toContain('Rollback')
  await act(() => Array.from(dialog.querySelectorAll('button')).find(b => b.textContent === 'Diff')!.click())
  expect(dialog.textContent).toContain('/healthz')
  expect(dialog.textContent).toContain('/ready')
  await act(() => root.unmount())
})
