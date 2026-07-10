/**
 * scrubber-math — pure geometry, selection math, and shared types for the
 * timeline strip. No React or JSX: everything here is unit-testable without a
 * DOM. The strip components (TimelineStrip, TimelineSwimlanes) and the host
 * wrappers import these; all rendering and theme colors live in the components.
 */

// ============================================================================
// Types
// ============================================================================

export interface ScrubberBucket {
  startMs: number
  endMs: number
  total: number
  warnings: number
}

export interface ScrubberRange {
  fromMs: number
  toMs: number
}

export interface ScrubberPreset {
  label: string
  ms: number
}

// ============================================================================
// Constants
// ============================================================================

// Smallest brush width the user can shrink to — below this the events fetch is
// too narrow to be useful and handles overlap.
const MIN_SELECTION_MS = 60_000

// Smallest view window any zoom control may produce. Shared by the strip's
// band resize, the swimlane zoom ladder (whose lowest rung is 0.25h), and the
// wheel/pinch zoom, so every way of narrowing the window bottoms out at the
// same width.
export const MIN_WINDOW_MS = 15 * 60_000

const MINUTE_MS = 60_000
const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS

// ============================================================================
// Pure geometry + selection math (exported for testing)
// ============================================================================

export type SelectionAnchor = 'start' | 'end' | 'center'

function normalize(sel: ScrubberRange): ScrubberRange {
  return sel.fromMs <= sel.toMs ? sel : { fromMs: sel.toMs, toMs: sel.fromMs }
}

/**
 * Clamp a selection to the domain and to [MIN_SELECTION_MS, maxSelectionMs].
 * `anchor` decides which edge stays put when the width is adjusted:
 *   - 'end'    keep the right edge (presets pinned to "now", left-handle drag)
 *   - 'start'  keep the left edge (right-handle drag)
 *   - 'center' keep the midpoint (zoom)
 * Returns `clampedToMax` for callers that want to surface the cap; none do yet.
 */
export function clampSelection(
  sel: ScrubberRange,
  domain: ScrubberRange,
  maxSelectionMs?: number,
  anchor: SelectionAnchor = 'end',
): { selection: ScrubberRange; clampedToMax: boolean } {
  const domainWidth = Math.max(0, domain.toMs - domain.fromMs)
  const n = normalize(sel)
  let { fromMs, toMs } = n
  const width = toMs - fromMs

  const minWidth = Math.min(MIN_SELECTION_MS, domainWidth)
  let maxWidth = domainWidth
  if (maxSelectionMs != null) maxWidth = Math.min(maxWidth, maxSelectionMs)

  const targetWidth = Math.max(minWidth, Math.min(width, maxWidth))
  const clampedToMax = maxSelectionMs != null && width > maxSelectionMs && targetWidth < width

  if (anchor === 'end') {
    fromMs = toMs - targetWidth
  } else if (anchor === 'start') {
    toMs = fromMs + targetWidth
  } else {
    const center = (fromMs + toMs) / 2
    fromMs = center - targetWidth / 2
    toMs = center + targetWidth / 2
  }

  // Shift the whole window back inside the domain without changing its width.
  if (fromMs < domain.fromMs) {
    const shift = domain.fromMs - fromMs
    fromMs += shift
    toMs += shift
  }
  if (toMs > domain.toMs) {
    const shift = toMs - domain.toMs
    fromMs -= shift
    toMs -= shift
  }
  fromMs = Math.max(domain.fromMs, fromMs)
  toMs = Math.min(domain.toMs, toMs)

  // Pixel-to-time math produces fractional ms; emit integers so consumers can
  // put the values straight into query params (servers parse epoch-ms ints).
  return { selection: { fromMs: Math.round(fromMs), toMs: Math.round(toMs) }, clampedToMax }
}

/**
 * Normalize recording-gap ranges for rendering: drop zero/negative-width spans,
 * dedupe identical ones, merge overlapping/adjacent spans, and (when a domain is
 * given) clip to it — spans entirely outside the domain are dropped. Pure so the
 * host's bucket→gap extraction can be unit-tested without a DOM.
 */
export function mergeGapRanges(ranges: ScrubberRange[], domain?: ScrubberRange): ScrubberRange[] {
  const valid = ranges
    .map((r) => ({ fromMs: Math.min(r.fromMs, r.toMs), toMs: Math.max(r.fromMs, r.toMs) }))
    .filter((r) => r.toMs > r.fromMs)
    .sort((a, b) => a.fromMs - b.fromMs)

  const merged: ScrubberRange[] = []
  for (const r of valid) {
    const last = merged[merged.length - 1]
    // <= folds both overlapping and back-to-back spans into one band.
    if (last && r.fromMs <= last.toMs) {
      last.toMs = Math.max(last.toMs, r.toMs)
    } else {
      merged.push({ ...r })
    }
  }

  if (!domain) return merged
  const clipped: ScrubberRange[] = []
  for (const r of merged) {
    const fromMs = Math.max(r.fromMs, domain.fromMs)
    const toMs = Math.min(r.toMs, domain.toMs)
    if (toMs > fromMs) clipped.push({ fromMs, toMs })
  }
  return clipped
}

