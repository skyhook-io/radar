import { useEffect, useState } from 'react'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import type { RelatedRead } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { canonicalReadCode, readActionFor } from '@skyhook-io/k8s-ui/utils/read-actions'
import { ApiError, isStillLoadingError, resourceQueryOptions, useResources } from '../../api/client'
import { getApiBase } from '../../api/config'

interface RelatedRef {
  kind: string
  group?: string
  namespace?: string
  name: string
}

/**
 * A failed refresh outranks data kept from an earlier success: a related
 * object last seen present but now unreadable is unread, not present.
 * Absence counts only when the server proved it (object_not_found).
 */
export function relatedReadFrom(query: { data?: { resource?: unknown }; error: unknown }): RelatedRead {
  const error = query.error
  if (!error) return query.data?.resource ? { state: 'found', object: query.data.resource } : { state: 'loading' }
  if (isStillLoadingError(error)) return { state: 'loading' }
  if (error instanceof ApiError) {
    const code = canonicalReadCode(error.data?.error_code)
    if (error.status === 404 && code === 'object_not_found') return { state: 'not_found' }
    // An older Radar's uncoded 404 can't say the object is absent.
    if (error.status === 404 && code === undefined) return { state: 'unconfirmed' }
    // An uncoded failure (an older Radar) can't say whose access refused it: Radar's own or the viewer's.
    return { state: 'unavailable', code: typeof code === 'string' ? code : undefined, message: error.message }
  }
  return { state: 'unavailable', message: error instanceof Error ? error.message : String(error) }
}

/**
 * One name looked up in a list read. Absence counts only when the list
 * covered everything it was asked for; an older Radar that sends no coverage
 * can confirm presence but not absence.
 */
interface ListCoverage { status: string; scope?: { type?: string; namespaces?: { names?: string[] } }; gaps?: { code: string; namespaces?: { names?: string[] } }[] }

/** The list says it read everything it was asked for, in a scope this client understands. */
function coversRequest(coverage: ListCoverage | undefined, namespace: string | undefined): boolean {
  if (coverage?.status !== 'complete') return false
  const scope = coverage.scope
  if (scope?.type === 'cluster' || scope?.type === 'all_namespaces') return true
  return scope?.type === 'namespaces' && !!namespace && !!scope.namespaces?.names?.includes(namespace)
}

export function relatedReadInList(list: { data?: any[]; coverage?: ListCoverage; error: unknown; namespace?: string }, name: string): RelatedRead {
  if (list.error) return relatedReadFrom({ error: list.error })
  if (!list.data) return { state: 'loading' }
  const object = list.data.find(item => item?.metadata?.name === name)
  if (object) return { state: 'found', object }
  if (coversRequest(list.coverage, list.namespace)) return { state: 'not_found' }
  if (list.coverage?.status === 'partial') {
    const gaps = list.coverage.gaps ?? []
    const gap = gaps.find(candidate => !list.namespace || candidate.namespaces?.names?.includes(list.namespace)) ?? gaps[0]
    return { state: 'unavailable', code: gap?.code, message: 'Radar’s list did not cover every object' }
  }
  return { state: 'unconfirmed' }
}

/** How often a related list re-reads: watch events don't announce most additions, so a missing object is re-checked on this clock. */
export const RELATED_LIST_RECHECK_MS = 30_000

/** The viewer's own access refused the read, as the server's code says. */
const denied = (error: unknown) => error instanceof ApiError && readActionFor(error.data?.error_code) === 'user_access'
/** A refused list worth trying name by name: a coded denial, or an older Radar's uncoded 403. */
const listRefused = (error: unknown) => denied(error) || (error instanceof ApiError && error.status === 403 && !error.data?.error_code)
/** Absence the server proved: asking again won't change it before the next scheduled read. */
const provedAbsent = (error: unknown) => error instanceof ApiError && error.status === 404 && canonicalReadCode(error.data?.error_code) === 'object_not_found'

