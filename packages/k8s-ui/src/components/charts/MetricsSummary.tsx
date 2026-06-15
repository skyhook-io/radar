import { useMemo } from 'react'
import { clsx } from 'clsx'
import { formatMetricValue } from './format'
import type { TimeSeries } from './types'

export function MetricsSummary({ series, unit, currentColorClass }: {
  series: TimeSeries[]
  unit: string
  /** Tailwind text class for the "Current" pill — caller's accent color. */
  currentColorClass?: string
}) {
  const stats = useMemo(() => {
    const allValues: number[] = []
    for (const s of series) {
      for (const dp of s.dataPoints) {
        if (dp.value == null) continue
        allValues.push(dp.value)
      }
    }
    if (allValues.length === 0) return null

    // Current = sum of each series' most recent finite data point (matches
    // operator mental model of "total across pods right now"). A trailing gap
    // is skipped — we walk back to the last real sample rather than treating
    // the gap as 0, which would understate the current total.
    const lastValues = series.map(s => {
      for (let i = s.dataPoints.length - 1; i >= 0; i--) {
        const v = s.dataPoints[i].value
        if (v != null) return v
      }
      return 0
    })
    const current = lastValues.reduce((a, b) => a + b, 0)
    const max = Math.max(...allValues)
    const avg = allValues.reduce((a, b) => a + b, 0) / allValues.length

    return { current, max, avg }
  }, [series])

  if (!stats) return null

  return (
    <div className="flex items-center gap-6">
      <StatPill label="Current" value={formatMetricValue(stats.current, unit)} className={currentColorClass} />
      <StatPill label="Average" value={formatMetricValue(stats.avg, unit)} className="text-theme-text-secondary" />
      <StatPill label="Peak" value={formatMetricValue(stats.max, unit)} className="text-theme-text-secondary" />
    </div>
  )
}

function StatPill({ label, value, className }: { label: string; value: string; className?: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="text-xs text-theme-text-quaternary uppercase tracking-wide">{label}</span>
      <span className={clsx('text-sm font-semibold tabular-nums', className)}>{value}</span>
    </div>
  )
}
