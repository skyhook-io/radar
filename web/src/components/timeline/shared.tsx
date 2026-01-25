/**
 * Shared timeline components and utilities.
 *
 * These components are used by both TimelineSwimlanes.tsx and the EventsTab in ResourceDetailPage.
 * They ensure consistent behavior for time formatting, zoom controls, legends, and markers.
 */

import { clsx } from 'clsx'
import { ZoomIn, ZoomOut } from 'lucide-react'
import type { TimelineEvent } from '../../types'
import { isChangeEvent, isHistoricalEvent } from '../../types'
import { isProblematicEvent } from '../../utils/resource-hierarchy'

// ============================================================================
// Constants
// ============================================================================

/**
 * Available zoom levels in hours.
 * These control how much time is visible on the timeline.
 */
export const ZOOM_LEVELS = [0.5, 1, 2, 6, 12, 24, 48, 168] as const
export type ZoomLevel = (typeof ZOOM_LEVELS)[number]

/**
 * Known noisy resources that update constantly (leases, locks, status tracking)
 */
const NOISY_NAME_PATTERNS = [
  /^kube-scheduler$/,
  /^kube-controller-manager$/,
  /-leader-election$/,
  /-lock$/,
  /-lease$/,
  /^cluster-autoscaler-status$/,
  /^cluster-kubestore$/,
  /^datadog-leader-election$/,
  /^cert-manager-controller$/,
]

/**
 * Event kinds that are always noise (tracking their lifecycle is useless)
 */
const NOISY_KINDS = new Set(['Lease', 'Endpoints', 'EndpointSlice', 'Event'])

// ============================================================================
// Time Formatting Utilities
// ============================================================================

/**
 * Format a timestamp for display on the time axis.
 * Shows time only for today, date + time for other days.
 */
export function formatAxisTime(date: Date): string {
  const now = new Date()
  const isToday = date.toDateString() === now.toDateString()
  const time = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

  if (isToday) {
    return time
  }

  const month = date.toLocaleDateString([], { month: 'short', day: 'numeric' })
  return `${month} ${time}`
}

/**
 * Format a timestamp for full display (tooltips, detail panels).
 */
export function formatFullTime(date: Date): string {
  return date.toLocaleString()
}

/**
 * Format a relative time string (e.g., "5m ago", "2h ago").
 */
