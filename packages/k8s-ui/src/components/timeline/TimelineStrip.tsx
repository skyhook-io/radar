/**
 * TimelineStrip — a log-explorer-style time control: ONE query-range
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
 *   2. Window − / ＋ — grows/shrinks the WINDOW keeping its end fixed, capped at the
 *      query span.
 *   3. The band — drag to pan the window, drag its edges to resize, or draw a new
 *      one anywhere on the histogram. All clamped inside the query.
 *
 * Pure presentation: it never fetches. All colors are theme CSS variables.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { Calendar, ChevronDown, ChevronLeft, ChevronRight, Minus, Plus } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import {
  barHeight,
  clampLensToSelection,
  clampSelection,
  formatLensDuration,
  formatScrubberPill,
  MIN_WINDOW_MS,
  type ScrubberBucket,
  type ScrubberRange,
  type ScrubberPreset,
} from './scrubber-math'
import { STALE_AMBER_AFTER_MS, type TimelineLiveState } from './timeline-live'

const TRACK_HEIGHT = 48
// The window band never renders thinner than this (a "you are here" box
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
  /** Exact loaded-event count for the footer caption. Buckets
   *  straddling the query edge spill a few events in/out, so a bucket sum can
   *  disagree with the toolbar chips (one story for the numbers). */
  totalInQueryRange?: number
  /** Loaded range = the histogram span (the strip shows exactly this). */
  selection: ScrubberRange
  onSelectionChange: (sel: ScrubberRange) => void
  maxSelectionMs?: number
  presets?: ScrubberPreset[]
  onPresetSelect?: (preset: ScrubberPreset) => void
  /** The swimlane's visible window, drawn as the draggable band inside the strip. */
  lens?: ScrubberRange
  onLensChange?: (lens: ScrubberRange) => void
  /** When false the band can only pan, not resize — for hosts (list view) where
   *  the lens mirrors a scrollport whose height fixes the window size. */
  lensResizable?: boolean
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
  className?: string
}

function msToX(ms: number, domain: ScrubberRange, width: number): number {
  const span = domain.toMs - domain.fromMs
  if (span <= 0) return 0
  return ((ms - domain.fromMs) / span) * width
}

