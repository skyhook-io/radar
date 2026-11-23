import { useQuery } from '@tanstack/react-query'
import { AreaChart, MetricsSummary, type TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
import { ApiError, fetchJSON, usePrometheusStatus, useRadarFeature, type PrometheusTimeRange } from '../../api/client'
import { getApiBase } from '../../api/config'
import { WORKLOAD_CATEGORIES } from './PrometheusCharts'
import { useNavCustomization } from '../../context/NavCustomization'
import { RightsizingPanel } from './RightsizingStrip'
import { DatadogRightsizingPanel } from './DatadogRightsizingPanel'

type Metric = 'cpu' | 'memory'
type Panel = { unit: string; series: TimeSeries[] }
type Panels = Record<Metric, Panel> & { throttling?: Panel }
type Workload = { kind: string; namespace: string; name: string }
type WorkloadCharts = { configured: false } | { configured: true; charts: Panels }

const datadogCategories = [
  ...WORKLOAD_CATEGORIES.filter((category) => category.key === 'cpu' || category.key === 'memory'),
  { key: 'throttling', label: 'CPU throttled periods', color: 'text-warning-text', chartColor: 'var(--warning-text)', fillColor: 'color-mix(in srgb, var(--warning-text) 14%, transparent)' },
] as const

interface DatadogStatus {
  configured: boolean
  rightsizing?: boolean
  rightsizingProvider?: 'datadog' | 'prometheus'
  setup?: 'owner' | 'member'
}

type RightsizingSource = 'datadog' | 'prometheus' | 'unknown' | 'datadog_unavailable'

function resolveRightsizingSource(status: DatadogStatus | undefined, error: Error | null, providerRequired: boolean): RightsizingSource {
  if (error) return !providerRequired && error instanceof ApiError && error.unknownRoute ? 'prometheus' : 'unknown'
  if (!status || (providerRequired && !status.rightsizingProvider)) return 'unknown'
  if (status.rightsizingProvider === 'datadog') return status.rightsizing ? 'datadog' : 'datadog_unavailable'
  return 'prometheus'
}

function useDatadogStatus(kind: string, namespace: string, embedded: boolean) {
  const { rightsizingProviderStatus = false } = useNavCustomization()
  const enabled = embedded && !!namespace && ['Deployment', 'StatefulSet', 'DaemonSet'].includes(kind)
  const apiBase = getApiBase()
  const status = useQuery({
    queryKey: ['datadog-metrics-status', apiBase],
    enabled,
    staleTime: 60_000,
    retry: false,
    queryFn: ({ signal }) => fetchJSON<DatadogStatus>('/metrics/datadog/status', signal),
  })
  const source = enabled ? resolveRightsizingSource(status.data, status.error, rightsizingProviderStatus) : 'prometheus'
  return { status, enabled, source, mapped: enabled && !status.isError && status.data?.configured === true }
}

function rightsizingOverride(workload: Workload, source: RightsizingSource, retry: () => void, error?: Error | null) {
  if (source === 'prometheus') return null
  if (source === 'datadog') return <DatadogRightsizingPanel key={[workload.kind, workload.namespace, workload.name].join('/')} {...workload} />
  return <section className="mt-4 rounded-lg border border-theme-border bg-theme-surface p-3">
    <h3 className="text-sm font-medium text-theme-text-primary">Rightsizing</h3>
    <p role="alert" className="mt-2 text-sm text-theme-text-secondary">
      {source === 'datadog_unavailable'
        ? 'Datadog rightsizing is selected, but this cluster has no usable rightsizing connection.'
        : 'Radar could not determine the rightsizing source.'} {error?.message}{' '}
      <button type="button" className="text-accent hover:underline" onClick={retry}>Retry</button>
    </p>
  </section>
}

export function WorkloadCostRightsizing(workload: Workload) {
  const { embedded = false } = useNavCustomization()
  const { status, source } = useDatadogStatus(workload.kind, workload.namespace, embedded)
  if (status.isLoading) return <p role="status" className="p-3 text-sm text-theme-text-secondary">Checking rightsizing source…</p>
  return rightsizingOverride(workload, source, () => void status.refetch(), status.error) ?? <RightsizingPanel {...workload} />
}

export function useDatadogWorkloadCharts({ kind, namespace, name }: Workload, range: PrometheusTimeRange, embedded: boolean) {
  const { status, enabled, mapped, source } = useDatadogStatus(kind, namespace, embedded)
  const apiBase = getApiBase()
  const path = [kind, namespace, name].map(encodeURIComponent).join('/')
  const charts = useQuery<WorkloadCharts>({
    queryKey: ['datadog-workload-metrics', apiBase, path, range],
    enabled: mapped,
    staleTime: 30_000,
    retry: false,
    queryFn: ({ signal }) => fetchJSON<WorkloadCharts>(`/metrics/datadog/workloads/${path}?range=${range}`, signal),
  })
  const rightsizing = rightsizingOverride({ kind, namespace, name }, source, () => void status.refetch(), status.error)
  return { charts, rightsizing, setup: enabled && !status.isError ? status.data?.setup : undefined, checking: status.isLoading, configured: mapped && charts.data?.configured !== false }
}

export function DatadogSetupHint({ owner }: { owner: boolean }) {
  const { data: prom, isError } = usePrometheusStatus()
  const { support } = useRadarFeature('datadogAgentPresence')
  const empty = !isError && prom?.connected === false && !prom.discovering && !prom.address
  const presence = useQuery({
    queryKey: ['datadog-agent-presence', getApiBase()],
    enabled: empty && support === 'supported',
    queryFn: ({ signal }) => fetchJSON<{detected: boolean}>('/metrics/datadog/agent', signal),
    staleTime: 60_000, retry: false,
  })
  if (!empty || support !== 'supported' || presence.isError || !presence.data?.detected) return null
  return <div className="p-4 text-center text-sm">
    <p className="font-medium text-theme-text-primary">Datadog Agent detected</p>
    <p className="mt-1 text-theme-text-secondary">{owner ? 'Connect Datadog once for CPU and memory charts across your matched clusters.' : 'An organization owner can connect Datadog for CPU and memory charts.'}</p>
    {owner && <a href="/settings/organization/integrations#datadog-metrics" className="mt-2 inline-block text-accent underline">Connect Datadog</a>}
  </div>
}

export function DatadogWorkloadCharts({
  charts, error, loading, onRetry,
}: {
  charts?: Panels
  error?: Error | null
  loading: boolean
  onRetry: () => void
}) {
  return (
    <section aria-label="Datadog workload totals">
      <h2 className="text-sm font-medium text-theme-text-primary">Datadog <span className="text-theme-text-tertiary">· Beta</span></h2>
      <p className="mb-3 text-xs text-theme-text-secondary">Workload totals across reporting containers, from workload creation</p>
      {loading && <p role="status" className="py-4 text-sm text-theme-text-secondary">Loading Datadog metrics…</p>}
      {error && (
        <div className="py-4 text-sm">
          <p role="alert" className="text-theme-text-secondary">{error.message}</p>
          <button type="button" className="mt-2 text-accent hover:underline" onClick={onRetry}>Retry</button>
        </div>
      )}
      {!error && !loading && charts && (
        <div className="grid gap-3 lg:grid-cols-2">
          {datadogCategories.map((category) => {
            const panel = charts[category.key as keyof Panels]
            if (!panel) return null
            const { unit, series } = panel
            const throttling = category.key === 'throttling'
            return (
              <section key={category.key} className={`min-w-0 rounded-lg border border-theme-border bg-theme-surface p-3${throttling ? ' lg:col-span-2' : ''}`}>
                <header className="mb-2 flex flex-wrap items-center justify-between gap-2">
                  <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-secondary">{category.label}</h3>
                  {series.length > 0 && <MetricsSummary series={series} unit={unit} currentColorClass={category.color} />}
                </header>
                {series.length > 0
                  ? <AreaChart series={series} unit={unit} color={category.chartColor} fillColor={category.fillColor} layout="dashboard" />
                  : <p className="py-6 text-center text-xs text-theme-text-secondary">No Datadog samples in this time range.</p>}
                {throttling && <p className="mt-2 text-xs text-theme-text-tertiary">Share of CPU quota periods throttled across reporting containers.</p>}
              </section>
            )
          })}
        </div>
      )}
    </section>
  )
}
