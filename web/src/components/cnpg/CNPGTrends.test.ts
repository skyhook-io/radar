import { describe, expect, it } from 'vitest'
import { historyGaps, sampleGaps } from './CNPGTrends'

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

