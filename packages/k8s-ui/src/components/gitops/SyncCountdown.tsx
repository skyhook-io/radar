import { useState, useEffect } from 'react'
import { Clock, RefreshCw } from 'lucide-react'
import { clsx } from 'clsx'

interface SyncCountdownProps {
  /** The reconciliation interval (e.g., "5m", "1h", "30s") */
  interval: string
  /** Last reconciliation time (ISO string) */
  lastSyncTime?: string
  /** Whether the resource is suspended */
  suspended?: boolean
  /** Whether to show a compact version */
  compact?: boolean
}

/**
 * Displays a countdown to the next reconciliation
 */
export function SyncCountdown({ interval, lastSyncTime, suspended, compact = false }: SyncCountdownProps) {
  const [secondsRemaining, setSecondsRemaining] = useState<number | null>(null)

  const intervalSeconds = parseInterval(interval)

  useEffect(() => {
    if (suspended || !lastSyncTime || intervalSeconds === null) {
      setSecondsRemaining(null)
      return
    }

    const calculateRemaining = () => {
      const lastSync = new Date(lastSyncTime).getTime()
      const nextSync = lastSync + intervalSeconds * 1000
      const now = Date.now()
      const remaining = Math.max(0, Math.floor((nextSync - now) / 1000))
      return remaining
    }

    setSecondsRemaining(calculateRemaining())

    const timer = setInterval(() => {
      setSecondsRemaining(calculateRemaining())
    }, 1000)

    return () => clearInterval(timer)
  }, [interval, lastSyncTime, suspended, intervalSeconds])

  if (suspended) {
    return (
      <div className={clsx('flex items-center gap-1.5', compact ? 'text-xs' : 'text-sm')}>
        <Clock className={clsx('text-yellow-400', compact ? 'w-3 h-3' : 'w-3.5 h-3.5')} />
        <span className="text-yellow-400">Paused</span>
      </div>
    )
  }

  if (secondsRemaining === null) {
    return (
      <div className={clsx('flex items-center gap-1.5 text-theme-text-tertiary', compact ? 'text-xs' : 'text-sm')}>
        <Clock className={compact ? 'w-3 h-3' : 'w-3.5 h-3.5'} />
        <span>Every {interval}</span>
      </div>
    )
  }

  const isImminent = secondsRemaining < 30
  const formatted = formatSecondsRemaining(secondsRemaining)

  return (
    <div
      className={clsx(
        'flex items-center gap-1.5',
        compact ? 'text-xs' : 'text-sm',
        isImminent ? 'text-blue-400' : 'text-theme-text-secondary'
      )}
      title={`Interval: ${interval}`}
    >
      {isImminent ? (
        <RefreshCw className={clsx('animate-pulse', compact ? 'w-3 h-3' : 'w-3.5 h-3.5')} />
      ) : (
        <Clock className={compact ? 'w-3 h-3' : 'w-3.5 h-3.5'} />
      )}
      <span>
        {secondsRemaining === 0 ? 'Now' : `in ${formatted}`}
      </span>
    </div>
  )
}

const UNIT_SECONDS: Record<string, number> = { h: 3600, m: 60, s: 1, ms: 0.001 }

/**
 * Parses a Go duration as Flux's interval fields accept it ("5m", "1h30m",
 * "1.5m", "1500ms") into seconds; null for anything else.
 */
export function parseInterval(interval: string): number | null {
  if (!/^(\d+(\.\d+)?(ms|h|m|s))+$/.test(interval)) return null
  let totalSeconds = 0
  for (const [, value, unit] of interval.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)) {
    totalSeconds += Number(value) * UNIT_SECONDS[unit]
  }
  return totalSeconds > 0 ? totalSeconds : null
}

/**
 * Formats seconds into a human-readable duration
 */
function formatSecondsRemaining(seconds: number): string {
  if (seconds < 60) {
    return `${seconds}s`
  }
  if (seconds < 3600) {
    const mins = Math.floor(seconds / 60)
    const secs = seconds % 60
    return secs > 0 ? `${mins}m ${secs}s` : `${mins}m`
  }
  const hours = Math.floor(seconds / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`
}

/**
 * Simple interval display without countdown
 */
export function IntervalDisplay({ interval, compact = false }: { interval: string; compact?: boolean }) {
  return (
    <div className={clsx('flex items-center gap-1.5 text-theme-text-secondary', compact ? 'text-xs' : 'text-sm')}>
      <Clock className={compact ? 'w-3 h-3' : 'w-3.5 h-3.5'} />
      <span>{interval}</span>
    </div>
  )
}
