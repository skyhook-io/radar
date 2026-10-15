import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { X } from 'lucide-react'
import { Tooltip, formatGrant, toneTextClass } from '@skyhook-io/k8s-ui'
import { AreaChart, SeriesLegend, type ChartTimeRange, type ReferenceLine, type TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
import type { CNPGRuntimeResponse } from '../../api/cnpg'
import {
  CNPG_HISTORY_RANGES,
  useCNPGClusterHistory,
  type CNPGHistoryChart,
  type CNPGHistoryRange,
  type CNPGClusterHistoryResponse,
} from '../../api/cnpg-history'
import { cacheHitSeries, chartedDatabases, rateSeries, sampleFrom, sessionStateSeries, SAMPLE_BUFFER_LIMIT, type Sample } from './trendSamples'
import { Notice, Segments } from '../workspace/layout'

const COLOR = '#60a5fa'
const FILL = '#60a5fa22'

export type { Sample } from './trendSamples'

export function useSampleBuffer(data: CNPGRuntimeResponse | undefined): Sample[] {
  const [samples, setSamples] = useState<Sample[]>([])
  const last = useRef<string | null>(null)
  useEffect(() => {
    if (!data || data.sampledAt === last.current) return
    last.current = data.sampledAt
    setSamples((prev) => [...prev, sampleFrom(data)].slice(-SAMPLE_BUFFER_LIMIT))
  }, [data])
  return samples
}

export type CNPGIntervalTarget = 'logs' | 'activity'

function chartUnit(unit: string): string {
  return unit === 'per second' || unit === 'per minute' ? '' : unit
}

function clock(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function span(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)} s`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`
  return `${(seconds / 3600).toFixed(1)} h`
}

/**
 * Steps where no series has a sample, as hatched ranges. A run that starts
 * the window is before the series existed or beyond retention; any other is a
 * missed scrape or an instance that was down.
 */
export function historyGaps(series: TimeSeries[], start: number, end: number, step: number): (ChartTimeRange & { label: string })[] {
  if (step <= 0 || end <= start) return []
  const present = new Set<number>()
  for (const s of series) for (const p of s.dataPoints) if (p.value != null) present.add(p.timestamp)
  const out: (ChartTimeRange & { label: string })[] = []
  let runStart: number | null = null
  const flush = (last: number) => {
    if (runStart === null) return
    out.push({
      start: Math.max(start, runStart - step / 2),
      end: Math.min(end, last + step / 2),
      label: runStart === start ? 'No sample: before this series existed or beyond Prometheus retention' : 'No sample recorded (instance down, restarting or not scraped)',
    })
    runStart = null
  }
  let prev = start
  for (let t = start; t <= end; t += step) {
    if (present.has(t)) flush(prev)
    else if (runStart === null) runStart = t
    prev = t
  }
  flush(prev)
  return out
}

/** Hatched ranges between the samples around each run where no series has a value. */
export function sampleGaps(series: TimeSeries[]): (ChartTimeRange & { label: string })[] {
  const ts = [...new Set(series.flatMap((s) => s.dataPoints.map((p) => p.timestamp)))].sort((a, b) => a - b)
  const present = new Set<number>()
  for (const s of series) for (const p of s.dataPoints) if (p.value != null) present.add(p.timestamp)
  const out: (ChartTimeRange & { label: string })[] = []
  let i = 0
  while (i < ts.length) {
    if (present.has(ts[i])) {
      i++
      continue
    }
    let j = i
    while (j + 1 < ts.length && !present.has(ts[j + 1])) j++
    out.push({ start: ts[Math.max(0, i - 1)], end: ts[Math.min(ts.length - 1, j + 1)], label: 'No sample: the source did not answer' })
    i = j + 1
  }
  return out
}

/**
 * The instances a per-instance chart should have a line for but does not. A
 * chart draws only what reported, so a stopped standby would otherwise vanish
 * from the very chart about it and leave the others reading calm.
 */
