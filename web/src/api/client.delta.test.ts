import { describe, expect, it } from 'vitest'
import { deltaFetchCursor, maxEventSeq, mergeDeltaEvents, type ChangesDeltaMeta } from './client'
import type { TimelineEvent } from '@skyhook-io/k8s-ui'

const mk = (id: string, seq: number, tsOffsetMs: number): TimelineEvent => ({
  id,
  seq,
  timestamp: new Date(1_700_000_000_000 + tsOffsetMs).toISOString(),
  source: 'informer',
  kind: 'Pod',
  namespace: 'default',
  name: id,
  eventType: 'update',
})

describe('maxEventSeq (delta cursor)', () => {
  it('returns the highest arrival number', () => {
    expect(maxEventSeq([mk('a', 3, 0), mk('b', 7, 1000), mk('c', 5, 2000)])).toBe(7)
  })

  it('returns 0 for an empty page or seq-less events', () => {
    expect(maxEventSeq([])).toBe(0)
    expect(maxEventSeq([{ ...mk('a', 0, 0), seq: undefined }])).toBe(0)
  })
})

describe('mergeDeltaEvents (delta page into cached page)', () => {
  it('returns the cached reference untouched for an empty delta', () => {
    const prev = [mk('a', 1, 0)]
    expect(mergeDeltaEvents(prev, [], 100)).toBe(prev)
  })

  it('adds new arrivals in newest-first order', () => {
    const prev = [mk('b', 2, 1000), mk('a', 1, 0)]
    const merged = mergeDeltaEvents(prev, [mk('c', 3, 2000)], 100)
    expect(merged.map((e) => e.id)).toEqual(['c', 'b', 'a'])
  })

  it('replaces a cached row when the same id re-arrives (K8s Event count bump)', () => {
    const prev = [{ ...mk('bump', 1, 0), count: 1 }, mk('a', 2, 500)]
    const merged = mergeDeltaEvents(prev, [{ ...mk('bump', 3, 1000), count: 5 }], 100)
    expect(merged).toHaveLength(2)
    expect(merged[0].id).toBe('bump')
    expect(merged[0].count).toBe(5)
  })

  it('orders a late arrival by its timestamp, not its arrival number', () => {
    const prev = [mk('b', 2, 2000), mk('a', 1, 1000)]
    const merged = mergeDeltaEvents(prev, [mk('late', 3, 0)], 100)
    expect(merged.map((e) => e.id)).toEqual(['b', 'a', 'late'])
  })

  it('caps the merged page by dropping the oldest', () => {
    const prev = [mk('b', 2, 2000), mk('a', 1, 1000)]
    const merged = mergeDeltaEvents(prev, [mk('c', 3, 3000)], 2)
    expect(merged.map((e) => e.id)).toEqual(['c', 'b'])
  })
})

describe('deltaFetchCursor (cursor selection incl. high-water)', () => {
  const NOW = 1_700_000_000_000
  const meta = (over: Partial<ChangesDeltaMeta> = {}): ChangesDeltaMeta => ({
    epoch: 'e1',
    lastFullMs: NOW - 10_000,
    highWaterSeq: 0,
    ...over,
  })

  it('requires an epoch-stamped prior load and a cached page', () => {
    expect(deltaFetchCursor(undefined, [mk('a', 1, 0)], NOW)).toBe(0)
    expect(deltaFetchCursor(meta({ epoch: '' }), [mk('a', 1, 0)], NOW)).toBe(0)
    expect(deltaFetchCursor(meta(), undefined, NOW)).toBe(0)
  })

  it('forces a full resync once the anti-entropy interval elapses', () => {
    expect(deltaFetchCursor(meta({ lastFullMs: NOW - 6 * 60_000 }), [mk('a', 1, 0)], NOW)).toBe(0)
  })

  it('uses the cached max seq in the ordinary case', () => {
    expect(deltaFetchCursor(meta(), [mk('a', 7, 0), mk('b', 3, 1000)], NOW)).toBe(7)
  })

  it('does not regress when a capped-out delta event held the highest seq', () => {
    // The seq-9 event was merged then dropped by the cap (oldest timestamp);
    // cached rows top out at 7. The cursor must hold at the high water or the
    // same delta gets re-downloaded on every refetch.
    expect(deltaFetchCursor(meta({ highWaterSeq: 9 }), [mk('a', 7, 0)], NOW)).toBe(9)
  })
})
