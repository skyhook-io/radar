import { describe, expect, it } from 'vitest'
import type { WorkloadHistoryPage } from '../../api/client'
import type { TimelineEvent } from '../../types'
import { addOlderPage, foldRefreshedPage } from './historyPaging'

// Rows newest first; ids double as their sequence number.
const rows = (from: number, to: number) =>
  Array.from({ length: from - to + 1 }, (_, i) => ({ id: String(from - i) }) as TimelineEvent)
const page = (from: number, to: number, truncated: boolean): WorkloadHistoryPage => ({
  events: rows(from, to),
  truncated,
  nextBeforeSeq: truncated ? to : undefined,
})
const ids = (p: WorkloadHistoryPage) => p.events.map((e) => Number(e.id))
const contiguous = (p: WorkloadHistoryPage, from: number, to: number) =>
  expect(new Set(ids(p))).toEqual(new Set(rows(from, to).map((e) => Number(e.id))))

describe('workload history paging', () => {
  it('keeps rows the newest page slides past once older pages are loaded', () => {
    const first = page(40, 21, true)
    const step = addOlderPage(null, page(20, 1, false), first, first)
    if (step.kind !== 'set') throw new Error(step.kind)
    let loaded = step.history
    for (const refreshed of [page(50, 31, true), page(60, 41, true)]) {
      const next = foldRefreshedPage(loaded, refreshed)
      if (next.kind === 'set') loaded = next.history
      else expect(next.kind).toBe('keep')
    }
    contiguous(loaded, 60, 1)
    expect(loaded.truncated).toBe(false)
  })

  it('restarts paging when a refresh no longer reaches what is loaded', () => {
    const loaded = page(40, 1, false)
    expect(foldRefreshedPage(loaded, page(80, 61, true)).kind).toBe('restart')
    expect(foldRefreshedPage(loaded, page(45, 26, true)).kind).toBe('set')
    expect(foldRefreshedPage(loaded, page(40, 21, true)).kind).toBe('keep')
  })

  it('anchors a first older page to what refreshed while it loaded', () => {
    const base = page(40, 21, true)
    const step = addOlderPage(null, page(20, 1, false), base, page(45, 26, true))
    if (step.kind !== 'set') throw new Error(step.kind)
    contiguous(step.history, 45, 1)
  })

  it('restarts instead of landing a first older page behind a hole', () => {
    const base = page(40, 21, true)
    expect(addOlderPage(null, page(20, 1, false), base, page(90, 71, true)).kind).toBe('restart')
  })
})
