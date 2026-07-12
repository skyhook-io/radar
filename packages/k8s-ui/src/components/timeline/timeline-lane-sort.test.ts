import { describe, expect, it } from 'vitest'
import type { TimelineEvent } from '../../types'
import type { ResourceLane } from '../../utils/resource-hierarchy'
import { extractPinnedLanes } from '../../utils/resource-hierarchy'
import { sortTimelineLanes, type LaneSortContext } from './timeline-lane-sort'

function ev(ts: number, over: Partial<TimelineEvent> = {}): TimelineEvent {
  return {
    id: `${ts}-${Math.random()}`,
    timestamp: new Date(ts).toISOString(),
    source: 'informer',
    kind: 'Pod',
    namespace: 'default',
    name: 'thing',
    eventType: 'update',
    ...over,
  }
}

function lane(over: Partial<ResourceLane> & { id: string; name: string }): ResourceLane {
  return {
    kind: 'Pod',
    namespace: 'default',
    isWorkload: true,
    events: [],
    ...over,
  }
}

// Visible window: [1000, 2000].
const WINDOW = { windowStart: 1000, windowEnd: 2000 }

// Importance scores keyed by lane id — injected so the sort stays pure.
const SCORES: Record<string, number> = { web: 5, api: 1, db: 9, cart: 3 }
const ctx: LaneSortContext = { ...WINDOW, scoreOf: (l) => SCORES[l.id] ?? 0 }

const web = lane({ id: 'web', name: 'web', namespace: 'default', events: [ev(1500)] })
const api = lane({ id: 'api', name: 'api', namespace: 'default', events: [ev(1900)] })
// db's only event predates the window — 'recent' falls back to its overall newest.
const dbLane = lane({ id: 'db', name: 'db', namespace: 'prod', events: [ev(500)] })
// An app-group lane: sorts by its title, not its synthetic name.
const cart = lane({ id: 'cart', name: 'cart-app', title: 'Cart', isAppGroup: true, events: [ev(1200)] })

const LANES = [web, api, dbLane, cart]

const ids = (lanes: ResourceLane[]) => lanes.map((l) => l.id)

describe('sortTimelineLanes', () => {
  it("'importance' (default) orders by score, highest first", () => {
    expect(ids(sortTimelineLanes(LANES, 'importance', ctx))).toEqual(['db', 'web', 'cart', 'api'])
  })

  it("'recent' orders by newest in-window event, falling back to overall newest", () => {
    // In-window newest: api(1900) > web(1500) > cart(1200); db has nothing
    // in-window so its overall newest (500) sinks it to the bottom.
    expect(ids(sortTimelineLanes(LANES, 'recent', ctx))).toEqual(['api', 'web', 'cart', 'db'])
  })

  it("'recent' breaks ties by importance", () => {
    const a = lane({ id: 'a', name: 'a', events: [ev(1500)] })
    const b = lane({ id: 'b', name: 'b', events: [ev(1500)] })
    const tieCtx: LaneSortContext = { ...WINDOW, scoreOf: (l) => (l.id === 'b' ? 10 : 1) }
    expect(ids(sortTimelineLanes([a, b], 'recent', tieCtx))).toEqual(['b', 'a'])
  })

  it("'name' orders alphabetically (case-insensitive), app groups by their title", () => {
    // api, Cart (title), db, web.
    expect(ids(sortTimelineLanes(LANES, 'name', ctx))).toEqual(['api', 'cart', 'db', 'web'])
  })

  it("'name' breaks ties by namespace", () => {
    const first = lane({ id: 'first', name: 'web', namespace: 'aaa' })
    const second = lane({ id: 'second', name: 'web', namespace: 'zzz' })
    expect(ids(sortTimelineLanes([second, first], 'name', ctx))).toEqual(['first', 'second'])
  })

  it('does not mutate the input array (pinned extraction reads the original order)', () => {
    const input = [...LANES]
    sortTimelineLanes(input, 'name', ctx)
    expect(ids(input)).toEqual(['web', 'api', 'db', 'cart'])
  })
})

describe('pinned section is unaffected by sort', () => {
  it('keeps strict pin order regardless of the active lane sort', () => {
    const pinnedRefs = [
      { id: 'db', kind: 'Pod', namespace: 'prod', name: 'db' },
      { id: 'api', kind: 'Pod', namespace: 'default', name: 'api' },
    ]
    // Extract from lanes ordered by two different sorts — the pinned rows follow
    // the ref order, never the lane order.
    const byImportance = sortTimelineLanes(LANES, 'importance', ctx)
    const byName = sortTimelineLanes(LANES, 'name', ctx)
    expect(ids(extractPinnedLanes(byImportance, pinnedRefs))).toEqual(['db', 'api'])
    expect(ids(extractPinnedLanes(byName, pinnedRefs))).toEqual(['db', 'api'])
  })
})
