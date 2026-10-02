import { describe, expect, it } from 'vitest'
import { applyClientFilters } from '../../api/timelineSource'
import type { TimelineEvent } from '../../types'
import { listIncludesManaged } from './TimelineList'

function ev(over: Partial<TimelineEvent> & { id: string }): TimelineEvent {
  return {
    timestamp: '2024-01-01T00:00:00.000Z',
    source: 'informer',
    kind: 'Deployment',
    namespace: 'ns-a',
    name: over.id,
    eventType: 'update',
    ...over,
  }
}

// Mirrors the host: the source filter runs first, then the UI keeps the selected kinds.
function listRows(events: TimelineEvent[], kinds: string[]): string[] {
  return applyClientFilters(events, { includeManaged: listIncludesManaged(false, kinds) })
    .filter((e) => kinds.length === 0 || kinds.includes(e.kind))
    .map((e) => e.id)
}

describe('listIncludesManaged', () => {
  it('includes managed rows when app-scoped or when any kind is selected', () => {
    expect(listIncludesManaged(true, [])).toBe(true)
    expect(listIncludesManaged(false, ['Pod'])).toBe(true)
    expect(listIncludesManaged(false, ['Job'])).toBe(true)
  })

  it('keeps hiding managed rows for the unfiltered list', () => {
    expect(listIncludesManaged(false, [])).toBe(false)
  })
})

describe('timeline list rows', () => {
  const events = [
    ev({ id: 'dep', kind: 'Deployment' }),
    ev({ id: 'pod', kind: 'Pod' }),
    ev({ id: 'job', kind: 'Job' }),
    ev({ id: 'cron-job', kind: 'Job', owner: { kind: 'CronJob', name: 'nightly' } }),
  ]

  it('lists pod rows for a Pod selection', () => {
    expect(listRows(events, ['Pod'])).toEqual(['pod'])
  })

  it('lists owned rows of an unmanaged kind for that kind', () => {
    expect(listRows(events, ['Job']).sort()).toEqual(['cron-job', 'job'])
  })

  it('hides pod and owned rows when no kind is selected', () => {
    expect(listRows(events, []).sort()).toEqual(['dep', 'job'])
  })
})
