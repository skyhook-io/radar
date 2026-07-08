/**
 * Shared timeline components and utilities.
 *
 * These components are used by both TimelineSwimlanes.tsx and the EventsTab in WorkloadView.
 * They ensure consistent behavior for time formatting, zoom controls, legends, and markers.
 */

import { clsx } from 'clsx'
import { ZoomIn, ZoomOut } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { TimelineEvent } from '../../types/core'
import { isChangeEvent, isDeploymentLikeWorkloadKind, isHistoricalEvent } from '../../types/core'
import { isProblematicEvent, type ResourceLane } from '../../utils/resource-hierarchy'

// ============================================================================
// Constants
// ============================================================================

/**
 * Available zoom levels in hours.
 * These control how much time is visible on the timeline.
 */
export const ZOOM_LEVELS = [0.5, 1, 2, 6, 12, 24, 48, 168] as const
export type ZoomLevel = (typeof ZOOM_LEVELS)[number]

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
        <span className="w-4 h-2 rounded-sm bg-green-500/60 dark:bg-green-600/60" />
        <span>Healthy</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-blue-500/60 dark:bg-blue-500/60" />
        <span>Rolling</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-amber-500/60 dark:bg-[#b8861e]" />
        <span>Degraded</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-red-500/60 dark:bg-red-500/60" />
        <span>Unhealthy</span>
      </span>
      <span className="flex items-center gap-1">
        <span className="w-4 h-2 rounded-sm bg-sky-500/60 dark:bg-sky-500/60" />
        <span>Idle</span>
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
      <Tooltip content="Zoom out (show more time)">
        <button
          onClick={onZoomOut}
          disabled={!canZoomOut}
          aria-label="Zoom out"
          className="p-1.5 hover:bg-theme-elevated rounded disabled:opacity-30"
        >
          <ZoomOut className="w-4 h-4" />
        </button>
      </Tooltip>
      <span className="text-xs min-w-[3ch] text-center">{formatZoomLevel(zoom)}</span>
      <Tooltip content="Zoom in (show less time)">
        <button
          onClick={onZoomIn}
          disabled={!canZoomIn}
          aria-label="Zoom in"
          className="p-1.5 hover:bg-theme-elevated rounded disabled:opacity-30"
        >
          <ZoomIn className="w-4 h-4" />
        </button>
      </Tooltip>
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

    if (isProblematic) {
      return dimmed ? 'bg-amber-500/50' : 'bg-amber-500'
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add':
          return dimmed ? 'bg-green-500/50' : 'bg-green-500'
        case 'delete':
          return dimmed ? 'bg-red-500/50' : 'bg-red-500'
        case 'update':
          return dimmed ? 'bg-blue-500/50' : 'bg-blue-500'
      }
    }
    return dimmed ? 'bg-theme-text-tertiary/50' : 'bg-theme-text-tertiary'
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
      <div className={clsx('shrink-0 bg-theme-surface/50 border-r border-theme-border', labelColumnClass)} />
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
  health: 'healthy' | 'rolling' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown' | string
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
        return 'bg-green-500/60 dark:bg-green-600/60'
      case 'rolling':
        return 'bg-blue-500/60 dark:bg-blue-500/60'
      case 'degraded':
        return 'bg-amber-500/60 dark:bg-[#b8861e]'
      case 'unhealthy':
        return 'bg-red-500/60 dark:bg-red-500/60'
      case 'neutral':
        // intentional/idle (suspended, scaled-to-0) — sky, calm
        return 'bg-sky-500/60 dark:bg-sky-500/60'
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
        <span className="absolute left-0.5 top-1/2 -translate-y-1/2 text-[9px] text-black/70 dark:text-black/60 whitespace-nowrap pointer-events-none group-hover:text-black/90 dark:group-hover:text-black/80">
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
 * - Event is for a deployment-like workload with degraded health
 */
