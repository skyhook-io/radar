import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { VeleroScheduleActivityResponse, VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { apiFetch, fetchJSON, useRadarFeature } from './client'
import { readErrorBody } from './httpErrors'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

// /api/velero/workspace
//
// Every Velero installation the caller can read, with per-kind coverage. The
// server ignores the namespace picker, so nothing view-scoped is sent. The API
// base is in the key because Radar Hub points one query client at many
// clusters.
export function useVeleroWorkspace(options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('veleroWorkspace')
  const base = getApiBase()
  return useQuery<VeleroWorkspaceResponse>({
    queryKey: ['velero', 'workspace', base, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<VeleroWorkspaceResponse>('/velero/workspace', signal)),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    // Keep the previous answer while refetching, but never another cluster's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === base ? prev : undefined),
    retry: shouldRetryRadarQuery,
  })
}

// /api/velero/schedules/{namespace}/{name}/activity
//
// One Schedule's reading and its recent runs, for the Schedule drawer.
export function useVeleroScheduleActivity(namespace: string, name: string, options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('veleroWorkspace')
  const base = getApiBase()
  return useQuery<VeleroScheduleActivityResponse>({
    queryKey: ['velero', 'schedule-activity', base, namespace, name, ...gatedKey],
    queryFn: ({ signal }) =>
      guard(() =>
        fetchJSON<VeleroScheduleActivityResponse>(
          `/velero/schedules/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/activity`,
          signal,
        ),
      ),
    enabled: (options?.enabled ?? true) && !!namespace && !!name,
    staleTime: 10_000,
    refetchInterval: 30_000,
    // Keep the previous answer only for the same schedule on the same cluster:
    // another schedule's reading must never stand in for this one while it loads.
    placeholderData: (prev, prevQuery) =>
      prev && prevQuery?.queryKey[2] === base && prev.reading.namespace === namespace && prev.reading.name === name ? prev : undefined,
    retry: shouldRetryRadarQuery,
  })
}

// POST /api/velero/schedules/{namespace}/{name}/backup
//
// Creates a Backup from the schedule's template, bound to the version of the
// schedule the user reviewed: a schedule edited since is refused (409).
export function useVeleroBackupNow() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ namespace, name, resourceVersion }: { namespace: string; name: string; resourceVersion: string }) => {
      const response = await apiFetch(
        `${getApiBase()}/velero/schedules/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backup`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ resourceVersion }) },
      )
      if (!response.ok) {
        const error = await readErrorBody(response)
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return (await response.json()) as { namespace: string; name: string }
    },
    meta: { errorMessage: 'Could not start the backup' },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['velero'] })
    },
  })
}
