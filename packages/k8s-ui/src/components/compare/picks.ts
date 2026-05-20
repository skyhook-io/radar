export interface Pick {
  namespace: string
  name: string
}

/** Max picks the compare flow accepts. Two-way compare only — see the design memo. */
export const COMPARE_PICK_CAP = 2

/**
 * Toggle a resource in the compare picks list.
 * - Existing pick → remove (deselect)
 * - Below cap → append
 * - At cap → drop the oldest and append (so the row click always has a visible effect)
 */
export function togglePick(picks: Pick[], ref: Pick): Pick[] {
  if (!ref.name) return picks
  const existingIdx = picks.findIndex(p => p.namespace === ref.namespace && p.name === ref.name)
  if (existingIdx >= 0) {
    return picks.filter((_, i) => i !== existingIdx)
  }
  if (picks.length >= COMPARE_PICK_CAP) {
    return [...picks.slice(picks.length - COMPARE_PICK_CAP + 1), ref]
  }
  return [...picks, ref]
}

/** -1 if not picked; otherwise the slot index (0 = A, 1 = B). */
export function pickIndex(picks: Pick[], ref: Pick): number {
  if (!ref.name) return -1
  return picks.findIndex(p => p.namespace === ref.namespace && p.name === ref.name)
}