function isRolloutEvent(event: TimelineEvent): boolean {
  // Only deployment-like workload controllers can have rollout transitions.
  if (!isDeploymentLikeWorkloadKind(event.kind)) return false

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
  allEvents?: TimelineEvent[],
  // The lane's own resource. Lanes aggregate descendant events (a CronJob lane
  // carries its Jobs' and Pods' events); without this, the FIRST child cleanup
  // delete in the stream would truncate the whole lane's existence — routine
  // cron churn erased the parent's health strip. Only a delete of the lane's
  // own resource ends it; child deletes neither end it nor change its health.
  ownResource?: { kind: string; namespace: string; name: string }
): HealthSpanResult {
  const isOwn = (e: TimelineEvent) =>
    !ownResource || (e.kind === ownResource.kind && e.namespace === ownResource.namespace && e.name === ownResource.name)
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
  const deleteEvent = sorted.find(e => e.eventType === 'delete' && isOwn(e))

  // Determine when the resource "existed"
  // - If we have createdAt, use it
  // - Otherwise, assume it existed before the time window
  // Birth clamp: when metadata createdAt is absent but the first observed
  // change IS the creation (add / reason "created"), the resource was born
  // then — the window-start fallback painted hours of 'unknown' before a cron
  // pod existed. Only a resource whose first event is a later mutation keeps
  // the "existed before the window" assumption.
  const firstChange = sorted[0]
  const bornAtFirstEvent = !!firstChange && (firstChange.eventType === 'add' || firstChange.reason === 'created')
  const existsFrom = createdAtTimestamp ?? (bornAtFirstEvent ? new Date(firstChange.timestamp).getTime() : startTime)
  const createdBeforeWindow = createdAtTimestamp ? createdAtTimestamp < startTime : false

  // If deleted, it stops existing at that point
  const existsUntil = deleteEvent ? new Date(deleteEvent.timestamp).getTime() : now

  const spans: { start: number; end: number; health: string }[] = []
  // `null` is the "no health observed yet" sentinel — kept distinct from the
  // real 'unknown' health value (emitted for node-lost pods). Overloading
  // 'unknown' as both swallowed genuine-unknown spans and then false-greened
  // them via the empty-spans fallback below.
  let currentHealth: string | null = null
  let spanStart = Math.max(existsFrom, startTime)

  for (const evt of sorted) {
    const ts = new Date(evt.timestamp).getTime()
    if (ts < existsFrom) continue // Resource didn't exist yet
    if (ts > existsUntil) continue // Resource was deleted
    if (evt.eventType === 'delete' && !isOwn(evt)) continue // Child cleanup — not this lane's death, not a health change

    // Use getEffectiveHealthState to distinguish rollouts from unexpected degradation
    const newHealth = getEffectiveHealthState(evt)

    if (ts < startTime) {
      // Pre-window event: track health state but don't create a span yet
      currentHealth = newHealth
      continue
    }

    if (newHealth !== currentHealth && currentHealth !== null) {
      spans.push({ start: spanStart, end: ts, health: currentHealth })
      spanStart = ts
    }
    currentHealth = newHealth
  }

  // Close final span (only up to when resource existed)
  if (currentHealth !== null) {
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

// ============================================================================
// State-family aggregate health sweep
//
// A collapsed parent / app-group strip must NOT be a latest-event-wins blend
// across its members — one member's fresh event would repaint the whole strip.
// Instead: compute each member's honest spans, overlay them into time slices,
// and per slice decide from the SET of states present. States group into
// families; a slice whose present states all fall in ONE family paints that
// family's dominant state; families that DISAGREE paint a neutral 'mixed'
// texture (reads as "not a health state — expand to see who").
// ============================================================================

/** One member's honest health timeline within the window: non-overlapping spans
 *  in chronological order (buildHealthSpans output for a single resource). */
export interface MemberHealthSpans {
  name: string
  spans: { start: number; end: number; health: string }[]
}

/** A painted segment of an aggregate lane's health strip. Either a real health
 *  state (a uniform-family slice) or 'mixed' (families disagree). Carries the
 *  per-state member attribution the tooltip renders. */
export interface AggregateHealthSegment {
  start: number
  end: number
  /** 'healthy' | 'idle' | 'degraded' | 'unhealthy' | 'rolling' | 'mixed' | … */
  health: string
  mixed: boolean
  /** Distinct members present (alive) in this slice. */
  total: number
  /** Member names grouped by their raw state in this slice. */
  byState: Record<string, string[]>
}

// State families. OK = benign-or-quiet; BAD = something wrong; ROLLING = a
// deploy in flight (its own family — a rollout beside a healthy peer is neither
// uniformly OK nor broken, so the slice reads MIXED). A benign healthy+idle
// slice is one family (OK) → NOT mixed: the founder's key case.
const OK_STATES = new Set(['healthy', 'idle', 'neutral', 'unknown'])
const BAD_STATES = new Set(['degraded', 'unhealthy'])
const ROLLING_STATES = new Set(['rolling'])

type HealthFamily = 'ok' | 'bad' | 'rolling'

function healthFamily(state: string): HealthFamily {
  if (BAD_STATES.has(state)) return 'bad'
  if (ROLLING_STATES.has(state)) return 'rolling'
  if (OK_STATES.has(state)) return 'ok'
  // Any unrecognized vocabulary word is treated as quiet, not a false alarm,
  // so it joins the OK family.
  return 'ok'
}

/** Gather an aggregate lane's LEAF resources (a Deployment's Pods, a CronJob's
 *  Jobs' Pods) and compute each one's honest single-resource health spans.
 *  Leaves are the units that carry real health; an intermediate node's health is
 *  just its children's. Seeds sweepAggregateHealth for a collapsed parent /
 *  app-group strip. A leaf with no observed spans is omitted (nothing to sweep). */
export function buildLaneMemberSpans(
  lane: ResourceLane,
  startTime: number,
  now: number,
): MemberHealthSpans[] {
  const leaves: ResourceLane[] = []
  const walk = (l: ResourceLane): void => {
    if (!l.children?.length) { leaves.push(l); return }
    for (const c of l.children) walk(c)
  }
  walk(lane)
  const out: MemberHealthSpans[] = []
  for (const leaf of leaves) {
    const changeEvents = leaf.events.filter((e) => isChangeEvent(e))
    const { spans } = buildHealthSpans(changeEvents, startTime, now, leaf.events, {
      kind: leaf.kind, namespace: leaf.namespace, name: leaf.name,
    })
    if (spans.length > 0) out.push({ name: leaf.name, spans })
  }
  return out
}

/** OK-family dominant state: the majority state by member count; ties → healthy
 *  (the founder rule — a split OK slice reads as the reassuring state). */
function dominantOkState(byState: Record<string, string[]>): string {
  let maxCount = 0
  for (const s of Object.keys(byState)) maxCount = Math.max(maxCount, byState[s].length)
  const top = Object.keys(byState).filter((s) => byState[s].length === maxCount)
  return top.length === 1 ? top[0] : 'healthy'
}

function paintSlice(
  start: number, end: number, byState: Record<string, string[]>, total: number,
): AggregateHealthSegment {
  const families = new Set<HealthFamily>()
  for (const state of Object.keys(byState)) families.add(healthFamily(state))
  if (families.size > 1) return { start, end, health: 'mixed', mixed: true, total, byState }
  const family = [...families][0]
  const health =
    family === 'bad' ? (byState['unhealthy'] ? 'unhealthy' : 'degraded')
    : family === 'rolling' ? 'rolling'
    : dominantOkState(byState)
  return { start, end, health, mixed: false, total, byState }
}

function sameSegment(a: AggregateHealthSegment, b: AggregateHealthSegment): boolean {
  if (a.health !== b.health || a.mixed !== b.mixed || a.total !== b.total) return false
  const ak = Object.keys(a.byState)
  if (ak.length !== Object.keys(b.byState).length) return false
  for (const k of ak) {
    const bv = b.byState[k]
    if (!bv || bv.length !== a.byState[k].length) return false
    const bs = new Set(bv)
    for (const n of a.byState[k]) if (!bs.has(n)) return false
  }
  return true
}

/** Interval sweep over member spans → aggregate health segments. At each slice:
 *  the SET of present member states → set of families. One family → paint that
 *  family's dominant state (unhealthy>degraded for BAD; OK paints the majority,
 *  ties→healthy; ROLLING→rolling). Families disagree → 'mixed'. Adjacent slices
 *  with identical result (state + attribution) merge. Empty slices (no member
 *  alive) paint nothing. Pure. */
export function sweepAggregateHealth(
  members: MemberHealthSpans[],
  startTime: number,
  endTime: number,
): AggregateHealthSegment[] {
  const bounds = new Set<number>()
  for (const m of members) {
    for (const s of m.spans) {
      if (s.start > startTime && s.start < endTime) bounds.add(s.start)
      if (s.end > startTime && s.end < endTime) bounds.add(s.end)
    }
  }
  const edges = [startTime, ...[...bounds].sort((a, b) => a - b), endTime]
  const raw: AggregateHealthSegment[] = []
  for (let i = 0; i < edges.length - 1; i++) {
    const t0 = edges[i]
    const t1 = edges[i + 1]
    if (t1 <= t0) continue
    const mid = (t0 + t1) / 2
    const byState: Record<string, string[]> = {}
    let total = 0
    for (const m of members) {
      const span = m.spans.find((s) => s.start <= mid && mid < s.end)
      if (!span) continue
      total++
      ;(byState[span.health] ??= []).push(m.name)
    }
    if (total === 0) continue
    raw.push(paintSlice(t0, t1, byState, total))
  }
  const merged: AggregateHealthSegment[] = []
  for (const seg of raw) {
    const prev = merged[merged.length - 1]
    if (prev && prev.end === seg.start && sameSegment(prev, seg)) prev.end = seg.end
    else merged.push({ ...seg })
  }
  return merged
}

/** Cap a name list at 3 + "+N more" for tooltip density. */
function capNames(list: string[]): string {
  if (list.length <= 3) return list.join(', ')
  return `${list.slice(0, 3).join(', ')}, +${list.length - 3} more`
}

function collectFamilyNames(
  byState: Record<string, string[]>, family: HealthFamily,
): { names: string[]; dominant: string } {
  const names: string[] = []
  let hasUnhealthy = false
  for (const [state, ns] of Object.entries(byState)) {
    if (healthFamily(state) !== family) continue
    names.push(...ns)
    if (state === 'unhealthy') hasUnhealthy = true
  }
  const dominant = family === 'bad' ? (hasUnhealthy ? 'unhealthy' : 'degraded')
    : family === 'rolling' ? 'rolling' : 'healthy'
  return { names, dominant }
}

/** Attribution-bearing tooltip for an aggregate strip segment. Examples:
 *  uniform → `healthy · 23/23`; mixed → `mixed · 2/23 unhealthy: a, b · 21 healthy/idle`. */
export function formatAggregateHealthTooltip(seg: AggregateHealthSegment): string {
  if (!seg.mixed) {
    const paintedCount = (seg.byState[seg.health] ?? []).length
    const parts = [`${seg.health} · ${paintedCount}/${seg.total}`]
    // Any other states in the same (uniform) family, for honesty about the split.
    for (const state of Object.keys(seg.byState).filter((s) => s !== seg.health).sort()) {
      parts.push(`${seg.byState[state].length} ${state}`)
    }
    return parts.join(' · ')
  }
  const parts = ['mixed']
  const bad = collectFamilyNames(seg.byState, 'bad')
  if (bad.names.length) parts.push(`${bad.names.length}/${seg.total} ${bad.dominant}: ${capNames(bad.names)}`)
  const rolling = collectFamilyNames(seg.byState, 'rolling')
  if (rolling.names.length) parts.push(`${rolling.names.length}/${seg.total} rolling: ${capNames(rolling.names)}`)
  const ok = collectFamilyNames(seg.byState, 'ok')
  if (ok.names.length) parts.push(`${ok.names.length} healthy/idle`)
  return parts.join(' · ')
}
