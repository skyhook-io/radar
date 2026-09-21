import type { TimeSeriesPoint } from './types'

export function nearestSample(points: TimeSeriesPoint[], timestamp: number, stepSeconds?: number): { timestamp: number; value: number } | null {
  let closest: { timestamp: number; value: number } | null = null
  let distance = Infinity
  for (const point of points) {
    if (point.value == null) continue
    const candidateDistance = Math.abs(point.timestamp - timestamp)
    if (candidateDistance < distance) {
      distance = candidateDistance
      closest = { timestamp: point.timestamp, value: point.value }
    }
  }
  return stepSeconds !== undefined && distance > stepSeconds / 2 ? null : closest
}
