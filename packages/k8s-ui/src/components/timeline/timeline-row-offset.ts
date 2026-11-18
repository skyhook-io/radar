import type { SizeRange } from 'react-virtuoso'

// Offset of row `index` from the list's top, from Virtuoso's size ranges, which
// cover every index from 0 (the last runs to Infinity). The list has no gap.
export function rowOffset(ranges: SizeRange[], index: number): number {
  let offset = 0
  for (const range of ranges) {
    if (range.startIndex >= index) break
    offset += (Math.min(range.endIndex, index - 1) - range.startIndex + 1) * range.size
  }
  return offset
}
