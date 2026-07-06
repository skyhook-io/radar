/**
 * TimelineScrubber — a horizontal histogram-with-brush strip (GCP-Logs style).
 *
 * Pure presentation: it never fetches. The host feeds pre-grouped display
 * buckets, a fixed domain (the full retained window), and a controlled
 * selection. All colors come from theme CSS variables — never hardcoded — so
 * the strip tracks light/dark automatically.
 *
 * Staged-commit interaction (GCP Cloud Logging pattern): brushing the strip
 * (draw / handle-drag / middle-drag / keyboard nudge) never runs the query.
 * It stages a PENDING range instead — rendered with dashed guides, dot handles
 * and timestamp pills plus a floating "Run query / Reset" popover. Only "Run
 * query" (or Enter) commits by firing `onSelectionChange`. Explicit intents
 * (presets, step arrows, zoom) still apply immediately and clear any pending.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { ChevronLeft, ChevronRight, Play, Minus, Plus, ZoomIn, ZoomOut } from 'lucide-react'
import { STALE_AMBER_AFTER_MS, formatAsOfTime, type TimelineLiveState } from './timeline-live'

// ============================================================================
// Types
// ============================================================================

export interface ScrubberBucket {
  startMs: number
  endMs: number
  total: number
  warnings: number
}

export interface ScrubberCoverageSpan {
  startMs: number
  endMs: number
  reason: string
}

export interface ScrubberRange {
  fromMs: number
  toMs: number
}

export interface ScrubberPreset {
  label: string
  ms: number
}

export interface TimelineScrubberProps {
  buckets: ScrubberBucket[]
  coverage?: ScrubberCoverageSpan[]
  // Recording gaps (connector offline / pruned) as plain time ranges. Rendered as
  // an inert hatched band beneath the selection/lens overlays. Merged with any
  // `coverage` spans into one band layer.
  gaps?: ScrubberRange[]
  // True while the histogram data is still loading: renders faint skeleton bars
  // in the track so the strip reads "shape incoming", never "no data".
  loading?: boolean
  historyUnavailableBeforeMs?: number
  domain: ScrubberRange
  selection: ScrubberRange
  onSelectionChange: (sel: ScrubberRange) => void
  maxSelectionMs?: number
  presets?: ScrubberPreset[]
  onPresetSelect?: (preset: ScrubberPreset) => void
  className?: string
  // The LENS: the swimlane's visible window, rendered as a draggable band inside
  // the applied selection. Two-way synced with the swimlane — the band never
  // leaves the selection. Lens changes are instant (no staging); the lens is a
  // free client-side view, not a query.
  lens?: ScrubberRange
  onLensChange?: (lens: ScrubberRange) => void
  // Preset ladder for the lens-width chip (− 8h +). When absent the chip's step
  // arrows fall back to doubling/halving and the duration label has no popover.
  lensPresets?: ScrubberPreset[]
  // Live/paused indicator (retained mode only). When set, one chip renders in the
  // header row's top-right, next to the step/zoom arrows — the strip is the time
  // authority, so the live/paused control belongs here. Clicking it while frozen
  // (or live-unlatched) fires `onLiveChipClick`; live-latched is inert.
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
}

// ============================================================================
// Constants
// ============================================================================

// Smallest brush width the user can shrink to — below this the events fetch is
// too narrow to be useful and handles overlap.
const MIN_SELECTION_MS = 60_000

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS

// The lens width can't shrink below the selection minimum — a narrower view
// window is useless and the band would collapse to the grip dots.
const MIN_LENS_MS = MIN_SELECTION_MS

// Width used before the container has been measured (SSR / first paint) so the
// strip renders bars immediately instead of collapsing to nothing.
const FALLBACK_WIDTH = 640

const TRACK_HEIGHT = 44
const AXIS_HEIGHT = 16

// Extra strip height below the axis reserved for the pending timestamp pills
// (rendered under the baseline, GCP-style). Reserved permanently so entering
// pending mode never shifts the layout below the strip.
const PILL_GUTTER = 8

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
 * Returns `clampedToMax` so the caller can flash the cap hint.
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
const DISPLAY_BUCKET_RUNGS_MS = [1, 2, 3, 6, 12, 24].map((h) => h * HOUR_MS)

// Upper bound on rendered bars — the smallest rung whose count fits under this
// wins, so bars stay as fine as the strip width reasonably supports.
const MAX_DISPLAY_BARS = 256

/**
 * Pick the finest display-bucket width for a domain: the smallest rung such that
 * `domainWidthMs / rung <= MAX_DISPLAY_BARS`, falling back to the coarsest rung.
 * Pure + exported so the rung boundaries can be unit-tested.
 */
