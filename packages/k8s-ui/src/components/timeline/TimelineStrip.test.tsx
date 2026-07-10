import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { TimelineStrip, resizeWindowWithinQuery, nextWindowRungMs } from './TimelineStrip'
import type { ScrubberBucket, ScrubberRange } from './scrubber-math'

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

describe('resizeWindowWithinQuery — end-anchored sub-range of the query', () => {
  const window: ScrubberRange = { fromMs: 10 * HOUR, toMs: 14 * HOUR } // 4h ending at 14h

  it('resizes keeping the END fixed (widening reaches back, narrowing keeps recent)', () => {
    const narrower = resizeWindowWithinQuery(window, 2 * HOUR, query)
    expect(narrower).toEqual({ fromMs: 12 * HOUR, toMs: 14 * HOUR })
    const wider = resizeWindowWithinQuery(window, 8 * HOUR, query)
    expect(wider).toEqual({ fromMs: 6 * HOUR, toMs: 14 * HOUR })
  })

  it('never grows wider than the query span', () => {
    const next = resizeWindowWithinQuery(window, 100 * HOUR, query)
    expect(next.fromMs).toBe(query.fromMs)
    expect(next.toMs).toBe(query.toMs)
  })

  it('yields the end only when the start hits the query floor', () => {
    const nearStart: ScrubberRange = { fromMs: HOUR, toMs: 2 * HOUR }
    const next = resizeWindowWithinQuery(nearStart, 4 * HOUR, query)
    expect(next).toEqual({ fromMs: 0, toMs: 4 * HOUR })
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

describe('query pill label — relative when live, absolute when frozen', () => {
  const buckets: ScrubberBucket[] = [{ startMs: 0, endMs: HOUR, total: 3, warnings: 0 }]
  const presets = [{ label: '24h', ms: 24 * HOUR }, { label: 'All', ms: 24 * HOUR * 7 }]

  it('shows "Last 24h" for a live preset-width query (absolute range on hover)', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}}
        presets={presets} liveState={{ kind: 'live', latched: true }}
      />,
    )
    expect(html).toContain('Last 24h')
  })

  it('shows "Last <width>" for a live non-preset width', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={{ fromMs: query.toMs - 5 * HOUR, toMs: query.toMs }}
        onSelectionChange={() => {}} presets={presets} liveState={{ kind: 'live', latched: true }}
      />,
    )
    expect(html).toContain('Last 5h')
  })

  it('keeps absolute stamps when frozen', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}}
        presets={presets} liveState={{ kind: 'frozen', asOfMs: query.toMs }}
      />,
    )
    expect(html).not.toContain('Last 24h')
    expect(html).toContain('—')
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
    // The stepper reflects the 4h window (its label rides the tooltip now).
    expect(html).toContain('aria-label="Zoom in"')
    expect(html).toContain('4h')
    expect(html).toContain('strip-histogram')
    expect(html).toContain('strip-lens')
  })

  it('hides the Zoom control when there is no window (lens)', () => {
    const html = renderToString(
      <TimelineStrip buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}} />,
    )
    expect(html).not.toContain('aria-label="Zoom out"')
  })
})

describe('state caption: full range names the state, band always paints', () => {
  const buckets: ScrubberBucket[] = [
    { startMs: 0, endMs: 12 * HOUR, total: 5, warnings: 0 },
    { startMs: 12 * HOUR, endMs: 24 * HOUR, total: 8, warnings: 0 },
  ]

  it('renders the ordinary band + the full-range caption when window == range', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}}
        lens={query} onLensChange={() => {}}
      />,
    )
    // The band (fill + grip + handles) stays visible even at full range — the
    // handles-only treatment made the window affordance nearly invisible.
    expect(html).toContain('strip-lens')
    expect(html).toContain('viewing full range')
  })

  it('renders the band + window-range caption and highlights in-window buckets when narrowed', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}}
        lens={{ fromMs: 4 * HOUR, toMs: 8 * HOUR }} onLensChange={() => {}}
      />,
    )
    expect(html).toContain('strip-lens')
    // The caption shows the window's time range + the exact loaded total (13); the
    // per-window count is NOT rendered here — from hourly buckets it can only be
    // approximate, and the toolbar already owns the exact "Showing" count.
    expect(html).toContain('13 events ·')
    // The in-window bucket (0–12h midpoint 6h is inside 4–8h) highlights accent;
    // the other stays neutral.
    expect(html).toContain('bg-accent/60')
  })

  it('suffixes the end stamp with "· now" while live', () => {
    const html = renderToString(
      <TimelineStrip
        buckets={buckets} domain={query} selection={query} onSelectionChange={() => {}}
        liveState={{ kind: 'live', latched: true }}
      />,
    )
    expect(html).toContain('· now')
  })
})
