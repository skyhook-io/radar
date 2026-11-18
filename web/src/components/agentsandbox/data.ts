import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import type { ResourceCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import { isLowerBound } from '@skyhook-io/k8s-ui/utils/resource-coverage'
import type { APIResource } from '../../types'
import { ApiError, fetchResourceList, isKindSyncFailed, isStillLoadingError, isUnretryableReadError } from '../../api/client'
import { createListRefetchInterval } from '../../api/resourceLists'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'

// The kind lists (Radar's tables for Sandboxes, claims and warm pools) read
// Radar's resource list endpoint, with its coverage. The Overview, Needs
// attention and the sidebar's badges come from /api/agentsandbox/workspace.

/**
 * One kind as Radar's list endpoint answered it. `failed` keeps why, so a
 * denied or unread kind is named and never counted as empty.
 */
export type KindRead =
  | { state: 'loading' }
  | { state: 'read'; items: any[]; coverage: ResourceCoverage }
  | { state: 'failed'; message: string; denied: boolean }
  /** Discovery does not serve the kind: its CRD is not installed. */
  | { state: 'notServed' }

export interface AgentSandboxLists {
  sandboxes: KindRead
  claims: KindRead
  warmPools: KindRead
}

export type AgentSandboxListKey = keyof AgentSandboxLists

export function agentSandboxDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === AGENT_SANDBOX_GROUP || r.group === AGENT_SANDBOX_EXTENSIONS_GROUP)
}

export function readItems(read: KindRead): any[] {
  return read.state === 'read' ? read.items : []
}

/**
 * Whether Radar read the kind in full in one namespace, so an object missing
 * from it there is absent rather than unread. A Radar that predates coverage
 * says nothing about it.
 */
export function readInNamespace(read: KindRead, namespace: string): boolean {
  if (read.state === 'notServed') return true
  if (read.state !== 'read') return false
  const c = read.coverage
  if (c.status === 'unknown') return false
  if (c.status === 'partial') return !!c.read?.namespaces?.names?.includes(namespace)
  if (c.scope.type === 'namespaces' || isLowerBound(c)) return !!c.scope.namespaces?.names?.includes(namespace)
  return true
}

const KINDS = [
  ['sandboxes', SANDBOX_KIND],
  ['claims', SANDBOX_CLAIM_KIND],
  ['warmPools', SANDBOX_WARM_POOL_KIND],
] as const

function served(apiResources: APIResource[] | undefined, kind: { name: string; group: string }): boolean {
  // Discovery not answered (or failed) is not "not installed": the list endpoint is asked.
  return !apiResources || apiResources.some((r) => r.group === kind.group && r.name === kind.name)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'request failed'
}

function isDenied(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 403 || error.data?.error_code === 'user_forbidden')
}

const REFRESH_MS = 30_000

/** The three listed kinds, read with the namespace filter applied. */
export function useAgentSandboxLists({
  namespaces,
  apiResources,
  enabled = true,
}: {
  namespaces: string[]
  apiResources: APIResource[] | undefined
  enabled?: boolean
}) {
  const nsKey = [...namespaces].sort().join(',')
  const intervals = useMemo(() => KINDS.map(() => createListRefetchInterval(REFRESH_MS)), [])
  const queries = useQueries({
    queries: KINDS.map(([, kind], i) => ({
      queryKey: ['agent-sandbox', kind.name, kind.group, nsKey],
      queryFn: ({ signal }: { signal: AbortSignal }) => fetchResourceList<any>(kind.name, { namespaces: nsKey ? nsKey.split(',') : [], group: kind.group, signal }),
      enabled: enabled && served(apiResources, kind),
      staleTime: REFRESH_MS,
      refetchInterval: intervals[i],
      retry: (failureCount: number, error: unknown) => {
        if (isStillLoadingError(error)) return true
        if (isKindSyncFailed(error) || isUnretryableReadError(error) || isDenied(error)) return false
        return failureCount < 1
      },
      retryDelay: (failureCount: number, error: unknown) => (isStillLoadingError(error) ? 2000 : Math.min(1000 * 2 ** failureCount, 30000)),
    })),
  })

  const [s, c, w] = queries
  const data: AgentSandboxLists = useMemo(() => {
    const read = (i: number, q: (typeof queries)[number]): KindRead => {
      if (apiResources && !served(apiResources, KINDS[i][1])) return { state: 'notServed' }
      if (q.data) return { state: 'read', items: q.data.items ?? [], coverage: q.data.coverage }
      if (q.error) return { state: 'failed', message: errorMessage(q.error), denied: isDenied(q.error) }
      return { state: 'loading' }
    }
    return { sandboxes: read(0, s), claims: read(1, c), warmPools: read(2, w) }
    // The query results are new objects each render; their data and errors are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [s.data, s.error, c.data, c.error, w.data, w.error, apiResources])
  const refetch = () => {
    for (const q of queries) void q.refetch()
  }
  return {
    data,
    isFetching: queries.some((q) => q.isFetching),
    refetch,
    refreshQueries: queries.map((q) => ({ isRefetchError: q.isRefetchError, error: q.error, dataUpdatedAt: q.dataUpdatedAt })),
  }
}
