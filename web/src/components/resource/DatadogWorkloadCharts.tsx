import { useQuery } from '@tanstack/react-query'
import { AreaChart, type TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
import { fetchJSON, type PrometheusTimeRange } from '../../api/client'
import { getApiBase } from '../../api/config'
import { MetricsSummary, TIME_RANGES, WORKLOAD_CATEGORIES } from './PrometheusCharts'
import { RestartEventLane } from './RestartChart'

type Metric = 'cpu' | 'memory'
type Panels = Record<Metric, { unit: string; series: TimeSeries[] }>
type Workload = { kind: string; namespace: string; name: string }
type WorkloadCharts = { configured: false } | { configured: true; charts: Panels }

export function useDatadogWorkloadCharts({ kind, namespace, name }: Workload, range: PrometheusTimeRange, embedded: boolean) {
  const enabled = embedded && !!namespace && ['Deployment', 'StatefulSet', 'DaemonSet'].includes(kind)
  const apiBase = getApiBase()
  const status = useQuery({
    queryKey: ['datadog-metrics-status', apiBase],
    enabled,
    staleTime: 60_000,
    retry: false,
    queryFn: ({ signal }) => fetchJSON<{ configured: boolean }>('/metrics/datadog/status', signal),
  })
  const mapped = enabled && status.data?.configured === true
  const path = [kind, namespace, name].map(encodeURIComponent).join('/')
  const charts = useQuery<WorkloadCharts>({
    queryKey: ['datadog-workload-metrics', apiBase, path, range],
    enabled: mapped,
    staleTime: 30_000,
    retry: false,
    queryFn: ({ signal }) => fetchJSON<WorkloadCharts>(`/metrics/datadog/workloads/${path}?range=${range}`, signal),
  })
  return { charts, checking: status.isLoading, configured: mapped && charts.data?.configured !== false }
}

export function DatadogWorkloadCharts({
  workload, charts, range, onRangeChange, error, loading, onRetry, fill,
}: {
  workload: Workload
  charts?: Panels
  range: PrometheusTimeRange
  onRangeChange: (range: PrometheusTimeRange) => void
  error?: Error | null
  loading: boolean
  onRetry: () => void
  fill: boolean
}) {
  return (
    <div className={fill ? "h-full overflow-auto p-4" : "p-4"}>
      <div className="flex items-center justify-between gap-3 pb-3">
        <div>
          <h2 className="text-sm font-medium text-theme-text-primary">Datadog <span className="text-theme-text-tertiary">· Beta</span></h2>
          <p className="text-xs text-theme-text-secondary">Container usage in the selected range, from workload creation</p>
        </div>
        <select aria-label="Metrics time range" value={range} onChange={(e) => onRangeChange(e.target.value as PrometheusTimeRange)}
          className="rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-xs text-theme-text-secondary">
          {TIME_RANGES.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
        </select>
      </div>
      {loading && <p role="status" className="py-4 text-sm text-theme-text-secondary">Loading Datadog metrics…</p>}
      {error && (
        <div className="py-4 text-sm">
          <p role="alert" className="text-theme-text-secondary">{error.message}</p>
          <button type="button" className="mt-2 text-accent hover:underline" onClick={onRetry}>Retry</button>
        </div>
      )}
      {!error && !loading && charts && (
        <>
          <div className="grid gap-3 lg:grid-cols-2">
            {WORKLOAD_CATEGORIES.filter((category) => category.key === 'cpu' || category.key === 'memory').map((category) => {
              const { unit, series } = charts[category.key as Metric]
              return (
                <section key={category.key} className="min-w-0 rounded-lg border border-theme-border bg-theme-surface p-3">
                  <header className="mb-2 flex flex-wrap items-center justify-between gap-2">
                    <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-secondary">{category.label}</h3>
                    {series.length > 0 && <MetricsSummary series={series} unit={unit} category={category} />}
                  </header>
                  {series.length > 0
                    ? <AreaChart series={series} unit={unit} color={category.chartColor} fillColor={category.fillColor} layout="dashboard" />
                    : <p className="py-6 text-center text-xs text-theme-text-secondary">No Datadog samples in this time range.</p>}
                </section>
              )
            })}
          </div>
          <div className="mt-4"><RestartEventLane {...workload} range={range} /></div>
        </>
      )}
    </div>
  )
}
