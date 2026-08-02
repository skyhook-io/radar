// Live/paused (relative/absolute) selection model for the retained timeline.
//
// Pure helpers so the host (radar/web TimelineView) can derive the concrete
// selection each render, quantize the fetch window, and keep a latched lens
// tracking the live edge — all unit-testable without a DOM.

import type { ScrubberRange } from './scrubber-math'

// Live/paused indicator model for the retained timeline, surfaced as one chip in
// the scrubber header. LIVE = the selection tracks now; `latched` is whether the
// lens still rides the live edge (false once the user drags the lens into the
// past — the chip then offers a jump-to-now). FROZEN = an explicit range is
// pinned (`asOfMs` = freeze time, stamped when the selection freezes).
export type TimelineLiveState =
  | { kind: 'live'; latched: boolean }
  // `newEventCount` (optional) = approximate events recorded after the frozen
  // edge, shown on the "Go live" CTA as a pull toward the fresh data.
  | { kind: 'frozen'; asOfMs: number; newEventCount?: number }

// Cadence the host slides a live selection at. Coarse on purpose: the precise
// [from,to] is re-derived every tick without refetching — data arrives via the
// source's own delta polling, so the tick only moves the visible window.
export const LIVE_TICK_MS = 30_000

// A frozen selection older than this reads as stale — the chip turns amber.
export const STALE_AMBER_AFTER_MS = 15 * 60_000

// The lens counts as "latched to the live edge" when its right edge is within
// one comfortable tick-plus-margin of the selection's right edge. Wider than
// LIVE_TICK_MS so a lens that just slid with the tick still reads as latched.
export const LENS_LATCH_EPSILON_MS = 60_000

/** LIVE selection: a width pinned to now. */
export function deriveLiveSelection(widthMs: number, nowMs: number): ScrubberRange {
  return { fromMs: nowMs - widthMs, toMs: nowMs }
}

/**
 * True when the lens rides the selection's live edge (so it should slide with
 * the tick). A lens the user dragged into the past sits further than epsilon
 * behind and stays put.
 */
export function isLensLatched(
  lens: ScrubberRange,
  selection: ScrubberRange,
  epsilonMs: number = LENS_LATCH_EPSILON_MS,
): boolean {
  return Math.abs(lens.toMs - selection.toMs) <= epsilonMs
}

/** Slide a latched lens to the new selection's right edge, preserving its width. */
export function advanceLatchedLens(lens: ScrubberRange, newSelection: ScrubberRange): ScrubberRange {
  const width = lens.toMs - lens.fromMs
  return { fromMs: newSelection.toMs - width, toMs: newSelection.toMs }
}
