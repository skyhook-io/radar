// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { TerminalPreferencesProvider, useTerminalCopyOnSelect } from './terminalPreferences'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('shares the choice, defaults off, isolates accounts and updates an existing connection getter', async () => {
  const element = document.createElement('div')
  const root = createRoot(element)
  let first: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  let second: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Consumer({ index }: { index: number }) {
    const preference = useTerminalCopyOnSelect()
    if (index === 0) first = preference
    else second = preference
    return null
  }
  const render = (scope: string) => act(async () => root.render(
    <TerminalPreferencesProvider scope={scope}><Consumer index={0} /><Consumer index={1} /></TerminalPreferencesProvider>,
  ))
  try {
    await render('test-account-a')
    expect(first?.enabled).toBe(false)
    const connectedGetter = first?.getState
    await act(async () => first?.setEnabled(true))
    expect(second?.enabled).toBe(true)
    expect(connectedGetter?.()).toBe('on')
    expect(localStorage.getItem('radar:terminal:copy-on-select:test-account-a')).toBe('true')
    await render('test-account-b')
    expect(second?.enabled).toBe(false)
    expect(connectedGetter?.()).toBe('off')
    await render('test-account-a')
    expect(second?.enabled).toBe(true)
    await act(async () => window.dispatchEvent(new window.StorageEvent('storage', {
      key: 'radar:terminal:copy-on-select:test-account-a', newValue: 'false',
      storageArea: localStorage,
    })))
    expect(first?.enabled).toBe(false)
    expect(connectedGetter?.()).toBe('off')
  } finally {
    await act(async () => root.unmount())
    localStorage.clear()
  }
})

it('retains a session choice across account switches when storage reads work but writes fail', async () => {
  const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
  const root = createRoot(document.createElement('div'))
  let current: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Consumer() { current = useTerminalCopyOnSelect(); return null }
  const render = (scope: string) => act(async () => root.render(
    <TerminalPreferencesProvider scope={scope}><Consumer /></TerminalPreferencesProvider>,
  ))
  try {
    await render('write-failed')
    await act(async () => current?.setEnabled(true))
    await render('another-account')
    expect(current?.enabled).toBe(false)
    await render('write-failed')
    expect(current?.enabled).toBe(true)
    await act(async () => window.dispatchEvent(new window.StorageEvent('storage', {
      key: 'radar:terminal:copy-on-select:write-failed', newValue: 'false', storageArea: sessionStorage,
    })))
    expect(current?.enabled).toBe(true)
  } finally {
    await act(async () => root.unmount())
    write.mockRestore()
    localStorage.clear()
  }
})

it('distinguishes unavailable account information from a pending request', async () => {
  const root = createRoot(document.createElement('div'))
  let current: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Consumer() { current = useTerminalCopyOnSelect(); return null }
  try {
    await act(async () => root.render(<TerminalPreferencesProvider unavailableReason="Could not load account information. Reload to try again."><Consumer /></TerminalPreferencesProvider>))
    expect(current?.available).toBe(false)
    expect(current?.unavailableReason).toBe('Could not load account information. Reload to try again.')
    await act(async () => root.render(<Consumer />))
    expect(current?.unavailableReason).toBe('Account information unavailable')
  } finally {
    await act(async () => root.unmount())
  }
})

it('keeps the shared choice in memory when browser storage is blocked', async () => {
  const read = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  const root = createRoot(document.createElement('div'))
  let current: ReturnType<typeof useTerminalCopyOnSelect> | undefined
  function Consumer() { current = useTerminalCopyOnSelect(); return null }
  try {
    await act(async () => root.render(<TerminalPreferencesProvider scope="blocked-storage"><Consumer /></TerminalPreferencesProvider>))
    expect(current?.enabled).toBe(false)
    await act(async () => current?.setEnabled(true))
    expect(current?.enabled).toBe(true)
    expect(current?.getState()).toBe('on')
  } finally {
    await act(async () => root.unmount())
    read.mockRestore()
    write.mockRestore()
  }
})
