import type { NamespacedRef } from './types'

/** Max picks the compare flow accepts. Two-way compare only. */
export const COMPARE_PICK_CAP = 2

/**
 * Toggle a resource in the compare picks list.
 * Existing pick → remove. Below cap → append. At cap → drop the oldest so the
 * row click always changes state (no silent no-op).
 */
function sameRef(a: NamespacedRef, b: NamespacedRef): boolean {
  // clusterId may be undefined on both (OSS single-cluster compare) or
  // set on both (cross-cluster compare). When set, two picks with the
  // same kind+ns+name in DIFFERENT clusters must be treated as distinct
  // — otherwise the second pick would toggle off the first.
  return a.clusterId === b.clusterId && a.namespace === b.namespace && a.name === b.name
}

export function togglePick(picks: NamespacedRef[], ref: NamespacedRef): NamespacedRef[] {
  if (!ref.name) return picks
  const existingIdx = picks.findIndex(p => sameRef(p, ref))
  if (existingIdx >= 0) {
    return picks.filter((_, i) => i !== existingIdx)
  }
  if (picks.length >= COMPARE_PICK_CAP) {
    return [...picks.slice(1), ref]
  }
  return [...picks, ref]
}

/** -1 if not picked; otherwise the slot index (0 = A, 1 = B). */
export function pickIndex(picks: NamespacedRef[], ref: NamespacedRef): number {
  if (!ref.name) return -1
  return picks.findIndex(p => sameRef(p, ref))
}
