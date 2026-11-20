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
    const { showError, showToast } = useToast()
    return <>
      <button onClick={() => showError('Permission denied', undefined, 'opaque raw error')}>Show</button>
      <button onClick={() => showToast('Checking for updates', { type: 'info', id: 'update', duration: 0 })}>Progress</button>
      <button onClick={() => showToast("You're up to date", { type: 'success', id: 'update' })}>Result</button>
      <button onClick={() => showToast('Update check failed', { type: 'error', id: 'update', rawDetail: 'stack trace' })}>Failed check</button>
    </>
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
it('keeps expanded error details, tints them from the error tone, and copies the raw error', async () => {
  const clipboard = { writeText: vi.fn(async () => {}) }
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: clipboard })
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!.click())
  await advance(60000)
  expect(toast()).not.toBeNull()
  // The error toast is dark in both themes; a theme surface would render the
  // panel near-white in light mode.
  const panel = host.querySelector('pre')!.closest('.rounded.p-2')!
  expect(panel.classList.contains('bg-theme-surface')).toBe(false)
  expect(panel.classList.contains('bg-current/10')).toBe(true)
  expect(panel.classList.contains('text-red-200')).toBe(true)
  const chevron = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!.querySelector('svg')!
  expect(chevron.classList.contains('text-theme-text-tertiary')).toBe(false)
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === 'Copy raw error')!.click())
  expect(clipboard.writeText).toHaveBeenCalledWith('opaque raw error')
  await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="Dismiss notification"]')!.click())
  await advance(1000)
  expect(toast()).toBeNull()
})
it('resumes the remaining dismissal time after closing details and ending hover and focus', async () => {
  await advance(9000)
  const toggle = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!
  await act(async () => { toggle.focus(); toggle.click(); toast().dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
  await advance(20000)
  await act(async () => toggle.click())
  expect(toggle.getAttribute('aria-expanded')).toBe('false')
  await advance(20000)
  expect(toast()).not.toBeNull()
  await act(async () => toast().dispatchEvent(new MouseEvent('mouseout', { bubbles: true })))
  await advance(20000)
  expect(toast()).not.toBeNull()
  await act(async () => toggle.blur())
  await advance(999)
  expect(toast()).not.toBeNull()
  await advance(1)
  expect(toast().classList.contains('animate-out')).toBe(true)
  await advance(1000)
  expect(toast()).toBeNull()
})
it('keeps a duration 0 toast until a result replaces it, then dismisses the result on the normal timer', async () => {
  const click = async (label: string) => act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === label)!.click())
  const update = () => [...host.querySelectorAll<HTMLElement>('.backdrop-blur-sm')].find(item => item.textContent?.includes('Checking') || item.textContent?.includes('up to date'))
  await click('Progress')
  await advance(60000)
  expect(update()?.textContent).toContain('Checking')
  await click('Result')
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(1)
  expect(update()?.textContent).toContain('up to date')
  await advance(6999)
  expect(update()).toBeDefined()
  await advance(1001)
  expect(update()).toBeUndefined()
})

it('dismisses a result that replaces an expanded error, whose details control is gone', async () => {
  const click = async (label: string) => act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === label)!.click())
  await advance(11000)
  await click('Failed check')
  const details = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!
  await act(async () => { details.focus(); details.click() })
  await advance(20000)
  await click('Result')
  expect(host.textContent).not.toContain('Error details')
  await advance(8000)
  expect(host.querySelector('.backdrop-blur-sm')).toBeNull()
})

it('ignores an expired timer when a replacement is queued before React commits it', async () => {
  const click = (label: string) => [...host.querySelectorAll('button')].find(button => button.textContent === label)!.click()
  await advance(11000)
  await act(async () => click('Failed check'))
  await advance(9999)
  await act(async () => {
    click('Result')
    vi.advanceTimersByTime(1)
  })
  await advance(1000)
  expect(toast().textContent).toContain("You're up to date")
  expect(toast().classList.contains('animate-out')).toBe(false)
})