// Field stamp for the custom-range pickers, e.g. "Jul 8, 12:01 AM".
function fieldStamp(ms: number): string {
  return new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

const DOW_LABELS = ['S', 'M', 'T', 'W', 'T', 'F', 'S']

// A compact, theme-styled datetime picker: a stamp button opening a month-grid
// popover with a time field and a "Now" shortcut.
function DateTimeField({ label, valueMs, onChange, nowMs }: {
  label: string
  valueMs: number
  onChange: (ms: number) => void
  nowMs: number
}) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const value = new Date(valueMs)
  const [viewY, setViewY] = useState(() => value.getFullYear())
  const [viewM, setViewM] = useState(() => value.getMonth())
  useEffect(() => {
    if (!open) return
    const d = new Date(valueMs)
    setViewY(d.getFullYear())
    setViewM(d.getMonth())
    const onDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false)
    }
    // Capture phase: a stopPropagation elsewhere must not keep the calendar open.
    window.addEventListener('pointerdown', onDown, true)
    return () => window.removeEventListener('pointerdown', onDown, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])
  const prevMonth = () => { if (viewM === 0) { setViewY(viewY - 1); setViewM(11) } else setViewM(viewM - 1) }
  const nextMonth = () => { if (viewM === 11) { setViewY(viewY + 1); setViewM(0) } else setViewM(viewM + 1) }
  const daysInMonth = new Date(viewY, viewM + 1, 0).getDate()
  const firstDow = new Date(viewY, viewM, 1).getDay()
  const pickDay = (day: number) =>
    onChange(new Date(viewY, viewM, day, value.getHours(), value.getMinutes()).getTime())
  const timeStr = `${String(value.getHours()).padStart(2, '0')}:${String(value.getMinutes()).padStart(2, '0')}`
  const setTime = (t: string) => {
    const m = t.match(/^(\d{2}):(\d{2})$/)
    if (m) onChange(new Date(value.getFullYear(), value.getMonth(), value.getDate(), +m[1], +m[2]).getTime())
  }
  const today = new Date(nowMs)
  return (
    <div ref={rootRef} className="relative flex min-w-0 flex-col gap-1">
      <span className="text-[10.5px] text-theme-text-tertiary">{label}</span>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="dialog"
        aria-expanded={open}
        className={clsx(
          'flex w-full items-center justify-between gap-2 rounded-md border bg-theme-elevated px-2 py-1.5 text-xs tabular-nums text-theme-text-primary',
          open ? 'border-accent' : 'border-theme-border hover:border-accent',
        )}
      >
        {fieldStamp(valueMs)}
        <Calendar className="h-3 w-3 shrink-0 text-theme-text-tertiary" />
      </button>
      {open && (
        <div
          className="absolute left-0 top-full z-[60] mt-1 w-[232px] rounded-lg border border-theme-border bg-theme-surface p-2.5 shadow-theme-lg"
          role="dialog"
          aria-label={`Pick ${label.toLowerCase()} date and time`}
        >
          <div className="mb-1.5 flex items-center justify-between">
            <button type="button" onClick={prevMonth} aria-label="Previous month" className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary">
              <ChevronLeft className="h-3.5 w-3.5" />
            </button>
            <span className="text-xs font-semibold text-theme-text-primary">
              {new Date(viewY, viewM).toLocaleDateString([], { month: 'long', year: 'numeric' })}
            </span>
            <button type="button" onClick={nextMonth} aria-label="Next month" className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary">
              <ChevronRight className="h-3.5 w-3.5" />
            </button>
          </div>
          <div className="grid grid-cols-7 text-center text-[10px] text-theme-text-tertiary">
            {DOW_LABELS.map((d, i) => <span key={i} className="py-0.5">{d}</span>)}
          </div>
          <div className="grid grid-cols-7">
            {Array.from({ length: firstDow }).map((_, i) => <span key={`pad-${i}`} />)}
            {Array.from({ length: daysInMonth }).map((_, i) => {
              const day = i + 1
              const isSelected = value.getFullYear() === viewY && value.getMonth() === viewM && value.getDate() === day
              const isToday = today.getFullYear() === viewY && today.getMonth() === viewM && today.getDate() === day
              return (
                <button
                  key={day}
                  type="button"
                  onClick={() => pickDay(day)}
                  className={clsx(
                    'h-7 rounded text-xs tabular-nums transition-colors',
                    isSelected
                      ? 'bg-accent font-semibold text-white'
                      : isToday
                        ? 'font-semibold text-accent-text hover:bg-theme-hover'
                        : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
                  )}
                >
                  {day}
                </button>
              )
            })}
          </div>
          <div className="mt-2 flex items-center justify-between gap-2 border-t border-theme-border pt-2">
            <input
              type="time"
              value={timeStr}
              onChange={(e) => setTime(e.target.value)}
              className="rounded border border-theme-border bg-theme-elevated px-1.5 py-1 text-xs tabular-nums text-theme-text-primary focus:border-accent focus:outline-none"
            />
            <button type="button" onClick={() => { onChange(nowMs); setOpen(false) }} className="text-xs font-medium text-accent-text hover:underline">
              Now
            </button>
          </div>
        </div>
      )}
    </div>
  )
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
  MIN_WINDOW_MS, 30 * MIN,
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
 * Resize the view WINDOW to `targetMs` keeping its END fixed, inside — and never
 * wider than — the QUERY range. End-anchored like the wheel zoom: widening
 * reaches farther back in time, narrowing focuses on the most recent slice
 * (a 2–5pm window widened stays ended at 5pm and grows past 2pm). Only when
 * the start hits the query floor does the end give way.
 */