export function unchartedInstances(expected: readonly string[] | undefined, charted: readonly string[]): string[] {
  if (!expected?.length) return []
  const seen = new Set(charted)
  return expected.filter((p) => !seen.has(p))
}

/** Why each instance has no line; `why` reads after "No line for <pods>: ". */
function MissingLines({ pods, why }: { pods: string[]; why: (them: string) => string }) {
  if (pods.length === 0) return null
  return (
    <p className={`mb-2 text-xs ${toneTextClass('degraded')}`}>
      No line for {pods.join(', ')}: {why(pods.length === 1 ? 'it' : 'them')}.
    </p>
  )
}

const promMissingWhy = (them: string) => `Prometheus has no samples from ${them} in this range`

/** The instances a history chart should have a line for, when its lines are per Pod. */
function expectedLines(chart: CNPGHistoryChart, standbys?: string[], instancePods?: string[]): string[] | undefined {
  if (chart.id === 'replicationLag') return standbys
  return chart.seriesBy === 'pod' ? instancePods : undefined
}

function referenceLines(chart: CNPGHistoryChart): ReferenceLine[] | undefined {
  const t = chart.thresholds ?? []
  if (t.length === 0) return undefined
  return t.map((x, i) => ({ value: x.value, label: x.label, kind: i === t.length - 1 ? 'limit' : 'request' }))
}

