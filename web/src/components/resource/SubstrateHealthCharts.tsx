import { Info, LineChart, Lock } from 'lucide-react'
import { AlertBanner, RadarUpgradeNote, Tooltip } from '@skyhook-io/k8s-ui'
import { AreaChart, SeriesLegend, formatMetricValue } from '@skyhook-io/k8s-ui/components/charts'
import { isForbiddenError, useAutoPromConnect, usePrometheusStatus } from '../../api/client'
import { useSubstrateHealth, type SubstrateChart, type SubstrateHealth, type SubstrateTarget } from '../../api/substrateHealth'

const RANGE = '1h'

const NOT_FOUND = {
  workerpool: 'Prometheus has no Agent Substrate metrics for this pool. Radar reads the names Substrate exports by default, such as ate_workerpool_workers; a pipeline that keeps UTF-8 names or sends native histograms is not read.',
  node: '',
} as const

/**
 * Agent Substrate charts for one WorkerPool or Node, from the curated
 * /prometheus/substrate endpoints. Aggregates only: Substrate keeps actor
 * identity out of metric labels. Hidden when Prometheus is not connected, and
 * on a Node that runs no Substrate; every other state says why there is no chart.
 */
export function SubstrateHealthCharts({ target }: { target: SubstrateTarget }) {
  useAutoPromConnect()
  const { data: status } = usePrometheusStatus()
  const connected = status?.connected === true
  const { data, error, upgrade } = useSubstrateHealth(target, RANGE, connected && Boolean(target.name))

  if (!connected) return null
  if (upgrade && target.kind === 'workerpool') {
    return <Frame><RadarUpgradeNote requirement={upgrade} /></Frame>
  }
  if (isForbiddenError(error)) {
    return (
      <Frame>
        <AlertBanner
          variant="info"
          icon={Lock}
          title="You don't have access to these metrics"
          message={target.kind === 'workerpool'
            ? 'Agent Substrate charts are available to users who can read this WorkerPool.'
            : 'Agent Substrate charts for a node are available to users who can read Nodes.'}
        />
      </Frame>
    )
  }
  if (error) {
    return <Frame><Muted tone="warning">Agent Substrate metrics could not be read: {error.message}</Muted></Frame>
  }
  if (!data) return null

  switch (data.status) {
    case 'notOnNode':
      return null
    case 'notFound':
      return target.kind === 'workerpool' ? <Frame><Muted>{NOT_FOUND.workerpool}</Muted></Frame> : null
    case 'notAttributable':
    case 'error':
      return <Frame><Muted tone={data.status === 'error' ? 'warning' : undefined}>{data.reason}</Muted></Frame>
  }
  return <Frame><Charts health={data} /></Frame>
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <section className="mt-4 rounded-lg border border-theme-border bg-theme-surface/30 p-3" aria-label="Agent Substrate metrics">
      <div className="flex items-center gap-2 mb-3 text-sm font-medium text-theme-text-secondary">
        <LineChart className="w-4 h-4 text-theme-text-tertiary" />
        Agent Substrate (last {RANGE})
      </div>
      {children}
    </section>
  )
}

function Muted({ children, tone }: { children: React.ReactNode; tone?: 'warning' }) {
  return <p className={tone === 'warning' ? 'text-xs text-amber-600 dark:text-amber-400' : 'text-xs text-theme-text-tertiary'}>{children}</p>
}

function Charts({ health }: { health: SubstrateHealth }) {
  const unverified = health.charts.find(c => c.state === 'ok' && c.isolation.mode === 'unverified')
  return (
    <>
      <p className="mb-3 text-xs text-theme-text-tertiary">
        Aggregates only: Substrate keeps actor names out of its metrics, so no chart is about one actor.
      </p>
      <div className="grid grid-cols-1 gap-3">
        {health.charts.map(chart => <Chart key={chart.id} chart={chart} health={health} />)}
      </div>
      {unverified && <p className="mt-3 text-xs text-theme-text-tertiary">{unverified.isolation.note}.</p>}
    </>
  )
}

function Chart({ chart, health }: { chart: SubstrateChart; health: SubstrateHealth }) {
  const series = chart.series.map(s => ({ labels: s.labels, dataPoints: s.dataPoints }))
  const seriesLabels = series.map(s => s.labels.series || chart.title)
  // Only a sample from the last two steps is "now"; an older one ended earlier in the range.
  const since = health.end - 2 * health.stepSeconds
  const latest = chart.state === 'ok' && series.length === 1 ? lastValue(series[0].dataPoints, since) : undefined
  return (
    <section className="flex flex-col rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0">
      <header className="flex items-baseline justify-between gap-2 mb-1">
        <h4 className="flex items-center gap-1 text-xs font-medium text-theme-text-secondary">
          {chart.title}
          <Tooltip content={`${chart.population} Source: ${chart.source}.`}>
            <Info className="w-3 h-3 text-theme-text-tertiary" aria-label={`About ${chart.title}`} />
          </Tooltip>
        </h4>
        {latest !== undefined && (
          <span className="text-sm font-semibold tabular-nums text-theme-text-primary">{formatMetricValue(latest, chart.unit)}</span>
        )}
      </header>
      {chart.state === 'ok' ? (
        <>
          <AreaChart
            series={series}
            unit={chart.unit}
            color="var(--accent)"
            fillColor="var(--accent-muted)"
            layout="dashboard"
            height={140}
            seriesLabels={seriesLabels}
            domain={{ start: health.start, end: health.end }}
            stepSeconds={health.stepSeconds}
          />
          {series.length > 1 && <SeriesLegend series={series} color="var(--accent)" seriesLabels={seriesLabels} />}
          {series.length > 1 && <LatestValues series={series} labels={seriesLabels} unit={chart.unit} since={since} />}
          {chart.omitted ? <Muted>{chart.omitted} more series not shown.</Muted> : null}
        </>
      ) : (
        <Muted tone={chart.state === 'empty' || chart.state === 'noSeries' ? undefined : 'warning'}>{chartReason(chart)}</Muted>
      )}
    </section>
  )
}

// Lines that share a value hide one another; the latest value of each stays readable here.
function LatestValues({ series, labels, unit, since }: { series: { dataPoints: { timestamp: number; value: number | null }[] }[]; labels: string[]; unit: string; since: number }) {
  const values = series.map((s, i) => ({ label: labels[i], value: lastValue(s.dataPoints, since) })).filter(v => v.value !== undefined)
  if (!values.length) return null
  return (
    <p className="mt-1 text-xs text-theme-text-tertiary tabular-nums">
      Now: {values.map(v => `${v.label} ${formatMetricValue(v.value!, unit)}`).join(' · ')}
    </p>
  )
}

function chartReason(chart: SubstrateChart): string {
  const reason = chart.reason ?? ''
  switch (chart.state) {
    case 'empty':
      return `No data: ${reason}.`
    case 'noSeries':
      return `Not reported: ${reason}.`
    default:
      return `Not shown: ${reason}.`
  }
}

function lastValue(points: { timestamp: number; value: number | null }[], since: number): number | undefined {
  for (let i = points.length - 1; i >= 0 && points[i].timestamp >= since; i--) {
    const v = points[i].value
    if (v != null && Number.isFinite(v)) return v
  }
  return undefined
}
