import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { TimelineStrip, resizeWindowWithinQuery, nextWindowRungMs, parseTimeInput } from './TimelineStrip'
import type { ScrubberBucket, ScrubberRange } from './TimelineScrubber'

const HOUR = 60 * 60 * 1000
const query: ScrubberRange = { fromMs: 0, toMs: 24 * HOUR }

describe('nextWindowRungMs — consistent round jumps', () => {
  it('steps up to the next round rung', () => {
    expect(nextWindowRungMs(HOUR, 1, 30 * 24 * HOUR)).toBe(2 * HOUR)
    expect(nextWindowRungMs(2 * HOUR, 1, 30 * 24 * HOUR)).toBe(6 * HOUR)
  })
  it('steps down to the previous round rung', () => {
    expect(nextWindowRungMs(6 * HOUR, -1, 30 * 24 * HOUR)).toBe(2 * HOUR)
    expect(nextWindowRungMs(HOUR, -1, 30 * 24 * HOUR)).toBe(30 * 60_000)
  })
  it('snaps an odd dragged size onto the ladder, not doubling it', () => {
    expect(nextWindowRungMs(7.5 * HOUR, 1, 30 * 24 * HOUR)).toBe(12 * HOUR) // not 15h
    expect(nextWindowRungMs(7.5 * HOUR, -1, 30 * 24 * HOUR)).toBe(6 * HOUR)
  })
  it('never exceeds the query-span cap', () => {
    expect(nextWindowRungMs(12 * HOUR, 1, 24 * HOUR)).toBe(24 * HOUR)
    expect(nextWindowRungMs(24 * HOUR, 1, 24 * HOUR)).toBe(24 * HOUR)
  })
})

describe('parseTimeInput — relative / now / absolute', () => {
  const now = Date.parse('2026-07-07T21:00:00Z')
  it('resolves "now"', () => {
    expect(parseTimeInput('now', now)).toBe(now)
  })
  it('resolves relative durations as "ago"', () => {
    expect(parseTimeInput('24h', now)).toBe(now - 24 * HOUR)
    expect(parseTimeInput('3d', now)).toBe(now - 3 * 24 * HOUR)
    expect(parseTimeInput('45m', now)).toBe(now - 45 * 60_000)
    expect(parseTimeInput('2w', now)).toBe(now - 14 * 24 * HOUR)
  })
  it('resolves absolute dates', () => {
    expect(parseTimeInput('2026-07-06T21:00:00Z', now)).toBe(now - 24 * HOUR)
  })
  it('returns null for empty or unrecognized input', () => {
    expect(parseTimeInput('', now)).toBeNull()
    expect(parseTimeInput('gibberish', now)).toBeNull()
  })
})

describe('resizeWindowWithinQuery — window is a sub-range of the query', () => {
  const window: ScrubberRange = { fromMs: 10 * HOUR, toMs: 14 * HOUR } // 4h, centered at 12h

  it('resizes around the center', () => {
    const next = resizeWindowWithinQuery(window, 2 * HOUR, query)
    expect(next.toMs - next.fromMs).toBe(2 * HOUR)
    expect((next.fromMs + next.toMs) / 2).toBe(12 * HOUR)
  })

  it('never grows wider than the query span', () => {
    const next = resizeWindowWithinQuery(window, 100 * HOUR, query)
    expect(next.fromMs).toBe(query.fromMs)
    expect(next.toMs).toBe(query.toMs)
  })

  it('keeps the window inside the query when resizing near an edge', () => {
    const nearEnd: ScrubberRange = { fromMs: 22 * HOUR, toMs: 23 * HOUR }
    const next = resizeWindowWithinQuery(nearEnd, 4 * HOUR, query)
    expect(next.toMs).toBeLessThanOrEqual(query.toMs)
    expect(next.fromMs).toBeGreaterThanOrEqual(query.fromMs)
    expect(next.toMs - next.fromMs).toBe(4 * HOUR)
  })
})

describe('TimelineStrip bar positioning', () => {
  it('positions sparse buckets by TIME, not by array index', () => {
    // Buckets are sparse (hosts omit empty slots). Index-spacing scattered bars
    // uniformly across the strip; a bar must render at its time's x-position.
    // SSR width defaults to 800; query = 24h, so hour 12 → x = 400.
    const sparse: ScrubberBucket[] = [
      { startMs: 0, endMs: HOUR, total: 5, warnings: 0 },
      { startMs: 12 * HOUR, endMs: 13 * HOUR, total: 8, warnings: 0 },
    ]
    const html = renderToString(
      <TimelineStrip buckets={sparse} domain={query} selection={query} onSelectionChange={() => {}} />,
    )
    // Time-positioned: second bar at 12/24 of 800px = 400. Index-positioned
    // would have put it at 1 * (800/2) = 400 too — so pin the FIRST bar's width
    // and a three-bucket case instead.
    expect(html).toContain('left:400px')
    const three: ScrubberBucket[] = [
      { startMs: 0, endMs: HOUR, total: 5, warnings: 0 },
      { startMs: 2 * HOUR, endMs: 3 * HOUR, total: 3, warnings: 0 },
      { startMs: 18 * HOUR, endMs: 19 * HOUR, total: 8, warnings: 0 },
    ]
    const html3 = renderToString(
      <TimelineStrip buckets={three} domain={query} selection={query} onSelectionChange={() => {}} />,
    )
    // 18h/24h of 800 = 600 (time). Index would give 2*(800/3) ≈ 533.
    expect(html3).toContain('left:600px')
    expect(html3).not.toContain('left:533')
  })
})

describe('TimelineStrip render', () => {
  const buckets: ScrubberBucket[] = [
    { startMs: 0, endMs: 12 * HOUR, total: 5, warnings: 1 },
    { startMs: 12 * HOUR, endMs: 24 * HOUR, total: 8, warnings: 0 },
  ]

  it('labels the Window control with the WINDOW duration, not the query duration', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets}
        domain={query}
        selection={query} // 24h query
        onSelectionChange={() => {}}
        lens={{ fromMs: 10 * HOUR, toMs: 14 * HOUR }} // 4h window
        onLensChange={() => {}}
      />,
    )
    // Window label reflects the 4h window, and the query pill still spans 24h.
    expect(html).toContain('Window')
    expect(html).toContain('4h')
    expect(html).toContain('strip-histogram')
    expect(html).toContain('strip-lens')
  })

  it('hides the Window control when there is no window (lens)', () => {
    const html = renderToString(
      <TimelineStrip buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}} />,
    )
    expect(html).not.toContain('Widen visible window')
  })
})
