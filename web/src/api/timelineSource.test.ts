import { describe, expect, it, vi, beforeEach } from 'vitest'

// The retained NDJSON parser goes through apiFetch; mock ONLY the network in
// the client module so a test can hand the stream readers an arbitrary body —
// ApiError and mergeDeltaEvents stay real (the ring fetch depends on the real
// merge semantics).
vi.mock('./client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./client')>()
  return { ...actual, apiFetch: vi.fn(), useChanges: vi.fn() }
})

import { apiFetch, ApiError } from './client'
import {
  fetchRetainedWindow,
  rangeSpanMs,
  RETAINED_RING_LIMIT,
  fetchRetainedDelta,
  runRetainedRingFetch,
  applyClientFilters,
  localOverviewFromEvents,
  needsWindowFetch,
  type RetainedRing,
} from './timelineSource'
import type { TimelineEvent } from '../types'

const mockApiFetch = vi.mocked(apiFetch)

// A minimal streamed Response: each string in `chunks` is delivered as one
// reader.read() so tests can split a JSON line across chunk boundaries.
function streamResponse(chunks: string[]): Response {
  const encoder = new TextEncoder()
  let i = 0
  const body = {
    getReader() {
      return {
        read() {
          if (i < chunks.length) {
            return Promise.resolve({ done: false, value: encoder.encode(chunks[i++]) })
          }
          return Promise.resolve({ done: true, value: undefined })
        },
      }
    },
  }
  return { ok: true, body } as unknown as Response
}

const T0 = Date.parse('2024-01-01T00:00:00.000Z')

function ev(over: Partial<TimelineEvent> & { id: string }): TimelineEvent {
  return {
    timestamp: new Date(T0).toISOString(),
    source: 'informer',
    kind: 'Pod',
    namespace: 'default',
    name: over.id,
    eventType: 'update',
    ...over,
  }
}

beforeEach(() => {
  mockApiFetch.mockReset()
})

describe('fetchRetainedWindow (NDJSON stream parser)', () => {
  it('reassembles a JSON line split across chunk boundaries', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse([
        '{"id":"a","kind":"Pod","name":"a","namespace":"ns","source":"informer","eventType":"update"',
        ',"timestamp":"2024-01-01T00:00:00.000Z"}\n{"type":"end"}\n',
      ]),
    )
    const { events } = await fetchRetainedWindow(0, 1000)
    expect(events).toHaveLength(1)
    expect(events[0].id).toBe('a')
    expect(events[0].kind).toBe('Pod')
  })

  it('separates coverage records from events', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse([
        '{"id":"a","kind":"Pod","name":"a","namespace":"ns","source":"informer","eventType":"update","timestamp":"2024-01-01T00:00:00.000Z"}\n',
        '{"type":"coverage","eventTimeStartMs":1,"eventTimeEndMs":2}\n',
        '{"type":"end"}\n',
      ]),
    )
    const { events, coverage } = await fetchRetainedWindow(0, 1000)
    expect(events).toHaveLength(1)
    expect(coverage).toHaveLength(1)
  })

  it('throws on a truncated stream (missing terminal record)', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse([
        '{"id":"a","kind":"Pod","name":"a","namespace":"ns","source":"informer","eventType":"update","timestamp":"2024-01-01T00:00:00.000Z"}\n',
      ]),
    )
    await expect(fetchRetainedWindow(0, 1000)).rejects.toThrow('truncated')
  })

  it('throws the message carried by an error terminal record', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse(['{"type":"error","message":"backend exploded"}\n']),
    )
    await expect(fetchRetainedWindow(0, 1000)).rejects.toThrow('backend exploded')
  })
})

