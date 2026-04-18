import {
  useState,
  useCallback,
  useRef,
  useEffect,
  useLayoutEffect,
  createContext,
  useContext,
  type ReactNode,
} from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ExternalLink,
  Copy,
  Check,
  Trash2,
  Loader2,
  ChevronUp,
  Plug,
  Globe,
  Monitor,
  PenLine,
} from 'lucide-react'
import { clsx } from 'clsx'
// CSS_EASE (the shared spring curve) is intentionally NOT used for this panel —
// its overshoot makes scale animations on small popovers look bouncy.
// We use a custom ease-out inline instead.
import { SEVERITY_BADGE } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'
import { useToast } from '../ui/Toast'
import { openExternal } from '../../utils/navigation'
import { apiUrl } from '../../api/config'

// --- Types -------------------------------------------------------------------

interface PortForwardSession {
  id: string
  namespace: string
  podName: string
  podPort: number
  localPort: number
  listenAddress: string
  serviceName?: string
  startedAt: string
  status: 'running' | 'stopped' | 'error'
  error?: string
}

// --- Shared query ------------------------------------------------------------

function usePortForwardQuery() {
  return useQuery<PortForwardSession[]>({
    queryKey: ['portforwards'],
    queryFn: async () => {
      const res = await fetch(apiUrl('/portforwards'))
      if (!res.ok) throw new Error('Failed to fetch port forwards')
      return res.json()
    },
    // 30s fallback poll — user mutations invalidate immediately, but out-of-band
    // session death (pod restart, OOM kill, server-side cleanup) only surfaces on
    // the next tick.
    refetchInterval: 30000,
  })
}

// --- Context & provider ------------------------------------------------------

// Show the panel this long after a new forward starts before auto-minimizing.
const AUTO_MINIMIZE_INITIAL_MS = 4000
// Shorter grace period after the cursor leaves a hovered panel.
const AUTO_MINIMIZE_HOVER_LEAVE_MS = 1500

/** Measured position for anchoring the panel to the indicator button. */
interface PanelAnchor {
  top: number
  right: number
  /** Horizontal center of the indicator (relative to panel's right edge) — for caret positioning. */
  caretRight: number
}

interface PortForwardContextValue {
  sessions: PortForwardSession[]
  activeSessions: PortForwardSession[]
  errorSessions: PortForwardSession[]
  isLoading: boolean
  /** True when the session query itself failed (network/server error). */
  isQueryError: boolean
  queryError: Error | null
  isPanelOpen: boolean
  openPanel: () => void
  minimizePanel: () => void
  togglePanel: () => void
  /**
   * Permanently disarms the auto-minimize timer. Call from any user-initiated
   * interaction inside the panel. Re-arming only happens when a new forward
   * starts AND the panel is currently closed (minimized or never opened) —
   * an already-open panel stays sticky regardless of count changes.
   */
  commitInteraction: () => void
  onPanelHoverEnter: () => void
  onPanelHoverLeave: () => void
  /** Ref for the indicator button — used for dynamic panel positioning. */
  indicatorRef: React.RefObject<HTMLButtonElement | null>
  /** Measured anchor position from the indicator, or null if not yet measured. */
  anchor: PanelAnchor | null
}

const PortForwardContext = createContext<PortForwardContextValue | null>(null)