/**
 * A key for the subject state that decides what its related reads must
 * answer: the names it points at and its conditions' status, reason and
 * generation — not counts or other status writes, which would re-read the
 * lists on every admission.
 */
export function relatedReadKey(subject: any, names: string[], conditionTypes: string[]): string {
  const conditions = (subject?.status?.conditions ?? [])
    .filter((condition: any) => conditionTypes.includes(condition?.type))
    .map((condition: any) => [condition.type, condition.status, condition.reason, condition.observedGeneration])
  return JSON.stringify([[...names].sort(), conditions])
}

export interface RelatedObjects {
  /** Each requested name's read state. */
  byName: Record<string, RelatedRead>
  /** The whole list: found with its items, or why it wasn't read. */
  all: RelatedRead<any[]>
  /** Whether the list covered every object: unknown when the Radar sends no coverage. */
  completeness: 'complete' | 'partial' | 'unknown'
}

/**
 * Reads named objects of one kind as the viewer. One list read serves every
 * name — it computes no relationships, unlike a resource GET — and re-reads
 * on RELATED_LIST_RECHECK_MS. A viewer allowed to get but not list the kind
 * reads each name with a GET instead, so a list denial never hides an object
 * the viewer may open. The cache is scoped to the API base because Radar Hub
 * points one query client at several clusters, where same-named queues are
 * different objects.
 */
export function useRelatedObjects(kind: string, group: string | undefined, namespace: string | undefined, names: string[], enabled = true, subjectState?: string): RelatedObjects {
  const queryClient = useQueryClient()
  const cacheScope = getApiBase()
  const [refused, setRefused] = useState(false)
  const listKey = ['resources', kind, group, namespace, cacheScope]
  // Marked before this hook's own observer starts, so a fetch someone else began earlier counts as old.
  const [mark, setMark] = useState(() => markFor(queryClient.getQueryState(listKey)))
  const list = useResources<any>(kind, namespace, group, { enabled, cacheScope, refetchInterval: refused ? false : RELATED_LIST_RECHECK_MS, refetchOnMount: 'always', staleTime: 0 })
  const { data, coverage, refetch } = list
  // The object on screen updates live. When its relevant state changes (the
  // caller's key: the names it points at and the conditions that decide what
  // is shown, not every status write), a read from before the change can't
  // answer for after it, so mark the change and re-read.
  const [seenState, setSeenState] = useState(subjectState)
  const [changes, setChanges] = useState(0)
  if (subjectState !== seenState) {
    setSeenState(subjectState)
    setMark(markFor(queryClient.getQueryState(listKey)))
    setChanges(changes + 1)
  }
  const fresh = answered(queryClient.getQueryState(listKey), mark)
  useEffect(() => {
    if (enabled && !fresh && !list.isFetching) void refetch()
  }, [enabled, fresh, list.isFetching, refetch])
  // An error read before the mark is no answer either.
  const error = fresh ? list.error : null
  // A refused list isn't polled again: the names are read one by one instead
  // (with their own freshness marks), and it is re-read when the subject changes.
  if (listRefused(list.error) !== refused) setRefused(listRefused(list.error))
  // An older Radar answers a list denial with an uncoded 403; a GET still says what the viewer may open.
  const listDenied = listRefused(list.error)
  const gets = useRelatedReads(names.map(name => ({ kind, group, namespace, name })), enabled && listDenied, changes)
  const all: RelatedRead<any[]> = !fresh ? { state: 'loading' } : error ? relatedReadFrom({ error }) : data ? { state: 'found', object: data } : { state: 'loading' }
  // Until a read started after the mark lands, the list answers nothing, present or absent: it may lend a tone.
  const byName = Object.fromEntries(names.map((name, index) => [name, listDenied ? gets[index] : !fresh ? { state: 'loading' } as RelatedRead : relatedReadInList({ data, coverage, error, namespace }, name)]))
  const completeness = coverage?.status === 'complete' || coverage?.status === 'partial' ? coverage.status : 'unknown'
  return { byName, all, completeness }
}

