import { Info, LineChart, Loader2, Lock, Wifi } from 'lucide-react'
import { clsx } from 'clsx'
import { AlertBanner, RadarUpgradeNote, Section, Tooltip, toneTextClass } from '@skyhook-io/k8s-ui'
import { AreaChart, SeriesLegend, formatMetricValue, formatTimestamp } from '@skyhook-io/k8s-ui/components/charts'
import { isForbiddenError, useAutoPromConnect, usePrometheusConnect, usePrometheusStatus } from '../../api/client'
import { useSubstrateHealth, type SubstrateChart, type SubstrateHealth, type SubstrateTarget } from '../../api/substrateHealth'

const RANGE = '1h'

const NO_WORKERS = 'The controller reports 0 worker Pods for this pool, and Prometheus has no Agent Substrate metrics for it.'

const NOT_FOUND = {
  workerpool: 'Prometheus has no Agent Substrate metrics for this pool. Radar reads them under the underscore names Substrate\'s exporter gives them by default (ate_workerpool_workers) or the dotted ones Google Managed Prometheus keeps (ate.workerpool.workers); native histograms are not read.',
  node: '',
} as const

/**
 * Agent Substrate charts for one WorkerPool or Node, from the curated
 * /prometheus/substrate endpoints. Aggregates only: Substrate keeps actor
 * identity out of metric labels. Hidden on a Node that runs no Substrate, and
 * when Prometheus is not connected unless standalone (the only content of its
 * tab), which offers to connect; every other state says why there is no chart.
 */
export function SubstrateHealthCharts({ target, standalone = false, collapsible = true, workerPods }: {
  target: SubstrateTarget
  standalone?: boolean
  collapsible?: boolean
  /** A WorkerPool's status.replicas, when the caller has it: with none, the empty charts need no metric-naming hint. */
  workerPods?: number
}) {
  useAutoPromConnect()
  const { data: status } = usePrometheusStatus()
  const connect = usePrometheusConnect()
  const connected = status?.connected === true
  const { data, error, upgrade, blockedReason, support } = useSubstrateHealth(target, RANGE, connected && Boolean(target.name))

  if (!connected) {
    if (!standalone || !status) return null
    return (
      <Frame collapsible={collapsible}>
        <Muted>{status.discovering ? 'Discovering Prometheus…' : status.error || 'Connect Prometheus to chart this pool\'s workers, resumes and crashes.'}</Muted>
        {!status.discovering && (
          <button type="button" onClick={() => connect.mutate()} disabled={connect.isPending} className="btn-brand mt-3 inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-xs font-medium">
            {connect.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Wifi className="h-3.5 w-3.5" />}
            Discover Prometheus
          </button>
        )}
      </Frame>
    )
  }
  if (upgrade && target.kind === 'workerpool') {
    return <Frame collapsible={collapsible}><RadarUpgradeNote requirement={upgrade} /></Frame>
  }
  // Until this Radar is known to serve the charts, the query never runs.
  if (!data && support !== 'supported' && blockedReason && target.kind === 'workerpool') {
    return <Frame collapsible={collapsible}><Muted>{blockedReason}</Muted></Frame>
  }
  if (isForbiddenError(error)) {
    return (
      <Frame collapsible={collapsible}>
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
    return <Frame collapsible={collapsible}><Muted tone="warning">Agent Substrate metrics could not be read: {error.message}</Muted></Frame>
  }
  if (!data) return null

  switch (data.status) {
    case 'notOnNode':
      return null
    case 'notFound':
      if (target.kind !== 'workerpool') return null
      return <Frame collapsible={collapsible}><Muted>{workerPods === 0 ? NO_WORKERS : NOT_FOUND.workerpool}</Muted></Frame>
    case 'notAttributable':
    case 'error':
      return <Frame collapsible={collapsible}><Muted tone={data.status === 'error' ? 'warning' : undefined}>{data.reason}</Muted></Frame>
  }
  return <Frame collapsible={collapsible}><Charts health={data} /></Frame>
}

// A section like its neighbours, not a card: the charts are the only cards.
// Collapsible among the drawer's sections; a plain heading on a Metrics tab,
// where collapsing it would only empty the tab.
function Frame({ children, collapsible }: { children: React.ReactNode; collapsible: boolean }) {
  const title = `Agent Substrate · last ${RANGE}`
  return (
    <section aria-label="Agent Substrate metrics">
      {collapsible ? (
        <Section title={title} icon={LineChart}>{children}</Section>
      ) : (
        <>
          <h3 className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-secondary">
            <LineChart className="h-4 w-4" />
            {title}
          </h3>
          {children}
        </>
      )}
    </section>
  )
}

// Averaged worker counts can be fractional; their axis stays on whole numbers.
function chartUnit(unit: string): string {
  return unit === 'workers' ? 'avgCount' : unit
}

function Muted({ children, tone }: { children: React.ReactNode; tone?: 'warning' }) {
  return <p className={clsx('text-xs', tone === 'warning' ? toneTextClass('degraded') : 'text-theme-text-tertiary')}>{children}</p>
}

function Charts({ health }: { health: SubstrateHealth }) {
  const unverified = health.charts.find(c => c.state === 'ok' && c.isolation.mode === 'unverified')
  return (
    <>
      <p className="mb-3 text-xs text-theme-text-tertiary">
        Aggregates: Substrate keeps actor names out of its metrics.
      </p>
      <div className="grid grid-cols-1 gap-3">
        {groupCharts(health.charts).map(group => group.length === 1
          ? <Chart key={group[0].id} chart={group[0]} health={health} />
          : <SharedReason key={group[0].id} charts={group} />)}
      </div>
      {unverified && <p className="mt-3 text-xs text-theme-text-tertiary">{unverified.isolation.note}.</p>}
    </>
  )
}

// Charts that cannot be drawn for the same reason share one note instead of
// repeating it card after card.
function groupCharts(charts: SubstrateChart[]): SubstrateChart[][] {
  const groups: SubstrateChart[][] = []
  const byReason = new Map<string, SubstrateChart[]>()
  for (const chart of charts) {
    if (chart.state === 'ok') {
      groups.push([chart])
      continue
    }
    const key = `${chart.state}\u0000${chart.reason ?? ''}`
    const group = byReason.get(key)
    if (group) {
      group.push(chart)
    } else {
      const created = [chart]
      byReason.set(key, created)
      groups.push(created)
    }
  }
  return groups
}

function SharedReason({ charts }: { charts: SubstrateChart[] }) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0">
      <h4 className="mb-1 text-xs font-medium text-theme-text-secondary">{charts.map(c => c.title).join(' · ')}</h4>
      <Muted tone={charts[0].state === 'empty' || charts[0].state === 'noSeries' ? undefined : 'warning'}>{chartReason(charts[0])}</Muted>
    </section>
  )
}

