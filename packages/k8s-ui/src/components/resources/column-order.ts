// The user's column order for a resource table, kept apart from visibility: it
// lists every movable column, hidden ones included, so a column hidden and later
// re-shown comes back where it was.

interface Keyed {
  key: string
}

/** Columns up to and including Name stay where the kind defines them. Name is
 *  the flexible column and the row link; host-injected leading columns sit in
 *  front of it by contract. */
export function pinnedColumnCount(columns: readonly Keyed[]): number {
  return columns.findIndex(c => c.key === 'name') + 1
}

/** A saved order read from localStorage: only string keys, first occurrence
 *  wins. Anything else reads as "no saved order". */
export function sanitizeColumnOrder(raw: unknown): string[] | null {
  if (!Array.isArray(raw)) return null
  const seen = new Set<string>()
  for (const k of raw) {
    if (typeof k === 'string' && !seen.has(k)) seen.add(k)
  }
  return seen.size ? Array.from(seen) : null
}

/**
 * Columns in the user's saved order.
 *
 * Saved keys that no longer exist are ignored. A column the saved order does not
 * name (a built-in, printer, or custom column) goes
 * where it sits by default: right before the column that follows it in the
 * definition, or at the end when nothing that follows it is in the saved order.
 */
export function applyColumnOrder<T extends Keyed>(columns: readonly T[], order: readonly string[] | null | undefined): T[] {
  if (!order?.length) return [...columns]
  const pinned = pinnedColumnCount(columns)
  const movable = columns.slice(pinned)
  const rank = new Map(order.map((k, i) => [k, i]))
  const result = movable
    .filter(c => rank.has(c.key))
    .sort((a, b) => rank.get(a.key)! - rank.get(b.key)!)
  movable.forEach((c, i) => {
    if (rank.has(c.key)) return
    const anchor = movable.slice(i + 1).find(n => rank.has(n.key))
    const at = anchor ? result.indexOf(anchor) : -1
    if (at === -1) result.push(c)
    else result.splice(at, 0, c)
  })
  return [...columns.slice(0, pinned), ...result]
}

/** The movable keys of `columns` (already in display order) after moving `key`
 *  to `toIndex` within them. Returns null when nothing would change. */
export function moveColumn(columns: readonly Keyed[], key: string, toIndex: number): string[] | null {
  const keys = columns.slice(pinnedColumnCount(columns)).map(c => c.key)
  const from = keys.indexOf(key)
  if (from === -1) return null
  const to = Math.max(0, Math.min(keys.length - 1, toIndex))
  if (to === from) return null
  keys.splice(from, 1)
  keys.splice(to, 0, key)
  return keys
}
