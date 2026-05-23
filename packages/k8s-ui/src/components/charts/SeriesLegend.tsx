import { seriesColor } from './colors'
import type { TimeSeries } from './types'

// Caps visible entries to match AreaChart's SERIES_COLORS length; extras
// collapse to "+N more".
export function SeriesLegend({ series, color }: { series: TimeSeries[]; color: string }) {
  const labels = series.map((s, i) => s.labels.pod || s.labels.instance || `series-${i}`)
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 px-1">
      {series.slice(0, 10).map((_, i) => {
        const shortName = labels[i].length > 40 ? '...' + labels[i].slice(-37) : labels[i]
        return (
          <div key={i} className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
            <div
              className="w-2.5 h-2.5 rounded-full shrink-0"
              style={{ backgroundColor: seriesColor(i, color) }}
            />
            <span className="truncate" title={labels[i]}>{shortName}</span>
          </div>
        )
      })}
      {series.length > 10 && (
        <span className="text-xs text-theme-text-quaternary">+{series.length - 10} more</span>
      )}
    </div>
  )
}
