import { describe, it, expect } from 'vitest'
import { matchesStatusRanges, bucketsFromCounts, bucketsFromStatus, isRateBasedSource, keepAvailable, effectiveThreshold, volumeUnit, isExternalKind, isPolicyDropReason } from './trafficFilters'

describe('matchesStatusRanges', () => {
  it('does not filter when nothing is selected', () => {
    expect(matchesStatusRanges(new Set(), [], false)).toBe(true)
  })

  it('matches an event-based flow on its own status bucket', () => {
    const ranges = new Set(['5xx'])
    expect(matchesStatusRanges(ranges, bucketsFromStatus(503), false)).toBe(true)
    expect(matchesStatusRanges(ranges, bucketsFromStatus(200), false)).toBe(false)
  })

  it('matches an aggregate on the buckets it actually reports', () => {
    const counts = { '2xx': 40, '5xx': 3 }
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts(counts), false)).toBe(true)
    expect(matchesStatusRanges(new Set(['4xx']), bucketsFromCounts(counts), false)).toBe(false)
  })

  it('ignores a bucket present but zero', () => {
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts({ '5xx': 0 }), false)).toBe(false)
  })

  // The load-bearing case. A rate-based source measures a 5xx rate rather than
  // observing individual responses, so it has no status code and no bucket. The
  // filter used to require one, which hid exactly the failing traffic the user
  // had asked to see.
  it('finds a failing edge that reports an error signal but no status', () => {
    expect(matchesStatusRanges(new Set(['5xx']), [], true)).toBe(true)
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromStatus(undefined), true)).toBe(true)
    expect(matchesStatusRanges(new Set(['5xx']), bucketsFromCounts(undefined), true)).toBe(true)
  })

  it('does not let an error signal satisfy a non-5xx selection', () => {
    expect(matchesStatusRanges(new Set(['2xx']), [], true)).toBe(false)
    expect(matchesStatusRanges(new Set(['4xx']), [], true)).toBe(false)
  })

  it('still matches a selected bucket when there are no errors', () => {
    expect(matchesStatusRanges(new Set(['2xx', '5xx']), bucketsFromStatus(204), false)).toBe(true)
  })
})

describe('isRateBasedSource', () => {
  it('knows which sources report rates rather than counts', () => {
    expect(isRateBasedSource('beyla')).toBe(true)
    expect(isRateBasedSource('istio')).toBe(true)
    expect(isRateBasedSource('hubble')).toBe(false)
    expect(isRateBasedSource('caretta')).toBe(false)
    expect(isRateBasedSource(undefined)).toBe(false)
    expect(isRateBasedSource('')).toBe(false)
  })
})

describe('keepAvailable', () => {
  it('drops a choice whose control is no longer offered', () => {
    // The button is gone, so the selection must stop filtering — otherwise the map
    // goes blank with nothing left to clear.
    expect(keepAvailable(new Set(['5xx']), [])).toEqual(new Set())
    expect(keepAvailable(new Set(['2xx', '5xx']), ['5xx'])).toEqual(new Set(['5xx']))
  })

  it('leaves an empty selection alone', () => {
    expect(keepAvailable(new Set(), ['5xx'])).toEqual(new Set())
  })

  it('keeps everything still on offer', () => {
    expect(keepAvailable(new Set(['GET', 'POST']), ['GET', 'POST', 'PUT'])).toEqual(new Set(['GET', 'POST']))
  })
})

describe('effectiveThreshold', () => {
  it('keeps a threshold chosen under the unit still in use', () => {
    expect(effectiveThreshold(100, 'connections', 'connections')).toBe(100)
    expect(effectiveThreshold(10, 'rate', 'rate')).toBe(10)
    expect(effectiveThreshold(0, 'rate', 'rate')).toBe(0)
  })

  it('drops one chosen under the other unit', () => {
    // 10000 connections against a rate source would hide every edge.
    expect(effectiveThreshold(10000, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(1, 'rate', 'connections')).toBe(0)
  })

  // The load-bearing case: these numbers are steps in BOTH scales, so membership in
  // the active list cannot tell them apart. "100+ connections" is a mild filter;
  // reinterpreted as "100+ req/s" it empties a map whose busiest edge runs at 6/s,
  // while the dropdown still shows a deliberate-looking selection.
  it('drops a value that exists in both scales but was chosen under the other', () => {
    expect(effectiveThreshold(100, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(1000, 'connections', 'rate')).toBe(0)
    expect(effectiveThreshold(100, 'rate', 'connections')).toBe(0)
  })
})

describe('volumeUnit', () => {
  it('names the quantity each kind of source measures', () => {
    expect(volumeUnit(true)).toBe('rate')
    expect(volumeUnit(false)).toBe('connections')
    expect(volumeUnit(undefined)).toBe('connections')
  })
})

describe('isExternalKind', () => {
  it('places the world, nodes and unidentified endpoints outside the workloads', () => {
    expect(isExternalKind('External')).toBe(true)
    expect(isExternalKind('Host')).toBe(true)
    expect(isExternalKind('Unknown')).toBe(true)
  })
  it('keeps pods and services inside', () => {
    expect(isExternalKind('Pod')).toBe(false)
    expect(isExternalKind('Service')).toBe(false)
  })
})

describe('isPolicyDropReason', () => {
  it('recognises both of Hubble\'s policy drop codes', () => {
    expect(isPolicyDropReason('POLICY_DENIED', 0)).toBe(true)
    expect(isPolicyDropReason('POLICY_DENY', 0)).toBe(true)
  })
  it('takes the plugin naming a policy as evidence even without a code', () => {
    expect(isPolicyDropReason(undefined, 1)).toBe(true)
  })
  it('does not assume policy for other or missing reasons', () => {
    expect(isPolicyDropReason('STALE_OR_UNROUTABLE_IP', 0)).toBe(false)
    expect(isPolicyDropReason(undefined, 0)).toBe(false)
    expect(isPolicyDropReason('', 0)).toBe(false)
  })
})
