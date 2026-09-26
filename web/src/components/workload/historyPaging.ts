import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { WorkloadHistoryPage } from '../../api/client'
import type { TimelineEvent } from '../../types'

// Paging state for a workload's history: the newest page keeps refreshing on
// its own, and older pages load on demand below it. These rules keep the two
// contiguous. When they can't (more arrived between refreshes than one page
// holds), paging restarts from the newest page instead of leaving a hole.

export type PagingStep = { kind: 'keep' } | { kind: 'restart' } | { kind: 'set'; history: WorkloadHistoryPage }

/** Folds a refreshed newest page into the loaded older pages. */
export function foldRefreshedPage(loaded: WorkloadHistoryPage, page: WorkloadHistoryPage): PagingStep {
  const known = new Set(loaded.events.map((e) => e.id))
  const added = page.events.filter((e) => !known.has(e.id))
  if (added.length === 0) return { kind: 'keep' }
  if (page.truncated && added.length === page.events.length) return { kind: 'restart' }
  return { kind: 'set', history: { ...loaded, events: [...added, ...loaded.events] } }
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
  if (loaded) return { kind: 'set', history: { events: [...loaded.events, ...page.events], ...more } }
  const base = basePage?.events ?? []
  const baseIds = new Set(base.map((e) => e.id))
  const since = latest && latest !== basePage ? latest.events.filter((e) => !baseIds.has(e.id)) : []
  if (latest?.truncated && since.length > 0 && since.length === latest.events.length) return { kind: 'restart' }
  return { kind: 'set', history: { events: [...since, ...base, ...page.events], ...more } }
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
