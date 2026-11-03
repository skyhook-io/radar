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
interface ListCoverage { status: string; scope?: { type?: string; namespaces?: { names?: string[] } }; gaps?: { code: string }[] }

/** The list says it read everything it was asked for, in a scope this client understands. */
function coversRequest(coverage: ListCoverage | undefined, namespace: string | undefined): boolean {
  if (coverage?.status !== 'complete') return false
  const scope = coverage.scope
  if (scope?.type === 'cluster' || scope?.type === 'all_namespaces') return true
  return scope?.type === 'namespaces' && !!namespace && !!scope.namespaces?.names?.includes(namespace)
}

export function relatedReadInList(list: { data?: any[]; coverage?: ListCoverage; error: unknown; readBeforeMount?: boolean; namespace?: string }, name: string): RelatedRead {
  if (list.error) return relatedReadFrom({ error: list.error })
  if (!list.data) return { state: 'loading' }
  const object = list.data.find(item => item?.metadata?.name === name)
  if (object) return { state: 'found', object }
  // A list cached before this view mounted can't prove absence now; its re-read is in flight.
  if (list.readBeforeMount) return { state: 'loading' }
  if (coversRequest(list.coverage, list.namespace)) return { state: 'not_found' }
  if (list.coverage?.status === 'partial') return { state: 'unavailable', code: list.coverage.gaps?.[0]?.code, message: 'Radar’s list did not cover every object' }
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
  const [mountedAt] = useState(Date.now)
  const list = useResources<any>(kind, namespace, group, { enabled, cacheScope: getApiBase(), refetchInterval: RELATED_LIST_RECHECK_MS, refetchOnMount: 'always', staleTime: 0 })
  const { data, coverage, error, dataUpdatedAt, refetch } = list
  // The object on screen updates live. When its relevant state changes (the
  // caller's key: the names it points at and the conditions that decide what
  // is shown, not every status write), a read from before can no longer prove
  // absence, so re-read it.
  const [subjectChangedAt, setSubjectChangedAt] = useState(mountedAt)
  const [seenState, setSeenState] = useState(subjectState)
  if (subjectState !== seenState) {
    setSeenState(subjectState)
    setSubjectChangedAt(Date.now())
  }
  useEffect(() => {
    if (enabled && subjectChangedAt > mountedAt) void refetch()
  }, [enabled, subjectChangedAt, mountedAt, refetch])
  const readBeforeMount = dataUpdatedAt < Math.max(mountedAt, subjectChangedAt)
  // An older Radar answers a list denial with an uncoded 403; a GET still says what the viewer may open.
  const listDenied = listRefused(error)
  const gets = useRelatedReads(names.map(name => ({ kind, group, namespace, name })), enabled && listDenied, subjectChangedAt)
  const all: RelatedRead<any[]> = error ? relatedReadFrom({ error }) : data && !readBeforeMount ? { state: 'found', object: data } : { state: 'loading' }
  const byName = Object.fromEntries(names.map((name, index) => [name, listDenied ? gets[index] : relatedReadInList({ data, coverage, error, readBeforeMount, namespace }, name)]))
  const completeness = coverage?.status === 'complete' || coverage?.status === 'partial' ? coverage.status : 'unknown'
  return { byName, all, completeness }
}

/**
 * How often a GET read of a related object repeats. Such a read computes the
 * object's relationships, so it repeats less often than a list; a viewer who
 * may get but not list a kind receives no watch events for it, so it must
 * repeat at all.
 */
export const RELATED_GET_RECHECK_MS = 60_000

/**
 * Reads single objects through the resource GET. A denial is final for the
 * view: it isn't retried or polled. `freshAfter` is when the object on screen
 * last changed: a read older than that is re-run, and until it lands it can't
 * prove absence.
 */
export function useRelatedReads(refs: RelatedRef[], enabled = true, changedAt = 0): RelatedRead[] {
  const cacheScope = getApiBase()
  const [mountedAt] = useState(Date.now)
  const freshAfter = Math.max(mountedAt, changedAt)
  const bases = refs.map(ref => resourceQueryOptions<unknown>(ref.kind, ref.namespace ?? '', ref.name, ref.group, { enabled, cacheScope, refetchInterval: RELATED_GET_RECHECK_MS }))
  const results = useQueries({
    queries: bases.map(base => ({
      ...base,
      retry: (failureCount: number, error: unknown) => !denied(error) && !provedAbsent(error) && base.retry(failureCount, error),
      refetchInterval: (query: { state: { error: unknown } }) => denied(query.state.error) ? false : base.refetchInterval(query),
    })),
  })
  const queryClient = useQueryClient()
  const signature = JSON.stringify(refs.map(ref => [ref.kind, ref.namespace ?? '', ref.name, ref.group ?? null]))
  useEffect(() => {
    if (!enabled || changedAt <= mountedAt) return
    for (const [kind, namespace, name, group] of JSON.parse(signature) as [string, string, string, string | null][]) {
      void queryClient.refetchQueries({ queryKey: resourceQueryOptions(kind, namespace, name, group ?? undefined, { cacheScope }).queryKey, exact: true })
    }
  }, [enabled, changedAt, mountedAt, signature, cacheScope, queryClient])
  return results.map(result => {
    const read = relatedReadFrom(result)
    return read.state === 'not_found' && result.dataUpdatedAt < freshAfter && result.errorUpdatedAt < freshAfter ? { state: 'loading' } : read
  })
}
