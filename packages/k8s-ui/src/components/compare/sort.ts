import type { NamespacedRef } from './types'

/**
 * Order candidates for the compare picker: same-namespace as source first
 * (that's the obvious target), then alphabetical, then namespace tie-break.
 * The source itself is filtered out.
 */
export function sortCandidates<T extends NamespacedRef>(candidates: T[], source: NamespacedRef): T[] {
  return [...candidates]
    .filter(c => !(c.namespace === source.namespace && c.name === source.name))
    .sort((x, y) => {
      const xSameNs = x.namespace === source.namespace ? 0 : 1
      const ySameNs = y.namespace === source.namespace ? 0 : 1
      if (xSameNs !== ySameNs) return xSameNs - ySameNs
      const nameCmp = x.name.localeCompare(y.name)
      if (nameCmp !== 0) return nameCmp
      return x.namespace.localeCompare(y.namespace)
    })
}

/** Apply a free-text filter to candidates by name OR namespace substring. */
export function filterCandidates<T extends NamespacedRef>(candidates: T[], query: string): T[] {
  const q = query.trim().toLowerCase()
  if (!q) return candidates
  return candidates.filter(c => c.name.toLowerCase().includes(q) || c.namespace.toLowerCase().includes(q))
}