function Chart({ chart, health }: { chart: SubstrateChart; health: SubstrateHealth }) {
  const series = chart.series.map(s => ({ labels: s.labels, dataPoints: s.dataPoints }))
  const seriesLabels = series.map(s => s.labels.series || chart.title)
  // Only a sample from the last two steps is "now"; an older one ended earlier in the range.
  const since = health.end - 2 * health.stepSeconds
  const unit = chartUnit(chart.unit)
  const latest = chart.state === 'ok' && series.length === 1 ? lastValue(series[0].dataPoints, since) : undefined
  // Each line's latest value sits in its legend entry: lines that share a
  // value hide one another on the plot.
  const legendValues = series.map(s => {
    const v = lastValue(s.dataPoints, since)
    return v === undefined ? undefined : formatMetricValue(v, unit)
  })
  const lateStart = chart.state === 'ok' ? lateFirstSample(series, health) : undefined
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 flex flex-col min-w-0">
      <header className="flex items-baseline justify-between gap-2 mb-1">
        <h4 className="flex items-center gap-1 text-xs font-medium text-theme-text-secondary">
          {chart.title}
          <Tooltip content={`${chart.population} Source: ${chart.source}.`}>
            <Info className="w-3 h-3 text-theme-text-tertiary" aria-label={`About ${chart.title}`} />
          </Tooltip>
        </h4>
        {latest !== undefined && (
          <span className="text-sm font-semibold tabular-nums text-theme-text-primary">{formatMetricValue(latest, unit)}</span>
        )}
      </header>
      {chart.state === 'ok' ? (
        <>
          <AreaChart
            series={series}
            unit={unit}
            color="var(--accent)"
            fillColor="var(--accent-muted)"
            layout="dashboard"
            height={140}
            seriesLabels={seriesLabels}
            domain={{ start: health.start, end: health.end }}
            stepSeconds={health.stepSeconds}
          />
          {series.length > 1 && <SeriesLegend series={series} color="var(--accent)" seriesLabels={seriesLabels} values={legendValues} />}
          {lateStart !== undefined && <Muted>No samples before {formatTimestamp(lateStart)}.</Muted>}
          {chart.omitted ? <Muted>{chart.omitted} more series not shown.</Muted> : null}
          {chart.reason ? <Muted>{capitalize(chart.reason)}.</Muted> : null}
        </>
      ) : (
        <Muted tone={chart.state === 'empty' || chart.state === 'noSeries' ? undefined : 'warning'}>{chartReason(chart)}</Muted>
      )}
    </section>
  )
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
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

// The first sample, when it comes well after the range starts: a mostly empty
// plot otherwise reads as a broken chart. It says when samples begin, not why.
function lateFirstSample(series: { dataPoints: { timestamp: number; value: number | null }[] }[], health: SubstrateHealth): number | undefined {
  let first: number | undefined
  for (const s of series) {
    const p = s.dataPoints.find(d => d.value != null && Number.isFinite(d.value))
    if (p && (first === undefined || p.timestamp < first)) first = p.timestamp
  }
  const gap = Math.max(2 * health.stepSeconds, (health.end - health.start) / 10)
  return first !== undefined && first - health.start > gap ? first : undefined
}

function lastValue(points: { timestamp: number; value: number | null }[], since: number): number | undefined {
  for (let i = points.length - 1; i >= 0 && points[i].timestamp >= since; i--) {
    const v = points[i].value
    if (v != null && Number.isFinite(v)) return v
  }
  return undefined
}
