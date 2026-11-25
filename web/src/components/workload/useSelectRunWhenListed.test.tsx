// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useSelectRunWhenListed } from './useSelectRunWhenListed'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let root: Root
let open: (key: string) => void
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => {
  act(() => root.unmount())
  vi.useRealTimers()
})

let unlisted = vi.fn()
let failed = false
function Probe({ scope, keys, refetch, select, onOpen }: { scope: string; keys: string[]; refetch: () => void; select: (key: string) => void; onOpen: (open: (key: string) => void) => void }) {
  const openRun = useSelectRunWhenListed(scope, keys, failed, refetch, select, unlisted)
  useEffect(() => onOpen(openRun))
  return null
}
function render(keys: string[], refetch: () => void, select: (key: string) => void, scope = 'workflowtemplates/jobs/migrate') {
  act(() => root.render(<Probe scope={scope} keys={keys} refetch={refetch} select={select} onOpen={(fn) => { open = fn }} />))
}
beforeEach(() => { unlisted = vi.fn(); failed = false })

it('waits for the run to be listed, refetching meanwhile, then selects it once', () => {
  root = createRoot(document.createElement('div'))
  const refetch = vi.fn()
  const select = vi.fn()
  render(['workflows/jobs/migrate-old'], refetch, select)
  act(() => open('workflows/jobs/migrate-new'))
  expect(select).not.toHaveBeenCalled()
  act(() => { vi.advanceTimersByTime(2000) })
  expect(refetch).toHaveBeenCalledTimes(2)

  render(['workflows/jobs/migrate-old', 'workflows/jobs/migrate-new'], refetch, select)
  expect(select).toHaveBeenCalledExactlyOnceWith('workflows/jobs/migrate-new')
  act(() => { vi.advanceTimersByTime(5000) })
  expect(refetch).toHaveBeenCalledTimes(2)
  render(['workflows/jobs/migrate-new'], refetch, select)
  expect(select).toHaveBeenCalledOnce()
})

it('opens the run another way when the list is denied', () => {
  root = createRoot(document.createElement('div'))
  const refetch = vi.fn()
  const select = vi.fn()
  render([], refetch, select)
  failed = true
  act(() => open('workflows/jobs/migrate-new'))
  render([], refetch, select)
  expect(unlisted).toHaveBeenCalledExactlyOnceWith('workflows/jobs/migrate-new')
  act(() => { vi.advanceTimersByTime(5000) })
  expect(refetch).not.toHaveBeenCalled()
  expect(select).not.toHaveBeenCalled()
})

it('opens the run another way after 15 seconds unlisted', () => {
  root = createRoot(document.createElement('div'))
  const refetch = vi.fn()
  const select = vi.fn()
  render([], refetch, select)
  act(() => open('workflows/jobs/migrate-new'))
  act(() => { vi.advanceTimersByTime(30_000) })
  expect(refetch).toHaveBeenCalledTimes(15)
  expect(unlisted).toHaveBeenCalledExactlyOnceWith('workflows/jobs/migrate-new')
  render(['workflows/jobs/migrate-new'], refetch, select)
  expect(select).not.toHaveBeenCalled()
})

it('drops a waiting run when the view moves to another template', () => {
  root = createRoot(document.createElement('div'))
  const refetch = vi.fn()
  const select = vi.fn()
  render([], refetch, select)
  act(() => open('workflows/jobs/migrate-new'))
  render([], refetch, select, 'workflowtemplates/jobs/other')
  act(() => { vi.advanceTimersByTime(3000) })
  expect(refetch).not.toHaveBeenCalled()
  render(['workflows/jobs/migrate-new'], refetch, select, 'workflowtemplates/jobs/other')
  render(['workflows/jobs/migrate-new'], refetch, select)
  expect(select).not.toHaveBeenCalled()
  expect(unlisted).not.toHaveBeenCalled()
})
