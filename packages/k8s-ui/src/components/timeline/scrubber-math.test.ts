import { describe, expect, it } from 'vitest'
import {
  barHeight,
  clampSelection,
  clampLensToSelection,
  countEventsAfter,
  formatLensDuration,
  mergeGapRanges,
  pickDisplayBucketSizeMs,
  presetToSelection,
  type ScrubberRange,
  type ScrubberBucket,
} from './scrubber-math'

const MIN = 60_000
const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR
const NOW = 1_700_000_000_000
const DOMAIN: ScrubberRange = { fromMs: NOW - 30 * DAY, toMs: NOW }

describe('barHeight (bucket → bar mapping)', () => {
  it('is zero for empty buckets or empty data', () => {
    expect(barHeight(0, 100, 44)).toBe(0)
    expect(barHeight(5, 0, 44)).toBe(0)
  })

  it('gives a sparse bar a visible floor instead of a sub-pixel sliver', () => {
    const h = barHeight(1, 10_000, 44)
    expect(h).toBeGreaterThanOrEqual(Math.max(2, 44 * 0.08))
  })

  it('caps at the track height and grows monotonically with total', () => {
    expect(barHeight(10_000, 10_000, 44)).toBeLessThanOrEqual(44)
    expect(barHeight(100, 10_000, 44)).toBeLessThan(barHeight(2_500, 10_000, 44))
  })
})

describe('clampSelection (domain + maxSelectionMs)', () => {
  it('caps width at maxSelectionMs and flags it, keeping the right edge', () => {
    const sel: ScrubberRange = { fromMs: NOW - 10 * DAY, toMs: NOW }
    const { selection, clampedToMax } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(clampedToMax).toBe(true)
    expect(selection.toMs).toBe(NOW)
    expect(selection.toMs - selection.fromMs).toBe(7 * DAY)
  })

  it('shifts a selection that runs past the domain back inside it', () => {
    const sel: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW + 5 * DAY }
    const { selection } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(selection.toMs).toBeLessThanOrEqual(DOMAIN.toMs)
    expect(selection.fromMs).toBeGreaterThanOrEqual(DOMAIN.fromMs)
  })

  it('enforces a minimum width so handles never overlap', () => {
    const sel: ScrubberRange = { fromMs: NOW - 1000, toMs: NOW }
    const { selection } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(selection.toMs - selection.fromMs).toBeGreaterThanOrEqual(MIN)
  })

  it('does not flag clampedToMax when the selection already fits', () => {
    const sel: ScrubberRange = { fromMs: NOW - 2 * DAY, toMs: NOW }
    const { clampedToMax } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(clampedToMax).toBe(false)
  })
})

describe('clampLensToSelection (lens ⊂ selection)', () => {
  const SEL: ScrubberRange = { fromMs: NOW - 4 * HOUR, toMs: NOW }

  it('leaves a lens already inside the selection untouched', () => {
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW - 2 * HOUR }
    expect(clampLensToSelection(lens, SEL)).toEqual(lens)
  })

  it('shifts an overhanging lens back inside, preserving its width', () => {
    const lens: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW + 2 * HOUR }
    const width = lens.toMs - lens.fromMs
    const clamped = clampLensToSelection(lens, SEL)
    expect(clamped.toMs).toBe(SEL.toMs)
    expect(clamped.toMs - clamped.fromMs).toBe(width)
    expect(clamped.fromMs).toBeGreaterThanOrEqual(SEL.fromMs)
  })

  it('shifts a lens hanging off the left edge back inside', () => {
    const lens: ScrubberRange = { fromMs: NOW - 6 * HOUR, toMs: NOW - 5 * HOUR }
    const width = lens.toMs - lens.fromMs
    const clamped = clampLensToSelection(lens, SEL)
    expect(clamped.fromMs).toBe(SEL.fromMs)
    expect(clamped.toMs - clamped.fromMs).toBe(width)
  })

  it('collapses a lens wider than the selection to the full selection', () => {
    const lens: ScrubberRange = { fromMs: NOW - 10 * HOUR, toMs: NOW + 5 * HOUR }
    expect(clampLensToSelection(lens, SEL)).toEqual({ fromMs: SEL.fromMs, toMs: SEL.toMs })
  })
})

describe('formatLensDuration (chip label)', () => {
  it('renders sub-hour widths as minutes', () => {
    expect(formatLensDuration(15 * 60_000)).toBe('15m')
    expect(formatLensDuration(30 * 60_000)).toBe('30m')
  })
  it('renders hour widths as hours, days as days', () => {
    expect(formatLensDuration(HOUR)).toBe('1h')
    expect(formatLensDuration(8 * HOUR)).toBe('8h')
    expect(formatLensDuration(DAY)).toBe('1d')
    expect(formatLensDuration(7 * DAY)).toBe('7d')
  })
})