export function resizeWindowWithinQuery(
  window: ScrubberRange,
  targetMs: number,
  query: ScrubberRange,
): ScrubberRange {
  const width = Math.min(targetMs, query.toMs - query.fromMs)
  let toMs = window.toMs
  let fromMs = toMs - width
  if (fromMs < query.fromMs) {
    fromMs = query.fromMs
    toMs = Math.min(query.toMs, fromMs + width)
  }
  return { fromMs, toMs }
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
  lensResizable = true,
  liveState,
  onLiveChipClick,
  className,
}: TimelineStripProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const trackRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(800)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [customFromMs, setCustomFromMs] = useState(0)
  const [customToMs, setCustomToMs] = useState(0)

  useLayoutEffect(() => {
    const el = trackRef.current
    if (!el) return
    const measure = () => setWidth(el.clientWidth || 800)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Seed the custom pickers from the current query when the dialog opens.
  useEffect(() => {
    if (!pickerOpen) return
    setCustomFromMs(selection.fromMs)
    setCustomToMs(selection.toMs)
  }, [pickerOpen, selection.fromMs, selection.toMs])

  // Close the picker on outside-click / Escape. "Outside" means outside the
  // trigger + dialog (pickerRef) — NOT the whole strip: scoping to containerRef
  // kept the dialog open when clicking the histogram, stepper, or live chip.
  // Capture phase, so a stopPropagation anywhere below can't swallow the close.
  const pickerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!pickerOpen) return
    const onDown = (e: PointerEvent) => {
      if (!pickerRef.current?.contains(e.target as Node)) setPickerOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setPickerOpen(false)
    window.addEventListener('pointerdown', onDown, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown, true)
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
      // Floor the drag-resize at the 15-min window minimum (or the query span,
      // whichever is smaller), not just the pixel-grabbable minimum.
      const minW = Math.max(LENS_MIN_WIDTH_PX * msPerPx, Math.min(MIN_WINDOW_MS, selSpan))
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
      if (!lensResizable && mode !== 'move') return
      e.preventDefault()
      e.stopPropagation()
      dragRef.current = { mode, startX: e.clientX, fromMs: lens.fromMs, toMs: lens.toMs }
    },
    [lens, onLensChange, lensResizable],
  )

  const applyCustom = useCallback(() => {
    if (customToMs <= customFromMs) return
    onSelectionChange(clampSelection({ fromMs: customFromMs, toMs: customToMs }, domain, maxSelectionMs, 'end').selection)
    setPickerOpen(false)
  }, [customFromMs, customToMs, domain, maxSelectionMs, onSelectionChange])
  const customValid = customToMs > customFromMs

  // A LIVE query reads as its relative width ("Last 7d") — absolute stamps
  // re-render on every tick and read as churn while new data streams in. The
  // exact range rides the tooltip; frozen/custom ranges keep explicit stamps.
  const isLiveSelection =
    liveState?.kind === 'live' ||
    (liveState == null && Math.abs(domain.toMs - selection.toMs) < 2 * 60_000)
  const matchedPreset = presets?.find((p) => Math.abs(selSpan - p.ms) <= Math.max(1000, p.ms * 0.01))
  const absoluteRange = `${formatScrubberPill(selection.fromMs)} — ${formatScrubberPill(selection.toMs)}`
  const queryPillLabel = isLiveSelection
    ? matchedPreset
      ? matchedPreset.label === 'All' ? 'All history' : `Last ${matchedPreset.label}`
      : `Last ${formatLensDuration(selSpan)}`
    : absoluteRange

  // Draw a fresh window by dragging on the histogram background. The band + its
  // resize edges stopPropagation, so this only fires on empty histogram space.
  const beginDraw = useCallback((e: React.PointerEvent) => {
    if (!onLensChange || !lensResizable || width <= 0) return
    const rect = trackRef.current?.getBoundingClientRect()
    if (!rect) return
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / width))
    dragRef.current = { mode: 'draw', anchorMs: selection.fromMs + frac * selSpan }
  }, [onLensChange, lensResizable, selection.fromMs, selSpan, width])

  // Window − / ＋ resizes the WINDOW (the blue band), not the query — snapping to
  // round rung sizes so the jumps are consistent. Capped at the query span.
  const windowMs = lens ? lens.toMs - lens.fromMs : null
  const stepWindow = (dir: 1 | -1) => {
    if (!lens || !onLensChange || windowMs == null) return
    const target = nextWindowRungMs(windowMs, dir, selection.toMs - selection.fromMs)
    onLensChange(resizeWindowWithinQuery(lens, target, selection))
  }

  const maxTotal = Math.max(1, ...buckets.map((b) => b.total))

  // ONE shared hover readout for the whole histogram: a floating chip above the
  // cursor describing the hovered bucket (or pre-data / gap region). Replaces
  // the per-bar native titles — one tooltip that tracks the pointer instead of
  // hundreds of independent ones.
  const [hoverX, setHoverX] = useState<number | null>(null)
  const handleTrackHover = useCallback((e: React.PointerEvent) => {
    const rect = trackRef.current?.getBoundingClientRect()
    if (!rect || width <= 0) return
    setHoverX(Math.min(width, Math.max(0, e.clientX - rect.left)))
  }, [width])
  const hoverLabel = (() => {
    if (hoverX == null || msPerPx <= 0) return null
    const t = selection.fromMs + hoverX * msPerPx
    if (historyUnavailableBeforeMs != null && historyUnavailableBeforeMs > selection.fromMs && t < historyUnavailableBeforeMs) {
      return `No data recorded before ${footerStamp(historyUnavailableBeforeMs)} — Radar wasn't watching yet`
    }
    if (gaps?.some((g) => t >= g.fromMs && t <= g.toMs)) {
      return 'No data recorded — connector was offline'
    }
    const b = buckets.find((bk) => t >= bk.startMs && t < bk.endMs)
    if (!b || b.total === 0) return `${bandTime(t)} · no events`
    return `${bandTime(b.startMs)} – ${bandTime(b.endMs)} · ${b.total.toLocaleString()} events${b.warnings ? ` · ${b.warnings} warnings` : ''}`
  })()

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

  // Full range still paints the ordinary band (fill + grip + handles) — the
  // handles-only treatment made the window affordance nearly invisible. Only
  // the CAPTION is state-aware: "viewing full range" instead of restating the
  // edge stamps.
  const FULL_RANGE_EPS_PX = 2
  const fullRange = lens != null && width > 0 &&
    msToX(lens.fromMs, selection, width) <= FULL_RANGE_EPS_PX &&
    msToX(lens.toMs, selection, width) >= width - FULL_RANGE_EPS_PX

  const totalEvents = totalInQueryRange ?? buckets.reduce((sum, b) => sum + b.total, 0)
  const gapsSuffix = gaps && gaps.length > 0 ? ` · ${gaps.length} gap${gaps.length > 1 ? 's' : ''}` : ''
  // Windowed: show the zoomed span's time range + the loaded total ("events ·
  // Last 7d"). The per-window count is deliberately NOT shown here — from hourly
  // buckets it can only be approximate (reads "0 of N" on a sub-hour window over
  // a busy hour), and the toolbar already renders the exact "Showing" count. Two
  // numbers for one thing, one of them wrong, erodes trust; the strip owns the
  // loaded total, the toolbar the visible count.
  const centerCaption = lens && !fullRange
    ? `${bandTime(lens.fromMs)} — ${bandTime(lens.toMs)} · ${totalEvents.toLocaleString()} events · ${queryPillLabel}${gapsSuffix}`
    : lens && lensResizable && onLensChange
      ? `${totalEvents.toLocaleString()} events · viewing full range — drag a handle to narrow${gapsSuffix}`
      : `${totalEvents.toLocaleString()} events · ${queryPillLabel}${gapsSuffix}`

  return (
    // ONE row: the range picker + window stepper cap the histogram on
    // the left, Live docks right, and the edge stamps + state caption sit under
    // the track. Metrics: every control is a fixed 34px whose vertical
    // center sits on the 48px track's midline (labels float in the airspace
    // above) — content-driven heights made true centering impossible. Cap
    // columns: 7px top + 13px label + 4px gap + 34px control → center 41 ≈
    // the track's 16px offset + 24.
    <div ref={containerRef} className={clsx('relative flex items-start gap-3', className)}>
      <div ref={pickerRef} className="relative mt-[7px] shrink-0">
          {/* No label — the pill's value ("Last 1h") names itself. The empty
              line keeps the 13px label airspace so this control stays level
              with the Zoom stepper and the track midline. */}
          <span aria-hidden className="mb-1 block text-[10.5px] leading-[13px]">&nbsp;</span>
          <Tooltip content={absoluteRange} position="bottom" disabled={pickerOpen}>
            <button
              type="button"
              onClick={() => setPickerOpen((v) => !v)}
              aria-expanded={pickerOpen}
              aria-haspopup="dialog"
              className={clsx(
                'flex h-[34px] items-center gap-2 whitespace-nowrap rounded-md border bg-theme-elevated px-3 text-[13px] font-semibold text-theme-text-primary',
                pickerOpen ? 'border-accent' : 'border-theme-border hover:border-accent',
              )}
            >
              {queryPillLabel}
              <ChevronDown className="h-3 w-3 text-theme-text-tertiary" />
            </button>
          </Tooltip>
          {pickerOpen && (
            // NO overflow-hidden: the DateTimeField calendars pop past the
            // dialog's bottom edge and were getting clipped to a header sliver.
            // The footer rounds its own bottom corners instead.
            <div className="absolute left-0 top-full z-50 mt-2 w-[460px] max-w-[92vw] rounded-xl border border-theme-border bg-theme-surface shadow-theme-lg" role="dialog" aria-label="Time range picker">
              <p className="border-b border-theme-border/60 px-4 py-2.5 text-[11px] leading-relaxed text-theme-text-secondary">
                How much history to load. The blue band zooms into a slice of it.
              </p>
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
                  <DateTimeField label="From" valueMs={customFromMs} onChange={setCustomFromMs} nowMs={domain.toMs} />
                  <DateTimeField label="To" valueMs={customToMs} onChange={setCustomToMs} nowMs={domain.toMs} />
                  {/* Label-height spacer keeps Apply on the INPUT row, not floated
                      to the top of the taller field columns. */}
                  <div className="flex flex-col gap-1">
                    <span aria-hidden className="text-[10.5px]">&nbsp;</span>
                    <button
                      type="button"
                      onClick={applyCustom}
                      disabled={!customValid}
                      className="btn-brand rounded-md px-3.5 py-1.5 text-xs font-semibold disabled:cursor-not-allowed disabled:opacity-40"
                    >
                      Apply
                    </button>
                  </div>
                </div>
                {!customValid && (
                  <span className="text-[10px] text-amber-600 dark:text-amber-400">"To" must be after "From".</span>
                )}
              </div>
              <div className="flex items-center justify-between gap-2 rounded-b-xl bg-theme-elevated px-4 py-1.5 text-[10.5px] text-theme-text-tertiary">
                <span>Browser time · {Intl.DateTimeFormat().resolvedOptions().timeZone}</span>
                <span>History since {formatScrubberPill(domain.fromMs)}</span>
              </div>
            </div>
          )}
        </div>

      {windowMs != null && lensResizable && (
        <div className="mt-[7px] shrink-0">
          <span className="mb-1 block text-center text-[10.5px] font-bold uppercase leading-[13px] tracking-[0.08em] text-theme-text-tertiary">Zoom</span>
          <Tooltip content="The slice shown in the lanes below — always within the loaded range" position="bottom" wrapperClassName="shrink-0">
            <div className="inline-flex h-[34px] items-center overflow-hidden rounded-md border border-theme-border bg-theme-elevated">
              <button type="button" aria-label="Zoom in" onClick={() => stepWindow(-1)} className="flex h-full w-[30px] items-center justify-center text-theme-text-secondary hover:bg-theme-hover">
                <Minus className="h-3 w-3" />
              </button>
              <span className="flex h-full items-center border-x border-theme-border-light px-2 text-[11.5px] font-semibold tabular-nums text-theme-text-primary">{formatLensDuration(windowMs)}</span>
              <button type="button" aria-label="Zoom out" onClick={() => stepWindow(1)} className="flex h-full w-[30px] items-center justify-center text-theme-text-secondary hover:bg-theme-hover">
                <Plus className="h-3 w-3" />
              </button>
            </div>
          </Tooltip>
        </div>
      )}

      {/* Histogram block — the flexible center: track above, edge stamps +
          state caption below. mt-4 = the label airspace, putting the 48px
          track's midline level with the 34px controls' centers. */}
      <div className="mt-4 min-w-0 flex-1">
      <div className="relative">
        {hoverLabel != null && hoverX != null && (
          <div
            className="pointer-events-none absolute -top-7 z-40 -translate-x-1/2 whitespace-nowrap rounded border border-theme-border bg-theme-base px-2 py-0.5 text-[11px] tabular-nums text-theme-text-primary shadow-theme-md"
            style={{ left: Math.min(Math.max(hoverX, 80), Math.max(80, width - 80)) }}
            data-testid="strip-hover-readout"
            role="status"
          >
            {hoverLabel}
          </div>
        )}
        <div
          ref={trackRef}
          onPointerDown={beginDraw}
          onPointerMove={handleTrackHover}
          onPointerLeave={() => setHoverX(null)}
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
            // Bars also clip at the recording floor: a bucket straddling it
            // otherwise bleeds under the pre-data dimming and renders as a
            // two-toned bar, even though all its events are after the floor.
            const barFromMs = Math.max(
              b.startMs,
              selection.fromMs,
              historyUnavailableBeforeMs ?? selection.fromMs,
            )
            const startX = msToX(barFromMs, selection, width)
            const endX = msToX(Math.min(b.endMs, selection.toMs), selection, width)
            if (endX <= startX) return null
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
                <div className={clsx('w-full rounded-[1px]', inWindow ? 'bg-accent/60' : 'bg-theme-text-tertiary/45')} style={{ height: h }} />
                {/* Warning cap is AMBER: red is reserved for "actually
                    broken"; these buckets count warning events, not failures. */}
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
            />
          )
        })}

        {/* view-window band (the lens) */}
        {lensGeom && lens && (
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
          >
            {lensResizable && (
              <span onPointerDown={beginLensDrag('resize-start')} className="absolute left-[-4px] top-1/2 h-5 w-2 -translate-y-1/2 cursor-ew-resize rounded" style={{ background: 'var(--accent)' }} aria-hidden />
            )}
            {/* three-bar grip */}
            <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
            <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
            <span aria-hidden style={{ width: 1.5, height: 11, borderRadius: 1, background: 'var(--accent)' }} />
            {lensResizable && (
              <span onPointerDown={beginLensDrag('resize-end')} className="absolute right-[-4px] top-1/2 h-5 w-2 -translate-y-1/2 cursor-ew-resize rounded" style={{ background: 'var(--accent)' }} aria-hidden />
            )}
          </div>
        )}
        </div>
      </div>

      {/* Edge stamps + state caption under the track: query start left, query
          end (+ "· now" while live) right, and between them either the window's
          range ("3:41 — 4:11 · 125 events · Last 7d") or the full-range state line. */}
      <div className="mt-1 flex items-center justify-between gap-2 text-[10.5px] tabular-nums text-theme-text-tertiary">
        <span className="whitespace-nowrap">{footerStamp(selection.fromMs)}</span>
        <span
          className={clsx('min-w-0 truncate whitespace-nowrap', lens && !fullRange && 'font-medium text-accent-text')}
          data-testid="strip-window-range"
        >
          {centerCaption}
        </span>
        <span className="whitespace-nowrap">
          {footerStamp(selection.toMs)}
          {isLiveSelection && <span className="text-theme-text-tertiary/80"> · now</span>}
        </span>
      </div>
      </div>

      {liveState && (
        // mt: track offset (16) + track center (24) − chip half-height (17).
        <div className="mt-[23px] shrink-0">
          <StripLiveChip state={liveState} onClick={onLiveChipClick} />
        </div>
      )}
    </div>
  )
}

