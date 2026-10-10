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
it('keeps expanded error details, uses the toast tint, and copies the raw error', async () => {
  const clipboard = { writeText: vi.fn(async () => {}) }
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: clipboard })
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Error details'))!.click())
  await advance(60000)
  expect(toast()).not.toBeNull()
  expect(host.querySelector('.bg-black\\/25')).not.toBeNull()
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
  expect(info.querySelector('.whitespace-nowrap:last-child')!.textContent).toBe('gaps-r4-demo.')
  await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent === 'Other shortcut')!.click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
  await act(async () => info.querySelector<HTMLButtonElement>('[aria-label="Dismiss notification"]')!.click())
  await act(async () => denied().click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
  await advance(1000)
  await act(async () => denied().click())
  expect(host.querySelectorAll('.backdrop-blur-sm')).toHaveLength(3)
})
