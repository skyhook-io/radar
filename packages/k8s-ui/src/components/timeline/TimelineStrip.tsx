/**
 * TimelineStrip — the 6a strip. A log-explorer-style time control: ONE query-range
 * picker + ONE results histogram whose draggable view-window band pans the lanes.
 * No minimap, no ×8 framing — the histogram spans the query range directly (the
 * host feeds `selection` as the displayed span), so a narrow window is never a
 * sub-pixel sliver because the query IS the view.
 *
 * Two nested spans, never confused:
 *   • QUERY = the data pulled from the server = the histogram's full width. Changed
 *     only by the query-range picker (presets or a custom From/To). Refetches.
 *   • WINDOW = the slice currently shown in the lanes below = the blue band on the
 *     histogram. Always a sub-range of the query (never wider). Purely a view —
 *     moving/resizing it re-renders the lanes instantly, it never re-queries.
 *
 * Three controls:
 *   1. Query-range picker — sets the QUERY (the fetched span).
 *   2. Window − / ＋ — grows/shrinks the WINDOW around its center, capped at the
 *      query span.
 *   3. The band — drag to pan the window, drag its edges to resize, or draw a new
 *      one anywhere on the histogram. All clamped inside the query.
 *
 * Pure presentation: it never fetches. All colors are theme CSS variables.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { ChevronDown, Minus, Plus } from 'lucide-react'
import {
  barHeight,
  clampLensToSelection,
  clampSelection,
  formatLensDuration,
  formatScrubberPill,
  type ScrubberBucket,
  type ScrubberRange,
  type ScrubberPreset,
} from './TimelineScrubber'
import { STALE_AMBER_AFTER_MS, type TimelineLiveState } from './timeline-live'

const TRACK_HEIGHT = 48
// The window band never renders thinner than this (Turn 7: a "you are here" box
// squeezed to a couple pixels is invisible) — a thumb-width minimum, centered on
// the actual lens so it stays grabbable and clearly marks where you're looking.
const LENS_MIN_BAND_PX = 40
// Below this the resize edge and the move body can't both be grabbed.
const LENS_MIN_WIDTH_PX = 20

export interface TimelineStripProps {
  buckets: ScrubberBucket[]
  loading?: boolean
  gaps?: ScrubberRange[]
  /** Full retained bounds — clamps the query controls; NOT the histogram span. */
  domain: ScrubberRange
  /** Oldest recorded moment. When the query extends before it, the strip dims
   *  that region — "you can't scroll here because nothing was recorded yet",
   *  not "this period was quiet". */
  historyUnavailableBeforeMs?: number
  /** Exact event count for the footer's "N events in query range". Buckets
   *  straddling the query edge spill a few events in/out, so a bucket sum can
   *  disagree with the toolbar chips (Turn 8: one story for the numbers). */
  totalInQueryRange?: number
  /** Query range = the histogram span (the strip shows exactly this). */
  selection: ScrubberRange
  onSelectionChange: (sel: ScrubberRange) => void
  maxSelectionMs?: number
  presets?: ScrubberPreset[]
  onPresetSelect?: (preset: ScrubberPreset) => void
  /** The swimlane's visible window, drawn as the draggable band inside the strip. */
  lens?: ScrubberRange
  onLensChange?: (lens: ScrubberRange) => void
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
  className?: string
}

function msToX(ms: number, domain: ScrubberRange, width: number): number {
  const span = domain.toMs - domain.fromMs
  if (span <= 0) return 0
  return ((ms - domain.fromMs) / span) * width
}

const UNIT_MS: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }

// Parse a From/To field: `now`, a relative duration (`24h`, `3d`, `45m`, `2w` —
// interpreted as "ago"), or any absolute date the browser understands. Returns
// null when it can't parse — the field then previews "unrecognized".
export function parseTimeInput(raw: string, nowMs: number): number | null {
  const s = raw.trim().toLowerCase()
  if (!s) return null
  if (s === 'now') return nowMs
  const rel = s.match(/^(\d+(?:\.\d+)?)\s*([smhdw])[a-z]*$/)
  if (rel) return nowMs - parseFloat(rel[1]) * UNIT_MS[rel[2]]
  const abs = new Date(raw.trim()).getTime()
  return Number.isFinite(abs) ? abs : null
}