// Go-live / frozen chip. Live+latched is inert (already following now); a frozen
// or unlatched state is a clickable CTA back to the live edge. Both live states
// render the SAME "Live" text so the chip width is constant and a latch flip
// never shoves the histogram over; the jump affordance rides the hollow style,
// the click, and the tooltip instead.
function StripLiveChip({ state, onClick }: { state: TimelineLiveState; onClick?: () => void }) {
  if (state.kind === 'live') {
    if (state.latched) {
      return (
        <span className="inline-flex h-[34px] items-center gap-1.5 rounded-full border border-transparent bg-theme-hover px-3 text-xs font-semibold text-theme-text-secondary">
          <span className="h-1.5 w-1.5 rounded-full bg-green-500" />
          Live
        </span>
      )
    }
    return (
      <Tooltip content="The window is behind the live edge — click to jump to now" position="bottom">
        <button type="button" onClick={onClick} className="inline-flex h-[34px] items-center gap-1.5 rounded-full border border-theme-border bg-theme-elevated px-3 text-xs font-semibold text-theme-text-secondary hover:bg-theme-hover">
          <span className="h-1.5 w-1.5 rounded-full border border-green-500 bg-transparent" />
          Live
        </button>
      </Tooltip>
    )
  }
  const stale = Date.now() - state.asOfMs > STALE_AMBER_AFTER_MS
  return (
    <button
      type="button"
      onClick={onClick}
      className={clsx(
        'inline-flex h-[34px] items-center gap-1.5 rounded-full px-3 text-xs font-semibold text-white',
        stale ? 'bg-amber-500 hover:bg-amber-600' : 'bg-green-600 hover:bg-green-700',
      )}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      Go live{state.newEventCount ? ` · ${state.newEventCount.toLocaleString()} new` : ''}
    </button>
  )
}
