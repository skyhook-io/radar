// @vitest-environment jsdom
import { act } from 'react'
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

function Probe({ scope, keys, refetch, select }: { scope: string; keys: string[]; refetch: () => void; select: (key: string) => void }) {
  open = useSelectRunWhenListed(scope, keys, refetch, select)
  return null
}
function render(keys: string[], refetch: () => void, select: (key: string) => void, scope = 'workflowtemplates/jobs/migrate') {
  act(() => root.render(<Probe scope={scope} keys={keys} refetch={refetch} select={select} />))
}

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

it('gives up after 15 seconds without selecting anything', () => {
  root = createRoot(document.createElement('div'))
  const refetch = vi.fn()
  const select = vi.fn()
  render([], refetch, select)
  act(() => open('workflows/jobs/migrate-new'))
  act(() => { vi.advanceTimersByTime(30_000) })
  expect(refetch).toHaveBeenCalledTimes(15)
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
})
