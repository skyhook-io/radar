import { useEffect, useMemo } from 'react'
import { LineChart } from 'lucide-react'
import { usePromQLRange, usePrometheusStatus, useAutoPromConnect, type PrometheusSeries } from '../../api/client'

/**
 * HPACharts — replicas-over-time chart for an HPA.
 *
 * Sources from KSM `kube_horizontalpodautoscaler_status_{current,desired}_replicas`.
 * Hidden silently when Prom isn't connected or KSM isn't reporting the series.
 *
 * Only the replicas series is plotted — KSM doesn't expose the observed metric
 * the HPA target compares against, so an "observed vs target" chart would need
 * cAdvisor derivation.
 */
export function HPACharts({ data }: { data: any }) {
  // HPA detail can be the first Prometheus-backed surface a user opens; without
  // this, the chart silently stays empty until they open a workload metrics tab.
  useAutoPromConnect()
  const { data: status } = usePrometheusStatus()
  const isConnected = status?.connected === true

  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const spec = data?.spec ?? {}
  const min = spec.minReplicas ?? 1
  const max = spec.maxReplicas

  const currentQuery = useMemo(
    () => `kube_horizontalpodautoscaler_status_current_replicas{namespace="${escapeLabel(namespace)}",horizontalpodautoscaler="${escapeLabel(name)}"}`,
    [namespace, name],
  )
  const desiredQuery = useMemo(
    () => `kube_horizontalpodautoscaler_status_desired_replicas{namespace="${escapeLabel(namespace)}",horizontalpodautoscaler="${escapeLabel(name)}"}`,
    [namespace, name],
  )

  const enabled = isConnected && Boolean(namespace && name)
  const { data: currentRes, error: currentErr } = usePromQLRange(currentQuery, '1h', enabled)
  const { data: desiredRes, error: desiredErr } = usePromQLRange(desiredQuery, '1h', enabled)

  const replicasPoints = useMemo(() => combineSeries({
    current: currentRes?.series,
    desired: desiredRes?.series,
  }), [currentRes, desiredRes])

  // Surface Prom-side failures in the console so an operator debugging a
  // missing HPA chart has a breadcrumb; the chart still hides silently when
  // KSM isn't reporting (the common no-data case). Effect-gated so we log
  // once per error change, not on every re-render.
  useEffect(() => {
    if (currentErr || desiredErr) {
      console.warn('[HPACharts] PromQL query failed', { currentErr, desiredErr })
    }
  }, [currentErr, desiredErr])

  if (!isConnected) return null
  if (!replicasPoints) return null

  return (
    <section className="mt-4 rounded-lg border border-theme-border bg-theme-surface/30 p-3">
      <div className="flex items-center gap-2 mb-3 text-sm font-medium text-theme-text-secondary">
        <LineChart className="w-4 h-4 text-theme-text-tertiary" />
        Activity (last 1h)
      </div>

      <DualLineChart
        title="Replicas"
        height={120}
        primary={{ label: 'current', points: replicasPoints.current, color: '#3b82f6' }}
        secondary={{ label: 'desired', points: replicasPoints.desired, color: '#a855f7', dashed: true }}
        referenceLines={[
          { value: min, label: `min ${min}`, color: '#94a3b8' },
          ...(max != null ? [{ value: max, label: `max ${max}`, color: '#94a3b8' }] : []),
        ]}
        formatY={(v) => v.toFixed(0)}
      />
    </section>
  )
}

// ============================================================================
// Internals
// ============================================================================

interface FlatPoint { timestamp: number; value: number }

function extractFirstSeries(series: PrometheusSeries[]): FlatPoint[] | null {
  for (const s of series) {
    // Drop gap points (null/undefined value) so the line never plots NaN or a
    // false 0. Replica-count series realistically never gap, so the survivors
    // are joined into one continuous line (this bridges a gap, if one occurred —
    // unlike AreaChart, which breaks the path)
    const finite = s.dataPoints.filter((dp): dp is FlatPoint => dp.value != null)
    if (finite.length > 0) {
      return finite.map(dp => ({ timestamp: dp.timestamp, value: dp.value }))
    }
  }
  return null
}

function combineSeries(args: { current?: PrometheusSeries[]; desired?: PrometheusSeries[] }): {
  current: FlatPoint[]
  desired: FlatPoint[]
} | null {
  const current = args.current ? extractFirstSeries(args.current) : null
  const desired = args.desired ? extractFirstSeries(args.desired) : null
  if (!current && !desired) return null
  return {
    current: current ?? [],
    desired: desired ?? [],
  }
}

