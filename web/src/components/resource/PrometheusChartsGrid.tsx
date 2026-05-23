import { useMemo, useState } from 'react'
import { BarChart3, ChevronDown, ChevronRight, Loader2, Wifi, WifiOff } from 'lucide-react'
import {
  AreaChart,
  SeriesLegend,
  type PrometheusSeries,
  type ReferenceLine,
} from '@skyhook-io/k8s-ui/components/charts'
import { SEVERITY_BADGE, type Severity } from '@skyhook-io/k8s-ui/utils/badge-colors'
import {
  usePrometheusStatus,
  usePrometheusConnect,
  usePrometheusResourceMetrics,
  usePrometheusRightsizing,
  useAutoPromConnect,
  type PrometheusMetricCategory,
  type PrometheusTimeRange,
  type RightsizingTone,
} from '../../api/client'
import {
  MetricsSummary,
  TIME_RANGES,
  WORKLOAD_CATEGORIES,
  NODE_CATEGORIES,
  computeRequestLimitLines,
  type CategoryDef,
} from './PrometheusCharts'
import { RestartEventLane } from './RestartChart'

/**
 * PrometheusChartsGrid — wide-screen multi-chart layout for the workload
 * Metrics tab. Renders CPU + Memory side-by-side on ≥md viewports, adds
 * Network RX/TX as a second row, with Disk I/O collapsed by default.
 *
 * All panels share a single time range so they stay aligned. The restart
 * event lane spans full grid width above the panels.
 *
 * Used when MetricsTabContent is in expanded (full-screen) mode. Drawer
 * mode continues to use the single-chart tabbed `PrometheusCharts`.
 */
export interface PrometheusChartsGridProps {
  kind: string
  namespace: string
  name: string
  /** Optional full K8s resource for request/limit overlay derivation. */
  resource?: any
  /** When false, suppresses the restart event lane (e.g. Node kind). */
  showRestartLane?: boolean
}

const SUPPORTED_KINDS = new Set([
  'Pod', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Node',
])

