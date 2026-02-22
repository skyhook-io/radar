import { clsx } from 'clsx'
import { type ReactNode } from 'react'
import { Tooltip } from './Tooltip'

type ColorScheme = 'utilization' | 'count'

interface ResourceBarProps {
  /** Formatted usage string (e.g., "847m", "2.1Gi", "17") */
  used: string
  /** Formatted total/allocatable string (e.g., "1930m", "7.6Gi", "110") */
  total: string
  /** Usage percentage (0–100) */
  percent: number
  /** Color scheme: "utilization" (green/yellow/red) or "count" (blue/yellow) */
  colorScheme?: ColorScheme
  /** Optional marker line position (0–100), e.g., request as % of limit */
  markerPercent?: number
  /** Optional tooltip content shown on hover */
  tooltip?: ReactNode
}

function getBarColor(percent: number, scheme: ColorScheme): string {
  if (scheme === 'count') {
    return percent > 90 ? 'bg-yellow-500' : 'bg-blue-500'
  }
  if (percent > 85) return 'bg-red-500'
  if (percent > 60) return 'bg-yellow-500'
  return 'bg-green-500'
}

export function ResourceBar({ used, total, percent, colorScheme = 'utilization', markerPercent, tooltip }: ResourceBarProps) {
  const bar = (
    <div className="flex flex-col gap-0.5 min-w-0">
      <div className="flex items-baseline justify-between gap-1">
        <span className="text-xs font-mono text-theme-text-secondary truncate">
          {used} / {total}
        </span>
        <span className="text-[10px] font-mono text-theme-text-tertiary shrink-0">
          {Math.round(percent)}%
        </span>
      </div>
      <div className="relative">
        <div className="h-1.5 rounded-full border border-theme-border bg-theme-bg-elevated overflow-hidden">
          <div
            className={clsx('h-full rounded-full transition-[width] duration-300 ease-out', getBarColor(percent, colorScheme))}
            style={{ width: `${Math.min(percent, 100)}%` }}
          />
        </div>
        {markerPercent != null && markerPercent > 0 && markerPercent <= 100 && (
          <div
            className="absolute -top-[1px] h-[calc(100%+2px)] w-[2px] bg-theme-text-primary/70"
            style={{ left: `${markerPercent}%` }}
          />
        )}
      </div>
    </div>
  )

  if (tooltip) {
    return (
      <Tooltip content={tooltip} delay={200} position="top" wrapperClassName="w-full min-w-0">
        {bar}
      </Tooltip>
    )
  }

  return bar
}