describe('applyClientFilters', () => {
  const events: TimelineEvent[] = [
    ev({ id: 'p1', kind: 'Pod', namespace: 'ns-a', timestamp: new Date(T0 + 3000).toISOString() }),
    ev({ id: 'd1', kind: 'Deployment', namespace: 'ns-b', timestamp: new Date(T0 + 1000).toISOString() }),
    ev({ id: 'k1', kind: 'Pod', namespace: 'ns-a', source: 'k8s_event', eventType: 'Warning', timestamp: new Date(T0 + 2000).toISOString() }),
    ev({ id: 'del1', kind: 'Pod', namespace: 'ns-b', eventType: 'delete', timestamp: new Date(T0 + 4000).toISOString() }),
  ]

  it('filters by namespace', () => {
    const out = applyClientFilters(events, { namespaces: ['ns-a'] })
    expect(out.map((e) => e.id).sort()).toEqual(['k1', 'p1'])
  })

  it('filters by kind', () => {
    const out = applyClientFilters(events, { kinds: ['Deployment'] })
    expect(out.map((e) => e.id)).toEqual(['d1'])
  })

  it('drops k8s events when includeK8sEvents is false', () => {
    const out = applyClientFilters(events, { includeK8sEvents: false })
    expect(out.some((e) => e.id === 'k1')).toBe(false)
  })

  it('drops deletes when includeDeleted is false', () => {
    const out = applyClientFilters(events, { includeDeleted: false })
    expect(out.some((e) => e.id === 'del1')).toBe(false)
  })

  it('bounds the result by [fromMs,toMs] event time', () => {
    const out = applyClientFilters(events, { fromMs: T0 + 1500, toMs: T0 + 3500 })
    expect(out.map((e) => e.id).sort()).toEqual(['k1', 'p1'])
  })

  it('sorts newest-first', () => {
    const out = applyClientFilters(events, {})
    expect(out.map((e) => e.id)).toEqual(['del1', 'p1', 'k1', 'd1'])
  })

  it('caps the result at limit (keeping the newest)', () => {
    const out = applyClientFilters(events, { limit: 2 })
    expect(out.map((e) => e.id)).toEqual(['del1', 'p1'])
  })

  // Retained mode passes no limit — the hub owns the window bound (31d + a 50k
  // row hard stop that surfaces as a stream error). The client must never
  // silently drop older events, so an unset limit returns the full window.
  it('returns every event when no limit is set', () => {
    const many: TimelineEvent[] = Array.from({ length: 12000 }, (_, i) =>
      ev({ id: `e${i}`, kind: 'Pod', namespace: 'ns-a', timestamp: new Date(T0 + i).toISOString() }),
    )
    expect(applyClientFilters(many, {})).toHaveLength(12000)
  })

  // Mirrors Go's TimelineEvent.IsManaged: owned, or ReplicaSet/Pod/Event.
  // Enforced client-side so retained mode (whose endpoint has no
  // include_managed param) behaves exactly like local mode.
  it('drops managed rows only when includeManaged is explicitly false', () => {
    const mixed: TimelineEvent[] = [
      ev({ id: 'dep', kind: 'Deployment', namespace: 'ns-a' }),
      ev({ id: 'pod', kind: 'Pod', namespace: 'ns-a' }),
      ev({ id: 'rs', kind: 'ReplicaSet', namespace: 'ns-a' }),
      ev({ id: 'owned-cm', kind: 'ConfigMap', namespace: 'ns-a', owner: { kind: 'Deployment', name: 'web' } }),
    ]
    const filtered = applyClientFilters(mixed, { includeManaged: false })
    expect(filtered.map((e) => e.id)).toEqual(['dep'])
    // Default (unset) keeps machinery rows — the swimlane's child lanes need them.
    expect(applyClientFilters(mixed, {})).toHaveLength(4)
  })
})

