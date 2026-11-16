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
    // backupName is chosen when the confirmation opens, so sending the same
    // confirmation again returns the backup it already created.
    mutationFn: async ({ namespace, name, resourceVersion, backupName }: { namespace: string; name: string; resourceVersion: string; backupName?: string }) => {
      const response = await apiFetch(
        `${getApiBase()}/velero/schedules/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backup`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ resourceVersion, name: backupName }) },
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

export interface VeleroDeletion {
  state: 'waiting' | 'inProgress' | 'refused' | 'failed' | 'deleted' | 'unknown'
  errors?: string[]
  serverStopped?: boolean
}

// POST /api/velero/backups/{namespace}/{name}/delete
//
// Asks Velero to delete a backup through a DeleteBackupRequest, bound to the
// backup the user reviewed (its UID). The submission ID is chosen when the
// confirmation opens, so sending it again returns the request it already made.
export function useVeleroDeleteBackup() {
  return useMutation({
    // fetchJSON throws ApiError, so the dialog can tell a refusal (4xx) from a
    // write whose outcome is unknown.
    mutationFn: ({ namespace, name, uid, submission }: { namespace: string; name: string; uid: string; submission: string }) =>
      fetchJSON<{ request: string; uid: string }>(
        `/velero/backups/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/delete`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uid, submission }) },
      ),
  })
}

const SETTLED: VeleroDeletion['state'][] = ['refused', 'failed', 'deleted', 'unknown']

// GET /api/velero/backups/{namespace}/{name}/deletion
//
// Where one deletion stands, polled until it settles.
export function useVeleroBackupDeletion(namespace: string, name: string, started: { request: string; uid: string } | null) {
  const base = getApiBase()
  return useQuery<VeleroDeletion>({
    queryKey: ['velero', 'deletion', base, namespace, name, started?.request, started?.uid],
    queryFn: ({ signal }) =>
      fetchJSON<VeleroDeletion>(
        `/velero/backups/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/deletion?request=${encodeURIComponent(started!.request)}&uid=${encodeURIComponent(started!.uid)}`,
        signal,
      ),
    enabled: !!started && !!namespace && !!name,
    refetchInterval: (query) => (query.state.data && SETTLED.includes(query.state.data.state) ? false : 2_000),
    retry: shouldRetryRadarQuery,
  })
}
