// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ToastProvider, useToast } from './Toast'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let host: HTMLDivElement
let root: ReturnType<typeof createRoot>
beforeEach(async () => {
  vi.useFakeTimers()
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  function Probe() {
    const { showError } = useToast()
    return <button onClick={() => showError('Permission denied', undefined, 'opaque raw error')}>Show</button>
  }
  await act(async () => root.render(<ToastProvider><Probe /></ToastProvider>))
  await act(async () => host.querySelector('button')!.click())
})
afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  vi.useRealTimers()
})
const toast = () => host.querySelector<HTMLElement>('.backdrop-blur-sm')!
const advance = async (ms: number) => { await act(async () => vi.advanceTimersByTime(ms)) }
it('pauses dismissal while hovered and resumes the remaining time', async () => {
  await advance(9000)
  await act(async () => toast().dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
  await advance(20000)
  expect(toast()).not.toBeNull()
  await act(async () => toast().dispatchEvent(new MouseEvent('mouseout', { bubbles: true })))
  await advance(1000)
  await advance(1000)
  expect(toast()).toBeNull()
})
it('keeps expanded error details, uses the toast tint, and copies the raw error', async () => {
  const clipboard = { writeText: vi.fn(async () => {}) }
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: clipboard })
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!.click())
  await advance(60000)
  expect(toast()).not.toBeNull()
  expect(host.querySelector('.bg-black\\/25')).not.toBeNull()
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === 'Copy raw error')!.click())
  expect(clipboard.writeText).toHaveBeenCalledWith('opaque raw error')
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="Dismiss notification"]')!.click())
  await advance(1000)
  expect(toast()).toBeNull()
})
