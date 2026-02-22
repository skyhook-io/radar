import { createContext, useContext, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

export type ShortcutScope = 'global' | 'topology' | 'resources' | 'timeline' | 'helm' | 'traffic' | 'drawer'

// Scope priority: higher number = higher priority (wins when multiple scopes active)
const SCOPE_PRIORITY: Record<ShortcutScope, number> = {
  global: 0,
  topology: 1,
  resources: 1,
  timeline: 1,
  helm: 1,
  traffic: 1,
  drawer: 2,
}

export type ShortcutCategory = 'Navigation' | 'Search' | 'Resource Actions' | 'Table' | 'General' | 'Topology' | 'Timeline' | 'Helm' | 'Drawer' | 'Dock'

export interface KeyboardShortcut {
  /** Unique ID for this shortcut */
  id: string
  /** Display key(s) — e.g. "j", "g g", "Shift+N", "Cmd+K" */
  keys: string
  /** Human-readable description */
  description: string
  /** Category for help overlay grouping */
  category: ShortcutCategory
  /** Scope determines priority — higher scope wins */
  scope: ShortcutScope
  /** The handler function */
  handler: (e: KeyboardEvent) => void
  /** Whether this shortcut is currently active */
  enabled?: boolean
}

// Internal registration with stable ID
interface RegisteredShortcut extends KeyboardShortcut {
  _registrationId: number
}

interface KeyboardShortcutContextType {
  registerShortcut: (shortcut: KeyboardShortcut) => () => void
  activeShortcuts: KeyboardShortcut[]
}

const KeyboardShortcutContext = createContext<KeyboardShortcutContextType | null>(null)

let nextRegistrationId = 0

// Check if focus is in an element that should suppress shortcuts
function shouldSuppressShortcut(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement
  if (!target) return false

  // Always allow Escape
  if (e.key === 'Escape') return false

  // Suppress in text inputs
  const tagName = target.tagName
  if (tagName === 'INPUT' || tagName === 'TEXTAREA' || tagName === 'SELECT') return true

  // Suppress in contenteditable
  if (target.isContentEditable) return true

  // Suppress in Monaco editor
  if (target.closest('.monaco-editor')) return true

  // Suppress in xterm terminal
  if (target.closest('.xterm')) return true

  return false
}

// Parse a key string like "Shift+N", "Cmd+K", "g g" into match criteria
interface KeyMatcher {
  sequence: string[] // For multi-key sequences like ["g", "g"]
  ctrlKey?: boolean
  metaKey?: boolean
  shiftKey?: boolean
  altKey?: boolean
}

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

function parseKeys(keys: string): KeyMatcher {
  // Multi-key sequence (space-separated, e.g. "g g")
  if (keys.includes(' ') && !keys.includes('+')) {
    return { sequence: keys.split(' ') }
  }

  const parts = keys.split('+')
  const matcher: KeyMatcher = { sequence: [] }

  for (const part of parts) {
    const lower = part.toLowerCase().trim()
    if (lower === 'cmd' || lower === 'meta') {
      // Cmd maps to Meta on Mac, Ctrl on Windows/Linux
      if (isMac) matcher.metaKey = true
      else matcher.ctrlKey = true
    }
    else if (lower === 'ctrl') matcher.ctrlKey = true
    else if (lower === 'shift') matcher.shiftKey = true
    else if (lower === 'alt') matcher.altKey = true
    else matcher.sequence = [part]
  }

  return matcher
}

function matchesKey(e: KeyboardEvent, matcher: KeyMatcher, currentKey: string): boolean {
  // Single key (possibly with modifiers)
  if (matcher.sequence.length === 1) {
    const key = matcher.sequence[0]
    // Match the key
    if (e.key !== key && e.key.toLowerCase() !== key.toLowerCase()) return false
    // For uppercase letters (Shift+X), check shift
    if (key.length === 1 && key === key.toUpperCase() && key !== key.toLowerCase()) {
      if (!e.shiftKey) return false
    }
    // Check required modifiers
    if (matcher.metaKey && !e.metaKey) return false
    if (matcher.ctrlKey && !e.ctrlKey) return false
    if (matcher.shiftKey && !e.shiftKey) return false
    if (matcher.altKey && !e.altKey) return false
    // Check no extra modifiers (unless required) — but only for non-modifier keys
    if (!matcher.metaKey && e.metaKey) return false
    if (!matcher.ctrlKey && e.ctrlKey) return false
    // Don't check shift for plain lowercase letters (prevents Shift+j triggering "j")
    // but allow Shift-produced symbols like ? (Shift+/) and + (Shift+=)
    if (matcher.shiftKey === undefined && e.shiftKey && key.length === 1 && key >= 'a' && key <= 'z') return false
    if (!matcher.altKey && e.altKey) return false
    return true
  }

  // Multi-key sequence — currentKey tracks the first key pressed
  return currentKey === matcher.sequence[0] && e.key === matcher.sequence[1]
}

const SEQUENCE_TIMEOUT = 500

export function KeyboardShortcutProvider({ children }: { children: ReactNode }) {
  const shortcutsRef = useRef<Map<number, RegisteredShortcut>>(new Map())
  const [version, setVersion] = useState(0)
  const sequenceKeyRef = useRef<string | null>(null)
  const sequenceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const registerShortcut = useCallback((shortcut: KeyboardShortcut) => {
    const id = nextRegistrationId++
    const registered: RegisteredShortcut = { ...shortcut, _registrationId: id }
    shortcutsRef.current.set(id, registered)
    setVersion(v => v + 1)

    return () => {
      shortcutsRef.current.delete(id)
      setVersion(v => v + 1)
    }
  }, [])

  // Global keydown listener
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      const suppressed = shouldSuppressShortcut(e)

      // Get all enabled shortcuts, sorted by scope priority (highest first)
      const shortcuts = Array.from(shortcutsRef.current.values())
        .filter(s => s.enabled !== false)
        .sort((a, b) => SCOPE_PRIORITY[b.scope] - SCOPE_PRIORITY[a.scope])

      // Check for multi-key sequence completion
      if (sequenceKeyRef.current) {
        const firstKey = sequenceKeyRef.current
        sequenceKeyRef.current = null
        if (sequenceTimerRef.current) {
          clearTimeout(sequenceTimerRef.current)
          sequenceTimerRef.current = null
        }

        if (!suppressed) {
          for (const shortcut of shortcuts) {
            const matcher = parseKeys(shortcut.keys)
            if (matcher.sequence.length === 2 && matchesKey(e, matcher, firstKey)) {
              e.preventDefault()
              try { shortcut.handler(e) } catch (err) { console.error(`[KeyboardShortcuts] Handler "${shortcut.id}" threw:`, err) }
              return
            }
          }
        }
        // Sequence didn't match — fall through to single-key handling
      }

      // Check single-key shortcuts
      for (const shortcut of shortcuts) {
        const matcher = parseKeys(shortcut.keys)

        // Skip multi-key sequences in this pass (handled above)
        if (matcher.sequence.length === 2) {
          // But check if this could be the START of a sequence
          if (!suppressed && e.key === matcher.sequence[0] && !e.metaKey && !e.ctrlKey && !e.altKey) {
            // Start sequence tracking
            sequenceKeyRef.current = e.key
            if (sequenceTimerRef.current) clearTimeout(sequenceTimerRef.current)
            sequenceTimerRef.current = setTimeout(() => {
              sequenceKeyRef.current = null
              sequenceTimerRef.current = null
            }, SEQUENCE_TIMEOUT)
            // Don't prevent default yet — the key might be standalone
            return
          }
          continue
        }

        // Escape always fires (even when suppressed)
        if (e.key === 'Escape' && shortcut.keys === 'Escape') {
          e.preventDefault()
          try { shortcut.handler(e) } catch (err) { console.error(`[KeyboardShortcuts] Handler "${shortcut.id}" threw:`, err) }
          return
        }

        if (suppressed) continue

        if (matchesKey(e, matcher, '')) {
          e.preventDefault()
          try { shortcut.handler(e) } catch (err) { console.error(`[KeyboardShortcuts] Handler "${shortcut.id}" threw:`, err) }
          return
        }
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      if (sequenceTimerRef.current) {
        clearTimeout(sequenceTimerRef.current)
        sequenceTimerRef.current = null
      }
    }
  }, [])

  // Compute active shortcuts for help overlay (memoized on version changes).
  // Shows ALL registered shortcuts regardless of `enabled` state — the help overlay
  // should present a stable view of what's available, not flicker based on transient state.
  const activeShortcuts = useMemo(() =>
    Array.from(shortcutsRef.current.values())
      .map(({ _registrationId, ...s }) => s as KeyboardShortcut)
      // Deduplicate by id (keep highest priority)
      .reduce((acc, s) => {
        const existing = acc.find(a => a.id === s.id)
        if (!existing || SCOPE_PRIORITY[s.scope] > SCOPE_PRIORITY[existing.scope]) {
          return [...acc.filter(a => a.id !== s.id), s]
        }
        return acc
      }, [] as KeyboardShortcut[])
      .sort((a, b) => {
        // Sort by category, then by keys
        if (a.category !== b.category) return a.category.localeCompare(b.category)
        return a.keys.localeCompare(b.keys)
      })
  , [version])

  return (
    <KeyboardShortcutContext.Provider value={{ registerShortcut, activeShortcuts }}>
      {children}
    </KeyboardShortcutContext.Provider>
  )
}

