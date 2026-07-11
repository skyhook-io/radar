import { describe, expect, it } from 'vitest'
import type { TimelineEvent } from '../../types'
import {
  matchesActivityFilter,
  matchesTimelineSearch,
  computeActivityStats,
  mergeKindOptions,
  describeActiveFilters,
  TIMELINE_RESOURCE_KINDS,
} from './timeline-filters'

function ev(partial: Partial<TimelineEvent>): TimelineEvent {
  return {
    id: 'id',
    timestamp: '2026-01-01T00:00:00Z',
    source: 'informer',
    kind: 'Pod',
    namespace: 'default',
    name: 'thing',
    eventType: 'update',
    ...partial,
  }
}

// Fixtures spanning the semantics the list originally encoded.
const changeAdd = ev({ source: 'informer', eventType: 'add' })
const changeUpdate = ev({ source: 'informer', eventType: 'update' })
const changeDelete = ev({ source: 'informer', eventType: 'delete' })
const changeUnhealthy = ev({ source: 'informer', eventType: 'update', healthState: 'unhealthy' })
const changeDegraded = ev({ source: 'historical', eventType: 'update', healthState: 'degraded' })
const changeHealthy = ev({ source: 'informer', eventType: 'update', healthState: 'healthy' })
const historicalAdd = ev({ source: 'historical', eventType: 'add' })
const k8sNormal = ev({ source: 'k8s_event', eventType: 'Normal', reason: 'Scheduled' })
const k8sWarning = ev({ source: 'k8s_event', eventType: 'Warning', reason: 'BackOff' })

describe('matchesActivityFilter (matrix mirrors the list view)', () => {
  it('empty selection passes everything', () => {
    for (const e of [changeAdd, k8sNormal, k8sWarning, changeUnhealthy]) {
      expect(matchesActivityFilter(e, [])).toBe(true)
    }
  })

  it("'changes' matches informer + historical, not K8s events", () => {
    expect(matchesActivityFilter(changeAdd, ['changes'])).toBe(true)
    expect(matchesActivityFilter(historicalAdd, ['changes'])).toBe(true)
    expect(matchesActivityFilter(changeUnhealthy, ['changes'])).toBe(true)
    expect(matchesActivityFilter(k8sNormal, ['changes'])).toBe(false)
    expect(matchesActivityFilter(k8sWarning, ['changes'])).toBe(false)
  })

  it("'k8s_events' matches only K8s Event objects (Normal + Warning)", () => {
    expect(matchesActivityFilter(k8sNormal, ['k8s_events'])).toBe(true)
    expect(matchesActivityFilter(k8sWarning, ['k8s_events'])).toBe(true)
    expect(matchesActivityFilter(changeAdd, ['k8s_events'])).toBe(false)
    expect(matchesActivityFilter(historicalAdd, ['k8s_events'])).toBe(false)
  })

  it("'warnings' matches only Warning-typed events", () => {
    expect(matchesActivityFilter(k8sWarning, ['warnings'])).toBe(true)
    expect(matchesActivityFilter(k8sNormal, ['warnings'])).toBe(false)
    expect(matchesActivityFilter(changeAdd, ['warnings'])).toBe(false)
    // A change is never eventType 'Warning', so it never counts as a warning.
    expect(matchesActivityFilter(changeUnhealthy, ['warnings'])).toBe(false)
  })

  it("'unhealthy' matches only changes with unhealthy/degraded health (no K8s events)", () => {
    expect(matchesActivityFilter(changeUnhealthy, ['unhealthy'])).toBe(true)
    expect(matchesActivityFilter(changeDegraded, ['unhealthy'])).toBe(true)
    expect(matchesActivityFilter(changeHealthy, ['unhealthy'])).toBe(false)
    expect(matchesActivityFilter(changeAdd, ['unhealthy'])).toBe(false)
    // A K8s warning is unhealthy-looking but is NOT a change → excluded.
    expect(matchesActivityFilter(k8sWarning, ['unhealthy'])).toBe(false)
  })
})

describe('matchesActivityFilter union semantics', () => {
  it('empty array = match everything (no chip selected)', () => {
    for (const e of [changeAdd, historicalAdd, k8sNormal, k8sWarning, changeUnhealthy, changeDegraded]) {
      expect(matchesActivityFilter(e, [])).toBe(true)
    }
  })

  it('single key behaves like the old single-value filter', () => {
    expect(matchesActivityFilter(changeAdd, ['changes'])).toBe(true)
    expect(matchesActivityFilter(k8sWarning, ['changes'])).toBe(false)
  })

  it('multiple keys match the UNION (event matches if it satisfies ANY key)', () => {
    // 'changes' + 'warnings': a change and a K8s warning both pass; a plain
    // Normal K8s event (neither a change nor a warning) does not.
    const sel = ['changes', 'warnings'] as const
    expect(matchesActivityFilter(changeAdd, sel)).toBe(true)
    expect(matchesActivityFilter(k8sWarning, sel)).toBe(true)
    expect(matchesActivityFilter(k8sNormal, sel)).toBe(false)
  })

  it('an event counted under two selected keys still matches once', () => {
    // k8s_events + warnings both cover a Warning event.
    expect(matchesActivityFilter(k8sWarning, ['k8s_events', 'warnings'])).toBe(true)
    // A Normal event matches k8s_events but not warnings — union still passes it.
    expect(matchesActivityFilter(k8sNormal, ['k8s_events', 'warnings'])).toBe(true)
  })
})

