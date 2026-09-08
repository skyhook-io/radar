import { describe, expect, it } from 'vitest'
import { chartLayout, integerAxisTop, yAxisValues } from './axis'

describe('integer axes', () => {
  it('ends a count axis on a whole step that reaches the maximum', () => {
    expect(integerAxisTop(0, 4)).toBe(1)
    expect(integerAxisTop(1, 4)).toBe(1)
    expect(integerAxisTop(3, 4)).toBe(3)
    expect(integerAxisTop(18, 4)).toBe(20)
    expect(integerAxisTop(250, 4)).toBe(300)
    expect(integerAxisTop(1.1, 4)).toBe(2)
    expect(integerAxisTop(18, 1)).toBe(20)
    expect(integerAxisTop(7, 1)).toBe(10)
  })

  it('never yields fractional ticks on an integer axis', () => {
    expect(yAxisValues(0, 1, 4, true)).toEqual([0, 1])
    expect(yAxisValues(0, 3, 4, true)).toEqual([0, 1, 2, 3])
    expect(yAxisValues(0, 20, 4, true)).toEqual([0, 5, 10, 15, 20])
    expect(yAxisValues(0, 20, 1, true)).toEqual([0, 20])
    expect(yAxisValues(0, 1.1, 4, false)).toEqual([0, 0.275, 0.55, 0.8250000000000001, 1.1])
  })

  it('has a compact layout with two ticks per axis and larger text', () => {
    const full = chartLayout(false)
    const compact = chartLayout(true)
    expect([full.yIntervals, full.xIntervals, full.fontSize]).toEqual([4, 6, 11])
    expect([compact.yIntervals, compact.xIntervals]).toEqual([1, 1])
    expect(compact.fontSize).toBeGreaterThan(full.fontSize)
    expect(compact.height / compact.width).toBeGreaterThan(full.height / full.width)
  })
})
