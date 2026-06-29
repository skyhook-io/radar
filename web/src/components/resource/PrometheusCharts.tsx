import { useState, useMemo } from 'react'
import {
  PrometheusChartsView,
  MetricsSummary as BaseMetricsSummary,
  type TimeSeries,
  type ReferenceLine,
} from '@skyhook-io/k8s-ui/components/charts'
import {
  usePrometheusStatus,
  usePrometheusConnect,
  usePrometheusResourceMetrics,
  useAutoPromConnect,
  type PrometheusMetricCategory,
  type PrometheusTimeRange,
} from '../../api/client'

// ============================================================================
// Radar-specific category config (lives here, not in k8s-ui, so consumers
// reusing AreaChart aren't forced to inherit Radar's category vocabulary).
// ============================================================================

const SUPPORTED_KINDS = new Set([
  'Pod', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Node',
])

export interface CategoryDef {
  key: PrometheusMetricCategory
  label: string
  color: string       // tailwind text class
  chartColor: string  // hex for SVG
  fillColor: string   // hex with alpha for SVG fill
}

export const WORKLOAD_CATEGORIES: CategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'network_rx', label: 'Net RX', color: 'text-emerald-400', chartColor: '#34d399', fillColor: '#34d39922' },
  { key: 'network_tx', label: 'Net TX', color: 'text-orange-400', chartColor: '#fb923c', fillColor: '#fb923c22' },
  { key: 'filesystem', label: 'Disk I/O', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

export const NODE_CATEGORIES: CategoryDef[] = [
  { key: 'cpu', label: 'CPU', color: 'text-blue-400', chartColor: '#60a5fa', fillColor: '#60a5fa22' },
  { key: 'memory', label: 'Memory', color: 'text-purple-400', chartColor: '#c084fc', fillColor: '#c084fc22' },
  { key: 'filesystem', label: 'Disk', color: 'text-amber-400', chartColor: '#fbbf24', fillColor: '#fbbf2422' },
]

export const TIME_RANGES: { value: PrometheusTimeRange; label: string }[] = [
  { value: '10m', label: '10m' },
  { value: '30m', label: '30m' },
  { value: '1h', label: '1h' },
  { value: '3h', label: '3h' },
  { value: '6h', label: '6h' },
  { value: '12h', label: '12h' },
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
]

// Radar's MetricsSummary thin wrapper — adapts CategoryDef to the slim
// interface of the shared k8s-ui primitive so callers downstream don't change.
export function MetricsSummary({ series, category, unit }: {
  series: TimeSeries[]
  category: CategoryDef
  unit: string
}) {
  return <BaseMetricsSummary series={series} unit={unit} currentColorClass={category.color} />
}

// ============================================================================
// Main Component
// ============================================================================

interface PrometheusChartsProps {
  kind: string
  namespace: string
  name: string
  /** When true, show "no data" empty state instead of hiding. Defaults to false (hide when no data). */
  showEmptyState?: boolean
  /**
   * Full K8s resource. When provided, CPU and memory charts overlay the
   * aggregate request/limit (summed across runtime containers including
   * native sidecars, multiplied by readyReplicas for replicated workloads).
   */
  resource?: any
  /** Notifies the parent when the user changes the time range, so sibling
   * panels (e.g. restart lane) can mirror the selection. */
  onTimeRangeChange?: (range: PrometheusTimeRange) => void
}

export function PrometheusCharts({ kind, namespace, name, showEmptyState = false, resource, onTimeRangeChange }: PrometheusChartsProps) {
  useAutoPromConnect()
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const connectMutation = usePrometheusConnect()

  const [activeCategory, setActiveCategory] = useState<PrometheusMetricCategory>('cpu')
  const [timeRange, setTimeRange] = useState<PrometheusTimeRange>('1h')

  const isConnected = status?.connected === true
  const isSupported = SUPPORTED_KINDS.has(kind)

  // Fetch metrics when connected
  const { data: metrics, isLoading: metricsLoading, error: metricsError } = usePrometheusResourceMetrics(
    kind, namespace, name, activeCategory, timeRange,
    isConnected && isSupported,
  )

  // Reference lines: aggregate request/limit overlaid on CPU and memory charts.
  // Computed unconditionally and placed above early returns to keep hook order
  // stable when connection state transitions (Rules of Hooks).
  const referenceLines = useMemo<ReferenceLine[] | undefined>(() => {
    if (!resource || (activeCategory !== 'cpu' && activeCategory !== 'memory')) return undefined
    return computeRequestLimitLines(resource, kind, activeCategory)
  }, [resource, kind, activeCategory])

  return (
    <PrometheusChartsView
      kind={kind}
      showEmptyState={showEmptyState}
      statusLoading={statusLoading}
      isConnected={isConnected}
      statusError={status?.error}
      onConnect={() => connectMutation.mutate()}
      connecting={connectMutation.isPending}
      category={activeCategory}
      onCategoryChange={setActiveCategory}
      range={timeRange}
      onRangeChange={(r) => { setTimeRange(r); onTimeRangeChange?.(r) }}
      metrics={metrics}
      metricsLoading={metricsLoading}
      metricsError={(metricsError as Error) ?? null}
      referenceLines={referenceLines}
    />
  )
}


// ============================================================================
// Request/limit overlay derivation
// ============================================================================

/**
 * Compute aggregate request + limit reference lines from a K8s resource spec.
 * Sums across runtime containers (regular + native sidecars), excluding pure
 * init containers. The values are per-pod — workload charts use
 * `sum(...) by (pod, namespace)` (one series per pod, at per-pod scale), so
 * the reference line lives on the same axis without any replica multiplier.
 *
 * Returns undefined when the spec doesn't have enough information to render
 * a meaningful line (no runtime containers, or no values set on any container).
 */
export function computeRequestLimitLines(
  resource: any,
  kind: string,
  category: 'cpu' | 'memory',
): ReferenceLine[] | undefined {
  if (!resource) return undefined
  const podSpec = extractPodSpec(resource, kind)
  if (!podSpec) return undefined

  const runtimeContainers = collectRuntimeContainers(podSpec)
  if (runtimeContainers.length === 0) return undefined

  let reqSum = 0, reqAny = false
  let limSum = 0, limAny = false
  for (const c of runtimeContainers) {
    const req = readQuantity(c.resources?.requests?.[category], category)
    const lim = readQuantity(c.resources?.limits?.[category], category)
    if (req != null) { reqSum += req; reqAny = true }
    if (lim != null) { limSum += lim; limAny = true }
  }

  const lines: ReferenceLine[] = []
  if (reqAny) {
    lines.push({
      value: reqSum,
      label: `request ${formatRequestLimitLabel(reqSum, category)}`,
      kind: 'request',
    })
  }
  if (limAny) {
    lines.push({
      value: limSum,
      label: `limit ${formatRequestLimitLabel(limSum, category)}`,
      kind: 'limit',
    })
  }
  return lines.length > 0 ? lines : undefined
}

function extractPodSpec(resource: any, kind: string): any | undefined {
  if (kind === 'Pod') return resource?.spec
  if (kind === 'CronJob') return resource?.spec?.jobTemplate?.spec?.template?.spec
  return resource?.spec?.template?.spec
}

function collectRuntimeContainers(podSpec: any): any[] {
  const out: any[] = []
  for (const c of (podSpec?.containers || [])) out.push(c)
  // Native sidecars (initContainers with restartPolicy: Always, GA in 1.33)
  // run for the pod's lifetime and contribute to steady-state usage. Pure
  // init containers run to completion and don't.
  for (const c of (podSpec?.initContainers || [])) {
    if (c?.restartPolicy === 'Always') out.push(c)
  }
  return out
}

const CPU_SUFFIXES: Record<string, number> = { n: 1e-9, u: 1e-6, m: 1e-3 }
const MEMORY_SUFFIXES: Record<string, number> = {
  Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, Pi: 1024 ** 5, Ei: 1024 ** 6,
  K: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15, E: 1e18,
}

// NOT a duplicate of @skyhook-io/k8s-ui/utils/format's parseCPUToNanocores /
// parseMemoryToBytes — those return 0 on invalid input. We need null so that
// "abcMi" doesn't silently poison the caller's running sum and produce a
// missing/zeroed reference line on the chart (real garbage data must be
// distinguishable from a legit 0).
function readQuantity(raw: unknown, category: 'cpu' | 'memory'): number | null {
  if (raw == null) return null
  const s = String(raw).trim()
  if (s === '') return null
  if (category === 'cpu') {
    if (s.endsWith('m')) return scaleOrNull(s, CPU_SUFFIXES.m)
    if (s.endsWith('n')) return scaleOrNull(s, CPU_SUFFIXES.n)
    if (s.endsWith('u')) return scaleOrNull(s, CPU_SUFFIXES.u)
    const v = parseFloat(s)
    return isNaN(v) ? null : v
  }
  // Memory: try two-character then one-character suffixes (Mi before M).
  for (const suffix of ['Ki', 'Mi', 'Gi', 'Ti', 'Pi', 'Ei']) {
    if (s.endsWith(suffix)) return scaleOrNull(s, MEMORY_SUFFIXES[suffix])
  }
  for (const suffix of ['K', 'M', 'G', 'T', 'P', 'E']) {
    if (s.endsWith(suffix)) return scaleOrNull(s, MEMORY_SUFFIXES[suffix])
  }
  const v = parseFloat(s)
  return isNaN(v) ? null : v
}

function scaleOrNull(s: string, scale: number): number | null {
  const v = parseFloat(s)
  return isNaN(v) ? null : v * scale
}

function formatRequestLimitLabel(value: number, category: 'cpu' | 'memory'): string {
  if (category === 'cpu') {
    if (value < 1) return `${Math.round(value * 1000)}m`
    return value.toFixed(2).replace(/\.?0+$/, '')
  }
  // Memory — match formatMetricValue's tier breakpoints.
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(0)}KiB`
  if (value < 1024 ** 3) return `${(value / (1024 ** 2)).toFixed(0)}MiB`
  return `${(value / (1024 ** 3)).toFixed(1)}GiB`
}

// ============================================================================
// Export helper to check if a kind is supported
// ============================================================================

export function isPrometheusSupported(kind: string): boolean {
  return SUPPORTED_KINDS.has(kind)
}
