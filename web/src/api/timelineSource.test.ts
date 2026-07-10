import { describe, expect, it, vi, beforeEach } from 'vitest'

// The retained NDJSON parser goes through apiFetch; mock the client module so a
// test can hand fetchRetainedWindow an arbitrary streamed body. ApiError is only
// hit on the non-ok branch (not exercised here) but must exist for module load.
vi.mock('./client', () => ({
  apiFetch: vi.fn(),
  useChanges: vi.fn(),
  ApiError: class ApiError extends Error {
    status: number
    constructor(message: string, status: number) {
      super(message)
      this.status = status
    }
  },
}))

import { apiFetch } from './client'
import {
  fetchRetainedWindow,
  applyClientFilters,
  localOverviewFromEvents,
  LIVE_WINDOW_MS,
} from './timelineSource'
import { BASE_QUANTIZE_STEP_MS } from '@skyhook-io/k8s-ui'
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

describe('cross-package live-window invariant', () => {
  it('quantization step stays under the live poll window so no fetch hole opens', () => {
    expect(BASE_QUANTIZE_STEP_MS).toBeLessThan(LIVE_WINDOW_MS)
  })
})
