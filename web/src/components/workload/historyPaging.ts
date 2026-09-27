import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { WorkloadHistoryPage } from '../../api/client'
import type { TimelineEvent } from '../../types'

// Paging state for a workload's history: the newest page keeps refreshing on
// its own, and older pages load on demand below it. These rules keep the two
// contiguous. When they can't (more arrived between refreshes than one page
// holds), paging restarts from the newest page instead of leaving a hole.
//
// Continuity is judged by seq, not by id: a K8s Event that fires again keeps
// its id but moves to a new, higher seq, so a page can share an id with what's
// loaded and still not reach it.

export type PagingStep = { kind: 'keep' } | { kind: 'restart' } | { kind: 'set'; history: WorkloadHistoryPage }

const seqOf = (e: TimelineEvent) => e.seq ?? 0

/** Whether a newest page reaches down to rows already loaded, leaving no gap. */
function reaches(page: WorkloadHistoryPage, loaded: TimelineEvent[]): boolean {
  if (!page.truncated) return true
  if (loaded.length === 0 || page.events.length === 0) return false
  // Loops, not Math.max(...rows): loaded history has no cap, and spreading it
  // into arguments overflows the stack.
  let newestLoaded = -Infinity
  for (const e of loaded) newestLoaded = Math.max(newestLoaded, seqOf(e))
  let oldestOnPage = Infinity
  for (const e of page.events) oldestOnPage = Math.min(oldestOnPage, seqOf(e))
  return oldestOnPage <= newestLoaded
}

/** Newer rows first; a row both lists hold keeps the newer copy (a repeated
 *  K8s Event's count and message move with it). */
function mergeNewer(newer: TimelineEvent[], older: TimelineEvent[]): TimelineEvent[] {
  const ids = new Set(newer.map((e) => e.id))
  return [...newer, ...older.filter((e) => !ids.has(e.id))]
}

/** Folds a refreshed newest page into the loaded older pages. */
export function foldRefreshedPage(loaded: WorkloadHistoryPage, page: WorkloadHistoryPage): PagingStep {
  const seqById = new Map(loaded.events.map((e) => [e.id, seqOf(e)]))
  if (page.events.every((e) => seqById.get(e.id) === seqOf(e))) return { kind: 'keep' }
  if (!reaches(page, loaded.events)) return { kind: 'restart' }
  return { kind: 'set', history: { ...loaded, events: mergeNewer(page.events, loaded.events) } }
}

/**
 * Adds an older page. The first one continues from the newest page it was
 * requested from (basePage), and keeps that page plus whatever refreshed
 * since (latest), unless the refresh has already moved past it.
 */
export function addOlderPage(
  loaded: WorkloadHistoryPage | null,
  page: WorkloadHistoryPage,
  basePage: WorkloadHistoryPage | undefined,
  latest: WorkloadHistoryPage | undefined,
): PagingStep {
  const more = { truncated: page.truncated, nextBeforeSeq: page.nextBeforeSeq }
  if (loaded) return { kind: 'set', history: { events: mergeNewer(loaded.events, page.events), ...more } }
  let newest = basePage?.events ?? []
  if (latest && latest !== basePage) {
    if (!reaches(latest, newest)) return { kind: 'restart' }
    newest = mergeNewer(latest.events, newest)
  }
  return { kind: 'set', history: { events: mergeNewer(newest, page.events), ...more } }
}

/**
 * A workload's history: the newest page (refreshed by its query) plus older
 * pages loaded on demand. identity names the workload; changing it restarts
 * paging, and a page requested before a restart is dropped when it arrives.
 */
export function useHistoryPaging(
  identity: string,
  newest: WorkloadHistoryPage | undefined,
  fetchOlder: (beforeSeq: number) => Promise<WorkloadHistoryPage>,
) {
  // Loaded pages carry the workload they belong to, so the render between a
  // navigation and the restart below never shows another workload's rows.
  const [olderState, setOlderState] = useState<{ identity: string; history: WorkloadHistoryPage } | null>(null)
  const older = olderState?.identity === identity ? olderState.history : null
  const setOlder = useCallback(
    (history: WorkloadHistoryPage | null) => setOlderState(history ? { identity, history } : null),
    [identity],
  )
  const [loadingOlder, setLoadingOlder] = useState(false)
  const [olderError, setOlderError] = useState<Error | null>(null)
  const generation = useRef(0)
  const latest = useRef(newest)
  latest.current = newest
  const loaded = useRef(older)
  loaded.current = older

  const restart = useCallback(() => {
    generation.current++
    // Effects later in this commit (the refresh fold below) read the ref
    // before the next render would update it.
    loaded.current = null
    setOlderState(null)
    setOlderError(null)
    setLoadingOlder(false)
  }, [])
  useEffect(restart, [identity, restart])

  useEffect(() => {
    if (!newest || !loaded.current) return
    const step = foldRefreshedPage(loaded.current, newest)
    if (step.kind === 'restart') restart()
    else if (step.kind === 'set') setOlder(step.history)
  }, [newest, restart, setOlder])

  const events = useMemo<TimelineEvent[] | undefined>(() => {
    if (!newest) return undefined
    if (!older) return newest.events
    const byId = new Map(newest.events.map((e) => [e.id, e]))
    for (const e of older.events) if (!byId.has(e.id)) byId.set(e.id, e)
    return [...byId.values()]
  }, [newest, older])

  const loadOlder = useCallback(async () => {
    const basePage = older ? undefined : newest
    const cursor = older ? older.nextBeforeSeq : basePage?.nextBeforeSeq
    if (!cursor) return
    const requested = generation.current
    setLoadingOlder(true)
    setOlderError(null)
    try {
      const page = await fetchOlder(cursor)
      if (generation.current !== requested) return
      const step = addOlderPage(loaded.current, page, basePage, latest.current)
      if (step.kind === 'restart') restart()
      else if (step.kind === 'set') setOlder(step.history)
    } catch (err) {
      if (generation.current !== requested) return
      setOlderError(err instanceof Error ? err : new Error(String(err)))
    } finally {
      if (generation.current === requested) setLoadingOlder(false)
    }
  }, [older, newest, fetchOlder, restart, setOlder])

  return {
    events,
    truncated: older ? older.truncated : Boolean(newest?.truncated),
    loadOlder,
    loadingOlder,
    olderError,
  }
}
