import { useEffect, useState } from 'react'
import { clsx } from 'clsx'
import { RefreshCw, Check } from 'lucide-react'
import { Tooltip } from './Tooltip'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { formatUpdatedAgo, formatCadence, msToNextBucket } from '../../utils/format'

export type FreshnessMode = 'polled' | 'live'
export type FreshnessConnection = 'connected' | 'disconnected' | 'connecting'

interface FreshnessControlProps {
  // 'polled' — the view auto-refetches on a timer (show cadence + age + refresh).
  // 'live'   — the view updates from the SSE stream as the cluster changes.
  mode: FreshnessMode
  // Epoch ms of the last successful load (React Query dataUpdatedAt). Polled only.
  dataUpdatedAt?: number
  // The view's representative refetch interval, for the "Auto-refreshes every X" copy.
  cadenceMs?: number
  // True while a fetch is in flight — spins the refresh icon for background refetches too.
  isFetching?: boolean
  // Manual refresh. Omit to render the signal without a button. May return a
  // promise — the refresh animation waits for it before showing success.
  onRefresh?: () => void | Promise<unknown>
  // Cluster/SSE connection health. When disconnected, freshness must not claim
  // currency — both modes degrade to "Reconnecting…" instead of a stale age/Live.
  connectionState?: FreshnessConnection
  // Live views only: the stream is paused (e.g. topology pause toggle). Showing
  // "Auto-updating" while paused would be a lie, so it degrades to "Paused".
  paused?: boolean
  className?: string
}

// The canonical freshness/liveness signal. Behavior-first: it answers "does this
// stay current on its own, and how?" rather than over-claiming a precise data age
// (dataUpdatedAt is browser-fetch time, not cluster-state age). Place it at the
// right end of a view's existing header/toolbar — never in a new band.
export function FreshnessControl({
  mode,
  dataUpdatedAt,
  cadenceMs,
  isFetching,
  onRefresh,
  connectionState = 'connected',
  paused = false,
  className,
}: FreshnessControlProps) {
  const [, force] = useState(0)
  const showAge = mode === 'polled' && typeof dataUpdatedAt === 'number' && dataUpdatedAt > 0

  // Re-render exactly when the displayed age bucket flips (not every second).
  useEffect(() => {
    if (!showAge) return
    let id: ReturnType<typeof setTimeout>
    function schedule() {
      const delay = Math.max(1000, msToNextBucket(Date.now() - (dataUpdatedAt as number)))
      id = setTimeout(() => {
        force((t) => t + 1)
        schedule()
      }, delay)
    }
    schedule()
    return () => clearTimeout(id)
  }, [showAge, dataUpdatedAt])

  const [refresh, , phase] = useRefreshAnimation(() => onRefresh?.())
  const spinning = phase === 'spinning' || !!isFetching

  // Any non-connected state (disconnected OR mid-reconnect) must not claim
  // currency — the signal degrades rather than showing a stale age/Live.
  const degraded = connectionState !== 'connected'

  let label: string | null
  let tooltip: string
  if (degraded) {
    label = 'Reconnecting…'
    tooltip = 'Not connected to the cluster — data may be stale until the connection is restored.'
  } else if (mode === 'live' && paused) {
    label = 'Paused'
    tooltip = 'Live updates are paused — resume to keep this view current.'
  } else if (mode === 'live') {
    label = 'Auto-updating'
    tooltip = 'Updates in real time as the cluster changes.'
  } else {
    const age = showAge ? formatUpdatedAgo(Date.now() - (dataUpdatedAt as number)) : null
    if (cadenceMs) {
      // Behavior-first: lead with the cadence (true of the whole view's poll),
      // not a precise age (which is only one query's browser-fetch time).
      label = `Auto-refreshes every ${formatCadence(cadenceMs)}`
      tooltip = age ? `${label} · updated ${age}` : label
    } else if (age) {
      // No cadence but a real load time (e.g. a manual-refresh snapshot).
      label = `Updated ${age}`
      tooltip = label
    } else {
      // Nothing to say (a consumer that only wires manual refresh, or before
      // the first load) — render just the button, no misleading text.
      label = null
      tooltip = 'Refresh'
    }
  }

  // The secondary "· updated N ago" only rides alongside the cadence label.
  const ageSuffix =
    !degraded && mode === 'polled' && cadenceMs && showAge
      ? formatUpdatedAgo(Date.now() - (dataUpdatedAt as number))
      : null

  return (
    <div className={clsx('flex items-center gap-1.5 whitespace-nowrap', className)}>
      {label && (
        <Tooltip content={tooltip} delay={100} position="bottom">
          <span className="flex items-center gap-1 text-xs text-theme-text-tertiary">
            {mode === 'live' && !degraded && (
              <span
                className={clsx(
                  'w-1.5 h-1.5 rounded-full',
                  paused ? 'bg-amber-400' : 'bg-green-500 animate-pulse',
                )}
                aria-hidden
              />
            )}
            <span className="tabular-nums">{label}</span>
            {/* Age is secondary — it drops first on narrow toolbars; the tooltip keeps it. */}
            {ageSuffix && <span className="hidden lg:inline tabular-nums">· updated {ageSuffix}</span>}
          </span>
        </Tooltip>
      )}
      {onRefresh && (
        <Tooltip content="Refresh now" delay={100} position="bottom">
          <button
            type="button"
            onClick={refresh}
            disabled={spinning}
            aria-label="Refresh now"
            className="p-1.5 rounded-lg text-theme-text-tertiary hover:text-theme-text-secondary hover:bg-theme-hover transition-colors disabled:opacity-50"
          >
            {phase === 'success' ? (
              <Check className="w-3.5 h-3.5 text-emerald-500" />
            ) : (
              <RefreshCw className={clsx('w-3.5 h-3.5', spinning && 'animate-spin')} />
            )}
          </button>
        </Tooltip>
      )}
    </div>
  )
}
