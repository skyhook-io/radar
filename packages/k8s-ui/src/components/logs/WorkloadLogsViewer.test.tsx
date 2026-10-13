// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { WorkloadLogsViewer, type WorkloadLogsResult } from './WorkloadLogsViewer'
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('uses a host empty state and disables source controls until a source is read', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  let result: WorkloadLogsResult = { pods: [], logs: [], emptyMessage: 'No instance Pods yet' }
  const stream = vi.fn()
  const fetchAll = vi.fn(async () => result)
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} createStream={stream} disableSourceControlsWithoutSource emptySourceState={<><p>The first instance cannot start.</p><a href="?tab=overview">Overview’s problem</a></>} />))
  expect(host.textContent).toContain('The first instance cannot start')
  expect(host.querySelector('a')?.getAttribute('href')).toBe('?tab=overview')
  expect(host.querySelector('fieldset')?.disabled).toBe(true)
  expect([...host.querySelectorAll('select')].every((el) => el.matches(':disabled'))).toBe(true)
  const streamButton = [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')!
  expect(streamButton.disabled).toBe(true)
  act(() => streamButton.click())
  expect(stream).not.toHaveBeenCalled()
  result = { pods: [{ name: 'analytics-1', containers: ['postgres'], ready: true }], logs: [] }
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.querySelector('.lucide-rotate-ccw'))!.click())
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  expect(streamButton.disabled).toBe(false)
  expect(host.textContent).not.toContain('The first instance cannot start')
  await act(async () => root.unmount())
})

it('keeps streaming available for hosts that wait for Pods to appear', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  await act(async () => root.render(<WorkloadLogsViewer name="pending-job" fetchAll={async () => ({ pods: [], logs: [] })} createStream={vi.fn()} />))
  expect([...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')?.disabled).toBe(false)
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  await act(async () => root.unmount())
})
