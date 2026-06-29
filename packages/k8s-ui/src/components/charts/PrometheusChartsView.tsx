import { clsx } from 'clsx'
import { BarChart3, Wifi, WifiOff, Loader2 } from 'lucide-react'
import { AreaChart } from './AreaChart'
import { PaneLoader } from '../ui/PaneLoader'
import { MetricsSummary } from './MetricsSummary'
import { SeriesLegend } from './SeriesLegend'
import type { TimeSeries, ReferenceLine } from './types'

// Pure presentational Prometheus charts view, shared between radar's WorkloadView
// and Radar Hub's Application metrics tab. It owns NO data fetching — the host
// passes status, the selected category/range, the metrics result, and an
// onConnect callback. radar/web injects its global-apiBase hooks; the hub injects
// per-cluster tunnel fetches. Built on the AreaChart/MetricsSummary/SeriesLegend
// primitives already in this package.

export type PrometheusMetricCategory = 'cpu' | 'memory' | 'network_rx' | 'network_tx' | 'filesystem' | 'restarts'
export type PrometheusTimeRange = '10m' | '30m' | '1h' | '3h' | '6h' | '12h' | '24h' | '48h' | '7d' | '14d'

export interface MetricCategoryDef {
  key: PrometheusMetricCategory
  label: string
  color: string // tailwind text class for the summary's current value
  chartColor: string // hex for the SVG line
  fillColor: string // hex + alpha for the SVG fill
}

