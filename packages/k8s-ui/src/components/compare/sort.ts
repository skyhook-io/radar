export interface SortableCandidate {
  namespace: string
  name: string
}

export interface SortSource {
  namespace: string
  name: string
}

/**
 * Order candidates for the compare picker:
 * 1. Same-namespace as source first — that's the obvious target.
 * 2. Then alphabetical by name (locale).
 * 3. Tie-break by namespace.
 * The source itself is filtered out.
 */
export function sortCandidates<T extends SortableCandidate>(
  candidates: T[],
  source: SortSource,
): T[] {
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
export function filterCandidates<T extends SortableCandidate>(candidates: T[], query: string): T[] {
  const q = query.trim().toLowerCase()
  if (!q) return candidates
  return candidates.filter(c => c.name.toLowerCase().includes(q) || c.namespace.toLowerCase().includes(q))
}
