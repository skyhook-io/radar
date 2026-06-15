import type { ReferenceLine, TimeSeries } from './types'

/**
 * Compute panel saturation as a ratio of peak observed value to its
 * operational ceiling. Picks the limit reference when both are present
 * (limit is the OOM/throttle boundary; request is the scheduler reservation —
 * the operationally meaningful number).
 *
 * Returns `undefined` for:
 * - empty series (nothing to derive a peak from)
 * - all-zero data (peak <= 0)
 * - no usable reference (missing, or ref.value <= 0)
 *
 * Callers should treat `undefined` as "don't render a saturation chip".
 */
export function computeSaturation(
  series: TimeSeries[],
  refs: ReferenceLine[],
): { ratio: number; against: 'limit' | 'request' } | undefined {
  let peak = 0
  for (const s of series) {
    for (const dp of s.dataPoints) {
      if (dp.value == null) continue
      if (dp.value > peak) peak = dp.value
    }
  }
  if (peak <= 0) return undefined
  const limit = refs.find(r => r.kind === 'limit')
  const request = refs.find(r => r.kind === 'request')
  const ref = limit ?? request
  if (!ref || ref.value <= 0) return undefined
  return { ratio: peak / ref.value, against: limit ? 'limit' : 'request' }
}