describe('localOverviewFromEvents', () => {
  it('buckets by the hour, floors availableFromMs, and tallies by type', () => {
    const h0 = Date.parse('2024-01-01T00:10:00.000Z')
    const h0b = Date.parse('2024-01-01T00:50:00.000Z')
    const h1 = Date.parse('2024-01-01T01:05:00.000Z')
    const res = localOverviewFromEvents([
      ev({ id: 'a', eventType: 'add', timestamp: new Date(h0).toISOString() }),
      ev({ id: 'b', eventType: 'update', timestamp: new Date(h0b).toISOString() }),
      ev({ id: 'c', eventType: 'delete', timestamp: new Date(h1).toISOString(), namespace: 'other' }),
    ])
    expect(res.buckets).toHaveLength(2)
    expect(res.availableFromMs).toBe(h0)
    const [b0, b1] = res.buckets
    expect(b0.startMs).toBe(Date.parse('2024-01-01T00:00:00.000Z'))
    expect(b0.summary.total).toBe(2)
    expect(b0.summary.adds).toBe(1)
    expect(b0.summary.updates).toBe(1)
    expect(b0.summary.namespaces).toEqual(['default'])
    expect(b1.summary.deletes).toBe(1)
  })

  it('marks a bucket unhealthy when it holds a Warning event', () => {
    const res = localOverviewFromEvents([
      ev({ id: 'w', eventType: 'Warning', source: 'k8s_event', timestamp: new Date(T0 + 60_000).toISOString() }),
    ])
    expect(res.buckets[0].summary.warnings).toBe(1)
    expect(res.buckets[0].summary.worstHealth).toBe('unhealthy')
  })

  it('rebuckets finer with a custom bucketSizeMs', () => {
    const FIVE_MIN = 5 * 60_000
    const events = [
      ev({ id: 'a', timestamp: new Date(T0).toISOString() }),
      ev({ id: 'b', timestamp: new Date(T0 + 10 * 60_000).toISOString() }),
    ]
    // Same hour → one hourly bucket, but two distinct 5-minute buckets.
    expect(localOverviewFromEvents(events).buckets).toHaveLength(1)
    expect(localOverviewFromEvents(events, FIVE_MIN).buckets).toHaveLength(2)
  })
})