// Candidate display-bucket widths (hours) for the strip's histogram, coarsest
// last. Adaptive selection keeps the bar count bounded without over-widening
// bars — a wide bucket smears a short burst of events across empty time, so the
// strip would paint data where there is none (e.g. across a recording gap).
// Sub-hour rungs serve hosts that bucket raw events client-side (the local
// ring) when the displayed span is framed tightly around a narrow selection;
// hosts limited to hourly rollups (the retained server overview) pass a
// minBucketMs of one hour and never see them.
const DISPLAY_BUCKET_RUNGS_MS = [
  MINUTE_MS,
  5 * MINUTE_MS,
  10 * MINUTE_MS,
  30 * MINUTE_MS,
  ...[1, 2, 3, 6, 12, 24].map((h) => h * HOUR_MS),
]

// Upper bound on rendered bars — the smallest rung whose count fits under this
// wins, so bars stay as fine as the strip width reasonably supports.
const MAX_DISPLAY_BARS = 256

/**
 * Pick the finest display-bucket width for a domain: the smallest rung such that
 * `domainWidthMs / rung <= MAX_DISPLAY_BARS`, falling back to the coarsest rung.
 * Pure + exported so the rung boundaries can be unit-tested.
 */
export function pickDisplayBucketSizeMs(domainWidthMs: number, minBucketMs = HOUR_MS): number {
  for (const rung of DISPLAY_BUCKET_RUNGS_MS) {
    if (rung < minBucketMs) continue
    if (domainWidthMs / rung <= MAX_DISPLAY_BARS) return rung
  }
  return DISPLAY_BUCKET_RUNGS_MS[DISPLAY_BUCKET_RUNGS_MS.length - 1]
}

/** A preset sets the brush to [now - ms, now], clamped. */
export function presetToSelection(
  ms: number,
  now: number,
  domain: ScrubberRange,
  maxSelectionMs?: number,
): { selection: ScrubberRange; clampedToMax: boolean } {
  return clampSelection({ fromMs: now - ms, toMs: now }, domain, maxSelectionMs, 'end')
}

/**
 * Clamp the lens window into the applied selection, preserving its width. If the
 * lens is wider than the selection it collapses to the full selection (the lens
 * can never show more than what the query loaded).
 */
export function clampLensToSelection(lens: ScrubberRange, selection: ScrubberRange): ScrubberRange {
  const selWidth = Math.max(0, selection.toMs - selection.fromMs)
  const lensWidth = lens.toMs - lens.fromMs
  if (lensWidth >= selWidth) return { fromMs: selection.fromMs, toMs: selection.toMs }
  let fromMs = lens.fromMs
  let toMs = lens.toMs
  if (fromMs < selection.fromMs) {
    toMs += selection.fromMs - fromMs
    fromMs = selection.fromMs
  }
  if (toMs > selection.toMs) {
    fromMs -= toMs - selection.toMs
    toMs = selection.toMs
  }
  return { fromMs: Math.round(fromMs), toMs: Math.round(toMs) }
}

/** Human-readable duration for the lens-width chip label, e.g. "15m" / "8h" / "3d". */
export function formatLensDuration(ms: number): string {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `${minutes}m`
  const hours = ms / HOUR_MS
  if (hours < 24) return Number.isInteger(hours) ? `${hours}h` : `${hours.toFixed(1)}h`
  const days = ms / DAY_MS
  return Number.isInteger(days) ? `${days}d` : `${days.toFixed(1)}d`
}

/**
 * Approximate count of events recorded after `toMs`, for the frozen chip's "Go
 * live · N new" pull. Floors `toMs` to the hour and sums the `total` of every
 * bucket starting at/after that hour — hour-granular is fine, it's a CTA hint,
 * not a metric. Pure + exported for testing.
 */
export function countEventsAfter(buckets: ScrubberBucket[], toMs: number): number {
  const hourStart = Math.floor(toMs / HOUR_MS) * HOUR_MS
  let sum = 0
  for (const b of buckets) {
    if (b.startMs >= hourStart) sum += b.total
  }
  return sum
}

/** Bar height ∝ total, sqrt-scaled with a visible floor so sparse bars show. */
export function barHeight(total: number, maxTotal: number, trackHeight: number): number {
  if (total <= 0 || maxTotal <= 0) return 0
  const floor = Math.max(2, trackHeight * 0.08)
  const scaled = (Math.sqrt(total) / Math.sqrt(maxTotal)) * trackHeight
  return Math.max(floor, Math.min(trackHeight, scaled))
}

/** Compact pill label, e.g. "Jul 1 14:30". */
export function formatScrubberPill(ms: number): string {
  const d = new Date(ms)
  const date = d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return `${date} ${time}`
}
