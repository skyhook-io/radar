import { useQuery } from '@tanstack/react-query'
import { debugNamespaceLog, fetchJSON, isStillLoadingError } from '../api/client'
import { useConnection } from '../context/ConnectionContext'

export interface ResourceCountsResponse {
  counts: Record<string, number>
  forbidden?: string[]
  reasons?: Record<string, string>
  unavailable?: string[]
}

// Lightweight per-kind counts for the Resources sidebar badges. Shared by the
// Resources view and any surface that renders the same sidebar standalone, so
// both read one cache entry.
export function useResourceCounts(namespaces: string[]) {
  const { connection } = useConnection()
  const namespacesParam = namespaces.join(',')
  return useQuery({
    queryKey: ['resource-counts', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      const startedAt = performance.now()
      debugNamespaceLog('resources:counts-fetch-start', { namespaces, params: params.toString() })
      try {
        return await fetchJSON<ResourceCountsResponse>(`/resource-counts?${params}`)
      } finally {
        debugNamespaceLog('resources:counts-fetch-end', {
          namespaces,
          params: params.toString(),
          durationMs: Math.round(performance.now() - startedAt),
        })
      }
    },
    staleTime: 10000,
    // SSE invalidation isn't running while connecting, and mid-sync counts
    // are what unlatch guarded kinds as their informers finish — poll fast
    // during the shell, settle to the safety net once connected.
    refetchInterval: connection.state === 'connecting' ? 3000 : 60000,
    // During the first seconds of the progressive shell the endpoint 503s
    // (cluster_connecting) until the mid-sync cache handle exists; keep the
    // query pending rather than parking it in error state, which would
    // unlatch the large-list guard at the connected flip.
    retry: (failureCount: number, error: Error) =>
      isStillLoadingError(error) ? failureCount < 15 : failureCount < 3,
    retryDelay: (failureCount: number, error: Error) =>
      isStillLoadingError(error) ? 2000 : Math.min(1000 * 2 ** failureCount, 30000),
  })
}