function ChartCard({ title, meta, children, footer }: { title: ReactNode; meta?: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <section className="min-w-0 overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex items-center gap-2 border-b border-theme-border px-4 py-2.5">
        <span className="text-sm font-semibold text-theme-text-primary">{title}</span>
        {meta && <span className="ml-auto text-xs text-theme-text-tertiary">{meta}</span>}
      </div>
      <div className="p-3">{children}</div>
      {footer && <div className="border-t border-theme-border px-4 py-2 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

function stateText(chart: CNPGHistoryChart): string {
  switch (chart.state) {
    case 'empty':
      return `Nothing to plot: ${chart.reason ?? 'no values in this range'}.`
    case 'noSeries':
      return chart.reason ? chart.reason.charAt(0).toUpperCase() + chart.reason.slice(1) + '.' : 'Not scraped.'
    case 'denied':
      return `No access: needs ${formatGrant(chart.grant) ?? 'more permissions'}.`
    case 'notRead':
      return `Not read: ${chart.reason ?? 'no source'}.`
    default:
      return chart.reason ?? 'Could not be read.'
  }
}

function HistoryChartCard({
  chart,
  data,
  selection,
  onSelect,
  expected,
}: {
  chart: CNPGHistoryChart
  data: CNPGClusterHistoryResponse
  selection: ChartTimeRange | null
  onSelect: (r: ChartTimeRange) => void
  /** The Pods this chart should have a line for, when its lines are per Pod. */
  expected?: string[]
}) {
  const start = Date.parse(data.start ?? '') / 1000
  const end = Date.parse(data.end ?? '') / 1000
  const step = data.stepSeconds ?? 60
  const series: TimeSeries[] = chart.series
  const labels = series.map((s) => s.labels[chart.seriesBy] ?? chart.title)
  const gaps = useMemo(() => historyGaps(series, start, end, step), [series, start, end, step])
  // Omitted series may include the ones that look missing, so nothing is claimed then.
  const missing = (chart.state === 'ok' || chart.state === 'empty') && !chart.omitted ? unchartedInstances(expected, labels) : []
  const coverage = (
    <Tooltip content="Evaluation steps in this range with at least one sample" position="top">
      <span className="font-mono">
        {chart.covered}/{chart.steps} samples
      </span>
    </Tooltip>
  )
  return (
    <ChartCard
      title={chart.title}
      meta={chart.state === 'ok' ? coverage : undefined}
      footer={
        <>
          Source: <span className="font-mono">{chart.source}</span>
          {chart.id === 'pvcUsed' && data.pvcIsolation ? ` · ${data.pvcIsolation.note}` : ''}
          {chart.omitted ? ` · ${chart.omitted} more series not shown` : ''}
          {chart.state === 'ok' && gaps.length > 0 ? ' · hatched: no sample' : ''}
        </>
      }
    >
      {chart.state === 'ok' ? (
        <>
          <MissingLines pods={missing} why={promMissingWhy} />
          <AreaChart
            series={series}
            seriesLabels={labels}
            color={COLOR}
            fillColor={FILL}
            unit={chartUnit(chart.unit)}
            referenceLines={referenceLines(chart)}
            domain={{ start, end }}
            stepSeconds={step}
            layout="dashboard"
            shadedRanges={gaps}
            selection={selection}
            onSelectRange={onSelect}
          />
          {series.length > 1 && (
            <div className="mt-1.5">
              <SeriesLegend series={series} color={COLOR} seriesLabels={labels} />
            </div>
          )}
        </>
      ) : (
        <div
          className={`flex h-24 items-center justify-center rounded-lg border border-dashed border-theme-border px-4 text-center text-sm ${missing.length > 0 ? toneTextClass('degraded') : 'text-theme-text-tertiary'}`}
        >
          {missing.length > 0 ? `No line for ${missing.join(', ')}: ${promMissingWhy(missing.length === 1 ? 'it' : 'them')}.` : stateText(chart)}
        </div>
      )}
    </ChartCard>
  )
}

function IntervalChip({ interval, onOpen, onClear }: { interval: ChartTimeRange; onOpen?: (target: CNPGIntervalTarget) => void; onClear: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-accent/40 bg-accent/10 px-3 py-1.5 text-sm">
      <span className="font-medium text-theme-text-primary">
        {clock(interval.start)} – {clock(interval.end)}
      </span>
      <span className="text-xs text-theme-text-tertiary">{span(interval.end - interval.start)} selected</span>
      {onOpen && (
        <>
          <button type="button" onClick={() => onOpen('logs')} className="rounded-md px-2 py-0.5 text-xs font-medium text-accent-text hover:bg-theme-hover">
            Logs in this interval
          </button>
          <button type="button" onClick={() => onOpen('activity')} className="rounded-md px-2 py-0.5 text-xs font-medium text-accent-text hover:bg-theme-hover">
            Activity in this interval
          </button>
        </>
      )}
      <button type="button" aria-label="Clear the selected interval" onClick={onClear} className="ml-auto rounded p-0.5 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary">
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}

/** The chart groups History filters by; topic tabs link to theirs. */
export type CNPGChartGroup = 'replication' | 'sessions' | 'throughput' | 'storage'

export const CNPG_CHART_GROUPS: { id: CNPGChartGroup | 'all'; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'replication', label: 'Replication' },
  { id: 'sessions', label: 'Sessions' },
  { id: 'throughput', label: 'Throughput' },
  { id: 'storage', label: 'Storage & WAL' },
]

// Prometheus chart ids, as the server names them.
const HISTORY_CHART_GROUP: Record<string, CNPGChartGroup> = {
  replicationLag: 'replication',
  sessions: 'sessions',
  waiting: 'sessions',
  tps: 'throughput',
  deadlocks: 'throughput',
  tempBytes: 'throughput',
  checkpoints: 'throughput',
  walArchive: 'storage',
  walSize: 'storage',
  pvcUsed: 'storage',
  databaseSize: 'storage',
}

function inGroup(group: CNPGChartGroup | undefined, chart: CNPGChartGroup | undefined): boolean {
  return !group || chart === group
}

function BufferCharts({
  samples,
  selection,
  onSelect,
  instance,
  picker,
  group,
  standbys,
  instancePods,
}: {
  samples: Sample[]
  selection: ChartTimeRange | null
  onSelect: (r: ChartTimeRange) => void
  group?: CNPGChartGroup
  /** The instance whose sessions-by-state chart is shown (the Performance picker's choice). */
  instance?: string
  picker?: ReactNode
  standbys?: string[]
  instancePods?: string[]
}) {
  const recent = samples.slice(-SAMPLE_BUFFER_LIMIT)
  const [dbPick, setDbPick] = useState<string[] | 'all' | null>(null)
  const pods = useMemo(() => [...new Set(recent.flatMap((s) => Object.keys(s.replayLag)))].sort(), [recent])
  const instances = useMemo(() => [...new Set(recent.flatMap((s) => Object.keys(s.instances ?? {})))].sort(), [recent])
  const dbs = useMemo(() => chartedDatabases(recent, dbPick), [recent, dbPick])
  if (recent.length < 2) {
    return <div className="text-sm text-theme-text-tertiary">Collecting samples… These trends start when this page opens and cover up to the last hour it stays open.</div>
  }
  const domain = { start: recent[0].t / 1000, end: recent[recent.length - 1].t / 1000 }
  const one = (label: string, pick: (s: Sample) => number | undefined): TimeSeries => ({
    labels: { series: label },
    dataPoints: recent.map((s) => ({ timestamp: s.t / 1000, value: pick(s) ?? null })),
  })
  const approximate = recent.some((s) => s.approximate)
  const rateNote = approximate
    ? 'approximate: the exporter publishes no cnpg_last_update_timestamp, so rates use the time Radar fetched each reading'
    : 'rates between the exporter\'s query runs (cnpg_last_update_timestamp); a change of primary or a counter reset is a gap'
  const sessionsOf = instance ?? instances[0]
  const dbCapped = recent.some((s) => Object.keys(s.dbSizes ?? {}).length >= 200)
  const charts: {
    group: CNPGChartGroup
    title: string
    unit: string
    series: TimeSeries[]
    labels: string[]
    source: string
    thresholds?: ReferenceLine[]
    rate?: boolean
    control?: ReactNode
    note?: string
    missing?: { pods: string[]; why: (them: string) => string }
  }[] = [
    {
      group: 'replication',
      title: 'Replay lag per standby',
      unit: 'seconds',
      series: pods.map((p) => one(p, (s) => s.replayLag[p])),
      labels: pods,
      source: "the primary's pg_stat_replication",
      missing: { pods: unchartedInstances(standbys, pods), why: (them) => `the primary has not listed ${them} as a connected standby since this page opened` },
      thresholds: [
        { value: 5, label: '5 s', kind: 'request' },
        { value: 30, label: '30 s', kind: 'limit' },
      ],
    },
    {
      group: 'sessions',
      title: 'Client sessions per instance',
      unit: 'count',
      series: instances.map((p) => one(p, (s) => s.instances?.[p]?.total)),
      labels: instances,
      source: 'cnpg_backends_total on each instance, platform sessions excluded',
      missing: { pods: unchartedInstances(instancePods, instances), why: (them) => `no reading from ${them} since this page opened` },
    },
    {
      group: 'sessions',
      title: sessionsOf ? `Sessions by state (${sessionsOf})` : 'Sessions by state',
      unit: 'count',
      series: sessionsOf ? sessionStateSeries(recent, sessionsOf) : [],
      labels: ['active', 'idle', 'idle in transaction', 'other'],
      source: 'cnpg_backends_total by state on the instance picked above',
      control: picker,
    },
    {
      group: 'sessions',
      title: 'Sessions waiting on locks',
      unit: 'count',
      series: instances.map((p) => one(p, (s) => s.instances?.[p]?.waiting)),
      labels: instances,
      source: 'cnpg_backends_waiting_total on each instance',
    },
    { group: 'throughput', title: 'Transactions per second (primary)', unit: '', series: [rateSeries(recent, 'commits'), rateSeries(recent, 'rollbacks')], labels: ['commits', 'rollbacks'], source: 'xact_commit / xact_rollback', rate: true },
    { group: 'throughput', title: 'Cache hit ratio (primary)', unit: '%', series: [cacheHitSeries(recent)], labels: ['hit ratio'], source: 'blks_hit / (blks_hit + blks_read)', rate: true },
    {
      group: 'storage',
      title: 'WAL archived / failed per minute',
      unit: '',
      series: [rateSeries(recent, 'archived', 60), rateSeries(recent, 'failed', 60)],
      labels: ['archived', 'failed'],
      source: 'pg_stat_archiver archived_count / failed_count on the primary',
      rate: true,
    },
    { group: 'storage', title: 'WAL on disk (primary)', unit: 'bytes', series: [one('WAL', (s) => s.walBytes)], labels: ['WAL'], source: 'cnpg_collector_pg_wal{value="size"} on the primary' },
    {
      group: 'storage',
      title: 'Database size (primary)',
      unit: 'bytes',
      series: dbs.shown.map((d) => one(d, (s) => s.dbSizes?.[d])),
      labels: dbs.shown,
      source: `pg_database size_bytes on the primary, ${dbPick === 'all' ? 'every database' : dbPick ? 'the databases picked' : 'the five largest'}`,
      control: dbs.all.length > 5 ? <DatabasePicker all={dbs.all} shown={dbs.shown} pick={dbPick} onPick={setDbPick} /> : undefined,
      note: dbCapped ? 'The exporter reports the 200 largest databases; smaller ones are not sampled.' : undefined,
    },
    {
      group: 'throughput',
      title: 'Checkpoints per minute (primary)',
      unit: '',
      series: [rateSeries(recent, 'checkpointsTimed', 60, 'timed'), rateSeries(recent, 'checkpointsRequested', 60, 'requested')],
      labels: ['timed', 'requested'],
      source: 'pg_stat_checkpointer (17+) or pg_stat_bgwriter checkpoints_timed / _req',
      rate: true,
    },
    { group: 'throughput', title: 'Deadlocks per minute (primary)', unit: '', series: [rateSeries(recent, 'deadlocks', 60)], labels: ['deadlocks'], source: 'pg_stat_database deadlocks, all databases', rate: true },
    { group: 'throughput', title: 'Temporary file writes (primary)', unit: 'bytes', series: [rateSeries(recent, 'tempBytes', 1, 'bytes/s')], labels: ['bytes/s'], source: 'pg_stat_database temp_bytes per second, all databases', rate: true },
  ]
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {charts.filter((c) => inGroup(group, c.group)).map((c) => {
        const gaps = sampleGaps(c.series)
        const hasValue = c.series.some((s) => s.dataPoints.some((p) => p.value != null))
        return (
          <ChartCard
            key={c.title}
            title={c.title}
            meta={c.rate && approximate ? 'approximate' : undefined}
            footer={
              <>
                Source: {c.source}, sampled by this page · {c.rate ? rateNote : 'gaps break the line, never zero'}
                {c.note ? ` · ${c.note}` : ''}
              </>
            }
          >
            {c.control && <div className="mb-2">{c.control}</div>}
            {c.missing && <MissingLines pods={c.missing.pods} why={c.missing.why} />}
            {hasValue ? (
              <>
                <AreaChart
                  series={c.series}
                  seriesLabels={c.labels}
                  color={COLOR}
                  fillColor={FILL}
                  unit={c.unit}
                  referenceLines={c.thresholds}
                  domain={domain}
                  layout="dashboard"
                  shadedRanges={gaps}
                  selection={selection}
                  onSelectRange={onSelect}
                />
                {c.series.length > 1 && (
                  <div className="mt-1.5">
                    <SeriesLegend series={c.series} color={COLOR} seriesLabels={c.labels} />
                  </div>
                )}
              </>
            ) : (
              <div className="flex h-24 items-center justify-center rounded-lg border border-dashed border-theme-border px-4 text-center text-sm text-theme-text-tertiary">
                {c.title.startsWith('Replay')
                  ? 'No standby reported by the primary since this page opened.'
                  : c.rate
                    ? 'Needs two exporter readings from the same primary since this page opened.'
                    : 'No sample since this page opened.'}
              </div>
            )}
          </ChartCard>
        )
      })}
    </div>
  )
}

function DatabasePicker({ all, shown, pick, onPick }: { all: string[]; shown: string[]; pick: string[] | 'all' | null; onPick: (p: string[] | 'all' | null) => void }) {
  const toggle = (d: string) => {
    const next = shown.includes(d) ? shown.filter((x) => x !== d) : [...shown, d]
    onPick(next.length === 0 ? null : next)
  }
  const chip = (on: boolean) =>
    `rounded-md border px-1.5 py-0.5 font-mono text-[11px] ${on ? 'border-accent bg-accent-muted text-theme-text-primary' : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover'}`
  return (
    <div className="flex flex-wrap items-center gap-1">
      <button type="button" className={chip(pick === null)} onClick={() => onPick(null)}>
        five largest
      </button>
      <button type="button" className={chip(pick === 'all')} onClick={() => onPick('all')}>
        all {all.length}
      </button>
      {all.map((d) => (
        <button key={d} type="button" aria-pressed={pick !== null && pick !== 'all' && shown.includes(d)} className={chip(pick !== null && pick !== 'all' && shown.includes(d))} onClick={() => toggle(d)}>
          {d}
        </button>
      ))}
    </div>
  )
}

/**
 * Performance › History: Prometheus range queries when Radar has
 * Prometheus, otherwise the samples this page took since it opened. Selecting
 * a time range on any chart carries both bounds to Logs and Activity.
 */
export function CNPGTrends({
  namespace,
  name,
  samples,
  onOpenInterval,
  instance,
  picker,
  samplingDenied,
  group,
  standbys,
  instancePods,
}: {
  namespace: string
  name: string
  samples: Sample[]
  onOpenInterval?: (target: CNPGIntervalTarget, since: string, until: string) => void
  /** The Performance instance picker's choice, for the per-instance sampled charts. */
  instance?: string
  picker?: ReactNode
  /** The grant the caller lacks for in-page samples (get pods/proxy); no sample can ever arrive. */
  samplingDenied?: string
  /** Show one chart group (from `?charts=`); all when unset. */
  group?: CNPGChartGroup
  /** The instance Pods that are standbys now, and all instance Pods: a per-instance chart names any of them it has no line for. */
  standbys?: string[]
  instancePods?: string[]
}) {
  const { range, setRange, interval, setSelected } = useTrendParams()
  const q = useCNPGClusterHistory(namespace, name, range)
  const data = q.data
  const fromPrometheus = data?.source === 'prometheus' && data.state === 'ok'
  // Interval selection needs a chart on the page: Prometheus history, or at least two in-page samples.
  const charted = fromPrometheus || (!q.isLoading && !samplingDenied && samples.length >= 2)
  const fallback = samplingDenied ? `In-page samples need ${samplingDenied} too.` : 'Below: samples since this page opened.'

  const open = onOpenInterval && interval
    ? (target: CNPGIntervalTarget) => onOpenInterval(target, new Date(interval.start * 1000).toISOString(), new Date(interval.end * 1000).toISOString())
    : undefined

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        {data?.source === 'prometheus' && (
          <Segments
            label="Trend range"
            value={range}
            onChange={(r) => setRange(r)}
            options={CNPG_HISTORY_RANGES}
          />
        )}
        <span className="text-xs text-theme-text-tertiary">
          {fromPrometheus
            ? `From Prometheus · one point every ${span(data.stepSeconds ?? 60)}${data.isolation ? ` · ${data.isolation.note}` : ''}`
            : q.isLoading
              ? 'Checking for Prometheus…'
              : samplingDenied
                ? 'No trend source readable'
                : 'Sampled every 5 s since this page opened · gaps are hatched, never zero'}
        </span>
        {charted && <span className="text-xs text-theme-text-tertiary">Drag across a chart, or click a point, to select an interval.</span>}
      </div>

      {fromPrometheus && data.reason && <Notice>{data.reason}</Notice>}
      {data?.source === 'none' && (
        <Notice>
          History needs Prometheus. {data.reason ?? 'Radar is not connected to one'}.{' '}
          {samplingDenied ? fallback : 'These trends cover only the time since this page opened.'}
        </Notice>
      )}
      {data?.source === 'prometheus' && data.state !== 'ok' && (
        <Notice>
          Prometheus history is not shown: {data.reason}. {fallback}
        </Notice>
      )}
      {q.error && !data && (
        <Notice>
          History could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}. {fallback}
        </Notice>
      )}

      <ChartGroupFilter group={group} />
      {interval && <IntervalChip interval={interval} onOpen={open} onClear={() => setSelected(null)} />}

      {fromPrometheus ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {data.charts.filter((c) => inGroup(group, HISTORY_CHART_GROUP[c.id])).map((c) => (
            <HistoryChartCard key={c.id} chart={c} data={data} selection={interval} onSelect={setSelected} expected={expectedLines(c, standbys, instancePods)} />
          ))}
        </div>
      ) : (
        !q.isLoading &&
        !samplingDenied && (
          <BufferCharts
            samples={samples}
            selection={interval}
            onSelect={setSelected}
            instance={instance}
            picker={picker}
            group={group}
            standbys={standbys}
            instancePods={instancePods}
          />
        )
      )}
    </div>
  )
}

