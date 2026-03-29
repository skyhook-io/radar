import { useState, useMemo, useRef, useCallback } from 'react'
import { clsx } from 'clsx'
import { Loader2, TrendingUp } from 'lucide-react'
import {
  useOpenCostTrend,
  type CostTimeRange,
  type OpenCostTrendSeries,
} from '../../api/client'

const SERIES_COLORS = [
  '#3b82f6', // blue-500
  '#10b981', // emerald-500
  '#f97316', // orange-500
  '#a855f7', // purple-500
  '#ec4899', // pink-500
  '#eab308', // yellow-500
  '#06b6d4', // cyan-500
  '#84cc16', // lime-500
  '#ef4444', // red-500
]

const TIME_RANGES: { value: CostTimeRange; label: string }[] = [
  { value: '6h', label: '6h' },
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
]

export function CostTrendChart() {
  const [timeRange, setTimeRange] = useState<CostTimeRange>('24h')
  const { data, isLoading } = useOpenCostTrend(timeRange)

  if (isLoading) {
    return (
      <div className="rounded-lg border border-theme-border bg-theme-surface/50 p-4">
        <div className="flex items-center justify-center h-[200px] text-theme-text-tertiary">
          <Loader2 className="w-5 h-5 animate-spin mr-2" />
          Loading cost trend...
        </div>
      </div>
    )
  }

  if (!data?.available || !data.series?.length) {
    return null
  }

  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50">
      <div className="flex items-center justify-between px-4 py-2.5 border-b border-theme-border">
        <div className="flex items-center gap-2">
          <TrendingUp className="w-4 h-4 text-theme-text-tertiary" />
          <span className="text-xs font-medium text-theme-text-secondary">Cost Trend</span>
        </div>
        <div className="flex items-center gap-1">
          {TIME_RANGES.map(tr => (
            <button
              key={tr.value}
              onClick={() => setTimeRange(tr.value)}
              className={clsx(
                'px-2 py-1 text-xs rounded-md transition-colors',
                timeRange === tr.value
                  ? 'bg-skyhook-600/20 text-blue-400 font-medium'
                  : 'text-theme-text-quaternary hover:text-theme-text-tertiary'
              )}
            >
              {tr.label}
            </button>
          ))}
        </div>
      </div>
      <div className="p-4">
        <StackedAreaChart series={data.series} />
        <ChartLegend series={data.series} />
      </div>
    </div>
  )
}