// The resolved absolute time a field currently points at, for the live preview.
function previewTime(raw: string, nowMs: number): string {
  const ms = parseTimeInput(raw, nowMs)
  if (ms == null) return raw.trim() ? 'unrecognized' : ''
  return new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

// Express a duration as a compact relative string (24h / 3d / 45m) for seeding
// the From field when the range ends at "now".
function formatRelWidth(ms: number): string {
  const min = Math.max(1, Math.round(ms / 60_000))
  if (min % (60 * 24) === 0) return `${min / (60 * 24)}d`
  if (min % 60 === 0) return `${min / 60}h`
  return `${min}m`
}

// Window-edge label parts, kept short + separate so the pill can stack the time
// over a muted date instead of one long wrapping string.
function bandTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}
// Strip-corner stamp, e.g. "Jul 6, 10:17 PM" — the query range's true bounds.
function footerStamp(ms: number): string {
  return new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

// Round window sizes the − / ＋ stepper snaps to, so the jumps are consistent
// regardless of a dragged start size (no 7.5h → 15h → 3.75h ladders). Capped to
// the query span at use — the window is always ⊆ the query.
const MIN = 60_000
const HR = 60 * MIN
const DY = 24 * HR
export const WINDOW_RUNGS_MS = [
  MIN, 5 * MIN, 15 * MIN, 30 * MIN,
  HR, 2 * HR, 6 * HR, 12 * HR,
  DY, 2 * DY, 7 * DY, 14 * DY, 30 * DY,
]

/** The next round window size above/below `currentMs`, capped at `capMs`. */
export function nextWindowRungMs(currentMs: number, dir: 1 | -1, capMs: number): number {
  const rungs = WINDOW_RUNGS_MS.filter((r) => r <= capMs)
  if (rungs.length === 0) return Math.min(currentMs, capMs)
  if (dir > 0) {
    // 5% slack so a value already on a rung jumps to the NEXT one, not itself.
    return rungs.find((r) => r > currentMs * 1.05) ?? capMs
  }
  const smaller = [...rungs].reverse().find((r) => r < currentMs * 0.95)
  return smaller ?? rungs[0]
}

/**
 * Resize the view WINDOW to `targetMs` around its center, kept inside — and never
 * wider than — the QUERY range. The window is always a sub-range of the query.
 */
export function resizeWindowWithinQuery(
  window: ScrubberRange,
  targetMs: number,
  query: ScrubberRange,
): ScrubberRange {
  const querySpan = query.toMs - query.fromMs
  const half = Math.min(targetMs, querySpan) / 2
  const center = (window.fromMs + window.toMs) / 2
  let fromMs = center - half
  let toMs = center + half
  if (fromMs < query.fromMs) { toMs += query.fromMs - fromMs; fromMs = query.fromMs }
  if (toMs > query.toMs) { fromMs -= toMs - query.toMs; toMs = query.toMs }
  return { fromMs: Math.max(query.fromMs, fromMs), toMs: Math.min(query.toMs, toMs) }
}

type LensDrag =
  | { mode: 'move'; startX: number; fromMs: number; toMs: number }
  | { mode: 'resize-start'; startX: number; fromMs: number; toMs: number }
  | { mode: 'resize-end'; startX: number; fromMs: number; toMs: number }
  // Draw a fresh window by dragging on the histogram background; anchored at the
  // pointer-down time, the other edge tracks the cursor.
  | { mode: 'draw'; anchorMs: number }

export function TimelineStrip({
  buckets,
  loading,
  gaps,
  domain,
  historyUnavailableBeforeMs,
  totalInQueryRange,
  selection,
  onSelectionChange,
  maxSelectionMs,
  presets,
  onPresetSelect,
  lens,
  onLensChange,
  liveState,
  onLiveChipClick,
  className,
}: TimelineStripProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const trackRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(800)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [customFrom, setCustomFrom] = useState('')
  const [customTo, setCustomTo] = useState('')

  useLayoutEffect(() => {
    const el = trackRef.current
    if (!el) return
    const measure = () => setWidth(el.clientWidth || 800)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Seed the custom fields from the current query when the picker opens. A range
  // ending at "now" reads naturally as a relative width (From "24h", To "now");
  // otherwise fall back to absolute locale strings the parser round-trips.
  useEffect(() => {
    if (!pickerOpen) return
    const endsNow = Math.abs(selection.toMs - domain.toMs) < 2 * 60_000
    if (endsNow) {
      setCustomFrom(formatRelWidth(selection.toMs - selection.fromMs))
      setCustomTo('now')
    } else {
      setCustomFrom(new Date(selection.fromMs).toLocaleString())
      setCustomTo(new Date(selection.toMs).toLocaleString())
    }
  }, [pickerOpen, selection.fromMs, selection.toMs, domain.toMs])

  // Close the picker on outside-click / Escape.
  useEffect(() => {
    if (!pickerOpen) return
    const onDown = (e: PointerEvent) => {
      if (!containerRef.current?.contains(e.target as Node)) setPickerOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setPickerOpen(false)
    window.addEventListener('pointerdown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [pickerOpen])

  const selSpan = selection.toMs - selection.fromMs
  const msPerPx = width > 0 ? selSpan / width : 0

  // Keep the window inside the query at all times (the invariant: window ⊆ query).
  // When the query shrinks under the current window — e.g. picking a smaller range
  // — clamp the window down to fit rather than let the band overflow the histogram.
  useEffect(() => {
    if (!lens || !onLensChange) return
    const clamped = clampLensToSelection(lens, selection)
    if (clamped.fromMs !== lens.fromMs || clamped.toMs !== lens.toMs) onLensChange(clamped)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lens?.fromMs, lens?.toMs, selection.fromMs, selection.toMs])

  // --- View-window (lens) drag ---
  const dragRef = useRef<LensDrag | null>(null)
  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      const drag = dragRef.current
      if (!drag || !onLensChange) return
      const minW = LENS_MIN_WIDTH_PX * msPerPx
      if (drag.mode === 'draw') {
        const rect = trackRef.current?.getBoundingClientRect()
        if (!rect || width <= 0) return
        const cur = selection.fromMs + Math.min(1, Math.max(0, (e.clientX - rect.left) / width)) * selSpan
        let from = Math.min(drag.anchorMs, cur)
        let to = Math.max(drag.anchorMs, cur)
        if (to - from < minW) to = from + minW
        onLensChange({ fromMs: Math.max(selection.fromMs, from), toMs: Math.min(selection.toMs, to) })
        return
      }
      const dxMs = (e.clientX - drag.startX) * msPerPx
      if (drag.mode === 'move') {
        const w = drag.toMs - drag.fromMs
        let from = drag.fromMs + dxMs
        from = Math.max(selection.fromMs, Math.min(from, selection.toMs - w))
        onLensChange({ fromMs: from, toMs: from + w })
      } else if (drag.mode === 'resize-start') {
        const from = Math.max(selection.fromMs, Math.min(drag.fromMs + dxMs, drag.toMs - minW))
        onLensChange({ fromMs: from, toMs: drag.toMs })
      } else {
        const to = Math.min(selection.toMs, Math.max(drag.toMs + dxMs, drag.fromMs + minW))
        onLensChange({ fromMs: drag.fromMs, toMs: to })
      }
    }
    const onUp = () => { dragRef.current = null }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [msPerPx, width, selSpan, onLensChange, selection.fromMs, selection.toMs])

  const beginLensDrag = useCallback(
    (mode: 'move' | 'resize-start' | 'resize-end') => (e: React.PointerEvent) => {
      if (!lens || !onLensChange) return
      e.preventDefault()
      e.stopPropagation()
      dragRef.current = { mode, startX: e.clientX, fromMs: lens.fromMs, toMs: lens.toMs }
    },
    [lens, onLensChange],
  )

  const applyCustom = useCallback(() => {
    const from = parseTimeInput(customFrom, domain.toMs)
    const to = parseTimeInput(customTo, domain.toMs)
    if (from == null || to == null || to <= from) return
    onSelectionChange(clampSelection({ fromMs: from, toMs: to }, domain, maxSelectionMs, 'end').selection)
    setPickerOpen(false)
  }, [customFrom, customTo, domain, maxSelectionMs, onSelectionChange])
  const customValid = (() => {
    const from = parseTimeInput(customFrom, domain.toMs)
    const to = parseTimeInput(customTo, domain.toMs)
    return from != null && to != null && to > from
  })()

  // Draw a fresh window by dragging on the histogram background. The band + its
  // resize edges stopPropagation, so this only fires on empty histogram space.
  const beginDraw = useCallback((e: React.PointerEvent) => {
    if (!onLensChange || width <= 0) return
    const rect = trackRef.current?.getBoundingClientRect()
    if (!rect) return
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / width))
    dragRef.current = { mode: 'draw', anchorMs: selection.fromMs + frac * selSpan }
  }, [onLensChange, selection.fromMs, selSpan, width])

  // Window − / ＋ resizes the WINDOW (the blue band), not the query — snapping to
  // round rung sizes so the jumps are consistent. Capped at the query span.
  const windowMs = lens ? lens.toMs - lens.fromMs : null
  const stepWindow = (dir: 1 | -1) => {
    if (!lens || !onLensChange || windowMs == null) return
    const target = nextWindowRungMs(windowMs, dir, selection.toMs - selection.fromMs)
    onLensChange(resizeWindowWithinQuery(lens, target, selection))
  }

  const maxTotal = Math.max(1, ...buckets.map((b) => b.total))

  // Lens band geometry (kept ≥ LENS_MIN_BAND_PX so a tiny lens stays grabbable).
  const lensGeom = (() => {
    if (!lens) return null
    const fromX = msToX(lens.fromMs, selection, width)
    const toX = msToX(lens.toMs, selection, width)
    const actualW = Math.max(2, toX - fromX)
    const visualW = Math.max(LENS_MIN_BAND_PX, actualW)
    const left = Math.min(Math.max(0, fromX - (visualW - actualW) / 2), width - visualW)
    return { left, visualW }
  })()

  const totalEvents = totalInQueryRange ?? buckets.reduce((sum, b) => sum + b.total, 0)

  return (
    <div ref={containerRef} className={clsx('relative', className)}>
      {/* Row 1 — query range + window size + go-live */}
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[10px] font-bold uppercase tracking-[0.07em] text-theme-text-tertiary">Query range</span>
        <div className="relative">
          <button
            type="button"
            onClick={() => setPickerOpen((v) => !v)}
            aria-expanded={pickerOpen}
            aria-haspopup="dialog"
            className={clsx(
              'flex items-center gap-2 whitespace-nowrap rounded-md border bg-theme-elevated px-3 py-1.5 text-[13px] font-semibold text-theme-text-primary',
              pickerOpen ? 'border-accent' : 'border-theme-border hover:border-accent',
            )}
          >
            {formatScrubberPill(selection.fromMs)} — {formatScrubberPill(selection.toMs)}
            <ChevronDown className="h-3 w-3 text-theme-text-tertiary" />
          </button>
          {pickerOpen && (
            <div className="absolute left-0 top-full z-50 mt-2 w-[460px] max-w-[92vw] overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-lg" role="dialog" aria-label="Query range picker">
              {presets && presets.length > 0 && (
                <div className="flex flex-col gap-2 border-b border-theme-border/60 px-4 py-3">
                  <span className="text-[10px] font-bold uppercase tracking-[0.07em] text-theme-text-tertiary">
                    Presets <span className="font-normal normal-case tracking-normal">· apply instantly</span>
                  </span>
                  <div className="flex flex-wrap gap-1.5">
                    {presets.map((p) => {
                      const active = Math.abs(selSpan - p.ms) <= Math.max(1000, p.ms * 0.01)
                      return (
                        <button
                          key={p.label}
                          type="button"
                          aria-pressed={active}
                          onClick={() => { onPresetSelect?.(p); setPickerOpen(false) }}
                          className={clsx(
                            'rounded-full border px-3 py-1 text-[12.5px] font-semibold transition-colors',
                            active
                              ? 'border-accent/60 bg-theme-hover text-theme-text-primary'
                              : 'border-theme-border bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover',
                          )}
                        >
                          {p.label}
                        </button>
                      )
                    })}
                  </div>
                </div>
              )}
              <div className="flex flex-col gap-2 px-4 py-3">
                <span className="text-[10px] font-bold uppercase tracking-[0.07em] text-theme-text-tertiary">Custom</span>
                <div className="grid grid-cols-[1fr_1fr_auto] items-start gap-2">
                  <label className="flex min-w-0 flex-col gap-1">
                    <span className="text-[10.5px] text-theme-text-tertiary">From</span>
                    <input
                      type="text"
                      inputMode="text"
                      value={customFrom}
                      onChange={(e) => setCustomFrom(e.target.value)}
                      onKeyDown={(e) => e.key === 'Enter' && applyCustom()}
                      placeholder="24h ago…"
                      className="w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-xs tabular-nums text-theme-text-primary focus:border-accent focus:outline-none"
                    />
                    <span className="truncate text-[10px] text-theme-text-tertiary" title={previewTime(customFrom, domain.toMs)}>{previewTime(customFrom, domain.toMs)}</span>
                  </label>
                  <label className="flex min-w-0 flex-col gap-1">
                    <span className="text-[10.5px] text-theme-text-tertiary">To</span>
                    <input
                      type="text"
                      inputMode="text"
                      value={customTo}
                      onChange={(e) => setCustomTo(e.target.value)}
                      onKeyDown={(e) => e.key === 'Enter' && applyCustom()}
                      placeholder="now"
                      className="w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-xs tabular-nums text-theme-text-primary focus:border-accent focus:outline-none"
                    />
                    <span className="truncate text-[10px] text-theme-text-tertiary" title={previewTime(customTo, domain.toMs)}>{previewTime(customTo, domain.toMs)}</span>
                  </label>
                  <button
                    type="button"
                    onClick={applyCustom}
                    disabled={!customValid}
                    className="btn-brand rounded-md px-3.5 py-1.5 text-xs font-semibold disabled:cursor-not-allowed disabled:opacity-40"
                  >
                    Apply
                  </button>
                </div>
                <span className="text-[10px] text-theme-text-tertiary">Accepts <span className="font-medium text-theme-text-secondary">24h</span>, <span className="font-medium text-theme-text-secondary">3d</span>, <span className="font-medium text-theme-text-secondary">now</span>, or a date — resolves as you type.</span>
              </div>
              <div className="flex items-center justify-between gap-2 bg-theme-elevated px-4 py-1.5 text-[10.5px] text-theme-text-tertiary">
                <span>Browser time · {Intl.DateTimeFormat().resolvedOptions().timeZone}</span>
                <span>Retained since {formatScrubberPill(domain.fromMs)}</span>
              </div>
            </div>
          )}
        </div>

        <span className="mx-1 h-5 w-px bg-theme-border" />
        {windowMs != null && (
          <>
            <span className="text-[10px] font-bold uppercase tracking-[0.07em] text-theme-text-tertiary" title="The slice shown in the lanes below — always within the query range">Window</span>
            <div className="inline-flex items-center overflow-hidden rounded-md border border-theme-border bg-theme-elevated">
              <button type="button" aria-label="Narrow visible window" onClick={() => stepWindow(-1)} className="flex h-6 w-6 items-center justify-center text-theme-text-secondary hover:bg-theme-hover">
                <Minus className="h-3 w-3" />
              </button>
              <span className="border-x border-theme-border-light px-2 text-[11.5px] font-semibold tabular-nums text-theme-text-primary">{formatLensDuration(windowMs)}</span>
              <button type="button" aria-label="Widen visible window" onClick={() => stepWindow(1)} className="flex h-6 w-6 items-center justify-center text-theme-text-secondary hover:bg-theme-hover">
                <Plus className="h-3 w-3" />
              </button>
            </div>
          </>
        )}

        {liveState && (
          <div className="ml-auto">
            <StripLiveChip state={liveState} onClick={onLiveChipClick} />
          </div>
        )}
      </div>

      {/* Row 2 — results histogram with the draggable view-window band. The
          wrapper reserves space above the track for the floating window-edge time
          label; the track itself stays overflow-hidden to clip the bars. */}
      <div className="relative pt-6">
        {/* Persistent range label (Turn 7): the window's exact times, always
            visible (not just on hover), centered over the band and clamped so it
            never clips at the sides. */}
        {lensGeom && lens && (
          <span
            className="pointer-events-none absolute top-0 z-30 -translate-x-1/2 whitespace-nowrap rounded-full bg-theme-text-primary px-2.5 py-0.5 text-[10.5px] font-semibold tabular-nums text-theme-base shadow-theme-md"
            style={{ left: Math.min(Math.max(lensGeom.left + lensGeom.visualW / 2, 60), width - 60) }}
          >
            {bandTime(lens.fromMs)} — {bandTime(lens.toMs)}
          </span>
        )}
        <div
          ref={trackRef}
          onPointerDown={beginDraw}
          className={clsx(
            'relative overflow-hidden rounded-md border border-theme-border bg-theme-elevated',
            lens && onLensChange && 'cursor-crosshair',
          )}
          style={{ height: TRACK_HEIGHT }}
          data-testid="strip-histogram"
        >
        {/* bars */}
        {loading
          ? buckets.length === 0 && (
            <div className="absolute inset-0 flex items-end gap-0.5 px-1.5 pb-1.5">
              {Array.from({ length: 24 }).map((_, i) => (
                <div key={i} className="flex-1 animate-pulse rounded-[1px] bg-theme-border/40" style={{ height: `${20 + (i % 5) * 12}%` }} />
              ))}
            </div>
          )
          : buckets.map((b, i) => {
            const h = barHeight(b.total, maxTotal, TRACK_HEIGHT - 8)
            if (h <= 0) return null
            // Position by TIME, never by array index: the host's buckets are
            // SPARSE (empty slots are omitted), so index-spacing scattered bars
            // uniformly across the strip at positions unrelated to their actual
            // time — a bar would sit "under the window" while holding events
            // from a different hour, and an empty window looked populated.
            const startX = msToX(Math.max(b.startMs, selection.fromMs), selection, width)
            const endX = msToX(Math.min(b.endMs, selection.toMs), selection, width)
            const w = Math.max(1, endX - startX - 1)
            if (endX <= 0 || startX >= width) return null
            const warnFrac = b.total > 0 ? Math.min(1, b.warnings / b.total) : 0
            const warnH = b.warnings > 0 ? Math.max(2, h * warnFrac) : 0
            // Bars inside the WINDOW (what's shown in the lanes) read bright; bars
            // in the query but outside the window are muted context — INCLUDING the
            // warning overlay, or an out-of-window warning bar shows a bright red
            // cap under the band and reads as "events here" when the window is empty.
            const mid = (b.startMs + b.endMs) / 2
            const inWindow = !lens || (mid >= lens.fromMs && mid <= lens.toMs)
            return (
              <div key={i} className="absolute bottom-1" style={{ left: startX, width: w }}>
                {/* Out-of-window bars are muted but still legible — the dimmed
                    part of a photo crop, not invisible. */}
                <div className={clsx('w-full rounded-[1px]', inWindow ? 'bg-accent/60' : 'bg-theme-text-tertiary/45')} style={{ height: h }} title={`${b.total} events${b.warnings ? ` · ${b.warnings} warnings` : ''}`} />
                {/* Warning cap is AMBER (Turn 7: red is reserved for "actually
                    broken"; these buckets count warning events, not failures). */}
                {warnH > 0 && <div className={clsx('absolute bottom-0 w-full rounded-[1px]', inWindow ? 'bg-[var(--color-warning)]/80' : 'bg-[var(--color-warning)]/40')} style={{ height: warnH }} />}
              </div>
            )
          })}

        {/* Pre-data region: the query extends before the oldest recorded moment.
            Dimmed + edged so "nothing was recorded yet" is distinguishable from
            "this period was quiet" — and the list's scroll floor is explained. */}
        {historyUnavailableBeforeMs != null && historyUnavailableBeforeMs > selection.fromMs && (() => {
          const edgeX = Math.min(msToX(historyUnavailableBeforeMs, selection, width), width)
          if (edgeX <= 0) return null
          return (
            <div
              className="pointer-events-auto absolute bottom-0 top-0 left-0 z-[5]"
              style={{ width: edgeX }}
              title={`No data recorded before ${footerStamp(historyUnavailableBeforeMs)} — Radar wasn't watching yet`}
              data-testid="strip-predata"
            >
              <div className="absolute inset-0 bg-theme-base/60" />
              <div className="absolute bottom-0 top-0 right-0 w-px bg-theme-border" />
              {edgeX > 130 && (
                <span className="absolute left-2 top-1/2 -translate-y-1/2 whitespace-nowrap text-[10px] text-theme-text-tertiary">
                  no data before {bandTime(historyUnavailableBeforeMs)}
                </span>
              )}
            </div>
          )
        })()}

        {/* recording-gap hatch */}
        {gaps?.map((g, i) => {
          const left = Math.max(0, msToX(g.fromMs, selection, width))
          const right = Math.min(width, msToX(g.toMs, selection, width))
          if (right - left <= 0) return null
          return (
            <div
              key={`gap-${i}`}
              className="pointer-events-none absolute top-0 bottom-0"
              style={{
                left,
                width: right - left,
                background: 'repeating-linear-gradient(45deg, transparent 0, transparent 4px, var(--border-default) 4px, var(--border-default) 5px)',
                opacity: 0.5,
              }}
              title="No data recorded — connector was offline"
            />
          )
        })}

        {/* view-window band (the lens) */}
        {lensGeom && lens && (
          <>
            <div
              role="slider"
              tabIndex={0}
              aria-label="Visible window — drag to pan the lanes"
              aria-valuemin={selection.fromMs}
              aria-valuemax={selection.toMs}
              aria-valuenow={lens.fromMs}
              onPointerDown={beginLensDrag('move')}
              className="absolute top-0 z-10 flex cursor-grab touch-none items-center justify-center gap-[2.5px] rounded-sm active:cursor-grabbing focus:outline-none focus:ring-2 focus:ring-accent"
              style={{
                left: lensGeom.left,
                width: lensGeom.visualW,
                height: TRACK_HEIGHT,
                background: 'var(--selection-bg)',
                border: '2px solid var(--accent)',
                boxShadow: '0 0 0 1px var(--bg-base)',
              }}
              data-testid="strip-lens"
              title="Drag to move the swimlane's view window"
            >
              {/* left resize edge */}
              <span onPointerDown={beginLensDrag('resize-start')} className="absolute left-[-4px] top-1/2 h-5 w-2 -translate-y-1/2 cursor-ew-resize rounded" style={{ background: 'var(--accent)' }} aria-hidden />
              {/* three-bar grip */}
              <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
              <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
              <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
              {/* right resize edge */}
              <span onPointerDown={beginLensDrag('resize-end')} className="absolute right-[-4px] top-1/2 h-5 w-2 -translate-y-1/2 cursor-ew-resize rounded" style={{ background: 'var(--accent)' }} aria-hidden />
            </div>
          </>
        )}
        </div>
      </div>

      {/* Strip footer (Turn 7): the query range's start/end times at the corners
          (so the strip's own ruler is labeled, distinct from the lanes' axis) with
          the labeled count between them. */}
      <div className="mt-1 flex items-center justify-between gap-2 text-[11px] tabular-nums text-theme-text-tertiary">
        <span className="whitespace-nowrap">{footerStamp(selection.fromMs)}</span>
        <span className="whitespace-nowrap">
          {totalEvents.toLocaleString()} events in query range
          {gaps && gaps.length > 0 && ` · ${gaps.length} gap${gaps.length > 1 ? 's' : ''}`}
        </span>
        <span className="whitespace-nowrap">{footerStamp(selection.toMs)}</span>
      </div>
    </div>
  )
}

// Go-live / frozen chip. Live+latched is inert (already following now); a frozen
// or unlatched state is a clickable CTA back to the live edge.
function StripLiveChip({ state, onClick }: { state: TimelineLiveState; onClick?: () => void }) {
  if (state.kind === 'live') {
    if (state.latched) {
      return (
        <span className="inline-flex items-center gap-1.5 rounded-full bg-theme-hover px-2.5 py-1 text-xs font-semibold text-theme-text-secondary">
          <span className="h-1.5 w-1.5 rounded-full bg-green-500" />
          Live
        </span>
      )
    }
    return (
      <button type="button" onClick={onClick} className="inline-flex items-center gap-1.5 rounded-full border border-theme-border bg-theme-elevated px-2.5 py-1 text-xs font-semibold text-theme-text-secondary hover:bg-theme-hover" title="Jump to the live edge">
        <span className="h-1.5 w-1.5 rounded-full bg-green-500" />
        Live · jump to now
      </button>
    )
  }
  const stale = Date.now() - state.asOfMs > STALE_AMBER_AFTER_MS
  return (
    <button
      type="button"
      onClick={onClick}
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-semibold text-white',
        stale ? 'bg-amber-500 hover:bg-amber-600' : 'bg-green-600 hover:bg-green-700',
      )}
      title="Return to live"
    >
      <span className="h-1.5 w-1.5 rounded-full bg-white" />
      Go live{state.newEventCount ? ` · ${state.newEventCount.toLocaleString()} new` : ''}
    </button>
  )
}
