import type { ResourceLane } from '../../utils/resource-hierarchy'

// The lane orderings offered by the swimlane's View → Sort control. 'importance'
// is the default (the interestingness ranking); a second/third ordering earns the
// control its keep — a lone always-on radio would be inert chrome.
export type TimelineSort = 'importance' | 'recent' | 'name'

export const TIMELINE_SORT_DEFAULT: TimelineSort = 'importance'

export interface LaneSortContext<L extends ResourceLane = ResourceLane> {
  // Visible window bounds — 'recent' prefers events inside this lens.
  windowStart: number
  windowEnd: number
  // Interestingness score, injected so the sort stays pure and reuses the score
  // the swimlane already computed per lane.
  scoreOf: (lane: L) => number
}

// App-group lanes sort by their header title; plain lanes by their resource name.
function laneName(lane: ResourceLane): string {
  return (lane.isAppGroup ? lane.title ?? lane.name : lane.name) ?? ''
}

// Most recent event time for a lane, preferring events inside the visible window
// (the live "what just moved" job) and falling back to the lane's newest event
// overall when nothing lands in-window (e.g. a lane adjacent to a pin). Lanes with
// no events return -Infinity so they sink to the bottom.
function laneRecency(lane: ResourceLane, windowStart: number, windowEnd: number): number {
  const events = lane.allEventsSorted ?? lane.events
  let inWindow = -Infinity
  let overall = -Infinity
  for (const e of events) {
    const t = new Date(e.timestamp).getTime()
    if (t > overall) overall = t
    if (t >= windowStart && t <= windowEnd && t > inWindow) inWindow = t
  }
  return inWindow !== -Infinity ? inWindow : overall
}

// Order the top-level lanes for the chosen sort. Pure + total so the swimlane can
// memo it and the tests can pin each ordering. The PINNED section is ordered
// elsewhere (strict pin order) and never flows through here.
export function sortTimelineLanes<L extends ResourceLane>(
  lanes: L[],
  sort: TimelineSort,
  ctx: LaneSortContext<L>,
): L[] {
  const copy = [...lanes]
  switch (sort) {
    case 'recent': {
      // Precompute recency once per lane (Schwartzian transform): laneRecency
      // scans every event, so calling it inside the comparator would repeat that
      // scan O(n log n) times.
      const recency = new Map<L, number>()
      for (const lane of copy) recency.set(lane, laneRecency(lane, ctx.windowStart, ctx.windowEnd))
      return copy.sort((a, b) => {
        const ra = recency.get(a)!
        const rb = recency.get(b)!
        if (ra !== rb) return rb - ra
        return ctx.scoreOf(b) - ctx.scoreOf(a)
      })
    }
    case 'name':
      return copy.sort((a, b) => {
        const na = laneName(a).toLowerCase()
        const nb = laneName(b).toLowerCase()
        if (na !== nb) return na < nb ? -1 : 1
        const nsa = a.namespace ?? ''
        const nsb = b.namespace ?? ''
        if (nsa !== nsb) return nsa < nsb ? -1 : 1
        return 0
      })
    case 'importance':
    default:
      return copy.sort((a, b) => ctx.scoreOf(b) - ctx.scoreOf(a))
  }
}