export function PortForwardProvider({ children }: { children: ReactNode }) {
  const {
    data: sessions = [],
    isLoading,
    isError: isQueryError,
    error: queryError,
  } = usePortForwardQuery()
  const activeSessions = sessions.filter((s) => s.status !== 'stopped')
  const errorSessions = sessions.filter((s) => s.status === 'error')
  const count = activeSessions.length

  const [isPanelOpen, setIsPanelOpen] = useState(false)
  // Mirror isPanelOpen in a ref so the count-watch effect can read the *current* open state
  // without needing isPanelOpen in its deps (which would re-run the effect on every open/close).
  const isPanelOpenRef = useRef(false)
  useEffect(() => { isPanelOpenRef.current = isPanelOpen }, [isPanelOpen])
  // Armed = a new session opened the panel and we still intend to auto-minimize.
  // Cleared by any user interaction (commitInteraction) or when the timer fires.
  const autoMinimizeArmedRef = useRef(false)
  const autoMinimizeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const isHoveringRef = useRef(false)
  const prevCountRef = useRef(0)

  // --- Indicator ref + panel anchor measurement ---
  // The panel is positioned dynamically below the indicator button.
  const indicatorRef = useRef<HTMLButtonElement>(null)
  const [anchor, setAnchor] = useState<PanelAnchor | null>(null)

  const measureAnchor = useCallback(() => {
    if (!indicatorRef.current) return
    const rect = indicatorRef.current.getBoundingClientRect()
    setAnchor({
      top: rect.bottom + 10,
      right: Math.max(16, window.innerWidth - rect.right),
      caretRight: rect.width / 2 - 6,
    })
  }, [])

  // Measure on mount / count change (indicator may appear/disappear) + window resize.
  useLayoutEffect(() => { measureAnchor() }, [count, measureAnchor])
  useEffect(() => {
    window.addEventListener('resize', measureAnchor)
    return () => window.removeEventListener('resize', measureAnchor)
  }, [measureAnchor])

  const clearAutoMinimizeTimer = useCallback(() => {
    if (autoMinimizeTimerRef.current) {
      clearTimeout(autoMinimizeTimerRef.current)
      autoMinimizeTimerRef.current = null
    }
  }, [])

  const scheduleAutoMinimize = useCallback(
    (delay: number) => {
      clearAutoMinimizeTimer()
      autoMinimizeTimerRef.current = setTimeout(() => {
        setIsPanelOpen(false)
        autoMinimizeArmedRef.current = false
        autoMinimizeTimerRef.current = null
      }, delay)
    },
    [clearAutoMinimizeTimer]
  )

  // Auto-open the panel when a new forward starts; fully close when all forwards stop.
  // Important: if the panel was already open (manually or from an earlier auto-open that the
  // user already committed to via interaction), don't re-arm the auto-minimize timer —
  // that would close a panel the user deliberately kept visible.
  useEffect(() => {
    const prev = prevCountRef.current
    prevCountRef.current = count
    if (count > prev && count > 0) {
      const wasClosed = !isPanelOpenRef.current
      setIsPanelOpen(true)
      if (wasClosed) {
        autoMinimizeArmedRef.current = true
        if (!isHoveringRef.current) {
          scheduleAutoMinimize(AUTO_MINIMIZE_INITIAL_MS)
        }
      }
      // If the panel was already open, leave armed state alone: a user who opened it
      // manually stays sticky; a user who's still within their initial grace window
      // keeps the existing timer.
    } else if (count === 0) {
      // Provider stays mounted across sessions — explicitly reset state so a future
      // forward starts with a fresh auto-open + auto-minimize cycle. (The indicator
      // and panel early-return when count===0 but they don't own this state.)
      setIsPanelOpen(false)
      autoMinimizeArmedRef.current = false
      clearAutoMinimizeTimer()
    }
  }, [count, scheduleAutoMinimize, clearAutoMinimizeTimer])

  // Cleanup any in-flight timer on unmount.
  useEffect(() => () => clearAutoMinimizeTimer(), [clearAutoMinimizeTimer])

  const openPanel = useCallback(() => {
    setIsPanelOpen(true)
    autoMinimizeArmedRef.current = false
    clearAutoMinimizeTimer()
  }, [clearAutoMinimizeTimer])

  const minimizePanel = useCallback(() => {
    setIsPanelOpen(false)
    autoMinimizeArmedRef.current = false
    clearAutoMinimizeTimer()
  }, [clearAutoMinimizeTimer])

  const togglePanel = useCallback(() => {
    if (isPanelOpen) minimizePanel()
    else openPanel()
  }, [isPanelOpen, openPanel, minimizePanel])

  const commitInteraction = useCallback(() => {
    autoMinimizeArmedRef.current = false
    clearAutoMinimizeTimer()
  }, [clearAutoMinimizeTimer])

  const onPanelHoverEnter = useCallback(() => {
    isHoveringRef.current = true
    clearAutoMinimizeTimer()
  }, [clearAutoMinimizeTimer])

  const onPanelHoverLeave = useCallback(() => {
    isHoveringRef.current = false
    if (autoMinimizeArmedRef.current) {
      scheduleAutoMinimize(AUTO_MINIMIZE_HOVER_LEAVE_MS)
    }
  }, [scheduleAutoMinimize])

  return (
    <PortForwardContext.Provider
      value={{
        sessions,
        activeSessions,
        errorSessions,
        isLoading,
        isQueryError,
        queryError: queryError as Error | null,
        isPanelOpen,
        openPanel,
        minimizePanel,
        togglePanel,
        commitInteraction,
        onPanelHoverEnter,
        onPanelHoverLeave,
        indicatorRef,
        anchor,
      }}
    >
      {children}
    </PortForwardContext.Provider>
  )
}