export function pickDisplayBucketSizeMs(domainWidthMs: number): number {
  for (const rung of DISPLAY_BUCKET_RUNGS_MS) {
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

/** Pan the selection by `deltaMs`, preserving its width, clamped to the domain. */
export function panSelection(
  sel: ScrubberRange,
  deltaMs: number,
  domain: ScrubberRange,
): ScrubberRange {
  const width = sel.toMs - sel.fromMs
  let fromMs = sel.fromMs + deltaMs
  let toMs = sel.toMs + deltaMs
  if (fromMs < domain.fromMs) {
    fromMs = domain.fromMs
    toMs = domain.fromMs + width
  }
  if (toMs > domain.toMs) {
    toMs = domain.toMs
    fromMs = domain.toMs - width
  }
  return { fromMs: Math.max(domain.fromMs, fromMs), toMs: Math.min(domain.toMs, toMs) }
}

/** Step the selection left/right by its own width (◀ ▶ buttons). */
export function stepSelection(
  sel: ScrubberRange,
  direction: -1 | 1,
  domain: ScrubberRange,
): ScrubberRange {
  return panSelection(sel, direction * (sel.toMs - sel.fromMs), domain)
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
 * Resize the lens to `widthMs`, centered on its current midpoint, then clamp back
 * inside the selection (never below MIN_LENS_MS, never wider than the selection).
 * Shared by the chip's − / + steps and its duration-preset popover so the two
 * paths can't drift.
 */
export function setLensWidth(
  lens: ScrubberRange,
  widthMs: number,
  selection: ScrubberRange,
): ScrubberRange {
  const selWidth = Math.max(0, selection.toMs - selection.fromMs)
  const width = Math.max(MIN_LENS_MS, Math.min(widthMs, selWidth))
  const center = (lens.fromMs + lens.toMs) / 2
  const half = width / 2
  return clampLensToSelection({ fromMs: center - half, toMs: center + half }, selection)
}

// Widths within this tolerance of a ladder rung count as sitting on it, so the
// chip steps to the adjacent rung rather than snapping to the same one.
const LENS_STEP_EPSILON_MS = 500

/**
 * Step the lens WIDTH to the adjacent rung of a preset ladder (or by a factor when
 * no ladder is given), centered on the current midpoint and clamped to the
 * selection. `dir` +1 widens, -1 narrows. Ladder stepping mirrors the swimlane's
 * ZOOM_LEVELS logic so the band and the swimlane resize through the same rungs.
 */
export function stepLensWidth(
  lens: ScrubberRange,
  presetsOrFactor: number[] | number,
  dir: -1 | 1,
  selection: ScrubberRange,
): ScrubberRange {
  const curWidth = lens.toMs - lens.fromMs
  let nextWidth: number
  if (Array.isArray(presetsOrFactor) && presetsOrFactor.length > 0) {
    const ladder = [...presetsOrFactor].sort((a, b) => a - b)
    const idx = ladder.findIndex((w) => w >= curWidth - LENS_STEP_EPSILON_MS)
    const cur = idx === -1 ? ladder.length - 1 : idx
    const nextIdx = dir > 0 ? Math.min(ladder.length - 1, cur + 1) : Math.max(0, cur - 1)
    nextWidth = ladder[nextIdx]
  } else {
    const factor = Array.isArray(presetsOrFactor) ? 2 : presetsOrFactor
    nextWidth = dir > 0 ? curWidth * factor : curWidth / factor
  }
  return setLensWidth(lens, nextWidth, selection)
}

/**
 * The lens band renders only when a lens is set AND nothing is staged — a pending
 * (staged) selection keeps visual priority, so the lens hides while brushing.
 */
export function shouldShowLensBand(
  lens: ScrubberRange | null | undefined,
  pending: ScrubberRange | null,
): boolean {
  return lens != null && pending == null
}

/** Zoom around the selection center. factor < 1 shrinks, > 1 grows. */
export function zoomSelection(
  sel: ScrubberRange,
  factor: number,
  domain: ScrubberRange,
  maxSelectionMs?: number,
): { selection: ScrubberRange; clampedToMax: boolean } {
  const center = (sel.fromMs + sel.toMs) / 2
  const half = ((sel.toMs - sel.fromMs) / 2) * factor
  return clampSelection({ fromMs: center - half, toMs: center + half }, domain, maxSelectionMs, 'center')
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

function msToX(ms: number, domain: ScrubberRange, width: number): number {
  const span = domain.toMs - domain.fromMs
  if (span <= 0) return 0
  return ((ms - domain.fromMs) / span) * width
}

function xToMs(x: number, domain: ScrubberRange, width: number): number {
  if (width <= 0) return domain.fromMs
  return domain.fromMs + (x / width) * (domain.toMs - domain.fromMs)
}

/** Compact pill label, e.g. "Jul 1 14:30". */
export function formatScrubberPill(ms: number): string {
  const d = new Date(ms)
  const date = d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return `${date} ${time}`
}

/** Precise pill label with seconds, e.g. "Jul 2 15:24:49" — used for pending edges. */
export function formatScrubberPillPrecise(ms: number): string {
  const d = new Date(ms)
  const date = d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  return `${date} ${time}`
}

/**
 * Horizontal centers for the two bottom pending pills. Each pill is clamped to
 * stay fully inside the container (shifted inward, never clipped); when the
 * range is too narrow for both, they push apart symmetrically around the
 * midpoint, then shift back inside as a pair.
 */
export function layoutPendingPillCenters(
  fromX: number,
  toX: number,
  containerWidth: number,
  fromPillWidth: number,
  toPillWidth: number,
  gap = 4,
): { fromCenter: number; toCenter: number } {
  const fromHalf = fromPillWidth / 2
  const toHalf = toPillWidth / 2
  const clamp = (v: number, half: number) =>
    Math.max(half, Math.min(containerWidth - half, v))

  let fromCenter = clamp(fromX, fromHalf)
  let toCenter = clamp(toX, toHalf)

  const needed = fromHalf + toHalf + gap
  if (toCenter - fromCenter < needed) {
    const mid = (fromCenter + toCenter) / 2
    fromCenter = mid - needed / 2
    toCenter = mid + needed / 2
    // Re-anchor the pushed-apart pair inside the container.
    if (fromCenter < fromHalf) {
      const shift = fromHalf - fromCenter
      fromCenter += shift
      toCenter += shift
    }
    if (toCenter > containerWidth - toHalf) {
      const shift = toCenter - (containerWidth - toHalf)
      fromCenter -= shift
      toCenter -= shift
    }
    fromCenter = clamp(fromCenter, fromHalf)
    toCenter = clamp(toCenter, toHalf)
  }
  return { fromCenter, toCenter }
}

function formatAxisTick(ms: number, spanMs: number): string {
  const d = new Date(ms)
  // Sub-day domains read as time; wider domains read as calendar dates.
  if (spanMs <= 24 * 60 * 60 * 1000) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

// ============================================================================
// Staged-commit reducer (exported for testing)
// ============================================================================

export interface ScrubberCommitState {
  /** Staged range awaiting an explicit run/reset, or null when nothing staged. */
  pending: ScrubberRange | null
}

export type ScrubberCommand =
  // Brush mutation (draw / handle-drag / middle-drag / keyboard nudge): stage only.
  | { kind: 'brush'; range: ScrubberRange }
  // Commit the staged range ("Run query" / Enter).
  | { kind: 'run' }
  // Discard the staged range ("Reset" / Escape).
  | { kind: 'reset' }
  // Explicit immediate intent (preset / step / zoom): commit now, drop pending.
  | { kind: 'apply'; range: ScrubberRange }
  // External `selection` prop changed: pending no longer relates to it, drop it.
  | { kind: 'sync' }

export interface ScrubberCommandResult {
  state: ScrubberCommitState
  /** Range to hand to `onSelectionChange`, or null to fire nothing. */
  commit: ScrubberRange | null
}

/**
 * Whether an external `selection` prop change should discard a staged (pending)
 * brush. It should for a GENUINE change (preset/extend/deep-link/host clamp) —
 * the pending was measured against a selection that no longer applies. It must
 * NOT for a LIVE-mode tick slide: in live mode the applied window slides forward
 * ~every 30s, but a pending brush is a fixed pair of absolute timestamps that
 * stays valid underneath the sliding window. Wiping on every tick would make
 * staging a range impossible while live — the pending (and its Run/Reset
 * popover) would vanish within one tick, including mid-drag if a tick lands
 * during the gesture. So while live, hold the pending and let the tick slide
 * under it; genuine live-mode changes clear pending through their own control
 * paths (preset button resets it) or by first flipping out of live mode (extend).
 * Pure + exported for testing.
 */
export function selectionChangeClearsPending(liveKind: 'live' | 'frozen' | undefined): boolean {
  return liveKind !== 'live'
}

/**
 * Single source of truth for WHEN a selection is committed to the host. Keeping
 * it pure lets the query-firing contract (brush never fires; run/apply fire
 * once) be unit-tested without a DOM.
 */
export function applyScrubberCommand(
  state: ScrubberCommitState,
  cmd: ScrubberCommand,
): ScrubberCommandResult {
  switch (cmd.kind) {
    case 'brush':
      return { state: { pending: cmd.range }, commit: null }
    case 'run':
      return state.pending
        ? { state: { pending: null }, commit: state.pending }
        : { state, commit: null }
    case 'reset':
      return { state: { pending: null }, commit: null }
    case 'apply':
      return { state: { pending: null }, commit: cmd.range }
    case 'sync':
      return { state: { pending: null }, commit: null }
  }
}

// ============================================================================
// Container width measurement
// ============================================================================

function useMeasuredWidth(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(0)

  useLayoutEffect(() => {
    const node = ref.current
    if (!node) return
    const measure = () => setWidth(node.clientWidth)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  return [ref, width]
}

// ============================================================================
// Component
// ============================================================================

type DragState = (
  | { mode: 'handle-start' }
  | { mode: 'handle-end' }
  | { mode: 'pan'; grabMs: number; startFrom: number; startTo: number }
  | { mode: 'new'; originMs: number }
  | { mode: 'lens-pan'; grabMs: number; startFrom: number; startTo: number }
) & {
  // Pointer x at pointer-down. Staging only begins once movement exceeds the
  // drag threshold ("latched"), so a plain click never flashes a zero-delta
  // pending popover.
  startClientX: number
  latched: boolean
}

const LENS_MIN_BAND_PX = 18

// Row below the axis reserved for the lens-width chip so it never clips the
// strip. Wider than PILL_GUTTER, so it also covers the pending pills' headroom.
const CHIP_ROW = 26
const KEYBOARD_NUDGE_FRACTION = 0.1

// Movement below this many px is a click, not a drag — nothing is staged.
const DRAG_THRESHOLD_PX = 3

/** True once pointer movement is a real drag rather than a plain click. */
export function dragExceedsThreshold(startClientX: number, clientX: number): boolean {
  return Math.abs(clientX - startClientX) >= DRAG_THRESHOLD_PX
}

export function TimelineScrubber({
  buckets,
  coverage,
  gaps,
  loading,
  historyUnavailableBeforeMs,
  domain,
  selection,
  onSelectionChange,
  maxSelectionMs,
  presets,
  onPresetSelect,
  className,
  lens,
  onLensChange,
  lensPresets,
  liveState,
  onLiveChipClick,
}: TimelineScrubberProps) {
  const [containerRef, measuredWidth] = useMeasuredWidth()
  const width = measuredWidth > 0 ? measuredWidth : FALLBACK_WIDTH

  const svgRef = useRef<SVGSVGElement | null>(null)
  const dragRef = useRef<DragState | null>(null)
  const [maxHint, setMaxHint] = useState(false)
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Hovered strip position (px + ms) for the bucket tooltip. Cleared on leave
  // and suppressed while any drag is in flight.
  const [hover, setHover] = useState<{ x: number; ms: number } | null>(null)
  // True while a staging drag is in flight (pointer still down). The Run/Reset
  // popover waits for release — it would sit under a moving cursor mid-drag.
  const [dragInFlight, setDragInFlight] = useState(false)
  // Pending (staged) range. Mirrored into a ref so the window-level drag/key
  // listeners read the latest value without re-subscribing.
  const [pending, setPendingState] = useState<ScrubberRange | null>(null)
  const pendingRef = useRef<ScrubberRange | null>(null)
  const setPending = useCallback((next: ScrubberRange | null) => {
    pendingRef.current = next
    setPendingState(next)
  }, [])

  // The range shown on the strip: the staged one while brushing, else applied.
  const display = pending ?? selection

  const domainSpan = domain.toMs - domain.fromMs
  const maxTotal = useMemo(
    () => buckets.reduce((m, b) => Math.max(m, b.total), 0),
    [buckets],
  )

  // Recording gaps + legacy coverage spans folded into one merged band list.
  const gapBands = useMemo(
    () => mergeGapRanges([
      ...(gaps ?? []),
      ...(coverage ?? []).map((c) => ({ fromMs: c.startMs, toMs: c.endMs })),
    ]),
    [gaps, coverage],
  )

  const flashMaxHint = useCallback(() => {
    setMaxHint(true)
    if (hintTimer.current) clearTimeout(hintTimer.current)
    hintTimer.current = setTimeout(() => setMaxHint(false), 1600)
  }, [])

  useEffect(() => () => { if (hintTimer.current) clearTimeout(hintTimer.current) }, [])

  const runCommand = useCallback(
    (cmd: ScrubberCommand, opts?: { clampedToMax?: boolean }) => {
      if (opts?.clampedToMax) flashMaxHint()
      const { state, commit } = applyScrubberCommand({ pending: pendingRef.current }, cmd)
      setPending(state.pending)
      if (commit) onSelectionChange(commit)
    },
    [onSelectionChange, flashMaxHint, setPending],
  )

  // Brush mutations stage a pending range (query not run).
  const stage = useCallback(
    (result: { selection: ScrubberRange; clampedToMax: boolean }) => {
      runCommand({ kind: 'brush', range: result.selection }, { clampedToMax: result.clampedToMax })
    },
    [runCommand],
  )

  // Explicit intents (preset/step/zoom) commit immediately and clear pending.
  const applyImmediate = useCallback(
    (result: { selection: ScrubberRange; clampedToMax: boolean }) => {
      runCommand({ kind: 'apply', range: result.selection }, { clampedToMax: result.clampedToMax })
    },
    [runCommand],
  )

  const runPending = useCallback(() => runCommand({ kind: 'run' }), [runCommand])
  const resetPending = useCallback(() => runCommand({ kind: 'reset' }), [runCommand])

  // An external selection change (host clamp, deep-link, extend, etc.) invalidates
  // any staged range — EXCEPT the live tick's slide, which must leave a pending
  // brush intact (see selectionChangeClearsPending). `liveState` is read from the
  // current render's closure: this effect re-runs whenever selection changes, and
  // in live mode a genuine change (extend) flips liveState to frozen in the same
  // render, so the gate reads the right value.
  useEffect(() => {
    if (!selectionChangeClearsPending(liveState?.kind)) return
    runCommand({ kind: 'sync' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selection.fromMs, selection.toMs])

  // Enter runs the staged query, Escape discards it — only while one exists.
  useEffect(() => {
    if (!pending) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter') {
        e.preventDefault()
        runPending()
      } else if (e.key === 'Escape') {
        e.preventDefault()
        resetPending()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pending, runPending, resetPending])

  const pointerMs = useCallback(
    (clientX: number): number => {
      const rect = svgRef.current?.getBoundingClientRect()
      if (!rect || rect.width <= 0) return domain.fromMs
      const x = Math.max(0, Math.min(rect.width, clientX - rect.left))
      return xToMs(x, domain, rect.width)
    },
    [domain],
  )

  // Global drag listeners — attached only while a drag is in flight. Every drag
  // mode stages a pending range; nothing commits until "Run query".
  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      const drag = dragRef.current
      if (!drag) return
      if (!drag.latched) {
        if (!dragExceedsThreshold(drag.startClientX, e.clientX)) return
        drag.latched = true
      }
      const ms = pointerMs(e.clientX)
      if (drag.mode === 'handle-start') {
        stage(clampSelection({ fromMs: ms, toMs: display.toMs }, domain, maxSelectionMs, 'start'))
      } else if (drag.mode === 'handle-end') {
        stage(clampSelection({ fromMs: display.fromMs, toMs: ms }, domain, maxSelectionMs, 'end'))
      } else if (drag.mode === 'pan') {
        stage({
          selection: panSelection({ fromMs: drag.startFrom, toMs: drag.startTo }, ms - drag.grabMs, domain),
          clampedToMax: false,
        })
      } else if (drag.mode === 'new') {
        stage(clampSelection({ fromMs: drag.originMs, toMs: ms }, domain, maxSelectionMs,
          ms >= drag.originMs ? 'start' : 'end'))
      } else if (drag.mode === 'lens-pan') {
        // Lens pan is instant (no staging): the lens is a free view, not a query.
        // Width is preserved and clamped inside the applied selection.
        const shift = ms - drag.grabMs
        onLensChange?.(clampLensToSelection(
          { fromMs: drag.startFrom + shift, toMs: drag.startTo + shift },
          selection,
        ))
      }
    }
    const onUp = () => { dragRef.current = null; setDragInFlight(false) }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [pointerMs, stage, display.fromMs, display.toMs, domain, maxSelectionMs, onLensChange, selection.fromMs, selection.toMs])

  const beginTrackDrag = useCallback(
    (e: React.PointerEvent) => {
      // Only the primary button starts a brush.
      if (e.button !== 0) return
      const ms = pointerMs(e.clientX)
      // Inside the current (displayed) selection → pan; outside → draw a new one.
      setDragInFlight(true)
      if (ms >= display.fromMs && ms <= display.toMs) {
        dragRef.current = {
          mode: 'pan',
          grabMs: ms,
          startFrom: display.fromMs,
          startTo: display.toMs,
          startClientX: e.clientX,
          latched: false,
        }
      } else {
        dragRef.current = { mode: 'new', originMs: ms, startClientX: e.clientX, latched: false }
      }
    },
    [pointerMs, display.fromMs, display.toMs],
  )

  const beginHandleDrag = useCallback(
    (edge: 'start' | 'end') => (e: React.PointerEvent) => {
      if (e.button !== 0) return
      setDragInFlight(true)
      dragRef.current = {
        mode: edge === 'start' ? 'handle-start' : 'handle-end',
        startClientX: e.clientX,
        latched: false,
      }
    },
    [],
  )

  const nudge = useCallback(
    (direction: -1 | 1) => {
      const step = Math.max(MIN_SELECTION_MS, (display.toMs - display.fromMs) * KEYBOARD_NUDGE_FRACTION)
      stage({ selection: panSelection(display, direction * step, domain), clampedToMax: false })
    },
    [display.fromMs, display.toMs, domain, stage],
  )

  const beginLensDrag = useCallback(
    (e: React.PointerEvent) => {
      if (e.button !== 0 || !lens) return
      // Own the gesture: don't also start a selection grab-pan on the layer below.
      e.stopPropagation()
      const ms = pointerMs(e.clientX)
      dragRef.current = {
        mode: 'lens-pan',
        grabMs: ms,
        startFrom: lens.fromMs,
        startTo: lens.toMs,
        startClientX: e.clientX,
        latched: false,
      }
    },
    [pointerMs, lens?.fromMs, lens?.toMs],
  )

  const onLensKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (!lens || (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight')) return
      e.preventDefault()
      const dir = e.key === 'ArrowLeft' ? -1 : 1
      const step = Math.max(MIN_SELECTION_MS, (lens.toMs - lens.fromMs) * KEYBOARD_NUDGE_FRACTION)
      onLensChange?.(clampLensToSelection(
        { fromMs: lens.fromMs + dir * step, toMs: lens.toMs + dir * step },
        selection,
      ))
    },
    [lens?.fromMs, lens?.toMs, onLensChange, selection.fromMs, selection.toMs],
  )

  const onHandleKeyDown = useCallback(
    (edge: 'start' | 'end', e: React.KeyboardEvent) => {
      const step = Math.max(MIN_SELECTION_MS, (display.toMs - display.fromMs) * KEYBOARD_NUDGE_FRACTION)
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault()
        const dir = e.key === 'ArrowLeft' ? -1 : 1
        if (edge === 'start') {
          stage(clampSelection(
            { fromMs: display.fromMs + dir * step, toMs: display.toMs },
            domain, maxSelectionMs, 'end',
          ))
        } else {
          stage(clampSelection(
            { fromMs: display.fromMs, toMs: display.toMs + dir * step },
            domain, maxSelectionMs, 'start',
          ))
        }
      }
    },
    [display.fromMs, display.toMs, domain, maxSelectionMs, stage],
  )

  if (domainSpan <= 0) {
    return (
      <div className={className} ref={containerRef}>
        <div className="h-[44px] rounded-lg border border-theme-border bg-theme-surface" />
      </div>
    )
  }

  const selFromX = msToX(display.fromMs, domain, width)
  const selToX = msToX(display.toMs, domain, width)
  const appliedFromX = msToX(selection.fromMs, domain, width)
  const appliedToX = msToX(selection.toMs, domain, width)
  const preRetentionX = historyUnavailableBeforeMs != null
    ? msToX(historyUnavailableBeforeMs, domain, width)
    : 0

  const axisTicks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({
    x: f * width,
    ms: domain.fromMs + f * domainSpan,
  }))

  return (
    <div ref={containerRef} className={className}>
      {(presets?.length || onPresetSelect || liveState) && (
        <div className="mb-1.5 flex items-center gap-1.5">
          <div className="flex items-center gap-1">
            {presets?.map((p) => (
              <button
                key={p.label}
                type="button"
                onClick={() => { resetPending(); onPresetSelect?.(p) }}
                className="rounded border border-theme-border bg-theme-elevated px-2 py-0.5 text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="ml-auto flex items-center gap-2">
            <div className="flex items-center gap-1">
              <ScrubberIconButton label="Step earlier" onClick={() => applyImmediate({ selection: stepSelection(selection, -1, domain), clampedToMax: false })}>
                <ChevronLeft className="h-3.5 w-3.5" />
              </ScrubberIconButton>
              <ScrubberIconButton label="Step later" onClick={() => applyImmediate({ selection: stepSelection(selection, 1, domain), clampedToMax: false })}>
                <ChevronRight className="h-3.5 w-3.5" />
              </ScrubberIconButton>
              <ScrubberIconButton label="Zoom out" onClick={() => applyImmediate(zoomSelection(selection, 2, domain, maxSelectionMs))}>
                <ZoomOut className="h-3.5 w-3.5" />
              </ScrubberIconButton>
              <ScrubberIconButton label="Zoom in" onClick={() => applyImmediate(zoomSelection(selection, 0.5, domain, maxSelectionMs))}>
                <ZoomIn className="h-3.5 w-3.5" />
              </ScrubberIconButton>
            </div>
            {/* Live/paused chip — hidden while a brush is staged: the pending
                Run/Reset popover pops up into this row, so the two would collide. */}
            {liveState && !pending && (
              <TimelineLiveChip state={liveState} onClick={onLiveChipClick} />
            )}
          </div>
        </div>
      )}

      {/* Hover handlers live on the WRAPPER (not the svg): the selection/lens
          overlay divs sit above the svg and would swallow hover inside the
          selection otherwise. They only set state — overlay drags unaffected. */}
      <div
        className="relative select-none"
        style={{ height: TRACK_HEIGHT + AXIS_HEIGHT + (lens && onLensChange ? CHIP_ROW : PILL_GUTTER) }}
        onMouseMove={(e) => {
          if (dragRef.current) { setHover(null); return }
          const rect = svgRef.current?.getBoundingClientRect()
          if (!rect) return
          setHover({ x: e.clientX - rect.left, ms: pointerMs(e.clientX) })
        }}
        onMouseLeave={() => setHover(null)}
      >
        <svg
          ref={svgRef}
          width="100%"
          height={TRACK_HEIGHT + AXIS_HEIGHT}
          className="block cursor-crosshair overflow-visible"
          onPointerDown={beginTrackDrag}
          role="img"
          aria-label="Event volume over the loaded window"
        >
          {/* Track background */}
          <rect
            x={0}
            y={0}
            width={width}
            height={TRACK_HEIGHT}
            rx={6}
            fill="var(--bg-surface)"
            stroke="var(--border-default)"
          />

          {/* Pre-retention dim region */}
          {historyUnavailableBeforeMs != null && preRetentionX > 0 && (
            <>
              <rect
                x={0}
                y={0}
                width={preRetentionX}
                height={TRACK_HEIGHT}
                fill="var(--bg-elevated)"
                opacity={0.6}
                data-testid="scrubber-preretention"
              />
              <line
                x1={preRetentionX}
                y1={0}
                x2={preRetentionX}
                y2={TRACK_HEIGHT}
                stroke="var(--border-light)"
                strokeDasharray="2 2"
              />
            </>
          )}

          {/* Histogram bars */}
          {/* Skeleton histogram while loading — same geometry as real bars so
              nothing shifts when data lands. Heights from a fixed pattern (no
              randomness: SSR-stable, no re-roll on re-render). */}
          {loading && buckets.length === 0 && Array.from({ length: 60 }, (_, i) => {
            const h = 6 + ((i * 37) % 23)
            const w = width / 60
            return (
              <rect
                key={`skel-${i}`}
                data-testid="scrubber-skeleton-bar"
                x={i * w + 0.5}
                y={TRACK_HEIGHT - h}
                width={Math.max(1, w - 1)}
                height={h}
                fill="var(--border-default)"
                rx={1}
                className="animate-pulse"
                opacity={0.35}
              />
            )
          })}
          {buckets.map((b, i) => {
            const x = msToX(b.startMs, domain, width)
            const barW = Math.max(1, msToX(b.endMs, domain, width) - x - 0.5)
            const h = barHeight(b.total, maxTotal, TRACK_HEIGHT)
            if (h <= 0) return null
            const warnFrac = b.total > 0 ? Math.min(1, b.warnings / b.total) : 0
            const warnH = b.warnings > 0 ? Math.max(2, h * warnFrac) : 0
            const y = TRACK_HEIGHT - h
            return (
              <g key={`bar-${i}`} data-testid="scrubber-bar">
                <rect x={x} y={y} width={barW} height={h} fill="var(--accent)" fillOpacity={0.38} rx={1} />
                {warnH > 0 && (
                  <rect x={x} y={TRACK_HEIGHT - warnH} width={barW} height={warnH} fill="var(--color-error)" fillOpacity={0.75} rx={1} />
                )}
              </g>
            )
          })}

          {/* Selection dimming outside the (displayed) brush */}
          <rect x={0} y={0} width={Math.max(0, selFromX)} height={TRACK_HEIGHT} fill="var(--bg-base)" opacity={0.55} pointerEvents="none" />
          <rect x={selToX} y={0} width={Math.max(0, width - selToX)} height={TRACK_HEIGHT} fill="var(--bg-base)" opacity={0.55} pointerEvents="none" />

          {/* Applied selection markers (quieter) shown behind a pending brush so
              the user can compare current-applied vs staged. */}
          {pending && (
            <g data-testid="scrubber-applied-marker" pointerEvents="none">
              <line x1={appliedFromX} y1={0} x2={appliedFromX} y2={TRACK_HEIGHT} stroke="var(--selection-border)" strokeWidth={1} opacity={0.4} strokeDasharray="2 3" />
              <line x1={appliedToX} y1={0} x2={appliedToX} y2={TRACK_HEIGHT} stroke="var(--selection-border)" strokeWidth={1} opacity={0.4} strokeDasharray="2 3" />
            </g>
          )}

          {/* Axis ticks */}
          {axisTicks.map((t, i) => (
            <text
              key={`tick-${i}`}
              x={Math.max(2, Math.min(width - 2, t.x))}
              y={TRACK_HEIGHT + AXIS_HEIGHT - 4}
              textAnchor={i === 0 ? 'start' : i === axisTicks.length - 1 ? 'end' : 'middle'}
              fontSize={10}
              fill="var(--text-tertiary)"
            >
              {formatAxisTick(t.ms, domainSpan)}
            </text>
          ))}
        </svg>

        {/* Recording-gap bands — periods with no recorded data (connector
            offline). Diagonal theme-border hatch, rendered above the track but
            BELOW the selection/lens overlays (which come later in DOM). Inert so
            brushing/dragging across a gap is unaffected; the selection's
            translucent tint lets the hatch keep reading inside an applied range. */}
        {gapBands.map((g, i) => {
          const gx = msToX(g.fromMs, domain, width)
          const left = Math.max(0, gx)
          const right = Math.min(width, msToX(g.toMs, domain, width))
          if (right - left <= 0) return null
          return (
            <div
              key={`gap-${i}`}
              className="pointer-events-none absolute top-0"
              style={{
                left,
                width: right - left,
                height: TRACK_HEIGHT,
                background:
                  'repeating-linear-gradient(45deg, transparent 0, transparent 4px, var(--border-default) 4px, var(--border-default) 5px)',
                opacity: 0.55,
              }}
              title="No data recorded — connector was offline"
              data-testid="scrubber-gap"
            />
          )
        })}

        {/* Selection overlay (HTML for focusable handles + pills). It covers the
            SVG inside the brush, so it must run the same hit-test itself:
            pointer-down here grabs-and-pans the whole range, width preserved. */}
        <div
          className="absolute top-0"
          style={{ left: selFromX, width: Math.max(0, selToX - selFromX), height: TRACK_HEIGHT }}
          onPointerDown={beginTrackDrag}
        >
          <div
            className="h-full cursor-grab touch-none border-x-2 active:cursor-grabbing"
            style={{ background: 'var(--selection-bg)', borderColor: 'var(--selection-border)' }}
          />
        </div>

        {pending ? (
          <ScrubberPendingOverlay
            showPopover={!dragInFlight}
            fromX={selFromX}
            toX={selToX}
            fromMs={display.fromMs}
            toMs={display.toMs}
            trackHeight={TRACK_HEIGHT}
            width={width}
            domain={domain}
            onStartPointerDown={beginHandleDrag('start')}
            onEndPointerDown={beginHandleDrag('end')}
            onStartKeyDown={(e) => onHandleKeyDown('start', e)}
            onEndKeyDown={(e) => onHandleKeyDown('end', e)}
            onRun={runPending}
            onReset={resetPending}
          />
        ) : (
          <>
            <ScrubberHandle
              edge="start"
              x={selFromX}
              valueMs={display.fromMs}
              domain={domain}
              onPointerDown={beginHandleDrag('start')}
              onKeyDown={(e) => onHandleKeyDown('start', e)}
            />
            <ScrubberHandle
              edge="end"
              x={selToX}
              valueMs={display.toMs}
              domain={domain}
              onPointerDown={beginHandleDrag('end')}
              onKeyDown={(e) => onHandleKeyDown('end', e)}
            />
          </>
        )}

        {/* LENS band — the swimlane's visible window inside the applied selection.
            Draggable to pan the swimlane (width fixed by the swimlane's zoom, so
            no resize handles in v1). Dimmed + inert while a pending brush is
            staged, but never invisible: a 15m lens inside a 24h selection is a
            ~2px sliver, so the band keeps a minimum visual width and grip dots
            or nobody discovers it exists. */}
        {lens && (() => {
          // Always draggable — moving the lens changes what you LOOK at, not
          // what's queried, so a staged (pending) brush is no reason to lock it.
          // Pending only dims it for visual hierarchy.
          const dimmed = !shouldShowLensBand(lens, pending)
          const lensFromX = msToX(lens.fromMs, domain, width)
          const lensToX = msToX(lens.toMs, domain, width)
          const actualW = Math.max(2, lensToX - lensFromX)
          const visualW = Math.max(LENS_MIN_BAND_PX, actualW)
          const left = Math.min(Math.max(0, lensFromX - (visualW - actualW) / 2), width - visualW)
          return (
            <div
              role="slider"
              tabIndex={0}
              aria-label="Visible window within the selection"
              aria-valuemin={selection.fromMs}
              aria-valuemax={selection.toMs}
              aria-valuenow={lens.fromMs}
              aria-valuetext={`${formatScrubberPill(lens.fromMs)} to ${formatScrubberPill(lens.toMs)}`}
              onPointerDown={beginLensDrag}
              onKeyDown={onLensKeyDown}
              className="absolute top-0 touch-none rounded-sm focus:outline-none focus:ring-2 focus:ring-accent flex items-center justify-center gap-0.5 cursor-grab active:cursor-grabbing"
              title="Drag to move the swimlane's view window"
              style={{
                left,
                width: visualW,
                height: TRACK_HEIGHT,
                background: 'var(--selection-bg)',
                opacity: dimmed ? 0.45 : 0.85,
                border: '2px solid var(--accent)',
                boxShadow: '0 0 0 1px var(--bg-theme-base)',
              }}
              data-testid="scrubber-lens"
            >
              <span aria-hidden className="rounded-full" style={{ width: 3, height: 12, background: 'var(--accent)' }} />
              <span aria-hidden className="rounded-full" style={{ width: 3, height: 12, background: 'var(--accent)' }} />
            </div>
          )
        })()}

        {/* Lens-width chip — the segmented ⟨ − 8h + ⟩ control under the band. Hidden
            while a brush is staged: the pending pills + Run/Reset popover own that
            zone, and a resize control has no meaning against an uncommitted range. */}
        {lens && onLensChange && !pending && (() => {
          const lensFromX = msToX(lens.fromMs, domain, width)
          const lensToX = msToX(lens.toMs, domain, width)
          const actualW = Math.max(2, lensToX - lensFromX)
          const visualW = Math.max(LENS_MIN_BAND_PX, actualW)
          const bandLeft = Math.min(Math.max(0, lensFromX - (visualW - actualW) / 2), width - visualW)
          return (
            <LensDurationChip
              lens={lens}
              selection={selection}
              presets={lensPresets}
              centerX={bandLeft + visualW / 2}
              top={TRACK_HEIGHT + AXIS_HEIGHT + 10}
              containerWidth={width}
              onLensChange={onLensChange}
            />
          )
        })()}

        {/* Bucket tooltip — small box above the strip under the cursor. Hidden
            while brushing (popover owns that zone) and over the axis gutter. */}
        {hover && !pending && !dragInFlight && (() => {
          const inGap = gapBands.some((g) => hover.ms >= g.fromMs && hover.ms <= g.toMs)
          const bucket = findBucketAt(hover.ms, buckets)
          if (!inGap && !bucket) return null
          const label = inGap
            ? 'No data recorded — connector offline'
            : `${bucket!.total.toLocaleString()} events${bucket!.warnings > 0 ? ` · ${bucket!.warnings.toLocaleString()} warnings` : ''}`
          const timeLabel = inGap ? null : `${formatScrubberPill(bucket!.startMs)} – ${new Date(bucket!.endMs).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
          return (
            <div
              className="pointer-events-none absolute z-40 rounded-md border border-theme-border bg-theme-elevated px-2 py-1 shadow-theme-md"
              style={
                // Cursor-anchored INSIDE the plot (chart convention): above the
                // strip collides with the header controls, below with the pills.
                hover.x > width - 190
                  ? { right: width - hover.x + 10, top: 3 }
                  : { left: hover.x + 10, top: 3 }
              }
              data-testid="scrubber-bucket-tooltip"
            >
              {timeLabel && <div className="whitespace-nowrap text-[10px] text-theme-text-tertiary">{timeLabel}</div>}
              <div className="whitespace-nowrap text-xs text-theme-text-primary">{label}</div>
            </div>
          )
        })()}

        {maxHint && (
          <div
            className="pointer-events-none absolute -top-6 rounded bg-theme-base px-2 py-0.5 text-xs text-theme-text-secondary shadow-theme-sm"
            style={{ left: Math.max(0, (selFromX + selToX) / 2 - 60) }}
          >
            Max {formatDuration(maxSelectionMs ?? 0)} range
          </div>
        )}
      </div>

      {/* Screen-reader nudge controls for the whole window */}
      <div className="sr-only">
        <button type="button" onClick={() => nudge(-1)}>Move selection earlier</button>
        <button type="button" onClick={() => nudge(1)}>Move selection later</button>
      </div>
    </div>
  )
}

/**
 * The consolidated live/paused control that lives in the scrubber header. Three
 * states:
 *   live + latched   → inert "● Live" (following now).
 *   live + unlatched → clickable "● Live · jump to now" (re-latch the lens).
 *   frozen           → a quiet "as of HH:MM" caption (amber once stale) next to a
 *                      loud green "Go live" CTA that returns to live mode; when
 *                      newer events exist, the CTA carries an approximate "· N new".
 */
function TimelineLiveChip({ state, onClick }: { state: TimelineLiveState; onClick?: () => void }) {
  if (state.kind === 'live') {
    const dot = (
      <span className="relative flex h-2 w-2">
        <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-green-500 opacity-75" />
        <span className="relative inline-flex h-2 w-2 rounded-full bg-green-500" />
      </span>
    )
    if (state.latched) {
      return (
        <span
          className="flex items-center gap-1.5 rounded-full border border-green-500/30 bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-600 dark:text-green-400"
          title="Following now"
          data-testid="timeline-live-chip"
        >
          {dot}
          Live
        </span>
      )
    }
    return (
      <button
        type="button"
        onClick={onClick}
        title="Jump to the live edge"
        data-testid="timeline-live-chip"
        className="flex items-center gap-1.5 rounded-full border border-green-500/30 bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-600 transition-colors hover:bg-green-500/20 dark:text-green-400"
      >
        {dot}
        Live
        <span className="text-green-600/70 dark:text-green-400/70">· jump to now</span>
      </button>
    )
  }

  const stale = Date.now() - state.asOfMs > STALE_AMBER_AFTER_MS
  const newCount = state.newEventCount ?? 0
  const newLabel = newCount > 999 ? '999+' : String(newCount)
  return (
    <span className="flex items-center gap-2" data-testid="timeline-paused-chip">
      <span
        className={clsx(
          'whitespace-nowrap text-xs',
          stale ? 'text-amber-600 dark:text-amber-400' : 'text-theme-text-tertiary',
        )}
      >
        as of {formatAsOfTime(state.asOfMs)}
      </span>
      <button
        type="button"
        onClick={onClick}
        title="Return to live — follow now again"
        className="flex items-center gap-1.5 rounded-full border border-emerald-500/50 px-2 py-0.5 text-xs font-medium text-emerald-600 transition-colors hover:bg-emerald-500/10 dark:text-emerald-400"
      >
        <Play className="h-3 w-3 fill-current" />
        Go live
        {newCount > 0 && <span className="opacity-75">{`· ${newLabel} new`}</span>}
      </button>
    </span>
  )
}

// Pre-measurement estimate for a "Jul 2 15:24:49" pill at 10px DM Sans.
const PILL_WIDTH_ESTIMATE = 84

/**
 * Pending (staged) brush overlay — GCP Cloud Logging vertical layout: Run/Reset
 * popover floating ABOVE the strip, dashed guides + round dot handles through
 * it (dots draggable to fine-tune without committing), precise timestamp pills
 * BELOW the baseline. Pills clamp inside the container and push apart when the
 * range is narrow, so no element overlaps another. Exported for testing.
 */

/** Bucket under a strip position, or null between/outside bars. Exported for tests. */
export function findBucketAt(ms: number, buckets: ScrubberBucket[]): ScrubberBucket | null {
  for (const b of buckets) {
    if (ms >= b.startMs && ms < b.endMs) return b
  }
  return null
}

export function ScrubberPendingOverlay({
  fromX,
  toX,
  fromMs,
  toMs,
  trackHeight,
  width,
  domain,
  onStartPointerDown,
  onEndPointerDown,
  onStartKeyDown,
  onEndKeyDown,
  onRun,
  onReset,
  showPopover = true,
}: {
  fromX: number
  toX: number
  fromMs: number
  toMs: number
  trackHeight: number
  width: number
  domain: ScrubberRange
  onStartPointerDown: (e: React.PointerEvent) => void
  onEndPointerDown: (e: React.PointerEvent) => void
  onStartKeyDown: (e: React.KeyboardEvent) => void
  onEndKeyDown: (e: React.KeyboardEvent) => void
  onRun: () => void
  onReset: () => void
  // Hidden while the staging drag is still in flight — the popover appears on release.
  showPopover?: boolean
}) {
  const fromLabel = formatScrubberPillPrecise(fromMs)
  const toLabel = formatScrubberPillPrecise(toMs)

  // Measure real pill widths post-paint so edge clamping is exact regardless
  // of locale/font; the estimate only covers SSR and the first frame.
  const fromPillRef = useRef<HTMLSpanElement | null>(null)
  const toPillRef = useRef<HTMLSpanElement | null>(null)
  const [pillWidths, setPillWidths] = useState({
    from: PILL_WIDTH_ESTIMATE,
    to: PILL_WIDTH_ESTIMATE,
  })
  useLayoutEffect(() => {
    const from = fromPillRef.current?.offsetWidth
    const to = toPillRef.current?.offsetWidth
    if (!from || !to) return
    setPillWidths((prev) => (prev.from === from && prev.to === to ? prev : { from, to }))
  }, [fromLabel, toLabel])

  const { fromCenter, toCenter } = layoutPendingPillCenters(
    fromX, toX, width, pillWidths.from, pillWidths.to,
  )

  // Pills sit just below the dot handles (dots straddle the baseline).
  const pillTop = trackHeight + PENDING_DOT_RADIUS + 1

  // Keep the popover fully on-strip when the range hugs an edge.
  // Clamp by the popover's half-width: an abs-positioned box with `left` set
  // shrink-to-fits against the container's right edge — under-clamping makes
  // "Run query" wrap into a tall two-line box near the edges.
  const popoverCenter = Math.max(95, Math.min(width - 95, (fromX + toX) / 2))

  return (
    <>
      {/* Dashed edge guides */}
      <div
        className="pointer-events-none absolute top-0 border-l border-dashed"
        style={{ left: fromX, height: trackHeight, borderColor: 'var(--accent)' }}
        data-testid="scrubber-pending-guide"
      />
      <div
        className="pointer-events-none absolute top-0 border-l border-dashed"
        style={{ left: toX, height: trackHeight, borderColor: 'var(--accent)' }}
        data-testid="scrubber-pending-guide"
      />

      {/* Precise timestamp pills below the baseline (GCP position) */}
      <span
        ref={fromPillRef}
        className="pointer-events-none absolute z-10 whitespace-nowrap rounded bg-accent px-1.5 py-0.5 text-[10px] font-medium leading-[12px] text-white shadow-theme-sm"
        style={{ left: fromCenter, top: pillTop, transform: 'translateX(-50%)' }}
        data-testid="scrubber-pending-pill"
      >
        {fromLabel}
      </span>
      <span
        ref={toPillRef}
        className="pointer-events-none absolute z-10 whitespace-nowrap rounded bg-accent px-1.5 py-0.5 text-[10px] font-medium leading-[12px] text-white shadow-theme-sm"
        style={{ left: toCenter, top: pillTop, transform: 'translateX(-50%)' }}
        data-testid="scrubber-pending-pill"
      >
        {toLabel}
      </span>

      {/* Round baseline dot handles (draggable to fine-tune, still uncommitted) */}
      <ScrubberPendingDot
        edge="start"
        x={fromX}
        valueMs={fromMs}
        baselineY={trackHeight}
        domain={domain}
        onPointerDown={onStartPointerDown}
        onKeyDown={onStartKeyDown}
      />
      <ScrubberPendingDot
        edge="end"
        x={toX}
        valueMs={toMs}
        baselineY={trackHeight}
        domain={domain}
        onPointerDown={onEndPointerDown}
        onKeyDown={onEndKeyDown}
      />

      {/* Floating Run/Reset popover above the strip. z-50 = the repo's floating
          menu/dialog layer, so it may overlay the toolbar without being clipped.
          Hidden mid-drag; it appears when the pointer is released. */}
      {showPopover && <div
        className="absolute z-50 flex flex-nowrap items-center gap-1 whitespace-nowrap rounded-lg border border-theme-border bg-theme-elevated px-1.5 py-1 shadow-theme-lg"
        style={{ left: popoverCenter, top: 4, transform: 'translateX(-50%)' }}
        data-testid="scrubber-pending-popover"
        role="group"
        aria-label="Pending time range"
      >
        <button
          type="button"
          onClick={onReset}
          className="rounded px-2 py-1 text-xs text-theme-text-secondary transition-colors hover:text-theme-text-primary"
        >
          Reset
        </button>
        <button
          type="button"
          onClick={onRun}
          className="btn-brand px-2.5 py-1 text-xs font-medium"
        >
          Run query
        </button>
      </div>}
    </>
  )
}

const PENDING_DOT_RADIUS = 6

function ScrubberPendingDot({
  edge,
  x,
  valueMs,
  baselineY,
  domain,
  onPointerDown,
  onKeyDown,
}: {
  edge: 'start' | 'end'
  x: number
  valueMs: number
  baselineY: number
  domain: ScrubberRange
  onPointerDown: (e: React.PointerEvent) => void
  onKeyDown: (e: React.KeyboardEvent) => void
}) {
  return (
    <div
      role="slider"
      tabIndex={0}
      aria-label={edge === 'start' ? 'Pending selection start' : 'Pending selection end'}
      aria-valuemin={domain.fromMs}
      aria-valuemax={domain.toMs}
      aria-valuenow={valueMs}
      aria-valuetext={formatScrubberPillPrecise(valueMs)}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      className="absolute touch-none rounded-full border-2 shadow-theme-sm focus:outline-none focus:ring-2 focus:ring-accent"
      style={{
        left: x - PENDING_DOT_RADIUS,
        top: baselineY - PENDING_DOT_RADIUS,
        width: PENDING_DOT_RADIUS * 2,
        height: PENDING_DOT_RADIUS * 2,
        background: 'var(--accent)',
        borderColor: 'var(--bg-surface)',
        cursor: 'ew-resize',
      }}
    />
  )
}

function ScrubberHandle({
  edge,
  x,
  valueMs,
  domain,
  onPointerDown,
  onKeyDown,
}: {
  edge: 'start' | 'end'
  x: number
  valueMs: number
  domain: ScrubberRange
  onPointerDown: (e: React.PointerEvent) => void
  onKeyDown: (e: React.KeyboardEvent) => void
}) {
  return (
    <div
      role="slider"
      tabIndex={0}
      aria-label={edge === 'start' ? 'Selection start' : 'Selection end'}
      aria-valuemin={domain.fromMs}
      aria-valuemax={domain.toMs}
      aria-valuenow={valueMs}
      aria-valuetext={formatScrubberPill(valueMs)}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      // z-30 keeps the handle grabbable when the lens band parks on top of a
      // selection edge (the lens renders later in the DOM and would otherwise
      // swallow the hit); explicit width gives a real grab target.
      className="absolute top-0 z-30 flex w-[11px] cursor-ew-resize touch-none flex-col items-center focus:outline-none"
      style={{ left: x - 5, height: TRACK_HEIGHT }}
    >
      {/* Label sits BELOW the track — same zone as the pending brush pills —
          so it never covers the header controls (pan/zoom/live chip) above. */}
      <span
        className="pointer-events-none absolute whitespace-nowrap rounded bg-theme-elevated px-1.5 py-0.5 text-[10px] font-medium text-theme-text-secondary shadow-theme-sm"
        style={{ top: TRACK_HEIGHT + 4, transform: edge === 'start' ? 'translateX(-40%)' : 'translateX(-60%)' }}
      >
        {formatScrubberPill(valueMs)}
      </span>
      <span
        className="h-full w-[3px] rounded-full"
        style={{ background: 'var(--accent)' }}
      />
    </div>
  )
}

// Half-width estimate used to clamp the chip inside the container before it's
// measured (SSR / first paint). The chip is ~90px wide with a 2-digit label.
const CHIP_HALF_PX = 46

/**
 * Segmented lens-width control (⟨ − 8h + ⟩) that rides under the lens band. The
 * − / + steps resize the lens WIDTH around the band's center; the center label
 * opens a preset popover. Every change flows through `onLensChange` (instant, no
 * staging) — the swimlane follows because it shares the same lens window.
 */
function LensDurationChip({
  lens,
  selection,
  presets,
  centerX,
  top,
  containerWidth,
  onLensChange,
}: {
  lens: ScrubberRange
  selection: ScrubberRange
  presets?: ScrubberPreset[]
  centerX: number
  top: number
  containerWidth: number
  onLensChange: (lens: ScrubberRange) => void
}) {
  const [menuOpen, setMenuOpen] = useState(false)
  // The chip rides at the top of the scrubber strip, so opening the preset menu
  // upward (its natural place, clear of the brush track below) clips the top
  // presets off the page edge. Flip down when there's more room below, and cap
  // the height to the chosen gap so no preset is ever unreachable.
  const [placement, setPlacement] = useState<{ up: boolean; maxH: number }>({ up: true, maxH: 320 })
  const rootRef = useRef<HTMLDivElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const widthMs = lens.toMs - lens.fromMs
  const hasPresets = !!presets?.length

  // Outside-click and Escape close the popover. Escape stops propagation so it
  // doesn't also bubble to the strip's pending-reset handler.
  useEffect(() => {
    if (!menuOpen) return
    const onDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setMenuOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setMenuOpen(false) }
    }
    window.addEventListener('pointerdown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [menuOpen])

  // Decide open direction from the space around the chip before painting the
  // menu, so it never clips at the viewport top (or bottom on a short window).
  useLayoutEffect(() => {
    if (!menuOpen) return
    const r = rootRef.current?.getBoundingClientRect()
    if (!r) return
    const above = r.top
    const below = window.innerHeight - r.bottom
    const up = above >= below
    setPlacement({ up, maxH: Math.max(120, (up ? above : below) - 8) })
  }, [menuOpen])

  // Move focus into the popover on open so arrow-key navigation works immediately.
  useEffect(() => {
    if (menuOpen) menuRef.current?.querySelector<HTMLButtonElement>('button')?.focus()
  }, [menuOpen])

  const step = (dir: -1 | 1) => onLensChange(stepLensWidth(lens, presets?.map((p) => p.ms) ?? 2, dir, selection))
  const pick = (ms: number) => { onLensChange(setLensWidth(lens, ms, selection)); setMenuOpen(false) }

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button') ?? [])
    const idx = items.indexOf(document.activeElement as HTMLButtonElement)
    const next = e.key === 'ArrowDown' ? idx + 1 : idx - 1
    items[(next + items.length) % items.length]?.focus()
  }

  const cx = Math.max(CHIP_HALF_PX, Math.min(containerWidth - CHIP_HALF_PX, centerX))

  return (
    <div
      ref={rootRef}
      className="absolute z-20 flex items-center rounded-md border border-theme-border bg-theme-elevated shadow-theme-sm"
      style={{ left: cx, top, transform: 'translateX(-50%)' }}
      data-testid="scrubber-lens-chip"
    >
      <button
        type="button"
        aria-label="Narrow view window"
        title="Narrow view window"
        onClick={() => step(-1)}
        className="flex h-5 w-5 items-center justify-center rounded-l-md text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary focus:outline-none focus:ring-1 focus:ring-accent"
      >
        <Minus className="h-3 w-3" />
      </button>
      {hasPresets ? (
        <button
          type="button"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          aria-label="View window duration — choose preset"
          onClick={() => setMenuOpen((o) => !o)}
          className="min-w-[2.25rem] border-x border-theme-border px-1.5 py-0.5 text-center font-mono text-[11px] leading-4 text-theme-text-primary transition-colors hover:bg-theme-hover focus:outline-none focus:ring-1 focus:ring-accent"
        >
          {formatLensDuration(widthMs)}
        </button>
      ) : (
        <span className="min-w-[2.25rem] border-x border-theme-border px-1.5 py-0.5 text-center font-mono text-[11px] leading-4 text-theme-text-primary">
          {formatLensDuration(widthMs)}
        </span>
      )}
      <button
        type="button"
        aria-label="Widen view window"
        title="Widen view window"
        onClick={() => step(1)}
        className="flex h-5 w-5 items-center justify-center rounded-r-md text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary focus:outline-none focus:ring-1 focus:ring-accent"
      >
        <Plus className="h-3 w-3" />
      </button>

      {menuOpen && hasPresets && (
        <div
          ref={menuRef}
          role="menu"
          aria-label="View window duration presets"
          onKeyDown={onMenuKeyDown}
          className={clsx(
            'absolute left-1/2 z-50 flex -translate-x-1/2 flex-col gap-0.5 overflow-auto rounded-lg border border-theme-border bg-theme-elevated p-1 shadow-theme-lg',
            placement.up ? 'bottom-full mb-1' : 'top-full mt-1',
          )}
          style={{ maxHeight: placement.maxH }}
          data-testid="scrubber-lens-preset-menu"
        >
          {presets!.map((p) => (
            <button
              key={p.label}
              type="button"
              role="menuitem"
              onClick={() => pick(p.ms)}
              className="rounded px-3 py-1 text-left font-mono text-[11px] text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary focus:bg-theme-hover focus:text-theme-text-primary focus:outline-none"
            >
              {p.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function ScrubberIconButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="flex h-6 w-6 items-center justify-center rounded border border-theme-border bg-theme-elevated text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
    >
      {children}
    </button>
  )
}

function formatDuration(ms: number): string {
  const h = Math.round(ms / (60 * 60 * 1000))
  if (h < 24) return `${h}h`
  return `${Math.round(h / 24)}d`
}
