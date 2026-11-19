// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '../components/ui/Toast'
import { installStaleAssetPrompt } from './staleAssetPrompt'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let container: HTMLDivElement
let root: Root
let uninstall: () => void
const reload = vi.fn()

function preloadError(): Event {
  const event = Object.assign(new Event('vite:preloadError', { cancelable: true }), { payload: new Error('Failed to fetch dynamically imported module') })
  act(() => {
    window.dispatchEvent(event)
  })
  return event
}

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
  reload.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<ToastProvider><div /></ToastProvider>))
  uninstall = installStaleAssetPrompt(window, reload)
})

afterEach(() => {
  uninstall()
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
})

const prompts = () => Array.from(document.querySelectorAll('[role="status"]')).filter(el => el.textContent?.includes('Part of Radar failed to load'))

describe('installStaleAssetPrompt', () => {
  it('offers a reload that stays until used, without taking over the failed import', () => {
    vi.useFakeTimers()
    const event = preloadError()

    expect(event.defaultPrevented).toBe(false)
    expect(prompts()).toHaveLength(1)
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(prompts()).toHaveLength(1)
    expect(reload).not.toHaveBeenCalled()

    const button = Array.from(prompts()[0].querySelectorAll('button')).find(b => b.textContent?.includes('Reload'))
    act(() => button!.click())
    expect(reload).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('shows one prompt however many chunks fail, and leaves it dismissed', () => {
    vi.useFakeTimers()
    preloadError()
    preloadError()
    expect(prompts()).toHaveLength(1)

    const dismiss = Array.from(prompts()[0].querySelectorAll('button')).find(b => !b.textContent?.includes('Reload'))
    act(() => dismiss!.click())
    act(() => {
      vi.advanceTimersByTime(1_000)
    })
    preloadError()
    expect(prompts()).toHaveLength(0)
    vi.useRealTimers()
  })
})
