// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { VersionInfo } from '../../api/client'

vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: { deployment: { mode: 'local' } } }),
  useVersionCheck: () => ({ data: { currentVersion: '1.2.3', updateAvailable: false, installMethod: 'desktop' } }),
  useStartDesktopUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useDesktopUpdateStatus: () => ({ data: undefined }),
  useApplyDesktopUpdate: () => ({ mutate: vi.fn() }),
}))

import { UpdateNotification } from './UpdateNotification'
import { ToastProvider, showApiNotice } from './Toast'
import { DURATION_TOAST_EXIT } from '../../utils/animation'
import { setApiBase } from '../../api/config'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let container: HTMLDivElement
let client: QueryClient
const current: VersionInfo = { currentVersion: '1.2.3', updateAvailable: false, installMethod: 'desktop' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  setApiBase('/desktop/api')
  client = new QueryClient()
  client.setQueryData(['version-check', '/desktop/api'], current)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(
    <QueryClientProvider client={client}>
      <ToastProvider>
        <UpdateNotification />
        <UpdateNotification placement="settings" />
      </ToastProvider>
    </QueryClientProvider>,
  ))
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  client.clear()
  setApiBase('/api')
  vi.useRealTimers()
})

describe('manual Desktop version checks', () => {
  it('acknowledges one check even when Settings is also mounted', () => {
    act(() => { window.dispatchEvent(new Event('radar:check-for-updates')) })
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(1)
    expect(container.textContent).toContain('Checking for updates…')
  })

  it('reports an up-to-date result once', () => {
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: current })) })
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(1)
    expect(container.textContent).toContain("You're up to date")
    expect(container.textContent).toContain('Radar 1.2.3')
  })

  it('publishes a fresh available update and clears its dismissal', () => {
    localStorage.setItem('radar-update-dismissed', '1.3.0')
    const info = { ...current, updateAvailable: true, latestVersion: '1.3.0' }
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: info })) })
    expect(client.getQueryData(['version-check', '/desktop/api'])).toEqual(info)
    expect(localStorage.getItem('radar-update-dismissed')).toBeNull()
    expect(container.textContent).toContain('Radar 1.3.0 is available')
  })

  it('reports failure without replacing known version data', () => {
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: { ...current, error: 'offline' } })) })
    expect(container.querySelector('[role="status"]')?.textContent).toContain('Could not check for updates')
    expect(container.textContent).toContain('offline')
    expect(container.textContent).not.toContain("You're up to date")
    expect(client.getQueryData(['version-check', '/desktop/api'])).toEqual(current)
  })

  it('does not claim a development build is up to date', () => {
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: { ...current, currentVersion: '1.3.0-37-g8b325b372', buildChannel: 'development', latestVersion: '1.3.0' } })) })
    expect(container.textContent).not.toContain("You're up to date")
    expect(container.textContent).toContain('Latest Radar release: 1.3.0')
    expect(container.textContent).toContain('You are running a development build.')
  })

  it('discards an automatic response that races with the manual result', async () => {
    let resolveAutomatic!: (value: VersionInfo) => void
    const pending = client.fetchQuery({
      queryKey: ['version-check', '/desktop/api'],
      queryFn: () => new Promise<VersionInfo>(resolve => { resolveAutomatic = resolve }),
      staleTime: 0,
    }).catch(() => undefined)
    const info = { ...current, updateAvailable: true, latestVersion: '1.3.0' }
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: info })) })
    resolveAutomatic(current)
    await pending
    expect(client.getQueryData(['version-check', '/desktop/api'])).toEqual(info)
  })

  it('lets the initial automatic fetch finish when a manual check fails', async () => {
    client.removeQueries({ queryKey: ['version-check', '/desktop/api'], exact: true })
    let resolveAutomatic!: (value: VersionInfo) => void
    const pending = client.fetchQuery({
      queryKey: ['version-check', '/desktop/api'],
      queryFn: () => new Promise<VersionInfo>(resolve => { resolveAutomatic = resolve }),
    })
    act(() => {
      window.dispatchEvent(new Event('radar:check-for-updates'))
      window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: { ...current, error: 'offline' } }))
    })
    resolveAutomatic(current)
    await pending
    expect(client.getQueryData(['version-check', '/desktop/api'])).toEqual(current)
  })
})


describe('manual check toast lifecycle', () => {
  it('updates the same toast element after a slow check and starts the result timer then', () => {
    act(() => { window.dispatchEvent(new Event('radar:check-for-updates')) })
    const toast = container.querySelector('[role="status"]')
    act(() => { vi.advanceTimersByTime(9000) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    expect(toast?.textContent).toContain('Checking for updates…')

    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: current })) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    expect(toast?.textContent).toContain("You're up to date")
    expect(container.textContent).not.toContain('Checking for updates…')
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(1)
    act(() => { vi.advanceTimersByTime(6999) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    act(() => { vi.advanceTimersByTime(1 + DURATION_TOAST_EXIT) })
    expect(container.querySelector('[role="status"]')).toBeNull()
  })

  it('replaces progress with an error in the same toast', () => {
    act(() => { window.dispatchEvent(new Event('radar:check-for-updates')) })
    const toast = container.querySelector('[role="status"]')
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: { ...current, error: 'offline' } })) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    expect(container.textContent).not.toContain('Checking for updates…')
    expect(container.querySelectorAll('[role="status"], [role="alert"]')).toHaveLength(1)
    act(() => { vi.advanceTimersByTime(9999) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    act(() => { vi.advanceTimersByTime(1 + DURATION_TOAST_EXIT) })
    expect(container.querySelector('[role="status"]')).toBeNull()
  })

  it('does not let a previous result timer dismiss the next check', () => {
    act(() => { window.dispatchEvent(new CustomEvent('radar:update-check-result', { detail: current })) })
    act(() => { vi.advanceTimersByTime(7100) })
    act(() => { window.dispatchEvent(new Event('radar:check-for-updates')) })
    const toast = container.querySelector('[role="status"]')
    act(() => { vi.advanceTimersByTime(9000) })
    expect(container.querySelector('[role="status"]')).toBe(toast)
    expect(toast?.textContent).toContain('Checking for updates…')
    expect(toast?.classList.contains('animate-out')).toBe(false)
  })

  it('keeps ordinary toasts independent and auto-dismisses them', () => {
    act(() => {
      showApiNotice('First notice', undefined, 'info')
      showApiNotice('Second notice', undefined, 'info')
    })
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(2)
    act(() => { vi.advanceTimersByTime(7000 + DURATION_TOAST_EXIT) })
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(0)
  })
})