function usePortForwardContext(): PortForwardContextValue {
  const ctx = useContext(PortForwardContext)
  if (!ctx) {
    throw new Error('usePortForwardContext must be used inside <PortForwardProvider>')
  }
  return ctx
}

// --- Header indicator --------------------------------------------------------

export function PortForwardIndicator() {
  const { activeSessions, errorSessions, isPanelOpen, togglePanel, indicatorRef } = usePortForwardContext()
  const count = activeSessions.length
  if (count === 0) return null

  const hasErrors = errorSessions.length > 0
  const tooltipText = hasErrors
    ? `${count} port forward${count !== 1 ? 's' : ''} — ${errorSessions.length} failed`
    : `${count} active port forward${count !== 1 ? 's' : ''}`

  return (
    <Tooltip content={tooltipText} delay={150} position="bottom" disabled={isPanelOpen}>
      <button
        ref={indicatorRef}
        type="button"
        onClick={togglePanel}
        aria-label={tooltipText}
        aria-expanded={isPanelOpen}
        className={clsx(
          'relative flex items-center gap-1.5 h-7 px-2 ml-2 rounded-md text-xs transition-colors',
          isPanelOpen
            ? 'bg-theme-elevated text-theme-text-primary'
            : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
        )}
      >
        {/* The icon itself pulses when running (replaces the separate dot overlay).
            Slightly larger than standard pill icons for visual weight as a status indicator. */}
        <Plug className={clsx(
          'w-5 h-5',
          hasErrors ? 'text-red-400' : 'text-green-400',
          !isPanelOpen && !hasErrors && 'animate-pulse'
        )} />
        <span className="font-mono tabular-nums">{count}</span>
        {!isPanelOpen && hasErrors && (
          <span className={clsx('badge-sm', SEVERITY_BADGE.error)}>
            {errorSessions.length}
          </span>
        )}
      </button>
    </Tooltip>
  )
}

// --- Floating panel ----------------------------------------------------------