function ChartGroupFilter({ group }: { group?: CNPGChartGroup }) {
  const [, setParams] = useSearchParams()
  const location = useLocation()
  return (
    <Segments
      label="Chart group"
      value={group ?? 'all'}
      onChange={(g) =>
        setParams(
          (prev) => {
            const next = new URLSearchParams(prev)
            if (g === 'all') next.delete('charts')
            else next.set('charts', g)
            return next
          },
          { replace: true, state: location.state },
        )
      }
      options={CNPG_CHART_GROUPS}
    />
  )
}

/**
 * The trend range and selected interval live in the URL (replaced, keeping
 * the navigation state), so Back from Logs or Activity lands on the same view.
 */
function useTrendParams() {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const range = CNPG_HISTORY_RANGES.find((r) => r.id === params.get('trendRange'))?.id ?? '1h'
  const [a, b] = (params.get('trendSel') ?? '').split('-').map(Number)
  const interval: ChartTimeRange | null = Number.isFinite(a) && Number.isFinite(b) && b > a ? { start: a, end: b } : null
  const update = (mutate: (next: URLSearchParams) => void) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        mutate(next)
        return next
      },
      { replace: true, state: location.state },
    )
  return {
    range,
    interval,
    setRange: (r: CNPGHistoryRange) =>
      update((next) => {
        if (r === '1h') next.delete('trendRange')
        else next.set('trendRange', r)
        next.delete('trendSel')
      }),
    setSelected: (sel: ChartTimeRange | null) =>
      update((next) => {
        if (sel) next.set('trendSel', `${Math.floor(sel.start)}-${Math.ceil(sel.end)}`)
        else next.delete('trendSel')
      }),
  }
}

/** The interval a trend selection carried into Logs or Activity, from `?since=&until=`. */
export function useCNPGIntervalParams(): { since: string; until: string; clear: () => void } | null {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const since = params.get('since')
  const until = params.get('until')
  if (!since || !until || !(Date.parse(until) > Date.parse(since))) return null
  return {
    since,
    until,
    clear: () => {
      const next = new URLSearchParams(params)
      next.delete('since')
      next.delete('until')
      setParams(next, { replace: true, state: location.state })
    },
  }
}

export function CNPGIntervalBanner({ since, until, note, onClear }: { since: string; until: string; note: ReactNode; onClear: () => void }) {
  const a = Date.parse(since) / 1000
  const b = Date.parse(until) / 1000
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-accent/40 bg-accent/10 px-3 py-1.5 text-sm">
      <span className="font-medium text-theme-text-primary">
        {new Date(a * 1000).toLocaleDateString()} {clock(a)} – {clock(b)}
      </span>
      <span className="text-xs text-theme-text-secondary">{note}</span>
      <button type="button" onClick={onClear} className="ml-auto rounded-md px-2 py-0.5 text-xs font-medium text-accent-text hover:bg-theme-hover">
        Show all
      </button>
    </div>
  )
}
