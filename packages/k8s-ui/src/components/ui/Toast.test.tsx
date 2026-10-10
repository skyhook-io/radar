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
      <button onClick={() => showToast("Your role can't patch Argo CD Applications in gaps-r4-demo.", { type: 'info', id: 'gitops-denied:patch' })}>Denied shortcut</button>
      <button onClick={() => showToast('Another denial', { type: 'info', id: 'gitops-denied:get' })}>Other shortcut</button>
      <button onClick={() => showToast('x'.repeat(300), { detail: 'https://example.test/' + 'y'.repeat(300), onDetailClick: () => {} })}>Long clickable</button>
      <button onClick={() => showToast('z'.repeat(300), { detail: 'a'.repeat(300) })}>Long detail</button>
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
it('reuses a toast id on repeated denied shortcuts and can show it again after dismissal', async () => {
  const denied = () => [...host.querySelectorAll('button')].find(button => button.textContent === 'Denied shortcut')!
  await act(async () => { denied().click(); denied().click(); denied().click() })
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(2)
  const info = [...host.querySelectorAll<HTMLElement>('.backdrop-blur-sm')].find(item => item.textContent?.includes('gaps-r4-demo'))!
  expect([...info.querySelectorAll('.inline-block')].find(word => word.textContent === 'gaps-r4-demo.')).toBeDefined()
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === 'Other shortcut')!.click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
  await act(async () => info.querySelector<HTMLButtonElement>('[aria-label="Dismiss notification"]')!.click())
  await act(async () => denied().click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
  await advance(1000)
  await act(async () => denied().click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
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
it.each(['Long clickable', 'Long detail'])('keeps short words whole and allows oversized message and detail tokens to wrap (%s)', async label => {
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === label)!.click())
  const longToast = [...host.querySelectorAll<HTMLElement>('.backdrop-blur-sm')].at(-1)!
  const words = [...longToast.querySelectorAll<HTMLElement>('.inline-block')]
  expect(words).toHaveLength(2)
  for (const word of words) {
    expect(word.textContent!.length).toBeGreaterThanOrEqual(300)
    expect(word.classList.contains('max-w-full')).toBe(true)
    expect(word.classList.contains('[overflow-wrap:anywhere]')).toBe(true)
    expect(word.classList.contains('whitespace-nowrap')).toBe(false)
  }
})
