import { useState } from 'react'
import { useQueries } from '@tanstack/react-query'
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
export function relatedReadInList(list: { data?: any[]; coverage?: { status: string; gaps?: { code: string }[] }; error: unknown; readBeforeMount?: boolean }, name: string): RelatedRead {
  if (list.error) return relatedReadFrom({ error: list.error })
  if (!list.data) return { state: 'loading' }
  const object = list.data.find(item => item?.metadata?.name === name)
  if (object) return { state: 'found', object }
  // A list cached before this view mounted can't prove absence now; its re-read is in flight.
  if (list.readBeforeMount) return { state: 'loading' }
  if (list.coverage?.status === 'complete') return { state: 'not_found' }
  if (list.coverage?.status === 'partial') return { state: 'unavailable', code: list.coverage.gaps?.[0]?.code, message: 'Radar’s list did not cover every object' }
  return { state: 'unconfirmed' }
}

/** How often a related list re-reads: watch events don't announce most additions, so a missing object is re-checked on this clock. */
export const RELATED_LIST_RECHECK_MS = 30_000

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
export function useRelatedObjects(kind: string, group: string | undefined, namespace: string | undefined, names: string[], enabled = true): RelatedObjects {
  const [mountedAt] = useState(Date.now)
  const { data, coverage, error, dataUpdatedAt } = useResources<any>(kind, namespace, group, { enabled, cacheScope: getApiBase(), refetchInterval: RELATED_LIST_RECHECK_MS, refetchOnMount: 'always' })
  const readBeforeMount = dataUpdatedAt < mountedAt
  // An older Radar answers a list denial with an uncoded 403; a GET still says what the viewer may open.
  const listDenied = error instanceof ApiError && (readActionFor(error.data?.error_code) === 'user_access' || (error.status === 403 && !error.data?.error_code))
  const gets = useRelatedReads(names.map(name => ({ kind, group, namespace, name })), enabled && listDenied)
  const all: RelatedRead<any[]> = error ? relatedReadFrom({ error }) : data ? { state: 'found', object: data } : { state: 'loading' }
  const byName = Object.fromEntries(names.map((name, index) => [name, listDenied ? gets[index] : relatedReadInList({ data, coverage, error, readBeforeMount }, name)]))
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

/** Reads single objects through the resource GET with useResource's retry rules. */
export function useRelatedReads(refs: RelatedRef[], enabled = true): RelatedRead[] {
  const cacheScope = getApiBase()
  const results = useQueries({
    queries: refs.map(ref => resourceQueryOptions<unknown>(ref.kind, ref.namespace ?? '', ref.name, ref.group, { enabled, cacheScope, refetchInterval: RELATED_GET_RECHECK_MS })),
  })
  return results.map(relatedReadFrom)
}