export function PrometheusChartsGrid({
  kind,
  namespace,
  name,
  resource,
  showRestartLane = true,
}: PrometheusChartsGridProps) {
  useAutoPromConnect()
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const connectMutation = usePrometheusConnect()
  const isConnected = status?.connected === true
  const isSupported = SUPPORTED_KINDS.has(kind)

  const [timeRange, setTimeRange] = useState<PrometheusTimeRange>('1h')
  const [diskExpanded, setDiskExpanded] = useState(false)

  const categories = kind === 'Node' ? NODE_CATEGORIES : WORKLOAD_CATEGORIES

  // CPU + memory get reference-line overlays when a resource is provided.
  // Computed once at the parent so each panel can stay otherwise generic.
  const cpuRefLines = useMemo<ReferenceLine[] | undefined>(
    () => (resource ? computeRequestLimitLines(resource, kind, 'cpu') : undefined),
    [resource, kind],
  )
  const memRefLines = useMemo<ReferenceLine[] | undefined>(
    () => (resource ? computeRequestLimitLines(resource, kind, 'memory') : undefined),
    [resource, kind],
  )

  if (!isSupported) return null

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
            className="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium rounded-lg btn-brand"
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

  const findCategory = (key: PrometheusMetricCategory): CategoryDef | undefined =>
    categories.find(c => c.key === key)

  // Build the primary panel set: CPU + Memory + (for workloads) Net RX + Net TX.
  // Disk I/O is collapsed by default — niche metric.
  const primaryCats: { def: CategoryDef; refLines?: ReferenceLine[] }[] = []
  const cpu = findCategory('cpu')
  if (cpu) primaryCats.push({ def: cpu, refLines: cpuRefLines })
  const mem = findCategory('memory')
  if (mem) primaryCats.push({ def: mem, refLines: memRefLines })
  if (kind !== 'Node') {
    const rx = findCategory('network_rx')
    if (rx) primaryCats.push({ def: rx })
    const tx = findCategory('network_tx')
    if (tx) primaryCats.push({ def: tx })
  }
  const disk = findCategory('filesystem')

  return (
    <div className="flex flex-col h-full overflow-auto">
      {/* Toolbar — shared time range across all panels */}
      <div className="shrink-0 flex items-center justify-between px-4 py-2.5 border-b border-theme-border bg-theme-surface/50">
        <div className="flex items-center gap-2 text-sm font-medium text-theme-text-secondary">
          <BarChart3 className="w-4 h-4 text-theme-text-tertiary" />
          Metrics
          <WorkloadHealthBadge kind={kind} namespace={namespace} name={name} />
        </div>
        <select
          value={timeRange}
          onChange={e => setTimeRange(e.target.value as PrometheusTimeRange)}
          className="px-2 py-1 text-xs rounded-md bg-theme-elevated border border-theme-border text-theme-text-secondary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
        >
          {TIME_RANGES.map(tr => (
            <option key={tr.value} value={tr.value}>{tr.label}</option>
          ))}
        </select>
      </div>

      {/* Restart event lane — full width, above the grid so its markers
          visually align with the time axis of the charts below. */}
      {showRestartLane && (
        <div className="px-4 pt-3">
          <RestartEventLane kind={kind} namespace={namespace} name={name} range={timeRange} />
        </div>
      )}

      {/* Primary grid — CPU + Memory always; Net RX + TX for non-Node workloads.
          1-col at narrow viewports, 2-col at md+ (768px) so primary panels sit
          side-by-side on any normal full-screen view. */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3 p-4">
        {primaryCats.map(({ def, refLines }) => (
          <MetricsPanel
            key={def.key}
            category={def}
            kind={kind}
            namespace={namespace}
            name={name}
            timeRange={timeRange}
            referenceLines={refLines}
          />
        ))}
      </div>

      {/* Disk I/O — collapsible, full-width when expanded. */}
      {disk && (
        <div className="px-4 pb-4">
          <button
            type="button"
            onClick={() => setDiskExpanded(v => !v)}
            className="flex items-center gap-1.5 text-xs font-medium text-theme-text-tertiary hover:text-theme-text-secondary py-1"
          >
            {diskExpanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
            Disk I/O
          </button>
          {diskExpanded && (
            <div className="mt-2">
              <MetricsPanel
                category={disk}
                kind={kind}
                namespace={namespace}
                name={name}
                timeRange={timeRange}
              />
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ============================================================================
// MetricsPanel — a single category card. One query, one chart, one summary.
// ============================================================================

interface MetricsPanelProps {
  category: CategoryDef
  kind: string
  namespace: string
  name: string
  timeRange: PrometheusTimeRange
  referenceLines?: ReferenceLine[]
}

function MetricsPanel({ category, kind, namespace, name, timeRange, referenceLines }: MetricsPanelProps) {
  const { data: metrics, isLoading, error } = usePrometheusResourceMetrics(
    kind, namespace, name, category.key, timeRange, true,
  )

  const series = metrics?.result?.series
  const hasData = (series?.length ?? 0) > 0

  // "% of limit / request" derived from current peak vs reference lines.
  // Without this, a low-utilization workload with a high limit looks like
  // an empty chart — the user can't tell healthy from starved at a glance.
  const saturation = hasData && series && referenceLines
    ? computeSaturation(series, referenceLines)
    : undefined

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 flex flex-col min-h-[260px]">
      <header className="flex items-center justify-between mb-2 gap-3">
        <div className="flex items-center gap-2">
          <h3 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide">
            {category.label}
          </h3>
          {saturation && <SaturationChip {...saturation} />}
        </div>
        {hasData && series && (
          <MetricsSummary series={series} category={category} unit={metrics!.unit} />
        )}
      </header>

      <div className="flex-1 min-h-[200px]">
        {isLoading ? (
          <PanelLoading />
        ) : error ? (
          <PanelError message={(error as Error).message} />
        ) : hasData && series ? (
          <>
            <AreaChart
              series={series}
              color={category.chartColor}
              fillColor={category.fillColor}
              unit={metrics!.unit}
              referenceLines={referenceLines}
            />
            {series.length > 1 && (
              <div className="mt-1.5">
                <SeriesLegend series={series} color={category.chartColor} />
              </div>
            )}
          </>
        ) : (
          <PanelNoData hint={metrics?.hint} />
        )}
      </div>
    </section>
  )
}

// ============================================================================
// SaturationChip — "12% of limit" at-a-glance read on utilization.
// Picks limit when both present (limit is the operationally meaningful number).
// Tone ramps with the percentage so a glance at the chip tells you "fine" vs
// "approaching pressure" vs "active risk" without reading the chart.
// ============================================================================

function computeSaturation(series: PrometheusSeries[], refs: ReferenceLine[]): { pct: number; against: 'limit' | 'request' } | undefined {
  // Peak across all series; matches the operator's "worst case in window" mental model.
  let peak = 0
  for (const s of series) {
    for (const dp of s.dataPoints) {
      if (dp.value > peak) peak = dp.value
    }
  }
  if (peak <= 0) return undefined
  const limit = refs.find(r => r.tone === 'limit')
  const request = refs.find(r => r.tone === 'request')
  const ref = limit ?? request
  if (!ref || ref.value <= 0) return undefined
  return { pct: peak / ref.value, against: limit ? 'limit' : 'request' }
}

function SaturationChip({ pct, against }: { pct: number; against: 'limit' | 'request' }) {
  // Thresholds chosen to match the rightsizing tone vocabulary: amber from
  // 75% (start watching), red at 90% (the same OOM-risk boundary the
  // backend uses for memory in classifyRightsizing).
  const tone: Severity = pct >= 0.9 ? 'error' : pct >= 0.75 ? 'warning' : pct < 0.05 ? 'info' : 'neutral'
  const label = `${(pct * 100).toFixed(pct < 0.1 ? 1 : 0)}% of ${against}`
  return <span className={`badge badge-sm ${SEVERITY_BADGE[tone]}`}>{label}</span>
}

// ============================================================================
// WorkloadHealthBadge — single-pill summary of the worst rightsizing tone
// across all containers × resources. Surfaces at-a-glance health state
// (Throttled / OOM risk / Healthy) without making the operator read the
// per-row rightsizing strip.
// ============================================================================

function WorkloadHealthBadge({ kind, namespace, name }: { kind: string; namespace: string; name: string }) {
  // The badge is only meaningful on rightsizing-supported workload kinds.
  const supported = kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet'
  const { data } = usePrometheusRightsizing(kind, namespace, name, supported)
  if (!supported || !data?.sampleAvailable || data.rows.length === 0) return null

  const worst = worstTone(data.rows.map(r => r.tone))
  // Skip the chip for the steady-state tones to avoid badge-blindness — we
  // only want to draw the eye when there's something to address.
  if (worst === 'ok' || worst === 'info') return null

  const { label, severity }: { label: string; severity: Severity } =
    worst === 'critical' ? { label: 'OOM risk', severity: 'error' } :
    worst === 'alert' ? { label: 'CPU throttling', severity: 'alert' } :
    /* warning */ { label: 'Needs review', severity: 'warning' }
  return <span className={`badge badge-sm ${SEVERITY_BADGE[severity]}`}>{label}</span>
}

const TONE_RANK: Record<RightsizingTone, number> = { ok: 0, info: 1, warning: 2, alert: 3, critical: 4 }
function worstTone(tones: RightsizingTone[]): RightsizingTone {
  return tones.reduce((acc, t) => (TONE_RANK[t] > TONE_RANK[acc] ? t : acc), 'ok' as RightsizingTone)
}

function PanelLoading() {
  return (
    <div className="flex items-center justify-center h-full min-h-[160px] text-theme-text-tertiary text-xs">
      <Loader2 className="w-4 h-4 animate-spin mr-2" />
      Loading...
    </div>
  )
}

function PanelError({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[160px] text-amber-700 dark:text-amber-400 text-xs px-3 text-center">
      Query failed
      <span className="text-theme-text-quaternary mt-0.5 line-clamp-2">{message}</span>
    </div>
  )
}

function PanelNoData({ hint }: { hint?: string }) {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[160px] text-theme-text-tertiary text-xs px-3 text-center">
      No data
      {hint && <span className="text-theme-text-quaternary mt-1 max-w-xs">{hint}</span>}
    </div>
  )
}

// Re-export so library consumers can detect whether a kind is chartable
// without importing from the tabbed component.
export { isPrometheusSupported } from './PrometheusCharts'

// Used by the type system inside this file. Re-exported as a value to keep
// PrometheusCharts.tsx the canonical source of `PrometheusSeries` typing.
export type { PrometheusSeries }
