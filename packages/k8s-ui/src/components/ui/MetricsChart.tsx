import { useMemo } from 'react'
import { clsx } from 'clsx'
import type { MetricsDataPoint } from '../../types/core'
import { formatCPUNanocores, formatMemoryBytes, parseCPUToNanocores, parseMemoryToBytes } from '../../utils/format'

/**
 * Round up a value to a "nice" number for chart axes.
 * Nice numbers are 1, 2, 5 multiplied by powers of 10.
 */
function niceRoundUp(value: number): number {
  if (value <= 0) return 0

  // Find the order of magnitude
  const magnitude = Math.pow(10, Math.floor(Math.log10(value)))
  const normalized = value / magnitude // Will be between 1 and 10

  // Round up to nice intervals: 1, 2, 5, 10
  let nice: number
  if (normalized <= 1) {
    nice = 1
  } else if (normalized <= 2) {
    nice = 2
  } else if (normalized <= 5) {
    nice = 5
  } else {
    nice = 10
  }

  return nice * magnitude
}

interface MetricsChartProps {
  dataPoints: MetricsDataPoint[]
  type: 'cpu' | 'memory'
  height?: number
  className?: string
  showAxis?: boolean
  /** K8s resource limit string (e.g., "500m", "1Gi") */
  limit?: string
  /** K8s resource request string (e.g., "100m", "256Mi") */
  request?: string
}

