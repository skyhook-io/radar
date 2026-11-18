// Automatic column widths for the resources table.
//
// The table uses table-layout:fixed so columns never jump while rows stream in
// or scroll past (it is virtualized, so the browser only ever sees ~25 rows and
// could not size a column to its data anyway). Widths are therefore computed
// here, from the data of every item, and handed to the <colgroup>.
//
// Each column is one of:
//   exact — the curated width (getColumnMinWidth). Status, Age, Ready, counts.
//           When the room is short it may give back down to `min`: the larger
//           of its header and its measured content (for the CPU and Memory
//           bars, their "used / limit" label).
//   fit   — sized to its content within [floor, max]. Namespace and Node are
//           sized to the 95th percentile of their values, so a few outliers
//           don't set the width, then take part in sharing the room left
//           (below) up to their longest value. Name is sized to its longest
//           name (capped by max) and gives way last, and only down to the
//           width most of its names need (NAME_YIELD_PERCENTILE).
//   grow  — starts at its floor and shares whatever room is left, by weight,
//           each up to its longest value. Long free text: Event messages,
//           selectors, image lists, uncurated CRD printer columns.
//   user  — a width the user dragged; kept as is.
//
// Room is handed out in a fixed order: user widths, Name, exact widths and fit
// columns at their 95th percentile; then the grow columns and the fit columns'
// longer values share what is left by weight, each up to its longest value;
// then whatever is still left is spread over every column in proportion to its
// width, so the table fills its room and keeps its proportions (placeLeftover).
//
// When the room is short it is taken back in rank order (yieldRank), each
// column down to its `min`: Node first (to its header), then exact columns (to
// the larger of their header and their content), then Namespace (to its
// header), then Name (to its 75th percentile, at most NAME_PROTECTED_MAX_PX:
// only names wider than that are cut, and only when the alternative is
// scrolling; a kind can opt its Name out, as Events does). Grow columns are
// already at their floor and user widths never shrink. If the table is still
// short, Name goes on to its median, but only if that ends the scrolling;
// otherwise the first pass stands and the table scrolls sideways. Where the
// table must fit (a window 1920px wide or more) a second pass in the same
// order always takes every column on to its heading and Name to NAME_MIN_PX.

export type ColumnSizingMode = 'exact' | 'fit' | 'grow'

export interface ColumnLayoutInput {
  key: string
  mode: ColumnSizingMode
  /** The narrowest it starts at; a fit column may be wider, an exact column is exactly this. */
  floor: number
  /** How far it may shrink when room is short (default: floor): exact, to its
   *  header or content; Namespace and Node, to their header; Name, to the 75th
   *  percentile of its names. A min above its width means it doesn't shrink. */
  min?: number
  /** When room is short, lower ranks give way first (default: exact and grow 2, fit 3). */
  yieldRank?: number
  /** Name only: how far past `min` it may shrink when that ends the
   *  scrolling (the width half its names need). */
  fitMin?: number
  /** Where the table must fit (`mustFit`), how far past `min` it shrinks:
   *  its heading, or for Name NAME_MIN_PX. */
  hardMin?: number
  /** Width the user dragged or double-click fitted. Overrides everything else. */
  userWidth?: number
  /** Measured content width, cell padding included. fit: the target; grow: the cap. */
  content?: number
  /** fit only: a wider target it may grow to from the room left over (Name's longest name). */
  stretch?: number
  /** Upper bound for fit and grow columns. */
  max?: number
  /** Share of the room left over (grow, and fit with a stretch). */
  weight?: number
  /** The width it rendered at last time, kept while room allows (see below). */
  hold?: number
}

export interface ColumnLayoutOptions {
  /** The table must not scroll sideways if the columns' hard minimums allow it. */
  mustFit?: boolean
}

