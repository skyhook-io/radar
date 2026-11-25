import { useQuery } from '@tanstack/react-query'
import type { LonghornWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

// /api/longhorn/workspace
//
// Every Longhorn Volume the caller can read, with per-kind coverage. The
// server ignores the namespace picker, so nothing view-scoped is sent. The API
// base is in the key because Radar Hub points one query client at many
// clusters.
export function useLonghornWorkspace(options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('longhornWorkspace')
  const base = getApiBase()
  return useQuery<LonghornWorkspaceResponse>({
    queryKey: ['longhorn', 'workspace', base, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<LonghornWorkspaceResponse>('/longhorn/workspace', signal)),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    // Keep the previous answer while refetching, but never another cluster's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === base ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}