function escapeLabel(s: string): string {
  return s.replace(/[\\"]/g, '\\$&')
}

// ============================================================================
// DualLineChart — minimal two-line chart for HPA-style time series.
// Deliberately separate from PrometheusCharts.AreaChart: the chart shapes are
// different (line not area, discrete integer Y axis for replicas), and
// reusing the area chart would require adding more knobs to it.
// ============================================================================

interface LineSpec {
  label: string
  points: FlatPoint[]
  color: string
  dashed?: boolean
}

interface RefLine { value: number; label: string; color: string }

function DualLineChart({ title, height, primary, secondary, referenceLines, formatY }: {
  title: string
  height: number
  primary: LineSpec
  secondary?: LineSpec
  referenceLines?: RefLine[]
  formatY: (v: number) => string
}) {
  const allPoints = [...primary.points, ...(secondary?.points ?? [])]
  if (allPoints.length === 0) {
    return (
      <div className="text-xs text-theme-text-tertiary">{title} — no data</div>
    )
  }

  const minTs = Math.min(...allPoints.map(p => p.timestamp))
  const maxTs = Math.max(...allPoints.map(p => p.timestamp))
  const tsSpan = Math.max(maxTs - minTs, 60)

  let maxV = Math.max(...allPoints.map(p => p.value), 1)
  if (referenceLines) {
    for (const rl of referenceLines) maxV = Math.max(maxV, rl.value)
  }
  // Add 10% headroom so the top line isn't flush with the top edge.
  maxV = maxV * 1.1

  const width = 600
  const marginL = 36
  const marginR = 16
  const marginT = 4
  const marginB = 18
  const plotW = width - marginL - marginR
  const plotH = height - marginT - marginB

  const toX = (ts: number) => marginL + ((ts - minTs) / tsSpan) * plotW
  const toY = (v: number) => marginT + plotH - (v / maxV) * plotH

  const drawLine = (spec: LineSpec) => {
    if (spec.points.length === 0) return null
    const d = spec.points.map((p, i) => `${i === 0 ? 'M' : 'L'}${toX(p.timestamp).toFixed(1)},${toY(p.value).toFixed(1)}`).join(' ')
    return (
      <path
        d={d}
        fill="none"
        stroke={spec.color}
        strokeWidth="1.75"
        strokeLinejoin="round"
        strokeDasharray={spec.dashed ? '4 3' : undefined}
      />
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-1.5">
        <span className="text-xs text-theme-text-secondary">{title}</span>
        <div className="flex items-center gap-3">
          <Legend color={primary.color} label={primary.label} />
          {secondary && <Legend color={secondary.color} label={secondary.label} dashed />}
        </div>
      </div>
      <svg viewBox={`0 0 ${width} ${height}`} className="w-full" preserveAspectRatio="none">
        {/* Y ticks */}
        {[0, 0.5, 1].map(frac => {
          const v = maxV * frac
          const y = toY(v)
          return (
            <g key={frac}>
              <line x1={marginL} y1={y} x2={width - marginR} y2={y} stroke="currentColor" className="text-theme-border/30" strokeWidth="1" />
              <text x={marginL - 4} y={y + 3} textAnchor="end" fontSize="9" fontFamily="ui-monospace, monospace" className="fill-theme-text-tertiary">
                {formatY(v)}
              </text>
            </g>
          )
        })}
        {/* Reference lines */}
        {referenceLines?.map((rl, i) => {
          const y = toY(rl.value)
          return (
            <g key={`rl-${i}`}>
              <line x1={marginL} y1={y} x2={width - marginR} y2={y} stroke={rl.color} strokeWidth="1" strokeDasharray="3 3" opacity="0.6" />
              <text x={width - marginR - 4} y={y - 2} textAnchor="end" fontSize="9" fontFamily="ui-monospace, monospace" fill={rl.color} opacity="0.85">
                {rl.label}
              </text>
            </g>
          )
        })}
        {drawLine(primary)}
        {secondary && drawLine(secondary)}
      </svg>
    </div>
  )
}

function Legend({ color, label, dashed }: { color: string; label: string; dashed?: boolean }) {
  return (
    <span className="flex items-center gap-1 text-[11px] text-theme-text-tertiary">
      <svg width="14" height="6" aria-hidden>
        <line x1="0" y1="3" x2="14" y2="3" stroke={color} strokeWidth="1.75" strokeDasharray={dashed ? '3 2' : undefined} />
      </svg>
      {label}
    </span>
  )
}
