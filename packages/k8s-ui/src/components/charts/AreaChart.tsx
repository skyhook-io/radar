import { useCallback, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { seriesColor, seriesFill, computeShortLabels } from './colors'
import { formatMetricValue, formatTimestamp } from './format'
import type { TimeSeries, ReferenceLine } from './types'

export function AreaChart({ series, color, fillColor, unit, referenceLines }: {
  series: TimeSeries[]
  color: string
  fillColor: string
  unit: string
  referenceLines?: ReferenceLine[]
}) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [hoverX, setHoverX] = useState<number | null>(null)
  const multiSeries = series.length > 1

  const chartData = useMemo(() => {
    if (!series.length) return null

    let minTs = Infinity
    let maxTs = -Infinity
    let maxVal = 0

    for (const s of series) {
      for (const dp of s.dataPoints) {
        if (dp.timestamp < minTs) minTs = dp.timestamp
        if (dp.timestamp > maxTs) maxTs = dp.timestamp
        if (dp.value != null && dp.value > maxVal) maxVal = dp.value
      }
    }

    if (minTs === maxTs) maxTs = minTs + 60
    if (maxVal === 0) {
      // Unit-appropriate floor so the Y-axis isn't misleadingly large.
      maxVal = unit === 'cores' ? 0.01 : unit === 'bytes' ? 1024 * 1024 : unit === 'bytes/s' ? 1024 : 1
    }

    // Extend axis to include reference lines so request/limit aren't clipped
    // at the top, which would make usage-vs-limit unreadable.
    if (referenceLines) {
      for (const rl of referenceLines) {
        if (rl.value > maxVal) maxVal = rl.value
      }
    }

    const padding = maxVal * 0.1
    const yMax = maxVal + padding

    return { minTs, maxTs, yMax, series }
  }, [series, unit, referenceLines])

  // Layout constants. marginLeft sized for the widest expected Y-tick label
  // ("422.4 MiB" etc.) — narrow grid panels squeeze the X axis so labels
  // need extra viewBox-space to survive the down-scale.
  const width = 1000
  const height = 300
  const marginLeft = 84
  const marginRight = 40
  const marginTop = 10
  const marginBottom = 30
  const plotWidth = width - marginLeft - marginRight
  const plotHeight = height - marginTop - marginBottom

  // Coord transforms. When chartData is null (empty series) these return 0;
  // the hooks downstream check chartData and bail to empty results so no
  // bad coords ever reach the DOM.
  const toX = (ts: number) => {
    if (!chartData) return marginLeft
    return marginLeft + ((ts - chartData.minTs) / (chartData.maxTs - chartData.minTs)) * plotWidth
  }
  const toY = (val: number) => {
    if (!chartData) return marginTop + plotHeight
    return marginTop + plotHeight - (val / chartData.yMax) * plotHeight
  }

  const yTicks = useMemo(() => {
    if (!chartData) return []
    const { yMax } = chartData
    const count = 4
    return Array.from({ length: count + 1 }, (_, i) => {
      const val = (yMax / count) * i
      return { val, y: toY(val), label: formatMetricValue(val, unit) }
    })
  }, [chartData, unit])

  const xTicks = useMemo(() => {
    if (!chartData) return []
    const { minTs, maxTs } = chartData
    const count = 6
    return Array.from({ length: count + 1 }, (_, i) => {
      const ts = minTs + ((maxTs - minTs) / count) * i
      return { ts, x: toX(ts), label: formatTimestamp(ts) }
    })
  }, [chartData])

  const paths = useMemo(() => {
    if (!chartData) return []
    const segments: {
      linePath: string
      areaPath: string
      strokeColor: string
      areaFillColor: string
      key: string
    }[] = []

    chartData.series.forEach((s, seriesIdx) => {
      const strokeColor = multiSeries ? seriesColor(seriesIdx, color) : color
      const areaFillColor = multiSeries ? seriesFill(seriesIdx, fillColor) : fillColor

      // Split the series into contiguous runs of finite points. A gap
      // (value === undefined) breaks the path so the line/area visibly stops
      // rather than bridging across missing data or dropping to y=0. Each run
      // becomes its own path segment; a run needs >=2 points to render a line.
      let run: { x: number; y: number }[] = []
      let runIdx = 0
      const flush = () => {
        if (run.length >= 2) {
          const linePath = run.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
          const areaPath = linePath +
            ` L${run[run.length - 1].x},${marginTop + plotHeight}` +
            ` L${run[0].x},${marginTop + plotHeight} Z`
          segments.push({
            linePath,
            areaPath,
            strokeColor,
            areaFillColor,
            key: `${seriesIdx}-${runIdx}`,
          })
        }
        run = []
        runIdx++
      }

      for (const dp of s.dataPoints) {
        if (dp.value == null) {
          flush()
          continue
        }
        run.push({ x: toX(dp.timestamp), y: toY(dp.value) })
      }
      flush()
    })

    return segments
  }, [chartData])

  // Hover: only emit a tooltip row when the hovered timestamp lies within
  // the series' actual sample range (with 2× median-step tolerance). Without
  // this filter a series that ended mid-window leaves stale ghost entries.
  const hoverData = useMemo(() => {
    if (!chartData || hoverX === null) return null
    const { minTs, maxTs } = chartData
    const clampedX = Math.max(marginLeft, Math.min(marginLeft + plotWidth, hoverX))
    const frac = (clampedX - marginLeft) / plotWidth
    const ts = minTs + frac * (maxTs - minTs)

    const validSeries = chartData.series
      .map((s, i) => ({ s, i }))
      .filter(({ s }) => s.dataPoints.filter(dp => dp.value != null).length >= 2)

    const fullLabels = validSeries.map(({ s, i }) =>
      s.labels.pod || s.labels.instance || s.labels.node || `series-${i}`
    )
    const shortLabels = computeShortLabels(fullLabels)

    const points = validSeries.map(({ s, i }, vi) => {
      const dps = s.dataPoints
      const seriesMin = dps[0].timestamp
      const seriesMax = dps[dps.length - 1].timestamp
      const medianStep = dps.length >= 2
        ? (seriesMax - seriesMin) / (dps.length - 1)
        : 30
      const tolerance = Math.max(medianStep * 2, 60)
      if (ts < seriesMin - tolerance || ts > seriesMax + tolerance) {
        return null
      }
      // Only snap to finite points — a gap (undefined value) has no value to
      // show and would render NaN in the tooltip / a dot at y=NaN.
      let closest: { timestamp: number; value: number } | null = null
      let closestDist = Infinity
      for (const dp of dps) {
        if (dp.value == null) continue
        const dist = Math.abs(dp.timestamp - ts)
        if (dist < closestDist) {
          closestDist = dist
          closest = { timestamp: dp.timestamp, value: dp.value }
        }
      }
      if (!closest) return null
      return {
        label: shortLabels[vi],
        fullLabel: fullLabels[vi],
        value: closest.value,
        y: toY(closest.value),
        color: multiSeries ? seriesColor(i, color) : color,
      }
    }).filter((p): p is NonNullable<typeof p> => p !== null)

    return { ts, x: clampedX, points }
  }, [hoverX, chartData])

  const handleMouseMove = useCallback((e: React.MouseEvent<SVGRectElement>) => {
    const svg = svgRef.current
    if (!svg) return
    const ctm = svg.getScreenCTM()
    if (!ctm) return
    setHoverX((e.clientX - ctm.e) / ctm.a)
  }, [])

  // Hook calls above run unconditionally; bail out of rendering only after
  // every hook has been invoked (Rules of Hooks).
  if (!chartData) return null

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        className="w-full h-full"
        preserveAspectRatio="xMidYMid meet"
      >
        {/* Grid lines */}
        {yTicks.map((tick, i) => (
          <line
            key={`grid-${i}`}
            x1={marginLeft}
            y1={tick.y}
            x2={width - marginRight}
            y2={tick.y}
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
            x={marginLeft - 8}
            y={tick.y + 4}
            textAnchor="end"
            className="fill-theme-text-secondary"
            fontSize="11"
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
            fontSize="11"
            fontFamily="ui-monospace, monospace"
          >
            {tick.label}
          </text>
        ))}

        {/* Area fills */}
        {paths.map(p => p && (
          <path
            key={`area-${p.key}`}
            d={p.areaPath}
            fill={p.areaFillColor}
          />
        ))}

        {/* Lines */}
        {paths.map(p => p && (
          <path
            key={`line-${p.key}`}
            d={p.linePath}
            fill="none"
            stroke={p.strokeColor}
            strokeWidth="2"
            strokeLinejoin="round"
          />
        ))}

        {/* Reference lines (request / limit overlays). Label sits on a subtle
            background pill so it stays legible against the chart fill. */}
        {referenceLines?.map((rl, i) => {
          const y = Math.max(marginTop, Math.min(marginTop + plotHeight, toY(rl.value)))
          const stroke = rl.kind === 'limit' ? '#f59e0b' : '#94a3b8'
          const labelText = rl.label
          // Sized to fit common label widths ("limit 384MiB", "request 100m"
          // ≈ 90px at fontSize 11). Conservative to prevent right-edge overlap.
          const labelWidth = labelText.length * 6.5 + 10
          const labelHeight = 14
          const labelX = width - marginRight - labelWidth
          const labelY = Math.max(marginTop + labelHeight + 2, y - 6)
          return (
            <g key={`ref-${i}`}>
              <line
                x1={marginLeft}
                y1={y}
                x2={width - marginRight}
                y2={y}
                stroke={stroke}
                strokeWidth="1"
                strokeDasharray="6 4"
                opacity="0.75"
              />
              <rect
                x={labelX}
                y={labelY - labelHeight + 2}
                width={labelWidth}
                height={labelHeight}
                rx="3"
                fill="currentColor"
                className="text-theme-surface"
                opacity="0.85"
              />
              <text
                x={width - marginRight - 5}
                y={labelY - 2}
                textAnchor="end"
                fontSize="11"
                fontFamily="ui-monospace, monospace"
                fontWeight="500"
                fill={stroke}
              >
                {labelText}
              </text>
            </g>
          )
        })}

        {/* Hover crosshair + dots */}
        {hoverData && (
          <>
            <line
              x1={hoverData.x} y1={marginTop}
              x2={hoverData.x} y2={marginTop + plotHeight}
              stroke="currentColor"
              className="text-theme-text-tertiary"
              strokeWidth="1"
              strokeDasharray="4 4"
            />
            {hoverData.points.map((p, i) => (
              <circle
                key={i}
                cx={hoverData.x} cy={p.y}
                r="4"
                fill={p.color}
                stroke="var(--color-theme-surface, #1a1a2e)"
                strokeWidth="2"
              />
            ))}
          </>
        )}

        {/* Invisible overlay for mouse events — must be last for event capture */}
        <rect
          x={marginLeft} y={marginTop}
          width={plotWidth} height={plotHeight}
          fill="transparent"
          style={{ cursor: 'crosshair' }}
          onMouseMove={handleMouseMove}
          onMouseLeave={() => setHoverX(null)}
        />
      </svg>

      {/* Tooltip outside SVG for HTML rendering */}
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
              {new Date(hoverData.ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
            </div>
            {hoverData.points.map((p, i) => (
              <div key={i} className="flex items-center gap-2 py-0.5">
                <div
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{ backgroundColor: p.color }}
                />
                <span className="text-theme-text-secondary font-mono" title={p.fullLabel}>
                  {p.label}
                </span>
                <span className="text-theme-text-primary font-semibold ml-auto pl-3 tabular-nums">
                  {formatMetricValue(p.value, unit)}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
