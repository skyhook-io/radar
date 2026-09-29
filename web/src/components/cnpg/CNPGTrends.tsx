import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { X } from 'lucide-react'
import { Tooltip } from '@skyhook-io/k8s-ui'
import { AreaChart, SeriesLegend, type ChartTimeRange, type ReferenceLine, type TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
import type { CNPGRuntimeResponse } from '../../api/cnpg'
import {
  CNPG_HISTORY_RANGES,
  useCNPGClusterHistory,
  type CNPGHistoryChart,
  type CNPGHistoryRange,
  type CNPGClusterHistoryResponse,
} from '../../api/cnpg-history'
import { Notice } from '../capacity/shared'
import { Segments } from './shared'

const COLOR = '#60a5fa'
const FILL = '#60a5fa22'

// A ring buffer of samples taken while this view is open: the Trends section
// without Prometheus. It says so, and starts empty.
export interface Sample {
  t: number
  /** When the metrics were scraped; the server memoizes them across polls. */
  metricsAt?: number
  replayLag: Record<string, number | undefined>
  sessions?: number
  waiting?: number
  commits?: number
  rollbacks?: number
  archived?: number
  failed?: number
}

export function useSampleBuffer(data: CNPGRuntimeResponse | undefined): Sample[] {
  const [samples, setSamples] = useState<Sample[]>([])
  const last = useRef<string | null>(null)
  useEffect(() => {
    if (!data || data.sampledAt === last.current) return
    last.current = data.sampledAt
    const primary = data.instances.find((i) => i.role === 'primary')
    const replayLag: Record<string, number | undefined> = {}
    for (const r of primary?.status.replication ?? []) replayLag[r.applicationName] = r.replayLag
    const m = primary?.metrics
    setSamples((prev) =>
      [
        ...prev,
        {
          t: Date.parse(data.sampledAt) || Date.now(),
          metricsAt: m?.capturedAt ? Date.parse(m.capturedAt) || undefined : undefined,
          replayLag,
          sessions: m?.state === 'ok' ? m.sessionsTotal : undefined,
          waiting: m?.state === 'ok' ? m.waitingBackends : undefined,
          commits: m?.xactCommitTotal,
          rollbacks: m?.xactRollbackTotal,
          archived: m?.archiver?.archivedCount,
          failed: m?.archiver?.failedCount,
        },
      ].slice(-720),
    )
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
      return `No access: needs ${chart.grant ?? 'more permissions'}.`
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
}: {
  chart: CNPGHistoryChart
  data: CNPGClusterHistoryResponse
  selection: ChartTimeRange | null
  onSelect: (r: ChartTimeRange) => void
}) {
  const start = Date.parse(data.start ?? '') / 1000
  const end = Date.parse(data.end ?? '') / 1000
  const step = data.stepSeconds ?? 60
  const series: TimeSeries[] = chart.series
  const labels = series.map((s) => s.labels[chart.seriesBy] ?? chart.title)
  const gaps = useMemo(() => historyGaps(series, start, end, step), [series, start, end, step])
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
          {chart.omitted ? ` · ${chart.omitted} more series not shown` : ''}
          {chart.state === 'ok' && gaps.length > 0 ? ' · hatched: no sample' : ''}
        </>
      }
    >
      {chart.state === 'ok' ? (
        <>
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
        <div className="flex h-24 items-center justify-center rounded-lg border border-dashed border-theme-border px-4 text-center text-sm text-theme-text-tertiary">
          {stateText(chart)}
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

function rateSeries(samples: Sample[], key: 'commits' | 'rollbacks'): TimeSeries {
  const points: TimeSeries['dataPoints'] = []
  let prev: Sample | undefined
  for (const s of samples) {
    if (s[key] === undefined || s.metricsAt === undefined) {
      points.push({ timestamp: s.t / 1000, value: null })
      continue
    }
    if (prev && prev.metricsAt !== s.metricsAt) {
      const dt = ((s.metricsAt as number) - (prev.metricsAt as number)) / 1000
      const dv = (s[key] as number) - (prev[key] as number)
      points.push({ timestamp: s.t / 1000, value: dt > 0 && dv >= 0 ? dv / dt : null })
    }
    if (!prev || prev.metricsAt !== s.metricsAt) prev = s
  }
  return { labels: { series: key }, dataPoints: points }
}

function BufferCharts({ samples, selection, onSelect }: { samples: Sample[]; selection: ChartTimeRange | null; onSelect: (r: ChartTimeRange) => void }) {
  const recent = samples.slice(-720)
  const pods = useMemo(() => [...new Set(recent.flatMap((s) => Object.keys(s.replayLag)))].sort(), [recent])
  if (recent.length < 2) {
    return <div className="text-sm text-theme-text-tertiary">Collecting samples… These trends start when this page opens and cover up to the last hour it stays open.</div>
  }
  const domain = { start: recent[0].t / 1000, end: recent[recent.length - 1].t / 1000 }
  const one = (label: string, pick: (s: Sample) => number | undefined): TimeSeries => ({
    labels: { series: label },
    dataPoints: recent.map((s) => ({ timestamp: s.t / 1000, value: pick(s) ?? null })),
  })
  const charts: { title: string; unit: string; series: TimeSeries[]; labels: string[]; source: string; thresholds?: ReferenceLine[] }[] = [
    {
      title: 'Replay lag per standby',
      unit: 'seconds',
      series: pods.map((p) => one(p, (s) => s.replayLag[p])),
      labels: pods,
      source: "the primary's pg_stat_replication",
      thresholds: [
        { value: 5, label: '5 s', kind: 'request' },
        { value: 30, label: '30 s', kind: 'limit' },
      ],
    },
    { title: 'Client sessions (primary)', unit: 'count', series: [one('sessions', (s) => s.sessions)], labels: ['sessions'], source: 'cnpg_backends_total on the primary' },
    { title: 'Sessions waiting on locks', unit: 'count', series: [one('waiting', (s) => s.waiting)], labels: ['waiting'], source: 'cnpg_backends_waiting_total on the primary' },
    { title: 'Transactions per second (primary)', unit: '', series: [rateSeries(recent, 'commits'), rateSeries(recent, 'rollbacks')], labels: ['commits', 'rollbacks'], source: 'xact_commit / xact_rollback between exporter refreshes' },
  ]
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {charts.map((c) => {
        const gaps = sampleGaps(c.series)
        const hasValue = c.series.some((s) => s.dataPoints.some((p) => p.value != null))
        return (
          <ChartCard key={c.title} title={c.title} footer={<>Source: {c.source}, sampled by this page · gaps break the line, never zero</>}>
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
                {c.title.startsWith('Replay') ? 'No standby reported by the primary since this page opened.' : 'No sample since this page opened.'}
              </div>
            )}
          </ChartCard>
        )
      })}
    </div>
  )
}

/**
 * The Runtime Trends section: Prometheus range queries when Radar has
 * Prometheus, otherwise the samples this page took since it opened. Selecting
 * a time range on any chart carries both bounds to Logs and Activity.
 */
export function CNPGTrends({
  namespace,
  name,
  samples,
  onOpenInterval,
}: {
  namespace: string
  name: string
  samples: Sample[]
  onOpenInterval?: (target: CNPGIntervalTarget, since: string, until: string) => void
}) {
  const [range, setRange] = useState<CNPGHistoryRange>('1h')
  const [interval, setSelected] = useState<ChartTimeRange | null>(null)
  const q = useCNPGClusterHistory(namespace, name, range)
  const data = q.data
  const fromPrometheus = data?.source === 'prometheus' && data.state === 'ok'

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
            onChange={(r) => {
              setRange(r)
              setSelected(null)
            }}
            options={CNPG_HISTORY_RANGES}
          />
        )}
        <span className="text-xs text-theme-text-tertiary">
          {fromPrometheus
            ? `From Prometheus · one point every ${span(data.stepSeconds ?? 60)} · ${data.isolation?.note ?? ''}`
            : q.isLoading
              ? 'Checking for Prometheus…'
              : 'Sampled every 5 s since this page opened · gaps are hatched, never zero'}
        </span>
        <span className="text-xs text-theme-text-tertiary">Drag across a chart, or click a point, to select an interval.</span>
      </div>

      {data?.source === 'none' && (
        <Notice>History needs Prometheus: {data.reason ?? 'Radar is not connected to one'}. These trends cover only the time since this page opened.</Notice>
      )}
      {data?.source === 'prometheus' && data.state !== 'ok' && (
        <Notice>Prometheus history is not shown: {data.reason}. Below: samples since this page opened.</Notice>
      )}
      {q.error && !data && <Notice>History could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}. Below: samples since this page opened.</Notice>}

      {interval && <IntervalChip interval={interval} onOpen={open} onClear={() => setSelected(null)} />}

      {fromPrometheus ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {data.charts.map((c) => (
            <HistoryChartCard key={c.id} chart={c} data={data} selection={interval} onSelect={setSelected} />
          ))}
        </div>
      ) : (
        !q.isLoading && <BufferCharts samples={samples} selection={interval} onSelect={setSelected} />
      )}
    </div>
  )
}

/** The interval a trend selection carried into Logs or Activity, from `?since=&until=`. */
export function useCNPGIntervalParams(): { since: string; until: string; clear: () => void } | null {
  const [params, setParams] = useSearchParams()
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
      setParams(next, { replace: true })
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