describe('matchesTimelineSearch', () => {
  const e = ev({
    name: 'checkout-api',
    kind: 'Deployment',
    namespace: 'payments',
    reason: 'ScalingReplicaSet',
    message: 'Scaled up replica set',
    diff: { summary: 'image bumped to v2', fields: [] },
  })

  it('empty term matches everything', () => {
    expect(matchesTimelineSearch(e, '')).toBe(true)
  })
  it('matches name / kind / namespace / reason / message / diff summary, case-insensitive', () => {
    expect(matchesTimelineSearch(e, 'CHECKOUT')).toBe(true)
    expect(matchesTimelineSearch(e, 'deployment')).toBe(true)
    expect(matchesTimelineSearch(e, 'payments')).toBe(true)
    expect(matchesTimelineSearch(e, 'scalingreplica')).toBe(true)
    expect(matchesTimelineSearch(e, 'replica set')).toBe(true)
    expect(matchesTimelineSearch(e, 'v2')).toBe(true)
  })
  it('non-match returns false', () => {
    expect(matchesTimelineSearch(e, 'zzz-nope')).toBe(false)
  })
})

describe('computeActivityStats', () => {
  it('counts changes, warnings, unhealthy, deleted from the full array', () => {
    const stats = computeActivityStats([
      changeAdd, changeUpdate, changeDelete, changeUnhealthy, changeDegraded,
      k8sNormal, k8sWarning,
    ])
    expect(stats.total).toBe(7)
    // changes = informer/historical: add, update, delete, unhealthy, degraded
    expect(stats.changes).toBe(5)
    expect(stats.warnings).toBe(1)
    expect(stats.unhealthy).toBe(2)
    expect(stats.deleted).toBe(1)
  })
  it('handles empty/undefined', () => {
    expect(computeActivityStats([])).toEqual({ total: 0, changes: 0, k8sEvents: 0, warnings: 0, unhealthy: 0, deleted: 0 })
    expect(computeActivityStats(undefined)).toEqual({ total: 0, changes: 0, k8sEvents: 0, warnings: 0, unhealthy: 0, deleted: 0 })
  })
})

describe('describeActiveFilters', () => {
  const none = { search: '', activityFilter: [] as const, kindFilter: [] as const, showDeleted: true }

  it('returns empty string when nothing is filtering', () => {
    expect(describeActiveFilters(none)).toBe('')
    // Blank/whitespace search does not count as active.
    expect(describeActiveFilters({ ...none, search: '   ' })).toBe('')
  })

  it('describes a single active filter', () => {
    expect(describeActiveFilters({ ...none, search: 'dfasdf' })).toBe('search "dfasdf"')
    expect(describeActiveFilters({ ...none, activityFilter: ['changes'] })).toBe('1 activity filter')
    expect(describeActiveFilters({ ...none, kindFilter: ['Pod'] })).toBe('1 kind')
    expect(describeActiveFilters({ ...none, showDeleted: false })).toBe('deleted hidden')
  })

  it('combines active filters in toolbar order with pluralization', () => {
    expect(
      describeActiveFilters({
        search: 'dfasdf',
        // Canonical: source=changes + Problems toggle → two activity picks.
        activityFilter: ['unhealthy'],
        kindFilter: ['Pod', 'Service', 'ConfigMap'],
        showDeleted: false,
      }),
    ).toBe('search "dfasdf" · 2 activity filters · 3 kinds · deleted hidden')
  })

  it('counts the two-axis Problems toggle as ONE activity filter, not two keys', () => {
    // source=all + Problems expands to keys [warnings, unhealthy] but is one pick;
    // counting raw keys read "2 activity filters" for a single toggle.
    expect(describeActiveFilters({ ...none, activityFilter: ['warnings', 'unhealthy'] })).toBe('1 activity filter')
  })
})

describe('mergeKindOptions', () => {
  it('keeps the seed order and appends discovered CRDs alphabetically', () => {
    const opts = mergeKindOptions(['Zebra', 'Alpha', 'Pod', 'Deployment'])
    expect(opts.slice(0, TIMELINE_RESOURCE_KINDS.length)).toEqual(TIMELINE_RESOURCE_KINDS)
    expect(opts.slice(TIMELINE_RESOURCE_KINDS.length)).toEqual(['Alpha', 'Zebra'])
  })
  it('dedups and drops falsy kinds', () => {
    const opts = mergeKindOptions(['', 'Widget', 'Widget'])
    expect(opts.filter((k) => k === 'Widget')).toHaveLength(1)
    expect(opts).not.toContain('')
  })
})