export function PortForwardPanel() {
  const {
    activeSessions,
    errorSessions,
    isLoading,
    isQueryError,
    queryError,
    isPanelOpen,
    minimizePanel,
    commitInteraction,
    onPanelHoverEnter,
    onPanelHoverLeave,
    anchor,
  } = usePortForwardContext()

  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [editingPortId, setEditingPortId] = useState<string | null>(null)
  const [editPortValue, setEditPortValue] = useState('')
  const [changingPortId, setChangingPortId] = useState<string | null>(null)
  const [togglingId, setTogglingId] = useState<string | null>(null)
  // Per-session stop tracking — allows stopping multiple forwards simultaneously
  // without disabling all stop buttons (the old shared-mutation approach blocked
  // every row when any single stop was in-flight).
  const [stoppingIds, setStoppingIds] = useState<Set<string>>(() => new Set())
  const queryClient = useQueryClient()
  const { showSuccess, showError } = useToast()

  // Track the "copied!" reset timeout so it can be cleared if the panel unmounts
  // before the 2s window elapses (otherwise React warns about setState on unmount).
  const copyResetTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    return () => {
      if (copyResetTimerRef.current) {
        clearTimeout(copyResetTimerRef.current)
      }
    }
  }, [])

  // Minimize on Escape when the panel is visible. Skip if focus is in an input —
  // the inline port editor has its own Escape handler (exits edit mode), and
  // upstream inputs (ResourcesSidebar search, etc.) should keep their own semantics.
  useEffect(() => {
    if (!isPanelOpen) return
    const handler = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      const active = document.activeElement
      if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) {
        return
      }
      minimizePanel()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [isPanelOpen, minimizePanel])

  const stopPortForward = useCallback(async (id: string) => {
    setStoppingIds(prev => new Set(prev).add(id))
    try {
      const res = await fetch(apiUrl(`/portforwards/${id}`), { method: 'DELETE' })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body.error || `Failed to stop port forward (HTTP ${res.status})`)
      }
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    } catch (err) {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      const msg = err instanceof Error ? err.message : 'Failed to stop port forward'
      showError('Failed to stop port forward', msg)
      console.error('Failed to stop port forward:', err)
    } finally {
      setStoppingIds(prev => {
        const next = new Set(prev)
        next.delete(id)
        return next
      })
    }
  }, [queryClient, showError])

  const toggleListenAddress = async (session: PortForwardSession) => {
    commitInteraction()
    const newAddress = session.listenAddress === '0.0.0.0' ? '127.0.0.1' : '0.0.0.0'
    const prevLabel = session.listenAddress === '0.0.0.0' ? 'network' : 'localhost'
    const nextLabel = newAddress === '0.0.0.0' ? 'network' : 'localhost'
    setTogglingId(session.id)
    // Track whether the DELETE half succeeded so we can tell "original still running"
    // apart from "original gone and recreate failed = data loss."
    let deleted = false
    try {
      const delRes = await fetch(apiUrl(`/portforwards/${session.id}`), { method: 'DELETE' })
      if (!delRes.ok) {
        const body = await delRes.json().catch(() => ({}))
        throw new Error(body.error || `Failed to stop existing port forward (HTTP ${delRes.status})`)
      }
      deleted = true
      const res = await fetch(apiUrl('/portforwards'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          namespace: session.namespace,
          podName: session.podName || undefined,
          serviceName: session.serviceName || undefined,
          podPort: session.podPort,
          localPort: session.localPort,
          listenAddress: newAddress,
        }),
      })
      if (!res.ok) {
        const error = await res.json().catch(() => ({}))
        throw new Error(error.error || `Failed to restart port forward (HTTP ${res.status})`)
      }
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    } catch (error) {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      const msg = error instanceof Error ? error.message : 'Failed to change network access'
      if (deleted) {
        // DELETE succeeded but POST failed — the original forward is gone.
        showError(
          'Port forward lost',
          `Forward on port ${session.localPort} (${prevLabel}) was stopped but recreating it as ${nextLabel} failed: ${msg}`
        )
      } else {
        // DELETE failed — the original forward is still running.
        showError('Failed to change network access', msg)
      }
      console.error('Failed to toggle listen address:', error)
    } finally {
      setTogglingId(null)
    }
  }

  const changeLocalPort = async (session: PortForwardSession, newPort: number) => {
    commitInteraction()
    if (newPort === session.localPort) {
      setEditingPortId(null)
      return
    }
    setChangingPortId(session.id)
    setEditingPortId(null)
    // Track whether the DELETE half succeeded so we can tell "original still running"
    // apart from "original gone and recreate failed = data loss."
    let deleted = false
    try {
      const delRes = await fetch(apiUrl(`/portforwards/${session.id}`), { method: 'DELETE' })
      if (!delRes.ok) {
        const body = await delRes.json().catch(() => ({}))
        throw new Error(body.error || `Failed to stop existing port forward (HTTP ${delRes.status})`)
      }
      deleted = true
      const res = await fetch(apiUrl('/portforwards'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          namespace: session.namespace,
          podName: session.podName || undefined,
          serviceName: session.serviceName || undefined,
          podPort: session.podPort,
          localPort: newPort,
          listenAddress: session.listenAddress,
        }),
      })
      if (!res.ok) {
        const error = await res.json().catch(() => ({}))
        throw new Error(error.error || `Failed to restart port forward (HTTP ${res.status})`)
      }
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      showSuccess('Port forward updated', `Now listening on localhost:${newPort}`)
    } catch (error) {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      const msg = error instanceof Error ? error.message : 'Failed to change local port'
      if (deleted) {
        showError(
          'Port forward lost',
          `Forward on port ${session.localPort} was stopped but port ${newPort} failed: ${msg}`
        )
      } else {
        // DELETE failed — the original forward on session.localPort is still running.
        showError('Failed to change local port', msg)
      }
      console.error('Failed to change local port:', error)
    } finally {
      setChangingPortId(null)
    }
  }

  const handleCopyUrl = useCallback(
    async (session: PortForwardSession) => {
      commitInteraction()
      try {
        await navigator.clipboard.writeText(`http://localhost:${session.localPort}`)
      } catch (err) {
        // Clipboard API can reject in non-secure contexts, denied permissions, or
        // when the document isn't focused. Surface the failure — the checkmark
        // would otherwise lie to the user.
        const msg = err instanceof Error ? err.message : 'Clipboard access denied'
        showError('Failed to copy URL', msg)
        console.error('Failed to copy URL:', err)
        return
      }
      setCopiedId(session.id)
      if (copyResetTimerRef.current) clearTimeout(copyResetTimerRef.current)
      copyResetTimerRef.current = setTimeout(() => {
        setCopiedId(null)
        copyResetTimerRef.current = null
      }, 2000)
    },
    [commitInteraction, showError]
  )

  const handleOpenUrl = useCallback(
    (session: PortForwardSession) => {
      commitInteraction()
      openExternal(`http://localhost:${session.localPort}`)
    },
    [commitInteraction]
  )

  // Unmount when there are no sessions AND the query isn't reporting a fault.
  // Distinguishing a failed query from "no sessions" keeps us from silently
  // telling the user their forwards vanished when really /api/portforwards errored.
  if (activeSessions.length === 0 && !isLoading && !isQueryError) {
    return null
  }

  const hasErrors = errorSessions.length > 0

  return (
    // Wrapper — positioning + opacity. Opacity fades fast (150ms), separately from the
    // height reveal (300ms) so users perceive the panel as "there" before it finishes growing.
    <div
      onMouseEnter={onPanelHoverEnter}
      onMouseLeave={onPanelHoverLeave}
      className={clsx(
        'fixed z-[51] w-80',
        'transition-opacity duration-150 ease-out',
        isPanelOpen
          ? 'opacity-100 pointer-events-auto'
          : 'opacity-0 pointer-events-none'
      )}
      style={{
        top: anchor?.top ?? 56,
        right: anchor?.right ?? 16,
      }}
      aria-hidden={!isPanelOpen}
    >
      {/* Panel shell — visual chrome (border, shadow, bg, corners). Height is driven
          by the grid-sizer child. As the grid grows 0→auto, the shell grows with it,
          keeping border and rounded corners correct at every intermediate height. */}
      <div className="overflow-hidden rounded-xl bg-theme-surface dark:bg-theme-elevated border-2 border-skyhook-500/35 dark:border-skyhook-400/40 shadow-2xl dark:shadow-[0_24px_60px_-12px_rgba(0,0,0,0.75),0_10px_24px_-6px_rgba(0,0,0,0.45)]">

        {/* Grid sizer — the height engine. grid-template-rows 0fr→1fr animates
            height from 0 to auto. Content clips from the bottom up, creating a
            natural top-to-bottom reveal (header appears first, sessions follow). */}
        <div
          className={clsx(
            'grid transition-[grid-template-rows] duration-300',
            isPanelOpen ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'
          )}
          style={{ transitionTimingFunction: 'cubic-bezier(0.16, 1, 0.3, 1)' }}
        >
          <div className="overflow-hidden">

      {/* Header — tinted green when all sessions running, red when any have failed. */}
      <div
        className={clsx(
          'flex items-center justify-between px-3 py-2 border-b transition-colors duration-200',
          hasErrors
            ? 'bg-red-500/10 dark:bg-red-500/15 border-red-500/25 dark:border-red-500/20'
            : 'bg-green-500/8 dark:bg-green-400/10 border-green-500/20 dark:border-green-400/15'
        )}
      >
        <div className="flex items-center gap-2">
          <Plug className="w-4 h-4 text-accent-text" />
          <span className="text-sm font-medium text-theme-text-primary">Port Forwards</span>
          <span className="badge-sm bg-theme-hover text-theme-text-secondary">
            {activeSessions.length}
          </span>
          {errorSessions.length > 0 && (
            <span className={clsx('badge-sm', SEVERITY_BADGE.error)}>
              {errorSessions.length} failed
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={minimizePanel}
          aria-label="Minimize port forwards"
          className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
        >
          <ChevronUp className="w-4 h-4" />
        </button>
      </div>

      {/* Sessions list */}
      <div className="max-h-64 overflow-y-auto">
        {isQueryError ? (
          <div className="p-3 text-xs bg-red-500/10 border-b border-theme-border">
            <div className={clsx('badge-sm mb-1 inline-block', SEVERITY_BADGE.error)}>
              Connection error
            </div>
            <div className="text-red-400 break-all">
              Failed to load port forwards: {queryError?.message ?? 'unknown error'}
            </div>
          </div>
        ) : null}
        {isLoading ? (
          <div className="flex items-center justify-center p-4">
            <Loader2 className="w-5 h-5 text-theme-text-tertiary animate-spin" />
          </div>
        ) : activeSessions.length === 0 ? (
          <div className="p-4 text-center text-sm text-theme-text-disabled">
            {isQueryError ? 'Unable to load port forwards' : 'No active port forwards'}
          </div>
        ) : (
          <div className="divide-y divide-theme-border">
            {activeSessions.map((session) => (
              <div
                key={session.id}
                className={clsx(
                  'p-3',
                  session.status === 'error' ? 'bg-red-500/10' : 'hover:bg-theme-elevated'
                )}
              >
                <div className="flex items-start justify-between gap-2">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span
                        className={clsx(
                          'w-2 h-2 rounded-full shrink-0',
                          session.status === 'running' ? 'bg-green-500' : 'bg-red-500'
                        )}
                      />
                      <span className="text-sm text-theme-text-primary font-medium truncate">
                        {session.serviceName || session.podName}
                      </span>
                      {session.status === 'error' && (
                        <span className={clsx('badge-sm', SEVERITY_BADGE.error)}>Failed</span>
                      )}
                    </div>
                    <div className="mt-1 text-xs text-theme-text-disabled">
                      {session.namespace} · Port {session.podPort}
                    </div>
                    {session.status === 'error' && session.error && (
                      <div className="mt-1.5 text-xs text-red-400 bg-red-500/10 px-2 py-1 rounded">
                        {session.error}
                      </div>
                    )}
                    {session.status === 'running' && (
                      <div className="mt-1.5 flex items-center gap-2">
                        {editingPortId === session.id ? (
                          <div className="flex items-center text-xs bg-theme-base rounded text-accent-text font-mono">
                            <span className="pl-2 py-1 text-theme-text-disabled select-none">
                              {session.listenAddress === '0.0.0.0' ? '0.0.0.0' : 'localhost'}:
                            </span>
                            <input
                              type="number"
                              autoFocus
                              min={1}
                              max={65535}
                              value={editPortValue}
                              onChange={(e) => {
                                // Any keystroke is a deliberate user action — keep the panel open.
                                commitInteraction()
                                setEditPortValue(e.target.value)
                              }}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter') {
                                  const val = Number(editPortValue)
                                  if (
                                    isNaN(val) ||
                                    val < 1 ||
                                    val > 65535 ||
                                    !Number.isInteger(val)
                                  ) {
                                    commitInteraction()
                                    showError(
                                      'Invalid port',
                                      'Port must be a number between 1 and 65535'
                                    )
                                    return
                                  }
                                  changeLocalPort(session, val)
                                } else if (e.key === 'Escape') {
                                  commitInteraction()
                                  setEditingPortId(null)
                                }
                              }}
                              onBlur={() => setEditingPortId(null)}
                              className="w-16 bg-transparent border-none pr-2 py-1 text-accent-text font-mono text-xs outline-none [appearance:textfield] [&::-webkit-outer-spin-button]:appearance-none [&::-webkit-inner-spin-button]:appearance-none"
                            />
                          </div>
                        ) : (
                          <Tooltip content="Click to change local port" delay={300} position="bottom" disabled={!isPanelOpen}>
                          <code
                            className={clsx(
                              'group/port text-xs bg-theme-base px-2 py-1 rounded text-accent-text transition-all inline-flex items-center gap-1',
                              changingPortId === session.id
                                ? 'opacity-50'
                                : 'cursor-pointer hover:ring-1 hover:ring-blue-500/50'
                            )}
                            onClick={() => {
                              if (changingPortId || togglingId) return
                              commitInteraction()
                              setEditingPortId(session.id)
                              setEditPortValue(String(session.localPort))
                            }}
                          >
                            {changingPortId === session.id && (
                              <Loader2 className="w-3 h-3 animate-spin inline mr-1" />
                            )}
                            {session.listenAddress === '0.0.0.0' ? '0.0.0.0' : 'localhost'}:
                            {session.localPort}
                            <PenLine className="w-3 h-3 text-theme-text-disabled opacity-0 group-hover/port:opacity-100 transition-opacity" />
                          </code>
                          </Tooltip>
                        )}
                        <Tooltip
                          content={session.listenAddress === '0.0.0.0' ? 'Switch to localhost only' : 'Allow access from other machines'}
                          delay={300} position="bottom" disabled={!isPanelOpen}
                        >
                        <button
                          onClick={() => toggleListenAddress(session)}
                          disabled={togglingId === session.id || changingPortId === session.id}
                          className={clsx(
                            'flex items-center gap-1 px-1.5 py-0.5 text-xs rounded transition-colors',
                            session.listenAddress === '0.0.0.0'
                              ? `${SEVERITY_BADGE.warning} hover:bg-amber-500/30`
                              : 'bg-theme-elevated text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary'
                          )}
                        >
                          {togglingId === session.id ? (
                            <Loader2 className="w-3 h-3 animate-spin" />
                          ) : session.listenAddress === '0.0.0.0' ? (
                            <Globe className="w-3 h-3" />
                          ) : (
                            <Monitor className="w-3 h-3" />
                          )}
                          {session.listenAddress === '0.0.0.0' ? 'network' : 'local'}
                        </button>
                        </Tooltip>
                      </div>
                    )}
                  </div>

                  <div className="flex items-center gap-1 shrink-0">
                    {session.status === 'running' && (
                      <>
                        <Tooltip content={copiedId === session.id ? 'Copied!' : 'Copy URL'} delay={300} position="bottom" disabled={!isPanelOpen}>
                        <button
                          onClick={() => handleCopyUrl(session)}
                          className="p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
                        >
                          {copiedId === session.id ? (
                            <Check className="w-3.5 h-3.5 text-green-400" />
                          ) : (
                            <Copy className="w-3.5 h-3.5" />
                          )}
                        </button>
                        </Tooltip>
                        <Tooltip content="Open in browser" delay={300} position="bottom" disabled={!isPanelOpen}>
                        <button
                          onClick={() => handleOpenUrl(session)}
                          className="p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
                        >
                          <ExternalLink className="w-3.5 h-3.5" />
                        </button>
                        </Tooltip>
                      </>
                    )}
                    <Tooltip content={session.status === 'error' ? 'Dismiss' : 'Stop'} delay={300} position="bottom" disabled={!isPanelOpen}>
                    <button
                      onClick={() => {
                        commitInteraction()
                        stopPortForward(session.id)
                      }}
                      disabled={stoppingIds.has(session.id)}
                      className="p-1.5 text-theme-text-tertiary hover:text-red-400 hover:bg-theme-hover rounded disabled:opacity-50"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                    </Tooltip>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

          </div>{/* /overflow-hidden */}
        </div>{/* /grid-sizer */}
      </div>{/* /panel-shell */}

      {/* Caret — rendered after the shell so it paints on top (z-10). Opaque fill
          covers the shell's border at the junction. The tint layer matches the header. */}
      <div
        className="absolute -top-[6px] w-3.5 h-3.5 rotate-45 z-10 bg-theme-surface dark:bg-theme-elevated border-t-2 border-l-2 border-skyhook-500/35 dark:border-skyhook-400/40"
        style={{ right: anchor?.caretRight ?? 16 }}
      >
        <div className={clsx(
          'absolute inset-0 transition-colors duration-200',
          hasErrors ? 'bg-red-500/10 dark:bg-red-500/15' : 'bg-green-500/8 dark:bg-green-400/10'
        )} />
      </div>
    </div>
  )
}

// --- Public mutation hook for starting a forward -----------------------------
// Stable hook shape — callers don't need to know about the panel UI. The provider's
// count-watch effect reacts to the new session and handles open/auto-minimize.

export function useStartPortForward() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (req: {
      namespace: string
      podName?: string
      serviceName?: string
      podPort: number
      localPort?: number
      listenAddress?: string // "127.0.0.1" (default) or "0.0.0.0"
    }) => {
      const res = await fetch(apiUrl('/portforwards'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      })
      if (!res.ok) {
        const error = await res.json().catch(() => ({}))
        throw new Error(error.error || 'Failed to start port forward')
      }
      return res.json() as Promise<PortForwardSession>
    },
    meta: {
      errorMessage: 'Failed to start port forward',
      // No successMessage — the panel auto-opens on new sessions and provides
      // strictly more information than a toast ("started" → here are the details).
      // Only the error toast remains as the signal-of-last-resort when the
      // mutation fails and no panel update can happen.
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    },
  })
}

// Backwards-compat: existing consumers that just want a count number.
export function usePortForwardCount() {
  const { data: sessions = [] } = usePortForwardQuery()
  return sessions.filter((s) => s.status !== 'stopped').length
}
