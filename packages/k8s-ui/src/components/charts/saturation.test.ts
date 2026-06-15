import { describe, expect, it } from 'vitest'
import { computeSaturation } from './saturation'
import type { ReferenceLine, TimeSeries } from './types'

const series = (values: number[]): TimeSeries => ({
  labels: { pod: 'p' },
  dataPoints: values.map((value, i) => ({ timestamp: i, value })),
})

// Builds a series where `undefined` entries are GAPS — the wire shape omits
// the `value` key entirely, so we construct points without it rather than
// setting `value: undefined`.
const gappedSeries = (values: (number | undefined)[]): TimeSeries => ({
  labels: { pod: 'p' },
  dataPoints: values.map((value, i) =>
    value === undefined ? { timestamp: i } : { timestamp: i, value },
  ),
})

const refRequest: ReferenceLine = { value: 100, label: 'request 100', kind: 'request' }
const refLimit: ReferenceLine = { value: 200, label: 'limit 200', kind: 'limit' }

describe('computeSaturation', () => {
  it('returns undefined for empty series array', () => {
    expect(computeSaturation([], [refLimit])).toBeUndefined()
  })

  it('returns undefined when all datapoints are zero', () => {
    expect(computeSaturation([series([0, 0, 0])], [refLimit])).toBeUndefined()
  })

  it('returns undefined when no references are provided', () => {
    expect(computeSaturation([series([10, 20, 30])], [])).toBeUndefined()
  })

  it('returns undefined when the chosen ref has zero value', () => {
    const zeroLimit: ReferenceLine = { value: 0, label: 'limit 0', kind: 'limit' }
    expect(computeSaturation([series([10, 20])], [zeroLimit])).toBeUndefined()
  })

  it('uses request when only request is present', () => {
    const result = computeSaturation([series([25, 50])], [refRequest])
    expect(result).toEqual({ ratio: 0.5, against: 'request' })
  })

  it('prefers limit over request when both are present', () => {
    const result = computeSaturation([series([100])], [refRequest, refLimit])
    // peak=100, against limit=200 → 0.5
    expect(result).toEqual({ ratio: 0.5, against: 'limit' })
  })

  it('peaks across all data points and all series', () => {
    const result = computeSaturation(
      [series([10, 20]), series([5, 180, 30])],
      [refLimit],
    )
    expect(result).toEqual({ ratio: 180 / 200, against: 'limit' })
  })

  it('handles ratio > 1 (workload exceeds its ceiling)', () => {
    const result = computeSaturation([series([300])], [refLimit])
    expect(result?.ratio).toBeCloseTo(1.5)
    expect(result?.against).toBe('limit')
  })

  it('ignores zero-valued samples but uses positive ones for peak', () => {
    const result = computeSaturation([series([0, 0, 50, 0])], [refLimit])
    expect(result?.ratio).toBeCloseTo(0.25)
  })

  it('skips undefined (gap) samples in the peak scan', () => {
    // The undefined sample (300) is a gap, not the peak — it must not count.
    const result = computeSaturation(
      [gappedSeries([10, undefined, 50, undefined])],
      [refLimit],
    )
    expect(result?.ratio).toBeCloseTo(0.25)
  })

  it('returns undefined when every sample is a gap', () => {
    expect(
      computeSaturation([gappedSeries([undefined, undefined, undefined])], [refLimit]),
    ).toBeUndefined()
  })

  it('returns undefined when gaps span multiple series with no finite sample', () => {
    expect(
      computeSaturation(
        [gappedSeries([undefined]), gappedSeries([undefined, undefined])],
        [refLimit],
      ),
    ).toBeUndefined()
  })
})
