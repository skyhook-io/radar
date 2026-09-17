import { describe, expect, it } from 'vitest'
import { nearestSample } from './nearestSample'

const points = [
  { timestamp: 0, value: 0 },
  { timestamp: 15, value: 2 },
  { timestamp: 105, value: 3 },
  { timestamp: 120, value: 4 },
]

describe('nearestSample', () => {
  it('withholds hover values inside missing evaluations, including a single missing slot', () => {
    expect(nearestSample(points, 60, 15)).toBeNull()
    expect(nearestSample([{ timestamp: 0, value: 1 }, { timestamp: 30, value: 2 }], 15, 15)).toBeNull()
    expect(nearestSample([...points, { timestamp: 135, value: null }], 135, 15)).toBeNull()
  })

  it('keeps measured zero and only snaps within half an evaluation interval', () => {
    expect(nearestSample(points, 0, 15)).toEqual(points[0])
    expect(nearestSample(points, 110, 15)).toEqual(points[2])
    expect(nearestSample(points, -8, 15)).toBeNull()
    expect(nearestSample(points, 128, 15)).toBeNull()
  })

  it('preserves unrestricted nearest-finite behavior for generic charts', () => {
    expect(nearestSample(points, 60)).toEqual(points[1])
    expect(nearestSample([...points, { timestamp: 135, value: null }], 135)).toEqual(points[3])
    expect(nearestSample([], 0)).toBeNull()
    expect(nearestSample([{ timestamp: 0, value: null }], 0)).toBeNull()
  })
})