export function computeColumnLayout(columns: ColumnLayoutInput[], available: number, options: ColumnLayoutOptions = {}): Record<string, number> {
  const widths: Record<string, number> = {}
  const cap = (c: ColumnLayoutInput, v: number) => Math.max(c.floor, Math.min(v, c.max ?? Infinity))
  for (const c of columns) {
    if (c.userWidth) widths[c.key] = c.userWidth
    else if (c.mode === 'fit') widths[c.key] = cap(c, c.content ?? 0)
    else widths[c.key] = c.floor
  }
  // Not laid out yet (first render, or no layout engine): the content widths
  // stand, and nothing is shared or taken back.
  if (!(available > 0)) return roundWidths(widths)

  const used = sum(Object.values(widths))
  if (used > available) {
    let short = used - available
    const yielding = columns.filter(c => !c.userWidth && (c.mode === 'exact' || c.mode === 'fit'))
    const rank = (c: ColumnLayoutInput) => c.yieldRank ?? (c.mode === 'fit' ? 3 : 2)
    const ranks = (cols: ColumnLayoutInput[]) => [...new Set(cols.map(rank))].sort((x, y) => x - y)
    for (const r of ranks(yielding)) {
      short = giveBack(widths, yielding.filter(c => rank(c) === r), lowerBound, short)
    }
    // Still short: a second pass, in the same order. Where the table must
    // fit, it always runs and takes every column on to its hardMin (headings,
    // grow columns included; Name to NAME_MIN_PX). Elsewhere only Name goes on,
    // to its median (fitMin), and only when that ends the scrolling: the other
    // columns keep their content (a usage bar without its limit, an Event
    // without its message, is worse than scrolling).
    if (short > 0.5) {
      const bound = (c: ColumnLayoutInput) => (options.mustFit ? c.hardMin ?? c.fitMin : c.fitMin)
      const second = columns.filter(c => !c.userWidth && bound(c) !== undefined)
      const slack = sum(second.map(c => Math.max(0, widths[c.key] - bound(c)!)))
      if (options.mustFit || slack >= short - 0.5) {
        for (const r of ranks(second)) {
          short = giveBack(widths, second.filter(c => rank(c) === r), c => bound(c)!, short)
        }
      }
    }
    // A table that fits fills its room exactly; one that scrolls keeps its
    // rounded widths, so rounding never adds to the scrolling.
    return short > 0.5 ? roundWidths(widths) : roundToRoom(widths, columns.filter(c => !c.userWidth).at(-1), available)
  }

  let left = available - used
  const sharing = columns
    .filter(c => !c.userWidth && (c.mode === 'grow' || (c.mode === 'fit' && c.stretch !== undefined)))
    .map(c => ({
      key: c.key,
      weight: c.weight && c.weight > 0 ? c.weight : 1,
      target: cap(c, (c.mode === 'grow' ? c.content : c.stretch) ?? 0),
      hold: c.hold,
    }))
  // Sharing by weight alone would let one column's longer new value take room
  // another was already showing, so a column could narrow while every
  // measurement only grew. Each first gets back the width it had (up to its
  // target) — scaled down evenly when they no longer all fit, so a shortfall
  // costs each only its share — and only the rest is shared.
  const held = sharing.map(g => Math.max(0, Math.min(g.hold ?? 0, g.target) - widths[g.key]))
  const heldTotal = sum(held)
  if (heldTotal > 0) {
    const scale = Math.min(1, left / heldTotal)
    sharing.forEach((g, i) => { widths[g.key] += held[i] * scale })
    left -= heldTotal * scale
  }
  left = waterFill(widths, sharing, left)
  const spread = columns.filter(c => !c.userWidth)
  placeLeftover(widths, spread, left)
  return roundToRoom(widths, spread.at(-1), available)
}

// Rounds each width down; the pixels that leaves go to `last` (a column the
// user didn't size), so the columns add up to exactly the room.
function roundToRoom(widths: Record<string, number>, last: ColumnLayoutInput | undefined, available: number): Record<string, number> {
  roundWidths(widths)
  if (last) widths[last.key] += Math.max(0, Math.floor(available) - sum(Object.values(widths)))
  return widths
}

