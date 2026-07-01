import { useEffect, useState } from 'react'
import { clsx } from 'clsx'
import { RefreshCw, Check } from 'lucide-react'
import { Tooltip } from './Tooltip'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { formatUpdatedAgo, msToNextBucket } from '../../utils/format'

export type FreshnessMode = 'auto' | 'snapshot'
export type FreshnessConnection = 'connected' | 'disconnected' | 'connecting'

interface FreshnessControlProps {
  // 'auto'     — the view keeps itself current (polls or streams). Reads
  //              "Auto-updating". Pass onRefresh on slow polled views for a
  //              "check now" hatch; omit it on instant stream-backed views.
  // 'snapshot' — a one-shot load that only changes on demand. Reads "Updated N
  //              ago" with a manual refresh button.
  mode: FreshnessMode
  // Epoch ms of the last successful load (React Query dataUpdatedAt). Optional
  // for 'auto' streams that have no per-fetch timestamp (e.g. topology).
  dataUpdatedAt?: number
  // Spins the refresh icon during background refetches (for views with a button).
  isFetching?: boolean
  // Manual refresh. Provide it on polled + snapshot views where forcing a fetch
  // is useful; omit on stream-backed 'auto' views (instant, so a refresh button
  // would just undercut "Auto-updating"). May return a promise — the animation
  // waits for it before showing success.
  onRefresh?: () => void | Promise<unknown>
  // Cluster/SSE connection health. When not connected, freshness must not claim
  // currency — it degrades to "Reconnecting…" instead of a stale age/Auto-updating.
  connectionState?: FreshnessConnection
  // 'auto' streams only: paused (e.g. topology pause toggle). "Auto-updating"
  // while paused would be a lie, so it degrades to "Paused".
  paused?: boolean
  className?: string
}

// The canonical freshness/liveness signal. It answers "does this view stay
// current on its own?" — "Auto-updating" for anything self-refreshing (poll or
// stream, mechanism deliberately not exposed), "Updated N ago" + refresh for a
// manual snapshot. Place it at the right end of a view's header — never a band.
export function FreshnessControl({
  mode,
  dataUpdatedAt,
  isFetching,
  onRefresh,
  connectionState = 'connected',
  paused = false,
  className,
}: FreshnessControlProps) {
  const [, force] = useState(0)
  const showAge = typeof dataUpdatedAt === 'number' && dataUpdatedAt > 0

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
  // currency — the signal degrades rather than showing a stale age/Auto-updating.
  const degraded = connectionState !== 'connected'

  // Show the refresh button whenever the host wired one. Stream-backed 'auto'
  // views (Resources, Topology) deliberately omit onRefresh — they update
  // instantly, so a manual refresh would only undercut "Auto-updating". Slow
  // polled views keep it as a "check now" escape hatch.
  const showRefresh = !!onRefresh

  const exact = showAge ? `Last updated ${new Date(dataUpdatedAt as number).toLocaleTimeString()}` : null
  const age = showAge ? formatUpdatedAgo(Date.now() - (dataUpdatedAt as number)) : null

  // Tooltip is only rendered when it ADDS information beyond the visible label.
  let label: string | null
  let tooltip: string | null
  let live = false
  if (degraded) {
    label = 'Reconnecting…'
    tooltip = 'Not connected to the cluster — data may be stale until the connection is restored.'
  } else if (mode === 'auto' && paused) {
    label = 'Paused'
    tooltip = 'Live updates are paused — resume to keep this view current.'
  } else if (mode === 'auto') {
    label = 'Auto-updating'
    tooltip = exact ?? 'Updates automatically as the data changes.'
    live = true
  } else if (age) {
    label = `Updated ${age}`
    tooltip = exact
  } else {
    // Snapshot before its first load — render just the button, no stale text.
    label = null
    tooltip = null
  }

  // The relative age is the dynamic trust detail. It rides alongside every 'auto'
  // label, and alongside a degraded label in either mode — "Reconnecting… ·
  // updated 8m ago" is exactly when staleness matters most. (In connected
  // 'snapshot' mode the age IS the label, so no suffix.)
  const ageSuffix = age && (mode === 'auto' || degraded) ? age : null

  const labelNode = label ? (
    <span className="flex items-center gap-1 text-xs text-theme-text-tertiary">
      {(live || (mode === 'auto' && paused)) && !degraded && (
        <span
          className={clsx('w-1.5 h-1.5 rounded-full', paused ? 'bg-amber-400' : 'bg-green-500 animate-pulse')}
          aria-hidden
        />
      )}
      <span className="tabular-nums">{label}</span>
      {ageSuffix && <span className="tabular-nums text-theme-text-quaternary">· updated {ageSuffix}</span>}
    </span>
  ) : null

  return (
    <div className={clsx('flex items-center gap-1.5 whitespace-nowrap', className)}>
      {/* Only wrap in a tooltip when it adds information beyond the label. */}
      {labelNode && (tooltip
        ? <Tooltip content={tooltip} delay={100} position="bottom">{labelNode}</Tooltip>
        : labelNode)}
      {showRefresh && (
        <Tooltip content="Refresh now" delay={100} position="bottom">
          <button
            type="button"
            onClick={refresh}
            // Only disable during the button's own refresh animation (prevents
            // double-trigger); stay clickable during background refetches.
            disabled={phase === 'spinning'}
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