export function formatRelativeTime(timestamp: string): string {
  const diff = Date.now() - new Date(timestamp).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

/**
 * Format zoom level for display (e.g., "1h", "6h", "24h", "7d").
 */
export function formatZoomLevel(hours: number): string {
  if (hours < 1) return `${Math.round(hours * 60)}m`
  if (hours < 24) return `${hours}h`
  return `${Math.round(hours / 24)}d`
}

// ============================================================================
// Event Filtering
// ============================================================================

/**
 * Check if an event is "routine noise" (constant heartbeats, lease updates, etc.)
 */
export function isRoutineEvent(event: TimelineEvent): boolean {
  // Historical events are never routine - they capture meaningful state transitions
  if (isHistoricalEvent(event)) return false

  // K8s Event objects - their lifecycle (add/update/delete) is always noise
  if (event.kind === 'Event' && isChangeEvent(event)) return true

  // Only filter update operations for other kinds - adds and deletes are interesting
  if (event.eventType !== 'update') return false

  // Known noisy resource kinds (for updates)
  if (NOISY_KINDS.has(event.kind)) return true

  // Known noisy resource name patterns
  if (NOISY_NAME_PATTERNS.some(pattern => pattern.test(event.name))) return true

  // ConfigMaps with noisy suffixes
  if (event.kind === 'ConfigMap') {
    if (event.name.endsWith('-lock') ||
        event.name.endsWith('-lease') ||
        event.name.endsWith('-leader') ||
        event.name.includes('kubestore')) {
      return true
    }
  }

  return false
}

// ============================================================================
// Shared Components
// ============================================================================

/**
 * Legend item with hover tooltip explaining what a color means.
 */
interface LegendItemProps {
  color: string
  label: string
  description: string
  dashed?: boolean
}

export function LegendItem({ color, label, description, dashed }: LegendItemProps) {
  return (
    <span className="relative flex items-center gap-1 group cursor-help">
      <span className={clsx(
        'w-2 h-2 rounded-full',
        dashed ? 'border border-dashed border-current bg-transparent' : color
      )} />
      <span>{label}</span>
      <span className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 px-2 py-1 text-xs bg-theme-base text-theme-text-primary rounded whitespace-nowrap opacity-0 group-hover:opacity-100 pointer-events-none z-50 transition-opacity duration-75 shadow-lg border border-theme-border-light">
        {description}
      </span>
    </span>
  )
}

/**
 * Legend for event dot markers (used in both swimlanes and detail view).
 */
export function EventDotLegend() {
  return (
    <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
      <LegendItem color="bg-green-500" label="Created" description="Resource was created" />
      <LegendItem color="bg-blue-500" label="Modified" description="Resource was modified" />
      <LegendItem color="bg-red-500" label="Deleted" description="Resource was deleted" />
      <LegendItem color="bg-amber-500" label="Warning" description="Warning event or error condition" />
      <LegendItem color="" label="Historical" description="Reconstructed from metadata" dashed />
    </div>
  )
}

/**
 * Legend for health span bars (colored bars showing health state over time).
 */
export function HealthSpanLegend() {
  return (
    <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-green-500/60" />
        <span>Healthy</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-blue-500/60" />
        <span>Rolling</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-yellow-500/60" />
        <span>Degraded</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-red-500/60" />
        <span>Unhealthy</span>
      </span>
    </div>
  )
}

/**
 * Zoom controls for the timeline (zoom in/out buttons + current level display).
 */
interface ZoomControlsProps {
  zoom: ZoomLevel
  onZoomIn: () => void
  onZoomOut: () => void
  canZoomIn: boolean
  canZoomOut: boolean
}

export function ZoomControls({ zoom, onZoomIn, onZoomOut, canZoomIn, canZoomOut }: ZoomControlsProps) {
  return (
    <div className="flex items-center gap-1 text-theme-text-tertiary">
      <button
        onClick={onZoomOut}
        disabled={!canZoomOut}
        className="p-1.5 hover:bg-theme-elevated rounded disabled:opacity-30"
        title="Zoom out (show more time)"
      >
        <ZoomOut className="w-4 h-4" />
      </button>
      <span className="text-xs min-w-[3ch] text-center">{formatZoomLevel(zoom)}</span>
      <button
        onClick={onZoomIn}
        disabled={!canZoomIn}
        className="p-1.5 hover:bg-theme-elevated rounded disabled:opacity-30"
        title="Zoom in (show less time)"
      >
        <ZoomIn className="w-4 h-4" />
      </button>
    </div>
  )
}

/**
 * Quick zoom level selector (5m, 30m, 1h, 6h, 24h buttons).
 */
interface QuickZoomSelectorProps {
  zoom: ZoomLevel
  onZoomChange: (level: ZoomLevel) => void
}

export function QuickZoomSelector({ zoom, onZoomChange }: QuickZoomSelectorProps) {
  const quickLevels: ZoomLevel[] = [0.5, 1, 6, 24]
  const labels: Record<number, string> = { 0.5: '30m', 1: '1h', 6: '6h', 24: '24h' }

  return (
    <div className="flex items-center gap-0.5 bg-theme-elevated rounded-lg p-0.5">
      {quickLevels.map(level => (
        <button
          key={level}
          onClick={() => onZoomChange(level)}
          className={clsx(
            'px-2 py-1 text-xs rounded transition-colors',
            zoom === level
              ? 'bg-theme-surface text-theme-text-primary shadow-sm'
              : 'text-theme-text-tertiary hover:text-theme-text-secondary'
          )}
        >
          {labels[level]}
        </button>
      ))}
    </div>
  )
}

/**
 * Event marker dot for timeline visualization.
 */
interface EventMarkerProps {
  event: TimelineEvent
  x: number
  selected?: boolean
  onClick: () => void
  dimmed?: boolean
  small?: boolean
}

export function EventMarker({ event, x, selected, onClick, dimmed, small }: EventMarkerProps) {
  const isChange = isChangeEvent(event)
  const isProblematic = isProblematicEvent(event)
  const isHistorical = isHistoricalEvent(event)

  const getMarkerStyle = () => {
    if (isHistorical) {
      if (isProblematic) {
        return 'bg-amber-500/20 border-2 border-dashed border-amber-500/60'
      }
      if (isChange) {
        switch (event.eventType) {
          case 'add':
            return 'bg-green-500/20 border-2 border-dashed border-green-500/60'
          case 'delete':
            return 'bg-red-500/20 border-2 border-dashed border-red-500/60'
          case 'update':
            return 'bg-blue-500/20 border-2 border-dashed border-blue-500/60'
        }
      }
      return 'bg-theme-hover/30 border-2 border-dashed border-theme-border-light'
    }

    const opacity = dimmed ? '/50' : ''
    if (isProblematic) {
      return `bg-amber-500${opacity}`
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add':
          return `bg-green-500${opacity}`
        case 'delete':
          return `bg-red-500${opacity}`
        case 'update':
          return `bg-blue-500${opacity}`
      }
    }
    return `bg-theme-text-tertiary${opacity}`
  }

  const getOperationLabel = () => {
    if (isProblematic) {
      return `⚠ ${event.reason || 'Warning'}`
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add': return '● Created'
        case 'delete': return '● Deleted'
        case 'update': return '● Modified'
        default: return '● Changed'
      }
    }
    if (event.reason) {
      return `● ${event.reason}`
    }
    return '● Event'
  }

  const tooltipLines: string[] = []
  tooltipLines.push(getOperationLabel())
  if (event.message) {
    const msg = event.message.length > 60 ? event.message.slice(0, 60) + '...' : event.message
    tooltipLines.push(msg)
  }
  tooltipLines.push(formatRelativeTime(event.timestamp))
  if (isHistorical) tooltipLines.push('(from metadata)')

  const tooltipText = tooltipLines.join(' · ')

  return (
    <button
      className={clsx(
        'absolute top-1/2 -translate-y-1/2 -translate-x-1/2 rounded-full transition-all group',
        small ? 'w-2.5 h-2.5' : 'w-3 h-3',
        getMarkerStyle(),
        selected ? 'ring-2 ring-white ring-offset-2 ring-offset-theme-base scale-150' : 'hover:scale-125',
        dimmed ? 'z-5' : isHistorical ? 'z-5' : 'z-10'
      )}
      style={{ left: `${x}%` }}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
    >
      <span className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 px-2 py-1 text-xs bg-theme-base text-theme-text-primary rounded whitespace-nowrap opacity-0 group-hover:opacity-100 pointer-events-none z-50 transition-opacity duration-75">
        {tooltipText}
      </span>
    </button>
  )
}

