import type { WorkloadHistoryPage } from '../../api/client'

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
