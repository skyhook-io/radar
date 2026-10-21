import { applyColumnOrder } from './column-order'

interface SizedColumn {
  key: string
  width: number
}

export function sanitizePinnedColumns(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.filter((key): key is string => typeof key === 'string' && key.trim().length > 0))]
}

export function resolveColumnPins(
  columns: readonly SizedColumn[],
  requested: readonly string[],
  viewportWidth: number,
  leadingWidth = 0,
): { offsets: ReadonlyMap<string, number>; paused: string[] } {
  const offsets = new Map<string, number>()
  const paused: string[] = []
  const widths = new Map(columns.map(column => [column.key, column.width]))
  // Keep a useful scrolling area, including on windows narrower than 600px.
  const budget = Math.max(0, viewportWidth - Math.min(240, viewportWidth * 0.4))
  let used = leadingWidth
  let full = false
  for (const key of requested) {
    const width = widths.get(key)
    if (width === undefined) continue
    if (!full && used + width <= budget) {
      offsets.set(key, used)
      used += width
    } else {
      full = true
      paused.push(key)
    }
  }
  return { offsets, paused }
}

/** One order for both the Columns panel and the table, independent of width. */
export function orderResourceColumns<T extends { key: string }>(
  columns: readonly T[], order: readonly string[] | null, pinned: readonly string[],
): T[] {
  const result = applyColumnOrder(columns, order, 0)
  const pinRank = new Map(pinned.map((key, index) => [key, index]))
  return [
    ...result.filter(column => pinRank.has(column.key))
      .sort((a, b) => (pinRank.get(a.key) ?? 0) - (pinRank.get(b.key) ?? 0)),
    ...result.filter(column => !pinRank.has(column.key)),
  ]
}

/** Pin appends to the frozen group; unpin joins the start of the scrolling group. */
export function toggleResourceColumnPin(
  columns: readonly { key: string }[], pinned: readonly string[], key: string,
): { order: string[]; pinned: string[] } {
  const next = pinned.includes(key) ? pinned.filter(pin => pin !== key) : [...pinned, key]
  const scrolling = columns.map(column => column.key).filter(column => !next.includes(column) && column !== key)
  if (pinned.includes(key)) scrolling.unshift(key)
  return { order: [...next, ...scrolling], pinned: next }
}

export function moveResourceColumn(
  columns: readonly { key: string }[], pinned: readonly string[], key: string, toIndex: number,
): string[] | null {
  const keys = columns.map(column => column.key)
  const from = keys.indexOf(key)
  if (from < 0) return null
  const inPinnedGroup = pinned.includes(key)
  const indices = keys.flatMap((item, index) => pinned.includes(item) === inPinnedGroup ? [index] : [])
  const to = Math.max(indices[0], Math.min(indices[indices.length - 1], toIndex))
  if (from === to) return null
  keys.splice(from, 1)
  keys.splice(to, 0, key)
  return keys
}
