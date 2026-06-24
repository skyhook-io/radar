import { useMemo, useState } from 'react'
import { BarChart3, ChevronDown, ChevronRight, Loader2, Wifi, WifiOff } from 'lucide-react'
import {
  AreaChart,
  SeriesLegend,
  computeSaturation,
  type ReferenceLine,
} from '@skyhook-io/k8s-ui/components/charts'
import { SEVERITY_BADGE, SEVERITY_TEXT, type Severity } from '@skyhook-io/k8s-ui/utils/badge-colors'
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
import { Tooltip } from '../ui/Tooltip'

// Used when MetricsTabContent is in expanded (full-screen) mode. Drawer mode
// uses the single-chart tabbed `PrometheusCharts` instead — drawer width
// can't fit the grid cleanly.
export interface PrometheusChartsGridProps {
  kind: string
  namespace: string
  name: string
  /** Optional full K8s resource for request/limit overlay derivation. */
  resource?: any
}

const SUPPORTED_KINDS = new Set([
  'Pod', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Node',
])

export function PrometheusChartsGrid({
  kind,
  namespace,
  name,
  resource,
}: PrometheusChartsGridProps) {
  useAutoPromConnect()
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const connectMutation = usePrometheusConnect()
  const isConnected = status?.connected === true
  const isSupported = SUPPORTED_KINDS.has(kind)
  const showRestartLane = isSupported && kind !== 'Node'

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

      {/* Restart lane sits above the grid so its markers visually align with
          the time axis of the charts below. */}
      {showRestartLane && (
        <div className="px-4 pt-3">
          <RestartEventLane kind={kind} namespace={namespace} name={name} range={timeRange} />
        </div>
      )}

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

function SaturationChip({ ratio, against }: { ratio: number; against: 'limit' | 'request' }) {
  // Thresholds match the rightsizing tone vocabulary: amber from 75% (start
  // watching), red at 90% (the same OOM-risk boundary the backend uses for
  // memory in classifyRightsizing).
  const tone: Severity = ratio >= 0.9 ? 'error' : ratio >= 0.75 ? 'warning' : ratio < 0.05 ? 'info' : 'neutral'
  const label = `${(ratio * 100).toFixed(ratio < 0.1 ? 1 : 0)}% of ${against}`
  return <span className={`badge badge-sm ${SEVERITY_BADGE[tone]}`}>{label}</span>
}

// Severity ordering for rightsizing tones. Aligned with `Tone` constants in
// `internal/prometheus/rightsizing.go` — adding a new tone there triggers a
// TypeScript exhaustiveness error on the Record key set here.
const TONE_RANK: Record<RightsizingTone, number> = {
  ok: 0,
  info: 1,
  warning: 2,
  alert: 3,
  critical: 4,
}

function WorkloadHealthBadge({ kind, namespace, name }: { kind: string; namespace: string; name: string }) {
  const supported = kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet'
  const { data, error } = usePrometheusRightsizing(kind, namespace, name, supported)
  if (!supported) return null
  // Surface a neutral "Health unknown" pill when the rightsizing endpoint
  // errors — otherwise an actually-throttled or OOM-risk workload would
  // silently render as fine while we have no signal to display.
  if (error && !data) {
    const msg = error instanceof Error ? error.message : String(error)
    return (
      <Tooltip content={`Health check failed: ${msg}`}>
      <span className={`badge badge-sm ${SEVERITY_BADGE.neutral}`}>Health unknown</span>
      </Tooltip>
    )
  }
  if (!data?.sampleAvailable || data.rows.length === 0) return null

  const worst = data.rows.reduce<RightsizingTone>(
    (acc, r) => (TONE_RANK[r.tone] > TONE_RANK[acc] ? r.tone : acc),
    'ok',
  )
  // Skip the chip for the steady-state tones to avoid badge-blindness — we
  // only want to draw the eye when there's something to address.
  if (worst === 'ok' || worst === 'info') return null

  const { label, severity }: { label: string; severity: Severity } =
    worst === 'critical' ? { label: 'OOM risk', severity: 'error' } :
    worst === 'alert' ? { label: 'CPU throttling', severity: 'alert' } :
    /* warning */ { label: 'Needs review', severity: 'warning' }
  return <span className={`badge badge-sm ${SEVERITY_BADGE[severity]}`}>{label}</span>
}

function PanelLoading() {
  return (
    <div className="flex items-center justify-center h-full min-h-[160px] text-theme-text-tertiary text-xs">
      <Loader2 className="w-4 h-4 animate-spin mr-2" />
      Loading…
    </div>
  )
}

function PanelError({ message }: { message: string }) {
  return (
    <div className={`flex flex-col items-center justify-center h-full min-h-[160px] ${SEVERITY_TEXT.warning} text-xs px-3 text-center`}>
      Query failed
      <Tooltip content={message} wrapperClassName="!block w-full">
      <span className="text-theme-text-quaternary mt-0.5 line-clamp-2">{message}</span>
      </Tooltip>
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

export { isPrometheusSupported } from './PrometheusCharts'
