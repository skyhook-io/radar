import { describe, expect, it } from 'vitest'
import { type ColumnLayoutInput, computeColumnLayout, percentile, rememberWidths } from './column-sizing'

const name = (content: number, stretch?: number): ColumnLayoutInput => ({ key: 'name', mode: 'fit', floor: 200, max: 480, content, stretch, weight: 2 })
const exact = (key: string, floor: number): ColumnLayoutInput => ({ key, mode: 'exact', floor })
const grow = (key: string, floor: number, content: number, weight = 1, max = 960): ColumnLayoutInput => ({ key, mode: 'grow', floor, content, weight, max })
const total = (widths: Record<string, number>) => Object.values(widths).reduce((a, b) => a + b, 0)

describe('computeColumnLayout', () => {
  it('keeps exact columns at their floor and fits Name to its content', () => {
    const w = computeColumnLayout([name(300), exact('status', 160), exact('age', 96)], 2000)
    expect(w).toEqual({ name: 300, status: 160, age: 96 })
  })

  it('gives Name at least its floor and at most its cap', () => {
    expect(computeColumnLayout([name(120)], 2000).name).toBe(200)
    expect(computeColumnLayout([name(900)], 2000).name).toBe(480)
  })

  it('leaves room beyond every column’s content to the spacer instead of stretching one', () => {
    const w = computeColumnLayout([name(300), grow('message', 256, 600)], 2560)
    expect(w).toEqual({ name: 300, message: 600 })
    expect(total(w)).toBeLessThan(2560)
  })

  it('shares the room left by weight, each grow column up to its longest value', () => {
    // 1000 - 200 - 2*100 = 600 left: weights 3:1 offer 450/150, object only needs 50,
    // so message takes the other 100 too.
    const w = computeColumnLayout([name(200), grow('message', 100, 5000, 3), grow('object', 100, 150, 1)], 1000)
    expect(w).toEqual({ name: 200, message: 650, object: 150 })
  })

  it('caps a grow column at its max even with room to spare', () => {
    expect(computeColumnLayout([grow('message', 256, 3000)], 5000).message).toBe(960)
  })

  it('lets Name reach for its longest name from the room left, sharing it with grow columns by weight', () => {
    // 900 - 250 - 192 = 458 left; Name (weight 2) and selector (1) share it: Name +305 capped at +150.
    const w = computeColumnLayout([name(250, 400), grow('selector', 192, 1000)], 900)
    expect(w.name).toBe(400)
    expect(w.selector).toBe(500)
    expect(computeColumnLayout([name(250, 300), grow('selector', 192, 500)], 2000)).toEqual({ name: 300, selector: 500 })
    // Without a stretch it stays at its content.
    expect(computeColumnLayout([name(250), grow('selector', 192, 500)], 2000)).toEqual({ name: 250, selector: 500 })
  })

  it('takes room back from fit columns, down to their floor, before the table scrolls', () => {
    const w = computeColumnLayout([name(400), { key: 'node', mode: 'fit', floor: 176, max: 400, content: 276 }, exact('age', 96), grow('images', 192, 400)], 700)
    expect(w.images).toBe(192) // grow columns are already at their floor
    expect(w.age).toBe(96)
    expect(total(w)).toBeLessThanOrEqual(700)
    expect(w.name).toBeGreaterThanOrEqual(200)
    expect(w.node).toBeGreaterThanOrEqual(176)
  })

  it('never goes below the floors: the table scrolls instead', () => {
    const w = computeColumnLayout([name(400), exact('status', 160), grow('images', 192, 400)], 300)
    expect(w).toEqual({ name: 200, status: 160, images: 192 })
  })

  it('keeps a width the user dragged, whatever the mode and room', () => {
    const w = computeColumnLayout([{ ...name(400), userWidth: 120 }, { ...exact('age', 96), userWidth: 300 }, grow('images', 192, 400)], 500)
    expect(w).toEqual({ name: 120, age: 300, images: 192 })
  })

  it('does not share or take back room before the viewport is measured', () => {
    expect(computeColumnLayout([name(300), grow('message', 256, 900)], 0)).toEqual({ name: 300, message: 256 })
  })

  it('keeps a grow column at its previous width when another grow column’s content widens', () => {
    const before = computeColumnLayout([name(200), grow('message', 100, 5000), grow('object', 100, 150)], 1000)
    expect(before).toEqual({ name: 200, message: 650, object: 150 })
    // object's content grows; without a hold it would take room message is showing.
    const inputs = [name(200), { ...grow('message', 100, 5000), hold: before.message }, { ...grow('object', 100, 400), hold: before.object }]
    const after = computeColumnLayout(inputs, 1000)
    expect(after.message).toBe(650)
    expect(after.object).toBe(150)
  })

  it('gives up only as much of the held widths as the room is short by', () => {
    const w = computeColumnLayout([name(200), { ...grow('message', 100, 5000), hold: 900 }], 600)
    expect(w.message).toBe(400)
    // Name's content grew by 100: A and B give it back in proportion, rather than A losing 250 to B.
    const shared = computeColumnLayout([name(300), { ...grow('a', 100, 5000), hold: 600 }, { ...grow('b', 100, 5000), hold: 200 }], 1000)
    expect(shared).toEqual({ name: 300, a: 516, b: 183 })
  })

  it('never sums to more than the room it was given', () => {
    const w = computeColumnLayout([name(333.7), grow('a', 100.3, 777.7, 3), grow('b', 100.3, 555.5, 2), grow('c', 90, 999)], 1333)
    expect(total(w)).toBeLessThanOrEqual(1333)
  })
})

describe('percentile', () => {
  it('trims the longest outliers', () => {
    const widths = [...Array(95).fill(200), 900, 900, 900, 900, 900]
    expect(percentile(widths, 0.95)).toBe(200)
    expect(percentile(widths, 1)).toBe(900)
    expect(percentile([], 0.95)).toBe(0)
  })
})

describe('rememberWidths', () => {
  it('only ever widens, and keeps its identity when nothing widened', () => {
    const first = rememberWidths(undefined, { name: 300, message: 500 })
    const narrower = rememberWidths(first, { name: 250, message: 400 })
    expect(narrower).toBe(first)
    expect(rememberWidths(first, { name: 320, message: 400 })).toEqual({ name: 320, message: 500 })
  })
})
