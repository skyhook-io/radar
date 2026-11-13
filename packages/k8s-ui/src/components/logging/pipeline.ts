import type { LogPodCount } from '../../types/core'

const PARTIAL = 'some data was not readable or not evaluated'

/** A pod count as shown: a lower bound reads "≥N", and its zero is unknown rather than none. */
export function countLabel(c: LogPodCount, what: string): { text: string; title: string } {
  if (!c.lowerBound) return { text: String(c.count), title: `${c.count} ${what}.` }
  if (c.count === 0) return { text: '?', title: `Unknown: ${PARTIAL}.` }
  return { text: `≥${c.count}`, title: `At least ${c.count} ${what}: ${PARTIAL}.` }
}
