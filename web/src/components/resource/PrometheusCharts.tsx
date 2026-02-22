import { useState, useMemo, useRef, useCallback } from 'react'
import { clsx } from 'clsx'
import { BarChart3, Wifi, WifiOff, Loader2 } from 'lucide-react'
import {
  usePrometheusStatus,
  usePrometheusConnect,
  usePrometheusResourceMetrics,
  type PrometheusMetricCategory,
  type PrometheusTimeRange,
  type PrometheusSeries,
} from '../../api/client'

// ============================================================================
// Types & Constants
// ============================================================================

const SUPPORTED_KINDS = new Set([
  'Pod', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Node',
])

interface CategoryDef {
  key: PrometheusMetricCategory
  label: string
  color: string       // tailwind text class
  chartColor: string  // hex for SVG
  fillColor: string   // hex with alpha for SVG fill
}

const WORKLOAD_CATEGORIES: CategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'network_rx', label: 'Net RX', color: 'text-emerald-400', chartColor: '#34d399', fillColor: '#34d39922' },
  { key: 'network_tx', label: 'Net TX', color: 'text-orange-400', chartColor: '#fb923c', fillColor: '#fb923c22' },
  { key: 'filesystem', label: 'Disk I/O', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

const NODE_CATEGORIES: CategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'filesystem', label: 'Disk', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

// Distinct colors for multi-series charts (up to 10 series).
// Uses 500-level shades for adequate contrast on both dark (#1e293b) and light (#ffffff) surfaces.
const SERIES_COLORS = [
  '#3b82f6', // blue-500
  '#10b981', // emerald-500
  '#f97316', // orange-500
  '#a855f7', // purple-500
  '#ec4899', // pink-500
  '#eab308', // yellow-500
  '#06b6d4', // cyan-500
  '#84cc16', // lime-500
  '#ef4444', // red-500
  '#6366f1', // indigo-500
]

const TIME_RANGES: { value: PrometheusTimeRange; label: string }[] = [
  { value: '10m', label: '10m' },
  { value: '30m', label: '30m' },
  { value: '1h', label: '1h' },
  { value: '3h', label: '3h' },
  { value: '6h', label: '6h' },
  { value: '12h', label: '12h' },
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
]

// ============================================================================
// Main Component
// ============================================================================

interface PrometheusChartsProps {
  kind: string
  namespace: string
  name: string
}

export function PrometheusCharts({ kind, namespace, name }: PrometheusChartsProps) {
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const connectMutation = usePrometheusConnect()

  const categories = kind === 'Node' ? NODE_CATEGORIES : WORKLOAD_CATEGORIES
  const [activeCategory, setActiveCategory] = useState<PrometheusMetricCategory>('cpu')
  const [timeRange, setTimeRange] = useState<PrometheusTimeRange>('1h')

  const isConnected = status?.connected === true
  const isSupported = SUPPORTED_KINDS.has(kind)

  // Fetch metrics when connected
  const { data: metrics, isLoading: metricsLoading, error: metricsError } = usePrometheusResourceMetrics(
    kind, namespace, name, activeCategory, timeRange,
    isConnected && isSupported,
  )

  if (!isSupported) {
    return null
  }

  // Loading state — checking Prometheus availability
  if (statusLoading) {
    return (
      <div className="flex items-center justify-center py-12 text-theme-text-tertiary">
        <Loader2 className="w-5 h-5 animate-spin mr-2" />
        Checking Prometheus availability...
      </div>
    )
  }

  if (!isConnected) {
    return (
      <div className="flex flex-col items-center justify-center py-12 gap-4">
        <WifiOff className="w-10 h-10 text-theme-text-quaternary" />
        <div className="text-center">
          <p className="text-sm text-theme-text-secondary mb-1">Prometheus not connected</p>
          <p className="text-xs text-theme-text-tertiary mb-4">
            {status?.error || 'Connect to view historical CPU, memory, and network metrics'}
          </p>
          <button
            onClick={() => connectMutation.mutate()}
            disabled={connectMutation.isPending}
            className="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium rounded-lg bg-blue-600 hover:bg-blue-500 text-white transition-colors disabled:opacity-50"
          >
            {connectMutation.isPending ? (
              <Loader2 className="w-4 h-4 animate-spin" />
            ) : (
              <Wifi className="w-4 h-4" />
            )}
            Discover Prometheus
          </button>
        </div>
      </div>
    )
  }

  const activeCategoryDef = categories.find(c => c.key === activeCategory) || categories[0]

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="shrink-0 flex items-center justify-between px-4 py-2.5 border-b border-theme-border bg-theme-surface/50">
        {/* Category tabs */}
        <div className="flex items-center gap-1">
          <BarChart3 className="w-4 h-4 text-theme-text-tertiary mr-2" />
          {categories.map(cat => (
            <button
              key={cat.key}
              onClick={() => setActiveCategory(cat.key)}
              className={clsx(
                'px-2.5 py-1 text-xs font-medium rounded-md transition-colors',
                activeCategory === cat.key
                  ? 'bg-theme-elevated text-theme-text-primary shadow-sm'
                  : 'text-theme-text-tertiary hover:text-theme-text-secondary hover:bg-theme-elevated/50'
              )}
            >
              {cat.label}
            </button>
          ))}
        </div>

        {/* Time range selector */}
        <div className="flex items-center gap-1">
          {TIME_RANGES.map(tr => (
            <button
              key={tr.value}
              onClick={() => setTimeRange(tr.value)}
              className={clsx(
                'px-2 py-1 text-xs rounded-md transition-colors',
                timeRange === tr.value
                  ? 'bg-blue-600/20 text-blue-400 font-medium'
                  : 'text-theme-text-quaternary hover:text-theme-text-tertiary'
              )}
            >
              {tr.label}
            </button>
          ))}
        </div>
      </div>

      {/* Chart area — fixed min-height prevents layout shift while loading */}
      <div className="min-h-[280px] p-4">
        {metricsLoading ? (
          <div className="flex items-center justify-center min-h-[240px] text-theme-text-tertiary">
            <Loader2 className="w-5 h-5 animate-spin mr-2" />
            Loading metrics...
          </div>
        ) : metricsError ? (
          <div className="flex items-center justify-center h-full text-red-400 text-sm">
            Failed to load metrics: {(metricsError as Error).message}
          </div>
        ) : metrics?.result?.series?.length ? (
          <div className="h-full flex flex-col gap-4">
            {/* Summary stats */}
            <MetricsSummary
              series={metrics.result.series}
              category={activeCategoryDef}
              unit={metrics.unit}
            />

            {/* Main chart */}
            <div className="flex-1 min-h-0">
              <AreaChart
                series={metrics.result.series}
                color={activeCategoryDef.chartColor}
                fillColor={activeCategoryDef.fillColor}
                unit={metrics.unit}
              />
            </div>

            {/* Per-pod legend for workload-level queries */}
            {metrics.result.series.length > 1 && (
              <SeriesLegend series={metrics.result.series} color={activeCategoryDef.chartColor} />
            )}
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
            <BarChart3 className="w-8 h-8 mb-2 opacity-40" />
            <p className="text-sm">No data for this time range</p>
            <p className="text-xs text-theme-text-quaternary mt-1">
              Try a different time range or check that metrics are being collected
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

// ============================================================================
// Sub-Components
// ============================================================================

function MetricsSummary({ series, category, unit }: {
  series: PrometheusSeries[]
  category: CategoryDef
  unit: string
}) {
  const stats = useMemo(() => {
    // Aggregate all data points across series
    const allValues: number[] = []
    for (const s of series) {
      for (const dp of s.dataPoints) {
        allValues.push(dp.value)
      }
    }
    if (allValues.length === 0) return null

    // Latest = sum of each series' most recent data point
    const lastValues = series.map(s => s.dataPoints[s.dataPoints.length - 1]?.value ?? 0)
    const current = lastValues.reduce((a, b) => a + b, 0)
    const max = Math.max(...allValues)
    const avg = allValues.reduce((a, b) => a + b, 0) / allValues.length

    return { current, max, avg }
  }, [series])

  if (!stats) return null

  return (
    <div className="flex items-center gap-6">
      <StatPill label="Current" value={formatMetricValue(stats.current, unit)} className={category.color} />
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

// ============================================================================
// Area Chart (pure SVG, no dependencies)
// ============================================================================

function seriesColor(index: number, fallback: string): string {
  return SERIES_COLORS[index % SERIES_COLORS.length] ?? fallback
}

function seriesFill(index: number, fallback: string): string {
  return (SERIES_COLORS[index % SERIES_COLORS.length] ?? fallback) + '22'
}

// Compute short labels that strip the shared prefix so pods are distinguishable.
// e.g. ["backend-podinfo-849bd668f9-4tzkg", "backend-podinfo-849bd668f9-5z79f"] → ["4tzkg", "5z79f"]
function computeShortLabels(labels: string[]): string[] {
  if (labels.length <= 1) return labels
  // Find longest common prefix
  let prefix = labels[0]
  for (let i = 1; i < labels.length; i++) {
    while (!labels[i].startsWith(prefix)) {
      prefix = prefix.slice(0, -1)
    }
  }
  // Trim to last separator (- or /) for cleaner cuts
  const lastSep = Math.max(prefix.lastIndexOf('-'), prefix.lastIndexOf('/'))
  if (lastSep > 0) prefix = prefix.slice(0, lastSep + 1)

  const suffixes = labels.map(l => l.slice(prefix.length))
  // If stripping made them empty or all the same, fall back to originals
  if (suffixes.some(s => s === '') || new Set(suffixes).size !== suffixes.length) return labels
  return suffixes
}

function AreaChart({ series, color, fillColor, unit }: {
  series: PrometheusSeries[]
  color: string
  fillColor: string
  unit: string
}) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [hoverX, setHoverX] = useState<number | null>(null)
  const multiSeries = series.length > 1

  const chartData = useMemo(() => {
    if (!series.length) return null

    // Merge all series into a single timeline for the X axis
    let minTs = Infinity
    let maxTs = -Infinity
    let maxVal = 0

    for (const s of series) {
      for (const dp of s.dataPoints) {
        if (dp.timestamp < minTs) minTs = dp.timestamp
        if (dp.timestamp > maxTs) maxTs = dp.timestamp
        if (dp.value > maxVal) maxVal = dp.value
      }
    }

    if (minTs === maxTs || maxVal === 0) {
      // Handle flat data - still show something
      if (maxVal === 0) maxVal = 1
      if (minTs === maxTs) maxTs = minTs + 60
    }

    const padding = maxVal * 0.1
    const yMax = maxVal + padding

    return { minTs, maxTs, yMax, series }
  }, [series])

  if (!chartData) return null

  const { minTs, maxTs, yMax } = chartData
  const width = 1000
  const height = 300
  const marginLeft = 60
  const marginRight = 20
  const marginTop = 10
  const marginBottom = 30
  const plotWidth = width - marginLeft - marginRight
  const plotHeight = height - marginTop - marginBottom

  const toX = (ts: number) => marginLeft + ((ts - minTs) / (maxTs - minTs)) * plotWidth
  const toY = (val: number) => marginTop + plotHeight - (val / yMax) * plotHeight

  // Y axis ticks
  const yTicks = useMemo(() => {
    const count = 4
    return Array.from({ length: count + 1 }, (_, i) => {
      const val = (yMax / count) * i
      return { val, y: toY(val), label: formatMetricValue(val, unit) }
    })
  }, [yMax, unit])

  // X axis ticks
  const xTicks = useMemo(() => {
    const count = 6
    return Array.from({ length: count + 1 }, (_, i) => {
      const ts = minTs + ((maxTs - minTs) / count) * i
      return { ts, x: toX(ts), label: formatTimestamp(ts) }
    })
  }, [minTs, maxTs])

  // Build paths for each series
  const paths = useMemo(() => {
    return chartData.series.map((s, seriesIdx) => {
      if (s.dataPoints.length < 2) return null
      const points = s.dataPoints.map(dp => ({ x: toX(dp.timestamp), y: toY(dp.value) }))

      const linePath = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')

      // Area path: line + close to bottom
      const areaPath = linePath +
        ` L${points[points.length - 1].x},${marginTop + plotHeight}` +
        ` L${points[0].x},${marginTop + plotHeight} Z`

      return {
        linePath,
        areaPath,
        strokeColor: multiSeries ? seriesColor(seriesIdx, color) : color,
        areaFillColor: multiSeries ? seriesFill(seriesIdx, fillColor) : fillColor,
        key: seriesIdx,
      }
    }).filter(Boolean)
  }, [chartData])

  // Hover data: find nearest data point per series at the hovered X position
  const hoverData = useMemo(() => {
    if (hoverX === null) return null
    const clampedX = Math.max(marginLeft, Math.min(marginLeft + plotWidth, hoverX))
    const frac = (clampedX - marginLeft) / plotWidth
    const ts = minTs + frac * (maxTs - minTs)

    const validSeries = chartData.series
      .map((s, i) => ({ s, i }))
      .filter(({ s }) => s.dataPoints.length >= 2)

    const fullLabels = validSeries.map(({ s, i }) =>
      s.labels.pod || s.labels.instance || s.labels.node || `series-${i}`
    )
    const shortLabels = computeShortLabels(fullLabels)

    const points = validSeries.map(({ s, i }, vi) => {
      let closest = s.dataPoints[0]
      let closestDist = Infinity
      for (const dp of s.dataPoints) {
        const dist = Math.abs(dp.timestamp - ts)
        if (dist < closestDist) {
          closestDist = dist
          closest = dp
        }
      }
      return {
        label: shortLabels[vi],
        fullLabel: fullLabels[vi],
        value: closest.value,
        y: toY(closest.value),
        color: multiSeries ? seriesColor(i, color) : color,
      }
    })

    return { ts, x: clampedX, points }
  }, [hoverX, chartData])

  // Convert client mouse coordinates to SVG viewBox coordinates
  const handleMouseMove = useCallback((e: React.MouseEvent<SVGRectElement>) => {
    const svg = svgRef.current
    if (!svg) return
    const ctm = svg.getScreenCTM()
    if (!ctm) return
    setHoverX((e.clientX - ctm.e) / ctm.a)
  }, [])

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        className="w-full h-full"
        preserveAspectRatio="xMidYMid meet"
      >
        {/* Grid lines */}
        {yTicks.map((tick, i) => (
          <line
            key={`grid-${i}`}
            x1={marginLeft}
            y1={tick.y}
            x2={width - marginRight}
            y2={tick.y}
            stroke="currentColor"
            className="text-theme-border/30"
            strokeWidth="1"
            strokeDasharray={i === 0 ? undefined : '4 4'}
          />
        ))}

        {/* Y axis labels */}
        {yTicks.map((tick, i) => (
          <text
            key={`ylabel-${i}`}
            x={marginLeft - 8}
            y={tick.y + 4}
            textAnchor="end"
            className="fill-theme-text-secondary"
            fontSize="11"
            fontFamily="ui-monospace, monospace"
          >
            {tick.label}
          </text>
        ))}

        {/* X axis labels */}
        {xTicks.map((tick, i) => (
          <text
            key={`xlabel-${i}`}
            x={tick.x}
            y={height - 4}
            textAnchor="middle"
            className="fill-theme-text-secondary"
            fontSize="11"
            fontFamily="ui-monospace, monospace"
          >
            {tick.label}
          </text>
        ))}

        {/* Area fills */}
        {paths.map(p => p && (
          <path
            key={`area-${p.key}`}
            d={p.areaPath}
            fill={p.areaFillColor}
          />
        ))}

        {/* Lines */}
        {paths.map(p => p && (
          <path
            key={`line-${p.key}`}
            d={p.linePath}
            fill="none"
            stroke={p.strokeColor}
            strokeWidth="2"
            strokeLinejoin="round"
          />
        ))}

        {/* Hover crosshair + dots */}
        {hoverData && (
          <>
            <line
              x1={hoverData.x} y1={marginTop}
              x2={hoverData.x} y2={marginTop + plotHeight}
              stroke="currentColor"
              className="text-theme-text-tertiary"
              strokeWidth="1"
              strokeDasharray="4 4"
            />
            {hoverData.points.map((p, i) => (
              <circle
                key={i}
                cx={hoverData.x} cy={p.y}
                r="4"
                fill={p.color}
                stroke="var(--color-theme-surface, #1a1a2e)"
                strokeWidth="2"
              />
            ))}
          </>
        )}

        {/* Invisible overlay for mouse events — must be last for event capture */}
        <rect
          x={marginLeft} y={marginTop}
          width={plotWidth} height={plotHeight}
          fill="transparent"
          style={{ cursor: 'crosshair' }}
          onMouseMove={handleMouseMove}
          onMouseLeave={() => setHoverX(null)}
        />
      </svg>

      {/* Tooltip positioned outside SVG for proper HTML rendering */}
      {hoverData && (
        <div
          className="absolute top-0 pointer-events-none z-10"
          style={{
            left: `${(hoverData.x / width) * 100}%`,
            transform: hoverData.x > width * 0.65 ? 'translateX(calc(-100% - 12px))' : 'translateX(12px)',
          }}
        >
          <div className="bg-theme-surface border border-theme-border rounded-lg shadow-lg px-3 py-2 text-xs whitespace-nowrap">
            <div className="text-theme-text-tertiary mb-1.5 font-mono">
              {new Date(hoverData.ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
            </div>
            {hoverData.points.map((p, i) => (
              <div key={i} className="flex items-center gap-2 py-0.5">
                <div
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{ backgroundColor: p.color }}
                />
                <span className="text-theme-text-secondary font-mono" title={p.fullLabel}>
                  {p.label}
                </span>
                <span className="text-theme-text-primary font-semibold ml-auto pl-3 tabular-nums">
                  {formatMetricValue(p.value, unit)}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function SeriesLegend({ series, color }: { series: PrometheusSeries[]; color: string }) {
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

// ============================================================================
// Formatters
// ============================================================================

function formatMetricValue(value: number, unit: string): string {
  if (value === 0) return '0'

  switch (unit) {
    case 'cores': {
      if (value < 0.001) return `${(value * 1000).toFixed(1)}m`
      if (value < 1) return `${(value * 1000).toFixed(0)}m`
      return `${value.toFixed(2)}`
    }
    case 'bytes': {
      if (value < 1024) return `${value.toFixed(0)} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB`
    }
    case 'bytes/s': {
      if (value < 1024) return `${value.toFixed(0)} B/s`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB/s`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB/s`
    }
    default:
      if (value < 0.01) return value.toExponential(1)
      if (value < 1) return value.toFixed(3)
      if (value < 100) return value.toFixed(2)
      if (value < 10000) return value.toFixed(0)
      return `${(value / 1000).toFixed(1)}k`
  }
}

function formatTimestamp(unix: number): string {
  const d = new Date(unix * 1000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// ============================================================================
// Export helper to check if a kind is supported
// ============================================================================

export function isPrometheusSupported(kind: string): boolean {
  return SUPPORTED_KINDS.has(kind)
}
