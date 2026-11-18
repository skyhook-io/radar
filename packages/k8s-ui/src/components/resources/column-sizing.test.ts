import { describe, expect, it } from 'vitest'
import { type ColumnLayoutInput, computeColumnLayout, coveredTableWidth, percentile, rememberWidths } from './column-sizing'

// Name as the table builds it: yields last, here no further than its content (fully protected).
const name = (content: number, min = content): ColumnLayoutInput => ({ key: 'name', mode: 'fit', floor: 200, max: 480, content, min, yieldRank: 4 })
const exact = (key: string, floor: number): ColumnLayoutInput => ({ key, mode: 'exact', floor })
const grow = (key: string, floor: number, content: number, weight = 1, max = 960): ColumnLayoutInput => ({ key, mode: 'grow', floor, content, weight, max })
const total = (widths: Record<string, number>) => Object.values(widths).reduce((a, b) => a + b, 0)

describe('computeColumnLayout', () => {
  it('keeps exact columns at their floor and fits Name to its content', () => {
    const w = computeColumnLayout([name(300), exact('status', 160), exact('age', 96)], 556)
    expect(w).toEqual({ name: 300, status: 160, age: 96 })
  })

  it('protects Name at its content, at least its floor and at most its cap', () => {
    expect(computeColumnLayout([name(120)], 0).name).toBe(200)
    expect(computeColumnLayout([name(900)], 0).name).toBe(480)
  })

  it('spreads room beyond every column’s content over all of them in proportion, keeping dragged widths', () => {
    // Content first: Name 300, Message grows to 600 (900 in all); the other 900 is spread 1:2.
    const w = computeColumnLayout([name(300), grow('message', 256, 600)], 1800)
    expect(w).toEqual({ name: 600, message: 1200 })
    const dragged = computeColumnLayout([name(300), grow('message', 256, 600), { ...exact('age', 96), userWidth: 100 }], 1900)
    expect(dragged).toEqual({ name: 600, message: 1200, age: 100 })
    // Name's cap limits only the width it claims before the others, not its share.
    expect(computeColumnLayout([name(900), exact('age', 96)], 1152).name).toBe(960)
  })

  it('shares the room left by weight, each grow column up to its longest value', () => {
    // 1000 - 200 - 2*100 = 600 left: weights 3:1 offer 450/150, object only needs 50,
    // so message takes the other 100 too.
    const w = computeColumnLayout([name(200), grow('message', 100, 5000, 3), grow('object', 100, 150, 1)], 1000)
    expect(w).toEqual({ name: 200, message: 650, object: 150 })
  })

  it('caps what a grow column claims at its max; past that it only takes its proportional share', () => {
    // Message claims up to 960; the remaining 1056 is spread 960:96, keeping the 10:1 ratio.
    const w = computeColumnLayout([grow('message', 256, 3000), exact('age', 96)], 2112)
    expect(w).toEqual({ message: 1920, age: 192 })
  })

  it('lets a fit column reach for its longest value from the room left, sharing it with grow columns by weight', () => {
    // 900 - 200 - 250 - 192 = 258 left; namespace (weight 2) and selector (1) share it: namespace +172 capped at +150.
    const ns = (content: number, stretch?: number): ColumnLayoutInput => ({ key: 'namespace', mode: 'fit', floor: 192, max: 400, content, stretch, weight: 2 })
    const w = computeColumnLayout([name(200), ns(250, 400), grow('selector', 192, 1000)], 900)
    expect(w.namespace).toBe(400)
    expect(w.selector).toBe(300)
    // Without a stretch it stays at its content.
    expect(computeColumnLayout([name(200), ns(250), grow('selector', 192, 500)], 950)).toEqual({ name: 200, namespace: 250, selector: 500 })
  })

  it('when short, takes room from exact columns down to their content before fit columns, and never from Name', () => {
    const cols: ColumnLayoutInput[] = [
      name(400),
      { key: 'node', mode: 'fit', floor: 176, max: 400, content: 276 },
      { key: 'status', mode: 'exact', floor: 160, min: 110 },
      { key: 'cpu', mode: 'exact', floor: 160, min: 136 },
      exact('age', 96),
      grow('images', 192, 400),
    ]
    // Room for everything at its normal width: 400+276+160+160+96+192 = 1284.
    expect(total(computeColumnLayout(cols, 1284))).toBe(1284)
    // 50 short: only the exact columns give, in proportion to their slack (50 and 24).
    const a = computeColumnLayout(cols, 1234)
    expect(a.name).toBe(400)
    expect(a.node).toBe(276)
    expect(a.status + a.cpu).toBeGreaterThanOrEqual(270 - 1)
    expect(a.age).toBe(96) // no known content: never shrinks
    // 150 short: exact columns are at their content (74 given), Node gives the rest down to its floor.
    const b = computeColumnLayout(cols, 1134)
    expect(b).toMatchObject({ name: 400, status: 110, cpu: 136, age: 96, images: 192 })
    expect(b.node).toBe(276 - 76)
    // Past every lower bound: Name still holds, the table scrolls.
    const c = computeColumnLayout(cols, 600)
    expect(c).toEqual({ name: 400, node: 176, status: 110, cpu: 136, age: 96, images: 192 })
  })

  it('shrinks fit columns to their header, not their normal width, when room is short', () => {
    const cols: ColumnLayoutInput[] = [
      name(300),
      { key: 'namespace', mode: 'fit', floor: 192, max: 320, content: 181, min: 145 },
      { key: 'node', mode: 'fit', floor: 176, max: 400, content: 214, min: 87, yieldRank: 1 },
    ]
    // 300 + 192 + 214 = 706 normally. 106 short: all from Node (to 108).
    expect(computeColumnLayout(cols, 600)).toEqual({ name: 300, namespace: 192, node: 108 })
    // 146 short: Node gives all it can (127, down to its header), Namespace the other 19.
    expect(computeColumnLayout(cols, 560)).toEqual({ name: 300, namespace: 173, node: 87 })
  })

  it('lets Node give way before the exact columns, and Namespace after them', () => {
    const cols: ColumnLayoutInput[] = [
      name(300),
      { key: 'namespace', mode: 'fit', floor: 150, max: 320, content: 200 },
      { key: 'status', mode: 'exact', floor: 160, min: 110 },
      { key: 'node', mode: 'fit', floor: 176, max: 400, content: 276, yieldRank: 1 },
    ]
    // 936 at normal widths. 60 short: all from Node.
    expect(computeColumnLayout(cols, 876)).toEqual({ name: 300, namespace: 200, status: 160, node: 216 })
    // 150 short: Node to its floor (100), then Status to its content (50).
    expect(computeColumnLayout(cols, 786)).toEqual({ name: 300, namespace: 200, status: 110, node: 176 })
    // 170 short: then Namespace (20).
    expect(computeColumnLayout(cols, 766)).toEqual({ name: 300, namespace: 180, status: 110, node: 176 })
  })

  it('takes room from Name last, and only down to its yield width, before the table scrolls', () => {
    const cols: ColumnLayoutInput[] = [
      name(480, 360),
      { key: 'namespace', mode: 'fit', floor: 192, max: 320, content: 181, min: 145 },
      { key: 'status', mode: 'exact', floor: 160, min: 130 },
      { key: 'node', mode: 'fit', floor: 176, max: 400, content: 214, min: 86, yieldRank: 1 },
    ]
    // 480 + 192 + 160 + 214 = 1046. Everything else gives first: Node 128, Status 30, Namespace 47 = 205.
    expect(computeColumnLayout(cols, 841)).toEqual({ name: 480, namespace: 145, status: 130, node: 86 })
    // 60 more short: only now Name gives, down to 420.
    expect(computeColumnLayout(cols, 781)).toEqual({ name: 420, namespace: 145, status: 130, node: 86 })
    // Past its yield width Name holds and the table scrolls.
    expect(computeColumnLayout(cols, 500)).toEqual({ name: 360, namespace: 145, status: 130, node: 86 })
  })

  it('takes columns on to their headings, and Name to its median, only when that ends the scrolling', () => {
    const cols: ColumnLayoutInput[] = [
      { ...name(400, 300), fitMin: 240, hardMin: 200 },
      { ...exact('status', 160), min: 150, hardMin: 90 },
      { ...grow('message', 256, 900), hardMin: 120 },
    ]
    // 816 wanted; the first pass gives 110 (Status 10, Name 100): 706.
    expect(computeColumnLayout(cols, 706)).toEqual({ name: 300, status: 150, message: 256 })
    // Status and Message to their headings (196) end the scrolling at 510...
    expect(computeColumnLayout(cols, 510)).toEqual({ name: 300, status: 90, message: 120 })
    // ...and Name to its median at 450.
    expect(computeColumnLayout(cols, 450)).toEqual({ name: 240, status: 90, message: 120 })
    // Short of that the table scrolls, with the first pass's widths.
    expect(computeColumnLayout(cols, 449)).toEqual({ name: 300, status: 150, message: 256 })
  })

  it('takes Name on to its median only when that ends the scrolling', () => {
    const cols: ColumnLayoutInput[] = [{ ...name(400, 300), fitMin: 240 }, exact('status', 160)]
    // 560 wanted; Name gives 100 to its yield width, the table fits at 460.
    expect(computeColumnLayout(cols, 460)).toEqual({ name: 300, status: 160 })
    // 20 short of 460: the median ends the scrolling, so Name goes on.
    expect(computeColumnLayout(cols, 440)).toEqual({ name: 280, status: 160 })
    expect(computeColumnLayout(cols, 400)).toEqual({ name: 240, status: 160 })
    // Past the median it would still scroll, so Name stays at its yield width.
    expect(computeColumnLayout(cols, 399)).toEqual({ name: 300, status: 160 })
  })

  it('where the table must fit, takes every column on to its hard minimum before scrolling, Name last', () => {
    const cols: ColumnLayoutInput[] = [
      { ...name(400, 300), hardMin: 200 },
      { ...exact('status', 160), min: 178, hardMin: 90 },
      { ...grow('message', 256, 900, 1), hardMin: 120 },
      { ...grow('object', 256, 900, 1), hardMin: 110 },
    ]
    // 400 + 160 + 256 + 256 = 1072. Without mustFit, where every hard minimum
    // still doesn't fit, Name gives 100 and the rest scrolls.
    expect(computeColumnLayout(cols, 400)).toEqual({ name: 300, status: 160, message: 256, object: 256 })
    // With it: Status and the grow columns give the other 172 first, in proportion to how far
    // each is above its heading (70 : 136 : 146); Name stays at 300.
    expect(computeColumnLayout(cols, 800, { mustFit: true })).toEqual({ name: 300, status: 125, message: 189, object: 184 })
    expect(computeColumnLayout(cols, 600, { mustFit: true })).toEqual({ name: 280, status: 90, message: 120, object: 110 })
    // Past every hard minimum it still scrolls.
    expect(computeColumnLayout(cols, 400, { mustFit: true })).toEqual({ name: 200, status: 90, message: 120, object: 110 })
  })

  it('never shrinks an exact column below its normal width when its content is wider', () => {
    const w = computeColumnLayout([name(200), { key: 'reason', mode: 'exact', floor: 128, min: 200 }], 250)
    expect(w.reason).toBe(128)
  })

  it('never goes below the floors: the table scrolls instead', () => {
    const w = computeColumnLayout([name(400), exact('status', 160), grow('images', 192, 400)], 300)
    expect(w).toEqual({ name: 400, status: 160, images: 192 })
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

  it('adds up to exactly the room when it spreads what is left', () => {
    const w = computeColumnLayout([name(333.3), grow('a', 100.3, 120.7), exact('age', 96.6)], 1333)
    expect(total(w)).toBe(1333)
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

describe('coveredTableWidth', () => {
  it('is 0 without a cover', () => {
    expect(coveredTableWidth(1600, 0, 0, 0)).toBe(0)
    expect(coveredTableWidth(1600, 15, 0, 40)).toBe(0)
  })

  it('hides the covered width, less the scrollbar the cover hides first', () => {
    expect(coveredTableWidth(1600, 0, 550, 0)).toBe(550)
    expect(coveredTableWidth(1585, 15, 550, 0)).toBe(535)
    // A cover narrower than the scrollbar hides no column.
    expect(coveredTableWidth(1585, 15, 10, 0)).toBe(0)
  })

  it('is ignored when it would leave no room beyond the fixed leading columns', () => {
    expect(coveredTableWidth(600, 0, 600, 0)).toBe(0)
    expect(coveredTableWidth(600, 0, 560, 40)).toBe(0)
    expect(coveredTableWidth(600, 0, 559, 40)).toBe(559)
  })

  it('lays the columns out for the uncovered width, giving way in the same order', () => {
    const columns = [name(300, 250), { ...exact('status', 160), min: 120 }, { ...exact('age', 96), min: 60 }]
    expect(total(computeColumnLayout(columns, 1600))).toBe(1600)
    // Drawer open: the same columns laid out for what it leaves visible.
    expect(total(computeColumnLayout(columns, 1600 - coveredTableWidth(1600, 0, 1000, 0)))).toBe(600)
    // Short of room: the curated columns give way to their content before Name does.
    expect(computeColumnLayout(columns, 1600 - coveredTableWidth(1600, 0, 1150, 0))).toEqual({ name: 270, status: 120, age: 60 })
  })
})
