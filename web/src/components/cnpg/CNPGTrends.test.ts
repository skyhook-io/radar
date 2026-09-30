import { describe, expect, it } from 'vitest'
import { cacheHitSeries, historyGaps, rateSeries, sampleGaps } from './CNPGTrends'

const s = (points: [number, number | null][], label = 'a') => ({ labels: { pod: label }, dataPoints: points.map(([timestamp, value]) => ({ timestamp, value })) })

describe('historyGaps', () => {
  it('hatches steps no series covers and says a leading run predates the series', () => {
    const gaps = historyGaps([s([[120, 1], [150, 2]]), s([[240, 1]], 'b')], 60, 240, 30)
    expect(gaps).toEqual([
      { start: 60, end: 105, label: expect.stringContaining('before this series existed') },
      { start: 165, end: 225, label: expect.stringContaining('not scraped') },
    ])
  })

  it('reports nothing when every step has a sample', () => {
    expect(historyGaps([s([[0, 1], [30, 0], [60, 3]])], 0, 60, 30)).toEqual([])
  })
})

describe('sampleGaps', () => {
  it('spans from the last sample before a null run to the first after it', () => {
    const gaps = sampleGaps([s([[0, 1], [5, null], [10, null], [15, 4]])])
    expect(gaps).toHaveLength(1)
    expect(gaps[0]).toMatchObject({ start: 0, end: 15 })
  })
})

describe('sampled counter rates', () => {
  const sample = (t: number, metricsAt: number | undefined, over: Partial<import('./CNPGTrends').Sample>) => ({ t: t * 1000, metricsAt: metricsAt === undefined ? undefined : metricsAt * 1000, replayLag: {}, ...over })
  it('rates counters by scrape time, per minute when asked, and gaps a reset', () => {
    const samples = [
      sample(0, 0, { archived: 10 }),
      sample(5, 0, { archived: 10 }),
      sample(30, 30, { archived: 16 }),
      sample(60, 60, { archived: 2 }),
    ]
    const pts = rateSeries(samples, 'archived', 60).dataPoints
    expect(pts).toEqual([
      { timestamp: 30, value: 12 },
      { timestamp: 60, value: null },
    ])
  })
  it('computes cache hit ratio from deltas and never shows 100 % for no reads', () => {
    const samples = [sample(0, 0, { blksHit: 100, blksRead: 10 }), sample(30, 30, { blksHit: 190, blksRead: 20 }), sample(60, 60, { blksHit: 190, blksRead: 20 })]
    const pts = cacheHitSeries(samples).dataPoints
    expect(pts[0]).toEqual({ timestamp: 30, value: 90 })
    expect(pts[1]).toEqual({ timestamp: 60, value: null })
  })
})
