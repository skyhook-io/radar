// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { useTerminalCopyOnSelect } from '../../../../packages/k8s-ui/src/components/dock/terminalPreferences'
import { TerminalPreferences } from './TerminalPreferences'

const { refetch } = vi.hoisted(() => ({ refetch: vi.fn() }))
vi.mock('../../api/client', () => ({ useAuthMe: () => ({ refetch }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

interface Result {
  data?: { authEnabled: boolean; username?: string }
  isSuccess: boolean
  isError: boolean
  fetchStatus: 'idle'
  dataUpdatedAt: number
}

it('blocks an existing terminal immediately on focus or visibility changes until the fresh account scope is applied', async () => {
  const responses: Array<(value: Result) => void> = []
  refetch.mockImplementation(() => new Promise<Result>(resolve => responses.push(resolve)))
  const root = createRoot(document.createElement('div'))
  let preference: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Terminal() { preference = useTerminalCopyOnSelect(); return null }
  localStorage.setItem('radar:terminal:copy-on-select:account:owned-a', 'true')
  try {
    await act(async () => root.render(<TerminalPreferences><Terminal /></TerminalPreferences>))
    await act(async () => responses.shift()?.({ data: { authEnabled: true, username: 'owned-a' }, isSuccess: true, isError: false, fetchStatus: 'idle', dataUpdatedAt: Date.now() }))
    const connectedGetter = preference?.getState
    expect(connectedGetter?.()).toBe('on')
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
      expect(connectedGetter?.()).toBe('pending')
      preference?.setEnabled(false)
      expect(localStorage.getItem('radar:terminal:copy-on-select:account:owned-a')).toBe('true')
    })
    expect(preference?.available).toBe(false)
    await act(async () => window.dispatchEvent(new Event('blur')))
    await act(async () => responses.shift()?.({ data: { authEnabled: true, username: 'owned-a' }, isSuccess: true, isError: false, fetchStatus: 'idle', dataUpdatedAt: Date.now() }))
    expect(connectedGetter?.()).toBe('pending')
    expect(preference?.available).toBe(false)
    await act(async () => window.dispatchEvent(new Event('focus')))
    await act(async () => responses.shift()?.({ data: { authEnabled: true, username: 'owned-b' }, isSuccess: true, isError: false, fetchStatus: 'idle', dataUpdatedAt: Date.now() }))
    expect(preference?.available).toBe(true)
    expect(connectedGetter?.()).toBe('off')
    await act(async () => preference?.setEnabled(true))
    expect(connectedGetter?.()).toBe('on')
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      expect(connectedGetter?.()).toBe('pending')
    })
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    await act(async () => document.dispatchEvent(new Event('visibilitychange')))
    expect(connectedGetter?.()).toBe('pending')
    await act(async () => responses.shift()?.({ isSuccess: false, isError: true, fetchStatus: 'idle', dataUpdatedAt: 0 }))
    expect(preference?.available).toBe(false)
    expect(connectedGetter?.()).toBe('pending')
    expect(preference?.unavailableReason).toContain('Could not load account information')
    expect(preference?.unavailableReason).toContain('Switch to another tab or window and back to retry')
  } finally {
    await act(async () => root.unmount())
    refetch.mockReset()
    localStorage.clear()
    Reflect.deleteProperty(document, 'visibilityState')
  }
})