describe('presetToSelection', () => {
  it('sets the brush to [now - ms, now]', () => {
    const { selection } = presetToSelection(DAY, NOW, DOMAIN, 7 * DAY)
    expect(selection.toMs).toBe(NOW)
    expect(selection.toMs - selection.fromMs).toBe(DAY)
  })

  it('clamps a preset wider than maxSelectionMs (e.g. 30d → 7d cap)', () => {
    const { selection, clampedToMax } = presetToSelection(30 * DAY, NOW, DOMAIN, 7 * DAY)
    expect(clampedToMax).toBe(true)
    expect(selection.toMs - selection.fromMs).toBe(7 * DAY)
  })
})

describe('mergeGapRanges (recording-gap dedupe / merge / clamp)', () => {
  it('drops zero-width spans', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 100 },
      { fromMs: 500, toMs: 500 },
    ])
    expect(gaps).toEqual([])
  })

  it('dedupes identical spans', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 200 },
      { fromMs: 100, toMs: 200 },
    ])
    expect(gaps).toEqual([{ fromMs: 100, toMs: 200 }])
  })

  it('merges overlapping and back-to-back spans into one band', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 300 },
      { fromMs: 250, toMs: 400 }, // overlaps
      { fromMs: 400, toMs: 500 }, // adjacent
      { fromMs: 700, toMs: 800 }, // disjoint
    ])
    expect(gaps).toEqual([
      { fromMs: 100, toMs: 500 },
      { fromMs: 700, toMs: 800 },
    ])
  })

  it('normalizes reversed edges before merging', () => {
    const gaps = mergeGapRanges([{ fromMs: 300, toMs: 100 }])
    expect(gaps).toEqual([{ fromMs: 100, toMs: 300 }])
  })

  it('clips to the domain and drops spans entirely outside it', () => {
    const domain: ScrubberRange = { fromMs: 200, toMs: 600 }
    const gaps = mergeGapRanges(
      [
        { fromMs: 100, toMs: 400 }, // clipped to 200
        { fromMs: 500, toMs: 900 }, // clipped to 600
        { fromMs: 1000, toMs: 1100 }, // outside → dropped
      ],
      domain,
    )
    expect(gaps).toEqual([
      { fromMs: 200, toMs: 400 },
      { fromMs: 500, toMs: 600 },
    ])
  })
})

describe('pickDisplayBucketSizeMs (adaptive strip bucket width)', () => {
  const H = HOUR

  it('keeps 1h bars at a 7-day domain (168 bars)', () => {
    expect(pickDisplayBucketSizeMs(7 * DAY)).toBe(H)
  })

  it('keeps 1h bars at the live ~7.66-day domain (no smearing across a gap)', () => {
    expect(pickDisplayBucketSizeMs(7.66 * DAY)).toBe(H)
  })

  it('steps to 3h bars around a 22-day domain', () => {
    expect(pickDisplayBucketSizeMs(22 * DAY)).toBe(3 * H)
  })

  it('steps to 12h bars around a 92-day domain', () => {
    expect(pickDisplayBucketSizeMs(92 * DAY)).toBe(12 * H)
  })

  it('never exceeds the coarsest rung (24h) for very deep retention', () => {
    expect(pickDisplayBucketSizeMs(3650 * DAY)).toBe(24 * H)
  })
})

describe('countEventsAfter (frozen "new events" pull)', () => {
  const bucket = (startMs: number, total: number): ScrubberBucket => ({
    startMs,
    endMs: startMs + HOUR,
    total,
    warnings: 0,
  })

  it('is zero for an empty bucket list', () => {
    expect(countEventsAfter([], NOW)).toBe(0)
  })

  it('sums totals of buckets starting at/after the toMs hour, floored to the hour', () => {
    // toMs mid-hour floors to its hour start, so the bucket AT that hour counts.
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [
      bucket(hourStart - 2 * HOUR, 5), // before → excluded
      bucket(hourStart, 3), // the toMs hour → included
      bucket(hourStart + HOUR, 7), // after → included
    ]
    expect(countEventsAfter(buckets, hourStart + 30 * MIN)).toBe(10)
  })

  it('excludes buckets that start strictly before the toMs hour', () => {
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [bucket(hourStart - HOUR, 9)]
    expect(countEventsAfter(buckets, hourStart)).toBe(0)
  })

  it('returns the raw sum (display cap is applied by the chip, not the helper)', () => {
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [bucket(hourStart, 1500)]
    expect(countEventsAfter(buckets, hourStart)).toBe(1500)
  })
})
