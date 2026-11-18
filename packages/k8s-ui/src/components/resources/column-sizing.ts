// Automatic column widths for the resources table.
//
// The table uses table-layout:fixed so columns never jump while rows stream in
// or scroll past (it is virtualized, so the browser only ever sees ~25 rows and
// could not size a column to its data anyway). Widths are therefore computed
// here, from the data of every item, and handed to the <colgroup>.
//
// Each column is one of:
//   exact — the curated width (getColumnMinWidth). Status, Age, Ready, counts.
//   fit   — sized to its content within [floor, max]: Name, Namespace, Node.
//   grow  — starts at its floor and shares whatever room is left, by weight,
//           each up to its longest value. Long free text: Event messages,
//           selectors, image lists, uncurated CRD printer columns.
//   user  — a width the user dragged; kept as is.
//
// Room is handed out in a fixed order — user and exact widths, then fit columns
// at their content, then grow columns — and whatever remains goes to the
// table's trailing spacer column rather than to any one column (not even to
// Name's longest values past its 95th percentile): a stretched column pushes
// the columns after it under the resource drawer, which overlays the right of
// the list. When the room is
// short, grow columns are the first to give it back (they are already at their
// floor before they get any), then fit columns shrink toward their floor; below
// the sum of floors the table scrolls sideways.

export type ColumnSizingMode = 'exact' | 'fit' | 'grow'

export interface ColumnLayoutInput {
  key: string
  mode: ColumnSizingMode
  /** Never rendered narrower than this (except a user width, which wins). */
  floor: number
  /** Width the user dragged or double-click fitted. Overrides everything else. */
  userWidth?: number
  /** Measured content width, cell padding included. fit: the target; grow: the cap. */
  content?: number
  /** Upper bound for fit and grow columns. */
  max?: number
  /** grow only: share of the room left over. */
  weight?: number
  /** grow only: the width it rendered at last time, kept while room allows (see below). */
  hold?: number
}

export function computeColumnLayout(columns: ColumnLayoutInput[], available: number): Record<string, number> {
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

  let used = sum(Object.values(widths))
  if (used > available) {
    const fits = columns.filter(c => !c.userWidth && c.mode === 'fit')
    const slack = sum(fits.map(c => widths[c.key] - c.floor))
    if (slack > 0) {
      const ratio = Math.min(1, (used - available) / slack)
      for (const c of fits) widths[c.key] -= (widths[c.key] - c.floor) * ratio
    }
    return roundWidths(widths)
  }

  let left = available - used
  const grows = columns
    .filter(c => !c.userWidth && c.mode === 'grow')
    .map(c => ({ key: c.key, weight: c.weight && c.weight > 0 ? c.weight : 1, target: cap(c, c.content ?? 0), hold: c.hold }))
  // Sharing by weight alone would let one column's longer new value take room
  // another was already showing, so a column could narrow while every
  // measurement only grew. Each first gets back the width it had (up to its
  // target) — scaled down evenly when they no longer all fit, so a shortfall
  // costs each only its share — and only the rest is shared.
  const held = grows.map(g => Math.max(0, Math.min(g.hold ?? 0, g.target) - widths[g.key]))
  const heldTotal = sum(held)
  if (heldTotal > 0) {
    const scale = Math.min(1, left / heldTotal)
    grows.forEach((g, i) => { widths[g.key] += held[i] * scale })
    left -= heldTotal * scale
  }
  waterFill(widths, grows, left)
  return roundWidths(widths)
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