function StackedAreaChart({ series }: { series: OpenCostTrendSeries[] }) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [hoverX, setHoverX] = useState<number | null>(null)

  const width = 1000
  const height = 240
  const marginLeft = 55
  const marginRight = 15
  const marginTop = 8
  const marginBottom = 28
  const plotWidth = width - marginLeft - marginRight
  const plotHeight = height - marginTop - marginBottom

  // All heavy computation in a single useMemo to avoid hooks-after-early-return
  const chartData = useMemo(() => {
    if (!series.length) return null

    // Collect all unique timestamps and sort
    const tsSet = new Set<number>()
    for (const s of series) {
      for (const dp of s.dataPoints) {
        tsSet.add(dp.timestamp)
      }
    }
    const timestamps = Array.from(tsSet).sort((a, b) => a - b)
    if (timestamps.length < 2) return null

    const minTs = timestamps[0]
    const maxTs = timestamps[timestamps.length - 1]

    const seriesLookups = series.map(s => {
      const map = new Map<number, number>()
      for (const dp of s.dataPoints) {
        map.set(dp.timestamp, dp.value)
      }
      return map
    })

    // Compute stacked values at each timestamp
    const stacked: number[][] = []
    let maxVal = 0
    for (let si = 0; si < series.length; si++) {
      stacked.push([])
      for (let ti = 0; ti < timestamps.length; ti++) {
        const val = seriesLookups[si].get(timestamps[ti]) ?? 0
        const prev = si > 0 ? stacked[si - 1][ti] : 0
        const cumVal = prev + val
        stacked[si].push(cumVal)
        if (cumVal > maxVal) maxVal = cumVal
      }
    }

    if (maxVal === 0) maxVal = 1
    const yMax = maxVal + maxVal * 0.1

    const toX = (ts: number) => marginLeft + ((ts - minTs) / (maxTs - minTs)) * plotWidth
    const toY = (val: number) => marginTop + plotHeight - (val / yMax) * plotHeight

    // Y axis ticks
    const tickCount = 4
    const yTicks = Array.from({ length: tickCount + 1 }, (_, i) => {
      const val = (yMax / tickCount) * i
      return { val, y: toY(val), label: formatCostAxis(val) }
    })

    // X axis ticks
    const xTickCount = 6
    const xTicks = Array.from({ length: xTickCount + 1 }, (_, i) => {
      const ts = minTs + ((maxTs - minTs) / xTickCount) * i
      return { ts, x: toX(ts), label: formatTimestamp(ts) }
    })

    // Build stacked area paths
    const paths = series.map((_, si) => {
      const topPoints = timestamps.map((ts, ti) => ({ x: toX(ts), y: toY(stacked[si][ti]) }))
      const bottomPoints = si > 0
        ? timestamps.map((ts, ti) => ({ x: toX(ts), y: toY(stacked[si - 1][ti]) }))
        : timestamps.map(ts => ({ x: toX(ts), y: toY(0) }))

      const topPath = topPoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
      const bottomPath = [...bottomPoints].reverse().map((p, i) => `${i === 0 ? 'L' : 'L'}${p.x},${p.y}`).join(' ')
      const areaPath = topPath + ' ' + bottomPath + ' Z'
      const linePath = topPoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')

      return { areaPath, linePath, color: SERIES_COLORS[si % SERIES_COLORS.length] }
    })

    return { timestamps, stacked, minTs, maxTs, yMax, seriesLookups, toX, toY, yTicks, xTicks, paths }
  }, [series])

  // Hover data — depends on hoverX + chartData, must be a separate hook (called unconditionally)
  const hoverData = useMemo(() => {
    if (hoverX === null || !chartData) return null
    const { timestamps, minTs, maxTs, seriesLookups, toX } = chartData
    const clampedX = Math.max(marginLeft, Math.min(marginLeft + plotWidth, hoverX))
    const frac = (clampedX - marginLeft) / plotWidth
    const ts = minTs + frac * (maxTs - minTs)

    let closestTi = 0
    let closestDist = Infinity
    for (let ti = 0; ti < timestamps.length; ti++) {
      const dist = Math.abs(timestamps[ti] - ts)
      if (dist < closestDist) {
        closestDist = dist
        closestTi = ti
      }
    }

    const closestTs = timestamps[closestTi]
    let total = 0
    const points = series.map((s, si) => {
      const val = seriesLookups[si].get(closestTs) ?? 0
      total += val
      return { namespace: s.namespace, value: val, color: SERIES_COLORS[si % SERIES_COLORS.length] }
    })

    return { ts: closestTs, x: toX(closestTs), total, points }
  }, [hoverX, chartData, series])

  const handleMouseMove = useCallback((e: React.MouseEvent<SVGRectElement>) => {
    const svg = svgRef.current
    if (!svg) return
    const ctm = svg.getScreenCTM()
    if (!ctm) return
    setHoverX((e.clientX - ctm.e) / ctm.a)
  }, [])

  // Early return AFTER all hooks
  if (!chartData) return null

  const { yTicks, xTicks, paths } = chartData

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        className="w-full"
        preserveAspectRatio="xMidYMid meet"
      >
        {/* Grid lines */}
        {yTicks.map((tick, i) => (
          <line
            key={`grid-${i}`}
            x1={marginLeft} y1={tick.y}
            x2={width - marginRight} y2={tick.y}
            stroke="currentColor"
            className="text-theme-border/30"
            strokeWidth="1"
            strokeDasharray={i === 0 ? undefined : '4 4'}
          />
        ))}

        {/* Y axis labels */}
        {yTicks.map((tick, i) => (
          <text
            key={`ylabel-${i}`}
            x={marginLeft - 6}
            y={tick.y + 4}
            textAnchor="end"
            className="fill-theme-text-secondary"
            fontSize="10"
            fontFamily="ui-monospace, monospace"
          >
            {tick.label}
          </text>
        ))}

        {/* X axis labels */}
        {xTicks.map((tick, i) => (
          <text
            key={`xlabel-${i}`}
            x={tick.x}
            y={height - 4}
            textAnchor="middle"
            className="fill-theme-text-secondary"
            fontSize="10"
            fontFamily="ui-monospace, monospace"
          >
            {tick.label}
          </text>
        ))}

        {/* Stacked area fills (render bottom to top) */}
        {paths.map((p, i) => (
          <path
            key={`area-${i}`}
            d={p.areaPath}
            fill={p.color + '33'}
          />
        ))}

        {/* Lines (top edges of each area) */}
        {paths.map((p, i) => (
          <path
            key={`line-${i}`}
            d={p.linePath}
            fill="none"
            stroke={p.color}
            strokeWidth="1.5"
            strokeLinejoin="round"
          />
        ))}

        {/* Hover crosshair */}
        {hoverData && (
          <line
            x1={hoverData.x} y1={marginTop}
            x2={hoverData.x} y2={marginTop + plotHeight}
            stroke="currentColor"
            className="text-theme-text-tertiary"
            strokeWidth="1"
            strokeDasharray="4 4"
          />
        )}

        {/* Mouse event overlay */}
        <rect
          x={marginLeft} y={marginTop}
          width={plotWidth} height={plotHeight}
          fill="transparent"
          style={{ cursor: 'crosshair' }}
          onMouseMove={handleMouseMove}
          onMouseLeave={() => setHoverX(null)}
        />
      </svg>

      {/* Tooltip */}
      {hoverData && (
        <div
          className="absolute top-0 pointer-events-none z-10"
          style={{
            left: `${(hoverData.x / width) * 100}%`,
            transform: hoverData.x > width * 0.65 ? 'translateX(calc(-100% - 12px))' : 'translateX(12px)',
          }}
        >
          <div className="bg-theme-surface border border-theme-border rounded-lg shadow-lg px-3 py-2 text-xs whitespace-nowrap">
            <div className="text-theme-text-tertiary mb-1.5 font-mono">
              {new Date(hoverData.ts * 1000).toLocaleString([], {
                month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
              })}
            </div>
            {hoverData.points
              .filter(p => p.value > 0)
              .sort((a, b) => b.value - a.value)
              .map((p, i) => (
                <div key={i} className="flex items-center gap-2 py-0.5">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ backgroundColor: p.color }}
                  />
                  <span className="text-theme-text-secondary">{p.namespace}</span>
                  <span className="text-theme-text-primary font-semibold ml-auto pl-3 tabular-nums">
                    {formatCostTooltip(p.value)}
                  </span>
                </div>
              ))}
            <div className="border-t border-theme-border/50 mt-1 pt-1 flex justify-between text-theme-text-primary font-semibold">
              <span>Total</span>
              <span className="tabular-nums">{formatCostTooltip(hoverData.total)}</span>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function ChartLegend({ series }: { series: OpenCostTrendSeries[] }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 mt-2">
      {series.map((s, i) => (
        <div key={s.namespace} className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
          <div
            className="w-2.5 h-2.5 rounded-full shrink-0"
            style={{ backgroundColor: SERIES_COLORS[i % SERIES_COLORS.length] }}
          />
          <span>{s.namespace}</span>
        </div>
      ))}
    </div>
  )
}

function formatCostAxis(value: number): string {
  if (value >= 1000) return `$${(value / 1000).toFixed(0)}k`
  if (value >= 1) return `$${value.toFixed(1)}`
  if (value >= 0.01) return `$${value.toFixed(2)}`
  if (value > 0) return `$${value.toFixed(3)}`
  return '$0'
}

function formatCostTooltip(value: number): string {
  if (value >= 1000) return `$${(value / 1000).toFixed(1)}k/hr`
  if (value >= 1) return `$${value.toFixed(2)}/hr`
  if (value >= 0.01) return `$${value.toFixed(3)}/hr`
  if (value > 0) return `$${value.toFixed(4)}/hr`
  return '$0.00/hr'
}

function formatTimestamp(unix: number): string {
  const d = new Date(unix * 1000)
  const now = new Date()
  const diffHours = (now.getTime() - d.getTime()) / (1000 * 60 * 60)
  // Show date+time for ranges > 24h, just time otherwise
  if (diffHours > 36) {
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  }
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
