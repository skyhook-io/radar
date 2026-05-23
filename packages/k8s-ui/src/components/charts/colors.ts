// Distinct colors for multi-series charts (up to 10 series).
// Uses 500-level shades for adequate contrast on both dark (#1e293b) and
// light (#ffffff) surfaces.
export const SERIES_COLORS: readonly string[] = [
  '#3b82f6', // blue-500
  '#10b981', // emerald-500
  '#f97316', // orange-500
  '#a855f7', // purple-500
  '#ec4899', // pink-500
  '#eab308', // yellow-500
  '#06b6d4', // cyan-500
  '#84cc16', // lime-500
  '#ef4444', // red-500
  '#6366f1', // indigo-500
]

export function seriesColor(index: number, fallback: string): string {
  return SERIES_COLORS[index % SERIES_COLORS.length] ?? fallback
}

export function seriesFill(index: number, fallback: string): string {
  return (SERIES_COLORS[index % SERIES_COLORS.length] ?? fallback) + '22'
}

/**
 * Strip the shared prefix from a set of labels so the differentiating suffix
 * is what's shown. Example:
 *   ["backend-podinfo-849bd668f9-4tzkg", "backend-podinfo-849bd668f9-5z79f"]
 *   → ["4tzkg", "5z79f"]
 *
 * If stripping would leave empty strings or duplicates, falls back to the
 * original labels — we'd rather show a long-but-correct label than a short
 * misleading one.
 */
export function computeShortLabels(labels: string[]): string[] {
  if (labels.length <= 1) return labels
  let prefix = labels[0]
  for (let i = 1; i < labels.length; i++) {
    while (!labels[i].startsWith(prefix)) {
      prefix = prefix.slice(0, -1)
    }
  }
  const lastSep = Math.max(prefix.lastIndexOf('-'), prefix.lastIndexOf('/'))
  if (lastSep > 0) prefix = prefix.slice(0, lastSep + 1)

  const suffixes = labels.map(l => l.slice(prefix.length))
  if (suffixes.some(s => s === '') || new Set(suffixes).size !== suffixes.length) return labels
  return suffixes
}