/**
 * How much of the table's columns a cover over its right edge hides, such as
 * a detail drawer `covered` px wide: the columns are laid out for the rest.
 * `viewport` is the scroller's inner width; `chrome` what its outer width adds
 * (a vertical scrollbar, borders), which the cover hides first. A cover that
 * would leave no room beyond the `leading` fixed columns is ignored (0).
 */
export function coveredTableWidth(viewport: number, chrome: number, covered: number, leading: number): number {
  const hidden = Math.max(0, covered - Math.max(0, chrome))
  return hidden > 0 && viewport - hidden > leading ? hidden : 0
}

// How far a column may shrink when room is short.
function lowerBound(c: ColumnLayoutInput): number {
  return c.min ?? c.floor
}

// Where room goes once every column has its content: spread over the columns
// in proportion to their width, so the table fills its room and keeps its
// proportions. Widths the user dragged stay as dragged. Kept as its own step
// so that choice can change in one place.
function placeLeftover(widths: Record<string, number>, columns: ColumnLayoutInput[], left: number): void {
  const total = sum(columns.map(c => widths[c.key]))
  if (!(left > 0) || total <= 0) return
  for (const c of columns) widths[c.key] += left * widths[c.key] / total
}

// Takes up to `short` back from the columns, each in proportion to how far it
// is above its lower bound. Returns what is still short.
function giveBack(
  widths: Record<string, number>,
  columns: ColumnLayoutInput[],
  lowerBound: (c: ColumnLayoutInput) => number,
  short: number,
): number {
  const slack = sum(columns.map(c => Math.max(0, widths[c.key] - lowerBound(c))))
  if (!(short > 0) || slack <= 0) return short
  const ratio = Math.min(1, short / slack)
  for (const c of columns) widths[c.key] -= Math.max(0, widths[c.key] - lowerBound(c)) * ratio
  return short - slack * ratio
}

// Shares `left` among columns by weight, each up to its target, re-sharing what
// a satisfied column did not need. Returns the room still left.
function waterFill(
  widths: Record<string, number>,
  columns: { key: string; weight: number; target: number }[],
  left: number,
): number {
  let active = columns.filter(c => c.target > widths[c.key])
  while (left > 0.5 && active.length) {
    const totalWeight = sum(active.map(c => c.weight))
    let given = 0
    for (const c of active) {
      const add = Math.min(left * c.weight / totalWeight, c.target - widths[c.key])
      widths[c.key] += add
      given += add
    }
    left -= given
    active = active.filter(c => c.target - widths[c.key] > 0.5)
    if (given < 0.5) break
  }
  return left
}

// Rounded down so the columns never add up to a pixel more than the room they
// were given, which would show a horizontal scrollbar for nothing.
function roundWidths(widths: Record<string, number>): Record<string, number> {
  for (const key of Object.keys(widths)) widths[key] = Math.floor(widths[key])
  return widths
}

function sum(values: number[]): number {
  let total = 0
  for (const v of values) total += v
  return total
}

/** The value below which `fraction` of the widths fall (nearest rank). */
export function percentile(values: number[], fraction: number): number {
  if (!values.length) return 0
  const sorted = [...values].sort((a, b) => a - b)
  const rank = Math.ceil(fraction * sorted.length) - 1
  return sorted[Math.max(0, Math.min(sorted.length - 1, rank))]
}

// ---------------------------------------------------------------------------
// Grow-only memory (kubectl's RememberWidths)
// ---------------------------------------------------------------------------

/**
 * Content widths only ever widen while the table shows the same data set, so a
 * row leaving the list (or the p95 dipping as rows arrive) never narrows a
 * column under the reader. Held in memory and dropped when the data set
 * changes; never persisted, because it describes one cluster's data and Radar
 * Hub points one UI at many clusters.
 */
export function rememberWidths(
  previous: Record<string, number> | undefined,
  measured: Record<string, number>,
): Record<string, number> {
  if (!previous) return measured
  const next: Record<string, number> = { ...previous }
  let changed = false
  for (const [key, width] of Object.entries(measured)) {
    if (!(next[key] >= width)) {
      next[key] = width
      changed = true
    }
  }
  return changed ? next : previous
}