interface QueryProgress { dataUpdateCount: number; errorUpdateCount: number; fetchStatus?: string; isFetching?: boolean }
interface ReadMark { settled: number; inFlight: boolean }

/** Where a query stood at a moment that later answers must postdate. */
function markFor(state: QueryProgress | undefined): ReadMark {
  return { settled: (state?.dataUpdateCount ?? 0) + (state?.errorUpdateCount ?? 0), inFlight: state?.isFetching ?? state?.fetchStatus === 'fetching' }
}

/**
 * A read started after the mark has settled: one more settled read than at
 * the mark, or two when a read was already in flight then (that one started
 * before the mark, so its answer may predate it).
 */
function answered(state: QueryProgress | undefined, mark: ReadMark): boolean {
  return (state?.dataUpdateCount ?? 0) + (state?.errorUpdateCount ?? 0) >= mark.settled + (mark.inFlight ? 2 : 1)
}

/**
 * How often a GET read of a related object repeats. Such a read computes the
 * object's relationships, so it repeats less often than a list; a viewer who
 * may get but not list a kind receives no watch events for it, so it must
 * repeat at all.
 */
export const RELATED_GET_RECHECK_MS = 60_000

/**
 * Reads single objects through the resource GET, re-reading on mount whatever
 * the host QueryClient's defaults. A coded denial is final for the view: it
 * isn't retried or polled. `changes` counts changes of the object on screen;
 * each one marks every read, and until a read started after the mark lands
 * the object reads as loading, found or not.
 */
export function useRelatedReads(refs: RelatedRef[], enabled = true, changes = 0): RelatedRead[] {
  const cacheScope = getApiBase()
  const queryClient = useQueryClient()
  const bases = refs.map(ref => resourceQueryOptions<unknown>(ref.kind, ref.namespace ?? '', ref.name, ref.group, { enabled, cacheScope, refetchInterval: RELATED_GET_RECHECK_MS }))
  const keys = bases.map(base => JSON.stringify(base.queryKey))
  // Marked before this hook's observers start, and again at each subject change.
  const [marks, setMarks] = useState<Record<string, ReadMark>>(() => Object.fromEntries(bases.map((base, index) => [keys[index], markFor(queryClient.getQueryState(base.queryKey))])))
  const [seenChanges, setSeenChanges] = useState(changes)
  const unmarked = keys.filter(key => !marks[key])
  if (changes !== seenChanges || unmarked.length > 0) {
    const remark = changes !== seenChanges
    setSeenChanges(changes)
    setMarks(Object.fromEntries(bases.map((base, index) => [keys[index], remark || !marks[keys[index]] ? markFor(queryClient.getQueryState(base.queryKey)) : marks[keys[index]]])))
  }
  const results = useQueries({
    queries: bases.map(base => ({
      ...base,
      staleTime: 0,
      refetchOnMount: 'always' as const,
      retry: (failureCount: number, error: unknown) => !denied(error) && !provedAbsent(error) && base.retry(failureCount, error),
      refetchInterval: (query: { state: { error: unknown } }) => denied(query.state.error) ? false : base.refetchInterval(query),
    })),
  })
  const stale = results.map((_, index) => !marks[keys[index]] || !answered(queryClient.getQueryState(bases[index].queryKey), marks[keys[index]]))
  const idleStale = JSON.stringify(keys.filter((_, index) => enabled && stale[index] && !results[index].isFetching))
  useEffect(() => {
    for (const key of JSON.parse(idleStale) as string[]) void queryClient.refetchQueries({ queryKey: JSON.parse(key), exact: true })
  }, [idleStale, queryClient])
  return results.map((result, index) => stale[index] ? { state: 'loading' } : relatedReadFrom(result))
}