/** Register a keyboard shortcut. Automatically deregisters on unmount or when key config changes. */
export function useRegisterShortcut(shortcut: KeyboardShortcut) {
  const ctx = useContext(KeyboardShortcutContext)
  const register = ctx?.registerShortcut
  const handlerRef = useRef(shortcut.handler)
  handlerRef.current = shortcut.handler

  const stableHandler = useCallback((e: KeyboardEvent) => {
    handlerRef.current(e)
  }, [])

  useEffect(() => {
    if (!register) {
      if (import.meta.env.DEV) {
        console.warn(`[useRegisterShortcut] Shortcut "${shortcut.id}" registered outside KeyboardShortcutProvider — it will not work.`)
      }
      return
    }
    return register({
      ...shortcut,
      handler: stableHandler,
    })
    // Re-register when key config changes (but NOT handler — that's stable via ref)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [register, shortcut.id, shortcut.keys, shortcut.scope, shortcut.enabled, stableHandler])
}

/** Register multiple shortcuts at once. */
export function useRegisterShortcuts(shortcuts: KeyboardShortcut[]) {
  const ctx = useContext(KeyboardShortcutContext)
  const handlersRef = useRef<Map<string, (e: KeyboardEvent) => void>>(new Map())

  // Update handler refs
  for (const s of shortcuts) {
    handlersRef.current.set(s.id, s.handler)
  }

  const register = ctx?.registerShortcut

  useEffect(() => {
    if (!register) {
      if (import.meta.env.DEV) {
        console.warn(`[useRegisterShortcuts] ${shortcuts.length} shortcuts (${shortcuts.map(s => s.id).join(', ')}) registered outside KeyboardShortcutProvider — they will not work.`)
      }
      return
    }

    const cleanups: (() => void)[] = []
    for (const shortcut of shortcuts) {
      const handler = handlersRef.current.get(shortcut.id)
      if (handler) {
        cleanups.push(register({
          ...shortcut,
          handler: (e) => {
            const current = handlersRef.current.get(shortcut.id)
            current?.(e)
          },
        }))
      }
    }

    return () => cleanups.forEach(fn => fn())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [register, shortcuts.map(s => `${s.id}:${s.keys}:${s.scope}:${s.enabled}`).join('|')])
}

/** Get all active shortcuts (for help overlay). */
export function useActiveShortcuts(): KeyboardShortcut[] {
  const ctx = useContext(KeyboardShortcutContext)
  return ctx?.activeShortcuts ?? []
}
