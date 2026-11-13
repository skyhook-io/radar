import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useSyncExternalStore, type ReactNode } from 'react'
import type { TerminalCopyOnSelectState } from './terminalClipboard'

interface Preference {
  enabled: boolean
  memoryOnly: boolean
  listeners: Set<() => void>
}

const preferences = new Map<string | undefined, Preference>()
const PreferenceScope = createContext<{ scope?: string; unavailableReason?: string; scopePending?: boolean; isScopeCurrent?: () => boolean } | null>(null)

function preference(scope: string | undefined): Preference {
  let value = preferences.get(scope)
  if (!value) {
    let enabled = false
    if (scope !== undefined) {
      try { enabled = localStorage.getItem(`radar:terminal:copy-on-select:${scope}`) === 'true' } catch { /* Keep an in-memory preference when storage is unavailable. */ }
    }
    value = { enabled, memoryOnly: false, listeners: new Set() }
    preferences.set(scope, value)
  }
  return value
}

/** Omit the scope until the host has confirmed the account identity. */
export function TerminalPreferencesProvider({ scope, unavailableReason, scopePending, isScopeCurrent, children }: { scope?: string; unavailableReason?: string; scopePending?: boolean; isScopeCurrent?: () => boolean; children: ReactNode }) {
  useEffect(() => {
    if (scope === undefined) return
    const key = `radar:terminal:copy-on-select:${scope}`
    const state = preference(scope)
    try {
      if (!state.memoryOnly) state.enabled = localStorage.getItem(key) === 'true'
      state.listeners.forEach((notify) => notify())
    } catch { /* Retain the in-memory choice when storage is unavailable. */ }
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== key && event.key !== null) return
      try { if (event.storageArea !== localStorage) return } catch { return }
      state.memoryOnly = false
      state.enabled = event.newValue === 'true'
      state.listeners.forEach((notify) => notify())
    }
    window.addEventListener('storage', handleStorage)
    return () => window.removeEventListener('storage', handleStorage)
  }, [scope])
  return <PreferenceScope.Provider value={{ scope, unavailableReason, scopePending, isScopeCurrent }}>{children}</PreferenceScope.Provider>
}

export function useTerminalCopyOnSelect() {
  const context = useContext(PreferenceScope)
  const { scope, unavailableReason } = context ?? {}
  const state = preference(scope)
  const current = useRef(state)
  const scopeCurrent = useRef(context?.isScopeCurrent)
  useLayoutEffect(() => { current.current = state; scopeCurrent.current = context?.isScopeCurrent }, [state, context?.isScopeCurrent])
  const getState = useCallback((): TerminalCopyOnSelectState => {
    if (!current.current.enabled) return 'off'
    return scopeCurrent.current?.() === false ? 'pending' : 'on'
  }, [])
  const enabled = useSyncExternalStore(
    (notify) => { state.listeners.add(notify); return () => { state.listeners.delete(notify) } },
    () => state.enabled,
    () => false,
  )
  return {
    enabled,
    available: scope !== undefined && !context?.scopePending,
    hasProvider: context !== null,
    unavailableReason: unavailableReason ?? 'Account information unavailable',
    getState,
    setEnabled: (next: boolean) => {
      if (scopeCurrent.current?.() === false) return
      state.enabled = next
      if (scope !== undefined) {
        try {
          localStorage.setItem(`radar:terminal:copy-on-select:${scope}`, String(next))
          state.memoryOnly = false
        } catch { state.memoryOnly = true }
      }
      state.listeners.forEach((notify) => notify())
    },
  }
}