export const WORKLOAD_METRIC_CATEGORIES: MetricCategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'network_rx', label: 'Net RX', color: 'text-emerald-400', chartColor: '#34d399', fillColor: '#34d39922' },
  { key: 'network_tx', label: 'Net TX', color: 'text-orange-400', chartColor: '#fb923c', fillColor: '#fb923c22' },
  { key: 'filesystem', label: 'Disk I/O', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

export const NODE_METRIC_CATEGORIES: MetricCategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'filesystem', label: 'Disk', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

export const METRIC_TIME_RANGES: { value: PrometheusTimeRange; label: string }[] = [
  { value: '10m', label: '10m' },
  { value: '30m', label: '30m' },
  { value: '1h', label: '1h' },
  { value: '3h', label: '3h' },
  { value: '6h', label: '6h' },
  { value: '12h', label: '12h' },
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
]

const SUPPORTED_KINDS = new Set([
  'Pod', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Node',
])

export interface PrometheusResourceMetricsResult {
  unit: string
  result?: { series?: TimeSeries[] }
  query?: string
  hint?: string
}

export interface PrometheusChartsViewProps {
  kind: string
  /** When false (embedded in an Overview), hide entirely when not connected or no data. */
  showEmptyState?: boolean
  // Connection status (host-provided)
  statusLoading: boolean
  isConnected: boolean
  statusError?: string
  onConnect: () => void
  connecting: boolean
  // Selection (controlled by host)
  category: PrometheusMetricCategory
  onCategoryChange: (c: PrometheusMetricCategory) => void
  range: PrometheusTimeRange
  onRangeChange: (r: PrometheusTimeRange) => void
  // Metrics (host-fetched)
  metrics?: PrometheusResourceMetricsResult
  metricsLoading: boolean
  metricsError?: Error | null
  referenceLines?: ReferenceLine[]
}

export function PrometheusChartsView({
  kind,
  showEmptyState = false,
  statusLoading,
  isConnected,
  statusError,
  onConnect,
  connecting,
  category,
  onCategoryChange,
  range,
  onRangeChange,
  metrics,
  metricsLoading,
  metricsError,
  referenceLines,
}: PrometheusChartsViewProps) {
  const categories = kind === 'Node' ? NODE_METRIC_CATEGORIES : WORKLOAD_METRIC_CATEGORIES
  const isSupported = SUPPORTED_KINDS.has(kind)
  const activeCategoryDef = categories.find((c) => c.key === category) || categories[0]
  const series = metrics?.result?.series ?? []

  if (!isSupported) return null

  if (statusLoading) {
    if (!showEmptyState) return null
    return (
      <PaneLoader label="Checking Prometheus availability…" className="py-12" />
    )
  }

  if (!showEmptyState) {
    if (!isConnected) return null
    if (!metricsLoading && !metricsError && !series.length) return null
  }

  if (!isConnected) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-12">
        <WifiOff className="h-10 w-10 text-theme-text-tertiary" />
        <div className="text-center">
          <p className="mb-1 text-sm text-theme-text-secondary">Prometheus not connected</p>
          <p className="mb-4 text-xs text-theme-text-tertiary">
            {statusError || 'Connect to view historical CPU, memory, and network metrics'}
          </p>
          <button
            type="button"
            onClick={onConnect}
            disabled={connecting}
            className="btn-brand inline-flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium"
          >
            {connecting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Wifi className="h-4 w-4" />}
            Discover Prometheus
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      {/* Toolbar */}
      <div className="flex shrink-0 items-center justify-between border-b border-theme-border bg-theme-surface/50 px-4 py-2.5">
        <div className="flex items-center gap-1">
          <BarChart3 className="mr-2 h-4 w-4 text-theme-text-tertiary" />
          {categories.map((cat) => (
            <button
              key={cat.key}
              type="button"
              onClick={() => onCategoryChange(cat.key)}
              className={clsx(
                'rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
                category === cat.key
                  ? 'bg-theme-elevated text-theme-text-primary shadow-sm'
                  : 'text-theme-text-tertiary hover:bg-theme-elevated/50 hover:text-theme-text-secondary',
              )}
            >
              {cat.label}
            </button>
          ))}
        </div>
        <select
          value={range}
          onChange={(e) => onRangeChange(e.target.value as PrometheusTimeRange)}
          className="rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-xs text-theme-text-secondary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
        >
          {METRIC_TIME_RANGES.map((tr) => (
            <option key={tr.value} value={tr.value}>
              {tr.label}
            </option>
          ))}
        </select>
      </div>

      {/* Chart area */}
      <div className="min-h-[280px] flex-1 p-4">
        {metricsLoading ? (
          <PaneLoader label="Loading metrics…" className="min-h-[240px]" />
        ) : metricsError ? (
          <div className="flex h-full items-center justify-center text-sm text-red-400">
            Failed to load metrics: {(metricsError as Error).message}
          </div>
        ) : series.length ? (
          <div className="flex h-full flex-col gap-4">
            <MetricsSummary series={series} unit={metrics!.unit} currentColorClass={activeCategoryDef.color} />
            <div className="min-h-0 flex-1">
              <AreaChart
                series={series}
                color={activeCategoryDef.chartColor}
                fillColor={activeCategoryDef.fillColor}
                unit={metrics!.unit}
                referenceLines={referenceLines}
              />
            </div>
            {series.length > 1 && <SeriesLegend series={series} color={activeCategoryDef.chartColor} />}
          </div>
        ) : (
          <div className="flex h-full flex-col items-center justify-center text-theme-text-tertiary">
            <BarChart3 className="mb-2 h-8 w-8 opacity-40" />
            <p className="text-sm">No data for this time range</p>
            <p className="mt-1 text-xs text-theme-text-quaternary">
              Try a different time range or check that metrics are being collected
            </p>
            {metrics?.hint && (
              <p className="mt-3 w-full max-w-lg rounded border border-yellow-500/30 bg-yellow-500/10 px-3 py-2 text-xs text-yellow-700 dark:text-yellow-400">
                {metrics.hint}
              </p>
            )}
            {metrics?.query && (
              <details className="mt-3 w-full max-w-lg text-left">
                <summary className="cursor-pointer text-xs text-theme-text-quaternary hover:text-theme-text-tertiary">
                  Diagnostics: show PromQL query
                </summary>
                <div className="mt-2 break-all rounded border border-theme-border bg-theme-base p-2 font-mono text-xs text-theme-text-secondary">
                  {metrics.query}
                </div>
              </details>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