// The hook arms the frozen-window fetch on this predicate alone, so these pin
// all directions: ring-covered explicit windows stay zero-fetch, the LIVE
// sliding selection (an explicit window ending at the clock) stays zero-fetch
// even under hub-ahead clock skew and tick lag, and a frozen selection whose
// to-edge sits below the newest loaded ring row (past the tick slack) with a
// from-edge below the oldest escapes to the server — however close its
// to-edge is to the clock.
describe('needsWindowFetch (frozen-window coverage predicate)', () => {
  const HOUR = 60 * 60 * 1000
  const NOW = T0 + 24 * HOUR
  const newestMs = NOW - HOUR
  const oldestMs = NOW - 2 * HOUR
  const ringEvents: TimelineEvent[] = [
    ev({ id: 'newer', timestamp: new Date(newestMs).toISOString() }),
    ev({ id: 'oldest', timestamp: new Date(oldestMs).toISOString() }),
  ]
  // A frozen selection wholly below the ring's oldest loaded row — THE bug
  // case: the ring slice for it is empty while the server holds rows.
  const deepFromMs = NOW - 10 * HOUR
  const deepToMs = NOW - 8 * HOUR

  it('an untruncated ring answers every window — no fetch', () => {
    // Even a from-edge far below the oldest loaded row: untruncated means the
    // server sent everything it holds in the ring's depth.
    expect(needsWindowFetch({ events: ringEvents, truncated: false }, deepFromMs, deepToMs, NOW)).toBe(false)
  })

  it('a truncated ring still answers windows at or after its oldest loaded row', () => {
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, oldestMs, newestMs, NOW)).toBe(false)
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, oldestMs + 1, newestMs, NOW)).toBe(false)
  })

  it('a from-edge below the oldest loaded row needs the server window when the to-edge is below the newest', () => {
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, deepFromMs, deepToMs, NOW)).toBe(true)
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, oldestMs - 1, newestMs - 5 * 60_000, NOW)).toBe(true)
  })

  it('a frozen window ending just below the newest ring row fetches even near the live edge', () => {
    // Ring rows newer than the window's to-edge consume ring budget a server
    // window fetch would spend on OLDER rows the truncated ring dropped —
    // wall-clock proximity of the to-edge proves nothing about coverage.
    const busyRing: TimelineEvent[] = [
      ev({ id: 'fresh', timestamp: new Date(NOW - 30_000).toISOString() }),
      ev({ id: 'oldest', timestamp: new Date(oldestMs).toISOString() }),
    ]
    expect(needsWindowFetch({ events: busyRing, truncated: true }, deepFromMs, NOW - 2 * 60_000, NOW)).toBe(true)
  })

  it('a window reaching to/past the newest ring row stays on the ring, however deep its from-edge', () => {
    // Every ring row falls inside such a window, and the ring is the server's
    // newest-ringLimit answer — a window fetch would return the identical set,
    // and arming it would re-key a full fetch on every live tick.
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, deepFromMs, NOW, NOW)).toBe(false)
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, deepFromMs, newestMs, NOW)).toBe(false)
    // An idle ring whose newest row is old: the live selection's to-edge sits
    // far above it and stays ring-served.
    expect(needsWindowFetch({ events: ringEvents, truncated: true }, deepFromMs, NOW - 60_000, NOW)).toBe(false)
  })

  it('rows stamped ahead of the client clock cannot re-arm the live selection', () => {
    // Hub clock ahead: the newest ring row sits in the client's future. The
    // newest-row edge caps at `now`, so a live to-edge at the clock still
    // counts as reaching the ring's edge.
    const skewedRing: TimelineEvent[] = [
      ev({ id: 'future', timestamp: new Date(NOW + 3 * 60_000).toISOString() }),
      ev({ id: 'oldest', timestamp: new Date(oldestMs).toISOString() }),
    ]
    expect(needsWindowFetch({ events: skewedRing, truncated: true }, deepFromMs, NOW, NOW)).toBe(false)
  })

  it('a delta row landing between live ticks cannot re-arm the live selection', () => {
    // The live to-edge lags the clock by up to one coarse tick while delta
    // merges keep pushing the newest ring row toward now; the tick-slack
    // tolerance keeps that lag from re-keying a full-window fetch per merge.
    const freshRing: TimelineEvent[] = [
      ev({ id: 'justmerged', timestamp: new Date(NOW - 5_000).toISOString() }),
      ev({ id: 'oldest', timestamp: new Date(oldestMs).toISOString() }),
    ]
    expect(needsWindowFetch({ events: freshRing, truncated: true }, deepFromMs, NOW - 30_000, NOW)).toBe(false)
  })

  it('an empty truncated ring fails toward fetching', () => {
    expect(needsWindowFetch({ events: [], truncated: true }, deepFromMs, deepToMs, NOW)).toBe(true)
  })
})

describe('rangeSpanMs (client-side preset resolution)', () => {
  const CAP = 31 * 24 * 60 * 60 * 1000
  it('resolves presets to their spans', () => {
    expect(rangeSpanMs('24h', CAP)).toBe(24 * 60 * 60 * 1000)
    expect(rangeSpanMs('7d', CAP)).toBe(7 * 24 * 60 * 60 * 1000)
  })
  it("falls back to the ring depth for 'all' and unset", () => {
    expect(rangeSpanMs('all', CAP)).toBe(CAP)
    expect(rangeSpanMs(undefined, CAP)).toBe(CAP)
  })
  it("preset arms return their nominal span — clamping is the caller's job", () => {
    // A 30d preset against a 7d host returns 30d here; the use site clamps
    // with Math.min against the ring depth (pinned indirectly by the hook).
    const shallow = 7 * 24 * 60 * 60 * 1000
    expect(rangeSpanMs('30d', shallow)).toBe(30 * 24 * 60 * 60 * 1000)
  })
})

