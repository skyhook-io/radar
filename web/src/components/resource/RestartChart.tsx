import { useMemo } from 'react'
import { AlertCircle } from 'lucide-react'
import { usePrometheusResourceMetrics, usePrometheusStatus, type PrometheusSeries } from '../../api/client'

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
  range?: '1h' | '6h' | '24h' | '7d'
}) {
  const { data: status } = usePrometheusStatus()
  const isConnected = status?.connected === true
  const { data: metrics, isLoading } = usePrometheusResourceMetrics(kind, namespace, name, 'restarts', range, isConnected)

  const restarts = useMemo(() => collectRestartEvents(metrics?.result?.series), [metrics])

  if (!isConnected || isLoading) return null
  if (restarts.length === 0) return null

  const minTs = Math.min(...restarts.map(r => r.timestamp))
  const maxTs = Math.max(...restarts.map(r => r.timestamp))
  // Avoid divide-by-zero when there's a single event.
  const span = Math.max(maxTs - minTs, 60)

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
          const left = `${((r.timestamp - minTs) / span) * 100}%`
          return (
            <div
              key={i}
              className="absolute top-0 h-full w-px bg-amber-500/80"
              style={{ left }}
              title={`${new Date(r.timestamp * 1000).toLocaleString()} · ${r.label}${r.value > 1 ? ` ×${r.value}` : ''}`}
            >
              <div className="absolute -top-0.5 left-1/2 -translate-x-1/2 w-1.5 h-1.5 rounded-full bg-amber-500" />
            </div>
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
    // A nonzero `changes()` value at timestamp T means restarts happened in the
    // preceding window. Use the timestamp as the event marker position.
    for (const dp of s.dataPoints) {
      if (dp.value > 0) {
        events.push({ timestamp: dp.timestamp, value: dp.value, label: pod })
      }
    }
  }
  events.sort((a, b) => a.timestamp - b.timestamp)
  return events
}