/**
 * "Now" marker line that shows the current time on the timeline.
 */
interface NowMarkerProps {
  /** X position as percentage (0-100) */
  x: number
  /** Label to show next to the line */
  label?: string
  /** Additional CSS classes for positioning */
  className?: string
}

export function NowMarker({ x, label = 'now', className }: NowMarkerProps) {
  if (x < 0 || x > 100) return null

  return (
    <div
      className={clsx(
        'absolute top-0 bottom-0 w-0.5 bg-purple-500/50 z-10 pointer-events-none',
        className
      )}
      style={{ left: `${x}%` }}
    >
      <span className="absolute -top-4 left-1/2 -translate-x-1/2 text-xs text-purple-500 font-medium">
        {label}
      </span>
    </div>
  )
}

/**
 * Time axis with tick marks and labels.
 */
interface TimeAxisProps {
  startTime: number
  endTime: number
  tickCount?: number
  /** CSS class for label column width (e.g., "w-64") */
  labelColumnClass?: string
}

export function TimeAxis({ startTime, endTime, tickCount = 8, labelColumnClass = 'w-64' }: TimeAxisProps) {
  const windowMs = endTime - startTime
  const ticks = Array.from({ length: tickCount + 1 }, (_, i) => {
    const t = startTime + (windowMs * i) / tickCount
    return { time: t, label: formatAxisTime(new Date(t)) }
  })

  const timeToX = (ts: number) => ((ts - startTime) / windowMs) * 100

  return (
    <div className="flex">
      <div className={clsx('flex-shrink-0 bg-theme-surface/50 border-r border-theme-border', labelColumnClass)} />
      <div className="flex-1 relative h-6 bg-theme-elevated/30">
        {ticks.map((tick, i) => {
          const x = timeToX(tick.time)
          return (
            <div
              key={i}
              className="absolute top-0 flex flex-col items-center"
              style={{ left: `${x}%`, transform: 'translateX(-50%)' }}
            >
              <div className="h-2 w-px bg-theme-border" />
              <span className="text-xs text-theme-text-tertiary">{tick.label}</span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/**
 * Health span bar showing a health state over a time range.
 */
interface HealthSpanProps {
  health: 'healthy' | 'degraded' | 'unhealthy' | string
  left: number // percentage
  width: number // percentage
  title?: string
  /** Show indicator that resource was created before visible window */
  createdBefore?: Date
}

export function HealthSpan({ health, left, width, title, createdBefore }: HealthSpanProps) {
  if (width <= 0) return null

  const getHealthColor = () => {
    switch (health) {
      case 'healthy':
        return 'bg-green-500/60'
      case 'rolling':
        // Blue/purple for expected degradation during rollout
        return 'bg-blue-500/60'
      case 'degraded':
        // Yellow for unexpected degradation
        return 'bg-yellow-500/60'
      case 'unhealthy':
        return 'bg-red-500/60'
      default:
        // Unknown or other states
        return 'bg-gray-400/40'
    }
  }

  return (
    <div
      className={clsx(
        'absolute top-1 bottom-1 rounded-sm group',
        getHealthColor()
      )}
      style={{ left: `${left}%`, width: `${width}%` }}
      title={title}
    >
      {createdBefore && left === 0 && (
        <span className="absolute left-0.5 top-1/2 -translate-y-1/2 text-[9px] text-theme-text-tertiary opacity-70 whitespace-nowrap pointer-events-none group-hover:opacity-100">
          ← {formatCreatedBefore(createdBefore)}
        </span>
      )}
    </div>
  )
}

/**
 * Format the "created before" date for display.
 * Shows relative time for recent dates, or short date for older ones.
 */
function formatCreatedBefore(date: Date): string {
  const now = Date.now()
  const diffMs = now - date.getTime()
  const diffHours = diffMs / (1000 * 60 * 60)
  const diffDays = diffHours / 24

  if (diffDays < 1) {
    return `${Math.round(diffHours)}h ago`
  } else if (diffDays < 7) {
    return `${Math.round(diffDays)}d ago`
  } else {
    return date.toLocaleDateString([], { month: 'short', day: 'numeric' })
  }
}

/**
 * Calculate visible time range from zoom level and optional pan offset.
 */
export function calculateTimeRange(
  zoom: ZoomLevel,
  now: number,
  panOffset: number = 0
): { start: number; end: number; windowMs: number } {
  const windowMs = zoom * 60 * 60 * 1000
  const end = now - panOffset
  const start = end - windowMs
  return { start, end, windowMs }
}

/**
 * Convert a timestamp to X position (0-100) within a time range.
 */
export function timeToX(timestamp: number, startTime: number, windowMs: number): number {
  return ((timestamp - startTime) / windowMs) * 100
}

/**
 * Result of building health spans, including metadata about resource creation.
 */
export interface HealthSpanResult {
  spans: { start: number; end: number; health: string }[]
  /** When the resource was actually created (from K8s metadata) */
  createdAt?: number
  /** True if resource was created before the visible window */
  createdBeforeWindow: boolean
}

/**
 * Check if an event indicates a rollout is in progress (expected degradation).
 * Rollout signals include:
 * - Diff summary contains "updated:" (indicating replica count or image changes)
 * - Diff summary mentions image changes
 * - Event is for Deployment/StatefulSet/DaemonSet with degraded health
 */
function isRolloutEvent(event: TimelineEvent): boolean {
  // Only workload kinds can have rollouts
  const rolloutKinds = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'ReplicaSet'])
  if (!rolloutKinds.has(event.kind)) return false

  // Check diff summary for rollout signals
  if (event.diff?.summary) {
    const summary = event.diff.summary.toLowerCase()
    // "updated:" typically indicates replica count changes during rollout
    if (summary.includes('updated:')) return true
    // Image changes trigger rollouts
    if (summary.includes('image(') || summary.includes('image:')) return true
    // Spec template changes often indicate rollout
    if (summary.includes('template')) return true
  }

  return false
}

/**
 * Determine the effective health state for an event, distinguishing between
 * expected rollout degradation (shown as 'rolling') and unexpected degradation.
 */
function getEffectiveHealthState(event: TimelineEvent): string {
  // Get the base health state
  const baseHealth = event.healthState || (isProblematicEvent(event) ? 'unhealthy' : 'healthy')

  // If degraded, check if it's a rollout (expected degradation)
  if (baseHealth === 'degraded' && isRolloutEvent(event)) {
    return 'rolling'
  }

  return baseHealth
}

/**
 * Build health spans from events for visualization.
 * Uses the resource's createdAt timestamp (from K8s metadata) to determine when it existed.
 * Returns metadata about creation time for rendering indicators.
 *
 * @param events - Change events (informer events) for health state transitions
 * @param allEvents - All events including K8s Events, used to extract createdAt
 */
export function buildHealthSpans(
  events: TimelineEvent[],
  startTime: number,
  now: number,
  allEvents?: TimelineEvent[]
): HealthSpanResult {
  // Use allEvents if provided to get createdAt from K8s Events too
  const eventsForMetadata = allEvents ?? events

  if (eventsForMetadata.length === 0) {
    return { spans: [], createdBeforeWindow: false }
  }

  const sorted = [...events].sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
  const allSorted = [...eventsForMetadata].sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())

  // Get the resource's actual creation time from K8s metadata (createdAt field)
  // Check all events (including K8s Events) for createdAt
  const firstEventWithCreatedAt = allSorted.find(e => e.createdAt)
  const createdAtTimestamp = firstEventWithCreatedAt?.createdAt
    ? new Date(firstEventWithCreatedAt.createdAt).getTime()
    : undefined

  // Check for delete event to know when resource stopped existing
  const deleteEvent = sorted.find(e => e.eventType === 'delete')

  // Determine when the resource "existed"
  // - If we have createdAt, use it
  // - Otherwise, assume it existed before the time window
  const existsFrom = createdAtTimestamp ?? startTime
  const createdBeforeWindow = createdAtTimestamp ? createdAtTimestamp < startTime : false

  // If deleted, it stops existing at that point
  const existsUntil = deleteEvent ? new Date(deleteEvent.timestamp).getTime() : now

  const spans: { start: number; end: number; health: string }[] = []
  let currentHealth = 'unknown'
  let spanStart = Math.max(existsFrom, startTime)

  for (const evt of sorted) {
    const ts = new Date(evt.timestamp).getTime()
    if (ts < startTime) continue
    if (ts < existsFrom) continue // Resource didn't exist yet
    if (ts > existsUntil) continue // Resource was deleted

    // Use getEffectiveHealthState to distinguish rollouts from unexpected degradation
    const newHealth = getEffectiveHealthState(evt)

    if (newHealth !== currentHealth && currentHealth !== 'unknown') {
      spans.push({ start: spanStart, end: ts, health: currentHealth })
      spanStart = ts
    }
    currentHealth = newHealth
  }

  // Close final span (only up to when resource existed)
  if (currentHealth !== 'unknown') {
    spans.push({ start: spanStart, end: Math.min(existsUntil, now), health: currentHealth })
  }

  // If no health spans but we know the resource exists (has createdAt), show a default "healthy" bar
  // This handles resources like Services that don't have explicit health tracking
  if (spans.length === 0 && createdAtTimestamp && !deleteEvent) {
    const effectiveStart = Math.max(existsFrom, startTime)
    if (effectiveStart < now) {
      spans.push({ start: effectiveStart, end: now, health: 'healthy' })
    }
  }

  return { spans, createdAt: createdAtTimestamp, createdBeforeWindow }
}