describe('retained ring fetch (the OSS-identical accumulate model)', () => {
  const DAY = 24 * 60 * 60 * 1000
  const CAP = 31 * DAY
  const NOW = T0 + 40 * DAY

  const line = (e: TimelineEvent) => JSON.stringify(e) + '\n'
  const end = (over: Record<string, unknown> = {}) => JSON.stringify({ type: 'end', ...over }) + '\n'

  function ring(events: TimelineEvent[], over: Partial<RetainedRing> = {}): RetainedRing {
    return { events, coverage: [], truncated: false, cursor: '100', loadedAtMs: NOW, ...over }
  }

  it('parses cursor/truncated from the end record and sends limit on the window fetch', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse([line(ev({ id: 'a' })), end({ cursor: '111', truncated: true })]),
    )
    const res = await fetchRetainedWindow(0, 1000, undefined, 50)
    expect(mockApiFetch.mock.calls[0][0]).toContain('limit=50')
    expect(res.cursor).toBe('111')
    expect(res.truncated).toBe(true)
  })

  it('sends the cursor on the delta fetch and surfaces more', async () => {
    mockApiFetch.mockResolvedValue(streamResponse([end({ cursor: '222', more: true })]))
    const res = await fetchRetainedDelta('111')
    expect(mockApiFetch.mock.calls[0][0]).toContain('since=111')
    expect(res.cursor).toBe('222')
    expect(res.more).toBe(true)
  })

  it('loads the full ring once, then accumulates deltas — including a LATE event', async () => {
    const flags = new Set<string>()
    // Full load: one event, cursor 100.
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e1', timestamp: new Date(NOW - DAY).toISOString() })), end({ cursor: '100' })]),
    )
    const first = await runRetainedRingFetch({ ringKey: 'k', cached: undefined, forceResync: flags, capMs: CAP, now: NOW })
    expect(first.events.map((e) => e.id)).toEqual(['e1'])
    expect(mockApiFetch.mock.calls[0][0]).toContain('from=')

    // Delta: a brand-new event AND a late one (event time 3 days back) — both
    // ride the ingestion-ordered poll and land in the ring.
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([
        line(ev({ id: 'e-new', timestamp: new Date(NOW).toISOString() })),
        line(ev({ id: 'e-late', timestamp: new Date(NOW - 3 * DAY).toISOString() })),
        end({ cursor: '200' }),
      ]),
    )
    const second = await runRetainedRingFetch({ ringKey: 'k', cached: first, forceResync: flags, capMs: CAP, now: NOW })
    expect(mockApiFetch.mock.calls[1][0]).toContain('since=100')
    expect(second.events.map((e) => e.id).sort()).toEqual(['e-late', 'e-new', 'e1'])
    expect(second.cursor).toBe('200')
  })

  it('a delta revision replaces its cached id', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1', eventType: 'add', timestamp: new Date(NOW - DAY).toISOString() })])
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e1', eventType: 'update', timestamp: new Date(NOW - DAY).toISOString() })), end({ cursor: '200' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(out.events).toHaveLength(1)
    expect(out.events[0].eventType).toBe('update')
  })

  it('a no-op delta returns the cached reference (no re-render)', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1' })])
    // No rows, cursor unchanged — the idle-cluster steady state.
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '100' })]))
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(out).toBe(cached)
  })

  it('an empty delta with a moved cursor commits the cursor WITH the data', async () => {
    const flags = new Set<string>()
    const loadedAt = NOW - 1000
    const cached = ring([ev({ id: 'e1', timestamp: new Date(NOW - DAY).toISOString() })], { loadedAtMs: loadedAt })
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '150' })]))
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    // The advanced cursor rides the same commit as the events it describes —
    // a discarded fetch discards both, so they can never diverge.
    expect(out).not.toBe(cached)
    expect(out.cursor).toBe('150')
    expect(out.events.map((e) => e.id)).toEqual(['e1'])
    // Delta commits must PRESERVE the full-load clock — resetting it to `now`
    // would silently disable the hourly anti-entropy on any active cluster.
    expect(out.loadedAtMs).toBe(loadedAt)
  })

  it('a ring loaded "in the future" (backward clock step) resyncs instead of suspending', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1', timestamp: new Date(NOW - DAY).toISOString() })], { loadedAtMs: NOW + 10 * 60 * 1000 })
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e-resynced', timestamp: new Date(NOW).toISOString() })), end({ cursor: '700' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(mockApiFetch.mock.calls[0][0]).toContain('from=')
    expect(out.loadedAtMs).toBe(NOW)
  })

  it('prunes events older than the retention depth on merge, keeping the skew-slack band', async () => {
    const flags = new Set<string>()
    const cached = ring([
      ev({ id: 'e-ancient', timestamp: new Date(NOW - CAP - DAY).toISOString() }),
      // Inside the clock-skew slack band just past the depth: a client clock
      // ahead of the hub must not prune what the hub still retains.
      ev({ id: 'e-slack', timestamp: new Date(NOW - CAP - 60 * 1000).toISOString() }),
      ev({ id: 'e-kept', timestamp: new Date(NOW - DAY).toISOString() }),
    ])
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e-new', timestamp: new Date(NOW).toISOString() })), end({ cursor: '200' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(out.events.map((e) => e.id).sort()).toEqual(['e-kept', 'e-new', 'e-slack'])
  })

  it('a capped delta page (more) pages forward within one fetch', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1', timestamp: new Date(NOW - DAY).toISOString() })])
    mockApiFetch
      .mockResolvedValueOnce(streamResponse([line(ev({ id: 'p1', timestamp: new Date(NOW).toISOString() })), end({ cursor: '150', more: true })]))
      .mockResolvedValueOnce(streamResponse([line(ev({ id: 'p2', timestamp: new Date(NOW).toISOString() })), end({ cursor: '200' })]))
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(mockApiFetch).toHaveBeenCalledTimes(2)
    expect(mockApiFetch.mock.calls[1][0]).toContain('since=150')
    expect(out.events.map((e) => e.id).sort()).toEqual(['e1', 'p1', 'p2'])
    expect(out.cursor).toBe('200')
  })

  it('a rejected cursor (400) resyncs with a full ring load', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e-stale' })])
    mockApiFetch
      .mockResolvedValueOnce({
        ok: false,
        status: 400,
        json: () => Promise.resolve({ error: 'invalid since cursor' }),
      } as unknown as Response)
      .mockResolvedValueOnce(streamResponse([line(ev({ id: 'e-fresh' })), end({ cursor: '300' })]))
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(out.events.map((e) => e.id)).toEqual(['e-fresh'])
    expect(out.cursor).toBe('300')
  })

  it('caps accumulated growth at the ring limit, dropping the oldest and flagging truncated', async () => {
    const flags = new Set<string>()
    // A ring already at the cap, oldest-last after sort.
    const full: TimelineEvent[] = Array.from({ length: RETAINED_RING_LIMIT }, (_, i) =>
      ev({ id: `e${i}`, timestamp: new Date(NOW - DAY - i * 1000).toISOString() }),
    )
    const cached = ring(full)
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([
        line(ev({ id: 'e-newest', timestamp: new Date(NOW).toISOString() })),
        end({ cursor: '200' }),
      ]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(out.events).toHaveLength(RETAINED_RING_LIMIT)
    expect(out.events[0].id).toBe('e-newest')
    // The OLDEST row fell off the ring, and the drop is flagged, not silent.
    expect(out.events.some((e) => e.id === `e${RETAINED_RING_LIMIT - 1}`)).toBe(false)
    expect(out.truncated).toBe(true)
  })

  // The local source shares this implementation with a smaller ceiling (its
  // binary pages at 10k); the cap and the window-load limit must both follow
  // the configured ringLimit, not the retained default.
  it('honors a per-source ringLimit for the cap and the window-load limit', async () => {
    const flags = new Set<string>()
    const cached = ring([
      ev({ id: 'old-1', timestamp: new Date(NOW - DAY - 1000).toISOString() }),
      ev({ id: 'old-2', timestamp: new Date(NOW - DAY - 2000).toISOString() }),
      ev({ id: 'old-3', timestamp: new Date(NOW - DAY - 3000).toISOString() }),
    ])
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'new-1', timestamp: new Date(NOW).toISOString() })), end({ cursor: '200' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW, ringLimit: 3 })
    expect(out.events.map((e) => e.id)).toEqual(['new-1', 'old-1', 'old-2'])
    expect(out.truncated).toBe(true)

    // A full load sends the configured limit on the wire.
    const flags2 = new Set<string>()
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '1' })]))
    await runRetainedRingFetch({ ringKey: 'k2', cached: undefined, forceResync: flags2, capMs: CAP, now: NOW, ringLimit: 3 })
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('limit=3')
  })

  // Unbounded depth (a local store configured to retain forever): the full
  // load starts at epoch zero and delta merges never age-prune.
  it('treats an unset capMs as unbounded depth', async () => {
    const flags = new Set<string>()
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '5' })]))
    await runRetainedRingFetch({ ringKey: 'k', cached: undefined, forceResync: flags, now: NOW })
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('from=0')

    const ancient = ev({ id: 'e-ancient', timestamp: new Date(NOW - 400 * DAY).toISOString() })
    const cached = ring([ancient])
    mockApiFetch.mockResolvedValueOnce(streamResponse([line(ev({ id: 'e-new', timestamp: new Date(NOW).toISOString() })), end({ cursor: '9' })]))
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, now: NOW })
    expect(out.events.some((e) => e.id === 'e-ancient')).toBe(true)
  })

  // The local source keeps its historical 5-minute anti-entropy cadence; a
  // per-source resyncMs below the ring age must force a full reload.
  it('honors a per-source resync interval', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1' })])
    cached.loadedAtMs = NOW - 6 * 60 * 1000
    mockApiFetch.mockResolvedValueOnce(streamResponse([line(ev({ id: 'e2' })), end({ cursor: '9' })]))
    await runRetainedRingFetch({
      ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW,
      resyncMs: 5 * 60 * 1000,
    })
    // 6 minutes old with a 5-minute cadence: a FULL window load, not a delta.
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('from=')
  })

  // The ring is row-capped, so a namespace-scoped consumer must scope the
  // server query itself — client-side filtering alone would let other
  // namespaces' events consume the cap.
  it('sends the namespace scope on window loads and delta polls', async () => {
    const flags = new Set<string>()
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '5' })]))
    const first = await runRetainedRingFetch({
      ringKey: 'k', cached: undefined, forceResync: flags, capMs: CAP, now: NOW,
      namespaces: ['team-a', 'team-b'],
    })
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('namespaces=team-a%2Cteam-b')

    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '6' })]))
    await runRetainedRingFetch({
      ringKey: 'k', cached: first, forceResync: flags, capMs: CAP, now: NOW,
      namespaces: ['team-a', 'team-b'],
    })
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('since=5')
    expect(mockApiFetch.mock.calls.at(-1)?.[0]).toContain('namespaces=team-a%2Cteam-b')
  })

  it('a hub without a delta cursor turns polls into no-ops instead of full reloads', async () => {
    const flags = new Set<string>()
    // Full load whose end record has NO cursor (pre-delta hub).
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e1', timestamp: new Date(NOW).toISOString() })), end()]),
    )
    const first = await runRetainedRingFetch({ ringKey: 'k', cached: undefined, forceResync: flags, capMs: CAP, now: NOW })
    expect(first.events.map((e) => e.id)).toEqual(['e1'])
    // Next poll: no network at all — the cached ring is returned as-is.
    const second = await runRetainedRingFetch({ ringKey: 'k', cached: first, forceResync: flags, capMs: CAP, now: NOW })
    expect(second).toBe(first)
    expect(mockApiFetch).toHaveBeenCalledTimes(1)
  })

  it('a stale ring resyncs with a full reload past the anti-entropy window', async () => {
    // loadedAtMs two hours back: cursor is valid, but the resync clock is due —
    // the poll must take the FULL path (refreshing coverage + truncated).
    const flags = new Set<string>()
    const cached = ring(
      [ev({ id: 'e-old-copy', timestamp: new Date(NOW - DAY).toISOString() })],
      { loadedAtMs: NOW - 2 * 60 * 60 * 1000 },
    )
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e-fresh', timestamp: new Date(NOW).toISOString() })), end({ cursor: '900' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(mockApiFetch.mock.calls[0][0]).toContain('from=')
    expect(mockApiFetch.mock.calls[0][0]).not.toContain('since=')
    expect(out.events.map((e) => e.id)).toEqual(['e-fresh'])
    expect(out.cursor).toBe('900')
    expect(out.loadedAtMs).toBe(NOW)
  })

  it('a manual refresh flag forces a full reload and is consumed', async () => {
    const flags = new Set<string>(['k'])
    const cached = ring([ev({ id: 'e1', timestamp: new Date(NOW - DAY).toISOString() })])
    mockApiFetch.mockResolvedValueOnce(
      streamResponse([line(ev({ id: 'e-resynced', timestamp: new Date(NOW).toISOString() })), end({ cursor: '500' })]),
    )
    const out = await runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW })
    expect(mockApiFetch.mock.calls[0][0]).toContain('from=')
    expect(out.events.map((e) => e.id)).toEqual(['e-resynced'])
    expect(flags.has('k')).toBe(false)
  })

  it('the initial window extends past the client clock by the skew slack', async () => {
    // A client clock behind the hub must not open a permanent hole at the live
    // edge: to > now, from slides back by the same slack to keep the span.
    const flags = new Set<string>()
    mockApiFetch.mockResolvedValueOnce(streamResponse([end({ cursor: '1' })]))
    await runRetainedRingFetch({ ringKey: 'k', cached: undefined, forceResync: flags, capMs: CAP, now: NOW })
    const url = String(mockApiFetch.mock.calls[0][0])
    const from = Number(/from=(\d+)/.exec(url)?.[1])
    const to = Number(/to=(\d+)/.exec(url)?.[1])
    expect(to).toBeGreaterThan(NOW)
    expect(to - from).toBe(CAP)
  })

  // The frozen-window fetch reuses fetchRetainedWindow with the hook's exact
  // arguments: the selection's [from,to], the source's ringLimit, and the
  // server-side namespace scope.
  it('a frozen-window fetch carries from/to/limit/namespaces on the wire and surfaces truncated', async () => {
    mockApiFetch.mockResolvedValue(
      streamResponse([
        line(ev({ id: 'deep', timestamp: new Date(T0).toISOString() })),
        end({ truncated: true }),
      ]),
    )
    const res = await fetchRetainedWindow(T0, T0 + 60_000, undefined, 10_000, ['team-a', 'team-b'])
    const url = String(mockApiFetch.mock.calls[0][0])
    expect(url).toContain(`from=${T0}`)
    expect(url).toContain(`to=${T0 + 60_000}`)
    expect(url).toContain('limit=10000')
    expect(url).toContain('namespaces=team-a%2Cteam-b')
    expect(res.events.map((e) => e.id)).toEqual(['deep'])
    // The window response's own truncated flag is what the hook surfaces while
    // the window serves — a row-capped frozen window must not read as complete.
    expect(res.truncated).toBe(true)
  })

  it('a non-400 delta failure propagates and keeps the cursor for the next poll', async () => {
    const flags = new Set<string>()
    const cached = ring([ev({ id: 'e1' })])
    mockApiFetch.mockResolvedValueOnce({
      ok: false,
      json: () => Promise.resolve({ error: 'boom' }),
      status: 503,
    } as unknown as Response)
    await expect(
      runRetainedRingFetch({ ringKey: 'k', cached, forceResync: flags, capMs: CAP, now: NOW }),
    ).rejects.toBeInstanceOf(ApiError)
    // cursor is intact on the cached ring — nothing was committed
  })
})
