import { describe, expect, it } from 'vitest'
import {
  LENS_LATCH_EPSILON_MS,
  STALE_AMBER_AFTER_MS,
  advanceLatchedLens,
  deriveLiveSelection,
  isLensLatched,
} from './timeline-live'
import { clampSelection, type ScrubberRange } from './scrubber-math'

const MIN = 60_000
const HOUR = 60 * MIN

describe('deriveLiveSelection', () => {
  it('pins the window to now with the given width', () => {
    const now = 1_000_000_000
    expect(deriveLiveSelection(HOUR, now)).toEqual({ fromMs: now - HOUR, toMs: now })
  })
})

describe('isLensLatched', () => {
  const sel: ScrubberRange = { fromMs: 0, toMs: 100 * HOUR }

  it('latched when the lens rides the live edge', () => {
    expect(isLensLatched({ fromMs: 90 * HOUR, toMs: sel.toMs }, sel)).toBe(true)
  })

  it('latched when the lens is within one tick of the edge', () => {
    const lens = { fromMs: 90 * HOUR, toMs: sel.toMs - 30_000 }
    expect(isLensLatched(lens, sel)).toBe(true)
  })

  it('exactly at the epsilon boundary counts as latched', () => {
    const lens = { fromMs: 0, toMs: sel.toMs - LENS_LATCH_EPSILON_MS }
    expect(isLensLatched(lens, sel)).toBe(true)
  })

  it('not latched when dragged into the past', () => {
    const lens = { fromMs: 0, toMs: sel.toMs - 2 * HOUR }
    expect(isLensLatched(lens, sel)).toBe(false)
  })
})

describe('advanceLatchedLens', () => {
  it('slides to the new right edge keeping width', () => {
    const lens = { fromMs: 100, toMs: 400 } // width 300
    const next = advanceLatchedLens(lens, { fromMs: 0, toMs: 1_000 })
    expect(next).toEqual({ fromMs: 700, toMs: 1_000 })
    expect(next.toMs - next.fromMs).toBe(300)
  })

  it('re-latches: an advanced lens stays within epsilon of the next tick edge', () => {
    let lens = { fromMs: 0, toMs: 100 * HOUR }
    let now = 100 * HOUR
    for (let i = 0; i < 5; i++) {
      now += 30_000
      const sel = { fromMs: now - 100 * HOUR, toMs: now }
      expect(isLensLatched(lens, sel)).toBe(true)
      lens = advanceLatchedLens(lens, sel)
    }
  })
})

describe('live selection vs domain clamp (RetainedTimelineScrubber fix)', () => {
  // Regression: a live selection whose to=now(tick) exceeds a stale overview
  // domain.to must NOT be rewritten backward. The fix derives domain.to as
  // max(staleNow, selection.to); this asserts the clamp is then a no-op.
  it('is not clamped when domain.to = max(staleNow, selection.to)', () => {
    const now = 100 * HOUR
    const selection = deriveLiveSelection(24 * HOUR, now)
    const staleNow = now - 45_000 // overview updatedAt lags the tick
    const availableFromMs = now - 7 * 24 * HOUR
    const domainTo = Math.max(staleNow, selection.toMs)
    const domain = { fromMs: availableFromMs, toMs: domainTo }
    const maxSelectionMs = Math.min(7 * 24 * HOUR, domain.toMs - domain.fromMs)
    const { selection: clamped } = clampSelection(selection, domain, maxSelectionMs, 'end')
    expect(clamped).toEqual(selection)
  })

  it('WITHOUT the fix a stale domain.to drags the live edge backward', () => {
    const now = 100 * HOUR
    const selection = deriveLiveSelection(24 * HOUR, now)
    const staleNow = now - 45_000
    const domain = { fromMs: now - 7 * 24 * HOUR, toMs: staleNow } // no max()
    const { selection: clamped } = clampSelection(selection, domain, 7 * 24 * HOUR, 'end')
    expect(clamped.toMs).toBeLessThan(selection.toMs)
  })
})

describe('STALE_AMBER_AFTER_MS', () => {
  it('is 15 minutes', () => {
    expect(STALE_AMBER_AFTER_MS).toBe(15 * 60_000)
  })
})
