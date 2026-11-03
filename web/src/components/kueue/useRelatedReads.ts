import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import type { RelatedRead } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { canonicalReadCode } from '@skyhook-io/k8s-ui/utils/read-actions'
import { ApiError, isStillLoadingError, resourceQueryOptions, useResources } from '../../api/client'
import { getApiBase } from '../../api/config'

export interface RelatedRef {
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
    if (typeof code === 'string') return { state: 'unavailable', code, message: error.message }
    // Resource reads answer 403 only for the caller's own access; some typed-kind denials carry no code.
    return { state: 'unavailable', code: error.status === 403 ? 'user_forbidden' : undefined, message: error.message }
  }
  return { state: 'unavailable', message: error instanceof Error ? error.message : String(error) }
}

/**
 * One name looked up in a list read. Absence counts only when the list
 * covered everything it was asked for; an older Radar that sends no coverage
 * can confirm presence but not absence.
 */
export function relatedReadInList(list: { data?: any[]; coverage?: { status: string; gaps?: { code: string }[] }; error: unknown }, name: string): RelatedRead {
  if (list.error) return relatedReadFrom({ error: list.error })
  if (!list.data) return { state: 'loading' }
  const object = list.data.find(item => item?.metadata?.name === name)
  if (object) return { state: 'found', object }
  if (list.coverage?.status === 'complete') return { state: 'not_found' }
  return { state: 'unavailable', code: list.coverage?.gaps?.[0]?.code, message: 'Radar’s list did not cover every object' }
}

export interface RelatedList {
  /** One name's read state. */
  find: (name: string) => RelatedRead
  /** The whole list: found with its items, or why it wasn't read. */
  all: RelatedRead<any[]>
  /** The list covered every object it was asked for. */
  complete: boolean
}

/**
 * Looks names up in one list read of a kind, as the viewer. A list read
 * computes no relationships and refreshes on the list's own schedule, so a
 * detail view that names several related objects costs one read per kind.
 * The cache is scoped to the API base because Radar Hub points one query
 * client at several clusters, where same-named queues are different objects.
 */
export function useRelatedList(kind: string, group: string | undefined, namespace: string | undefined, enabled = true): RelatedList {
  const { data, coverage, error } = useResources<any>(kind, namespace, group, { enabled, cacheScope: getApiBase() })
  return useMemo(() => {
    const all: RelatedRead<any[]> = error ? relatedReadFrom({ error }) : data ? { state: 'found', object: data } : { state: 'loading' }
    return { find: (name: string) => relatedReadInList({ data, coverage, error }, name), all, complete: coverage?.status === 'complete' }
  }, [data, coverage, error])
}

/** Reads single objects through the resource GET with useResource's retry and polling rules. */
export function useRelatedReads(refs: RelatedRef[], enabled = true): RelatedRead[] {
  const cacheScope = getApiBase()
  const results = useQueries({
    queries: refs.map(ref => resourceQueryOptions<unknown>(ref.kind, ref.namespace ?? '', ref.name, ref.group, { enabled, cacheScope })),
  })
  return results.map(relatedReadFrom)
}