export function MetricsChart({
  dataPoints,
  type,
  height = 60,
  className,
  showAxis = true,
  limit,
  request,
}: MetricsChartProps) {
  // Parse limit and request to the same unit as data (nanocores or bytes)
  const parseValue = type === 'cpu' ? parseCPUToNanocores : parseMemoryToBytes
  const limitValue = limit ? parseValue(limit) : undefined
  const requestValue = request ? parseValue(request) : undefined

  const { values, chartMax, dataMax, current, timeRange } = useMemo(() => {
    if (!dataPoints || dataPoints.length === 0) {
      return { values: [], chartMax: 0, dataMax: 0, current: 0, timeRange: '' }
    }

    const vals = dataPoints.map(d => type === 'cpu' ? d.cpu : d.memory)
    const maxVal = Math.max(...vals)
    const currentVal = vals[vals.length - 1]

    // Chart max should include limit if present and higher than data
    let chartMaxVal = maxVal
    if (limitValue && limitValue > chartMaxVal) {
      chartMaxVal = limitValue
    }
    // Round up to a nice number for clean axis labels
    chartMaxVal = niceRoundUp(chartMaxVal * 1.05) // Small padding before rounding

    // Calculate time range
    const firstTime = new Date(dataPoints[0].timestamp)
    const lastTime = new Date(dataPoints[dataPoints.length - 1].timestamp)
    const diffMinutes = Math.round((lastTime.getTime() - firstTime.getTime()) / 60000)
    const range = diffMinutes > 60 ? `${Math.round(diffMinutes / 60)}h` : `${diffMinutes}m`

    return {
      values: vals,
      chartMax: chartMaxVal,
      dataMax: maxVal,
      current: currentVal,
      timeRange: range,
    }
  }, [dataPoints, type, limitValue])

  if (values.length === 0) {
    return (
      <div className={clsx('flex items-center justify-center text-theme-text-tertiary text-xs', className)} style={{ height }}>
        No metrics data yet
      </div>
    )
  }

  const color = type === 'cpu' ? 'bg-blue-500' : 'bg-purple-500'
  const format = type === 'cpu' ? formatCPUNanocores : formatMemoryBytes

  // Calculate line positions (from bottom, as percentage)
  const limitPercent = limitValue && chartMax > 0 ? (limitValue / chartMax) * 100 : undefined
  const requestPercent = requestValue && chartMax > 0 ? (requestValue / chartMax) * 100 : undefined

  // Y-axis values
  const yAxisMax = chartMax
  const yAxisMid = chartMax / 2

  return (
    <div className={clsx('flex flex-col', className)}>
      {/* Chart with Y-axis */}
      <div className="flex">
        {/* Y-axis labels */}
        <div className="flex flex-col justify-between text-[9px] text-theme-text-tertiary pr-1 w-12 text-right font-mono" style={{ height }}>
          <span className="leading-none">{format(yAxisMax)}</span>
          <span className="leading-none">{format(yAxisMid)}</span>
          <span className="leading-none">0</span>
        </div>

        {/* Chart area */}
        <div className="relative flex-1" style={{ height }}>
          {/* Bars */}
          <div className="absolute inset-0 flex items-end">
            {values.map((value, i) => {
              const normalizedHeight = chartMax > 0 ? (value / chartMax) * 100 : 0
              const barHeight = Math.max((normalizedHeight / 100) * height, 2)
              return (
                <div
                  key={i}
                  className="flex-1 px-px flex items-end"
                  title={`${format(value)} at ${new Date(dataPoints[i].timestamp).toLocaleTimeString()}`}
                >
                  <div
                    className={clsx(color, 'w-full rounded-t-sm opacity-70 hover:opacity-100 transition-opacity')}
                    style={{ height: barHeight }}
                  />
                </div>
              )
            })}
          </div>

          {/* Limit line */}
          {limitPercent !== undefined && limitPercent <= 100 && (
            <div
              className="absolute left-0 right-0 border-t-2 border-red-500 border-dashed pointer-events-none"
              style={{ bottom: `${limitPercent}%` }}
              title={`Limit: ${limit}`}
            />
          )}

          {/* Request line */}
          {requestPercent !== undefined && requestPercent <= 100 && (
            <div
              className="absolute left-0 right-0 border-t-2 border-yellow-500 border-dashed pointer-events-none"
              style={{ bottom: `${requestPercent}%` }}
              title={`Request: ${request}`}
            />
          )}

          {/* Grid lines */}
          <div className="absolute inset-0 flex flex-col justify-between pointer-events-none">
            <div className="border-b border-theme-border/20" />
            <div className="border-b border-theme-border/20" />
            <div className="border-b border-theme-border/10" />
          </div>
        </div>

        {/* Right-side labels for limit/request */}
        {(limitPercent !== undefined || requestPercent !== undefined) && (
          <div className="relative w-8 pl-1" style={{ height }}>
            {limitPercent !== undefined && limitPercent <= 100 && (
              <span
                className="absolute text-[9px] text-red-400 leading-none"
                style={{ bottom: `${limitPercent}%`, transform: 'translateY(50%)' }}
              >
                limit
              </span>
            )}
            {requestPercent !== undefined && requestPercent <= 100 && (
              <span
                className="absolute text-[9px] text-yellow-400 leading-none"
                style={{ bottom: `${requestPercent}%`, transform: 'translateY(50%)' }}
              >
                req
              </span>
            )}
          </div>
        )}
      </div>

      {/* X-axis labels */}
      {showAxis && (
        <div className="flex justify-between text-[10px] text-theme-text-tertiary mt-1 ml-12 mr-8">
          <span>{timeRange} ago</span>
          <span>now</span>
        </div>
      )}

      {/* Current value */}
      <div className="flex items-baseline gap-2 mt-1 ml-12 font-mono">
        <span className={clsx(
          'text-sm font-medium',
          type === 'cpu' ? 'text-blue-400' : 'text-purple-400'
        )}>
          {format(current)}
        </span>
        <span className="text-[10px] text-theme-text-tertiary">
          (max: {format(dataMax)})
        </span>
      </div>
    </div>
  )
}

// Compact sparkline version for inline use
interface SparklineProps {
  dataPoints: MetricsDataPoint[]
  type: 'cpu' | 'memory'
  width?: number
  height?: number
  className?: string
}

export function MetricsSparkline({
  dataPoints,
  type,
  width = 80,
  height = 24,
  className,
}: SparklineProps) {
  const values = useMemo(() => {
    if (!dataPoints || dataPoints.length === 0) return []
    return dataPoints.map(d => type === 'cpu' ? d.cpu : d.memory)
  }, [dataPoints, type])

  if (values.length < 2) {
    return null
  }

  const max = Math.max(...values)
  const points = values.map((v, i) => {
    const x = (i / (values.length - 1)) * width
    const y = max > 0 ? height - (v / max) * height : height
    return `${x},${y}`
  }).join(' ')

  const color = type === 'cpu' ? '#3b82f6' : '#a855f7'

  return (
    <svg width={width} height={height} className={className}>
      <polyline
        fill="none"
        stroke={color}
        strokeWidth="1.5"
        points={points}
      />
    </svg>
  )
}
