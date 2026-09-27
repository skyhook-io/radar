import { describe, expect, it } from 'vitest'
import type { WorkloadHistoryPage } from '../../api/client'
import type { TimelineEvent } from '../../types'
import { addOlderPage, foldRefreshedPage } from './historyPaging'

// Rows newest first, each id its own seq.
const ev = (id: string, seq: number) => ({ id, seq }) as TimelineEvent
const rows = (from: number, to: number) => Array.from({ length: from - to + 1 }, (_, i) => ev(String(from - i), from - i))
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

  it('a repeated K8s Event must not hide a gap after Load older', () => {
    // Loaded after Load older: events 120..1. "backoff" is a K8s Event already held, at seq 50.
    const loaded: WorkloadHistoryPage = { events: [...rows(120, 51), ev('backoff', 50), ...rows(49, 1)], truncated: false }
    // 200 arrive (121..320) and the page holds 20; "backoff" fired again: same id, seq 320.
    const refreshed: WorkloadHistoryPage = { events: [ev('backoff', 320), ...rows(319, 301)], truncated: true, nextBeforeSeq: 301 }
    expect(foldRefreshedPage(loaded, refreshed).kind).toBe('restart')
  })

  it("keeps a repeated K8s Event's newer copy", () => {
    const loaded: WorkloadHistoryPage = { events: [...rows(30, 21), ev('backoff', 20), ...rows(19, 1)], truncated: false }
    const step = foldRefreshedPage(loaded, { events: [ev('backoff', 31), ...rows(30, 22)], truncated: true, nextBeforeSeq: 22 })
    if (step.kind !== 'set') throw new Error(step.kind)
    expect(step.history.events.filter((e) => e.id === 'backoff').map((e) => e.seq)).toEqual([31])
  })
})
