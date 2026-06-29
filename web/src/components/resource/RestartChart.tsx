import { useEffect, useMemo } from 'react'
import { AlertCircle } from 'lucide-react'
import { usePrometheusResourceMetrics, usePrometheusStatus, type PrometheusSeries, type PrometheusTimeRange } from '../../api/client'
import { Tooltip } from '../ui/Tooltip'

/**
 * RestartEventLane — vertical markers at each restart event, on a dedicated
 * row below the chart. Markers stay readable when they cluster because they
 * don't overlay the chart waveform. KSM-gated (uses kube_pod_container_status_restarts_total)
 * — silently hidden when Prom isn't connected or the series doesn't exist.
 */
export function RestartEventLane({ kind, namespace, name, range = '1h' }: {
  kind: string
  namespace: string
  name: string
  range?: PrometheusTimeRange
}) {
  const { data: status } = usePrometheusStatus()
  const isConnected = status?.connected === true
  const { data: metrics, isLoading, error } = usePrometheusResourceMetrics(kind, namespace, name, 'restarts', range, isConnected)

  const restarts = useMemo(() => collectRestartEvents(metrics?.result?.series), [metrics])

  // A real Prom-side failure shouldn't look identical to "no restarts" — log
  // it so an operator investigating a missing lane has a breadcrumb. The lane
  // still hides because we don't want a permanent red banner on every pod.
  // Effect-gated so we log once per error change, not on every re-render.
  useEffect(() => {
    if (error) {
      console.warn('[RestartEventLane] restart query failed', error)
    }
  }, [error])

  if (!isConnected || isLoading) return null
  if (restarts.length === 0) return null

  // Position markers within the chart's full time window — not the
  // min/max of detected events — so a cluster of restarts at the start
  // of the window doesn't visually spread across the whole lane.
  const nowSec = Date.now() / 1000
  const windowStart = nowSec - rangeToSeconds(range)
  const span = nowSec - windowStart

  return (
    <div className="rounded-md border border-amber-500/20 bg-amber-500/[0.04] px-3 py-2">
      <div className="flex items-center gap-2 mb-1.5">
        <AlertCircle className="w-3.5 h-3.5 text-amber-500/70" />
        <span className="text-xs font-medium text-theme-text-secondary">
          Restarts in last {range}
        </span>
        <span className="text-xs text-theme-text-quaternary tabular-nums">
          {restarts.reduce((n, r) => n + r.value, 0)} total
        </span>
      </div>
      <div className="relative h-5">
        {/* Baseline */}
        <div className="absolute inset-x-0 top-1/2 h-px bg-theme-border/40" />
        {/* Markers */}
        {restarts.map((r, i) => {
          const left = `${Math.max(0, Math.min(100, ((r.timestamp - windowStart) / span) * 100))}%`
          return (
            <Tooltip
              key={i}
              content={`${new Date(r.timestamp * 1000).toLocaleString()} · ${r.label}${r.value > 1 ? ` ×${r.value}` : ''}`}
              wrapperClassName="absolute top-0 h-full w-px bg-amber-500/80"
              wrapperStyle={{ left }}
            >
              <div className="absolute -top-0.5 left-1/2 -translate-x-1/2 w-1.5 h-1.5 rounded-full bg-amber-500" />
            </Tooltip>
          )
        })}
      </div>
    </div>
  )
}

// ============================================================================
// Internals
// ============================================================================

interface RestartEvent {
  timestamp: number
  value: number
  label: string
}

function collectRestartEvents(series: PrometheusSeries[] | undefined): RestartEvent[] {
  if (!series) return []
  const events: RestartEvent[] = []
  for (const s of series) {
    const pod = s.labels.pod ?? 'pod'
    // `changes(...[1h])` produces a rolling count, so a single restart shows
    // value=1 for ~60 consecutive samples (the whole 1h window). Emit a marker
    // only when the count increases — that's when a *new* restart entered the
    // window — and use the increase as the marker's restart count.
    let prev: number | null = null
    for (const dp of s.dataPoints) {
      // Skip gap samples and drop the previous reference, so the next finite
      // sample isn't diffed across the gap — a delta across a gap is
      // meaningless and could fabricate a restart count. We compare only
      // consecutive finite samples.
      if (dp.value == null) {
        prev = null
        continue
      }
      if (prev !== null) {
        // Only count positive deltas — restarts that entered the rolling 1h
        // window during the chart range. The first sample's value covers
        // [start-1h, start] which is outside the user's chosen window; counting
        // it would inflate the total with pre-window restarts.
        const delta = dp.value - prev
        if (delta > 0) {
          events.push({ timestamp: dp.timestamp, value: delta, label: pod })
        }
      }
      prev = dp.value
    }
  }
  events.sort((a, b) => a.timestamp - b.timestamp)
  return events
}

function rangeToSeconds(range: PrometheusTimeRange): number {
  const match = range.match(/^(\d+)([mhd])$/)
  if (!match) return 3600 // default to 1h if unrecognized
  const n = parseInt(match[1], 10)
  switch (match[2]) {
    case 'm': return n * 60
    case 'h': return n * 3600
    case 'd': return n * 86400
    default: return 3600
  }
}
