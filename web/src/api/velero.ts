import { useMutation, useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query'
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
    mutationFn: async ({ namespace, name, resourceVersion, backupName, submission }: { namespace: string; name: string; resourceVersion: string; backupName?: string; submission: string }) => {
      const response = await apiFetch(
        `${getApiBase()}/velero/schedules/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backup`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ resourceVersion, name: backupName, submission }) },
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

/** internal/velero Run: one Backup or Restore as its list and pages show it. */
export interface VeleroRunRow {
  namespace: string
  name: string
  uid: string
  phase?: string
  created?: string
  started?: string
  completed?: string
  expiration?: string
  schedule?: string
  storageLocation?: string
  itemsDone?: number
  itemsTotal?: number
  errors: number
  warnings: number
  reason?: string
  backupName?: string
  scheduleName?: string
  namespaceMapping?: Record<string, string>
}

/** A Restore's row: its namespace selection is always sent, empty for every namespace in the backup. */
export interface VeleroRestoreRow extends VeleroRunRow {
  includedNamespaces: string[]
  excludedNamespaces: string[]
}

export interface VeleroRunsResponse<R extends VeleroRunRow = VeleroRunRow> {
  observedAt: string
  coverage: import('@skyhook-io/k8s-ui').VeleroKindCoverage
  runs: R[]
  total: number
  truncated?: boolean
}

// GET /api/velero/runs/{backups|restores}?namespace=&schedule=&backup=
//
// Every Backup or Restore the caller can read, newest first, read through the
// same per-kind access as the Velero views; `coverage` says what it covered.
// `schedule` and `backup` narrow the answer on the server: one schedule's
// runs, or the restores made from one backup.
export function useVeleroRuns(kind: 'restores', namespace?: string, filter?: { backup?: string }): UseQueryResult<VeleroRunsResponse<VeleroRestoreRow>>
export function useVeleroRuns(kind: 'backups', namespace?: string, filter?: { schedule?: string }): UseQueryResult<VeleroRunsResponse>
export function useVeleroRuns(kind: 'backups' | 'restores', namespace?: string, filter?: { schedule?: string; backup?: string }) {
  const { guard, gatedKey } = useRadarFeature('veleroRuns')
  const base = getApiBase()
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (filter?.schedule) params.set('schedule', filter.schedule)
  if (filter?.backup) params.set('backup', filter.backup)
  const query = params.toString()
  return useQuery<VeleroRunsResponse>({
    queryKey: ['velero', 'runs', base, kind, namespace ?? '', filter?.schedule ?? '', filter?.backup ?? '', ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<VeleroRunsResponse>(`/velero/runs/${kind}${query ? `?${query}` : ''}`, signal)),
    staleTime: 10_000,
    refetchInterval: 30_000,
    placeholderData: (prev, prevQuery) =>
      prevQuery?.queryKey[2] === base && prevQuery?.queryKey[3] === kind && prevQuery?.queryKey[4] === (namespace ?? '') && prevQuery?.queryKey[5] === (filter?.schedule ?? '') && prevQuery?.queryKey[6] === (filter?.backup ?? '') ? prev : undefined,
    retry: shouldRetryRadarQuery,
  })
}

export interface VeleroDeletion {
  state: 'waiting' | 'inProgress' | 'refused' | 'failed' | 'deleted' | 'unknown'
  errors?: string[]
  request?: string
  /** When the request was made; absent from an older Radar. */
  created?: string
  serverStopped?: boolean
}

// POST /api/velero/backups/{namespace}/{name}/delete
//
// Asks Velero to delete a backup through a DeleteBackupRequest, bound to the
// backup the user reviewed (its UID). An open request is reused by the server.
export function useVeleroDeleteBackup() {
  return useMutation({
    // fetchJSON throws ApiError, so the dialog can tell a refusal (4xx) from a
    // write whose outcome is unknown.
    mutationFn: ({ namespace, name, uid }: { namespace: string; name: string; uid: string }) =>
      fetchJSON<{ request: string }>(
        `/velero/backups/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/delete`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uid }) },
      ),
  })
}

const SETTLED = ['refused', 'failed', 'deleted', 'unknown']

export function useVeleroBackupDeletion(namespace: string, name: string, uid: string, enabled = true) {
  const { guard, gatedKey } = useRadarFeature('veleroDeleteBackup')
  const base = getApiBase()
  return useQuery<VeleroDeletion>({
    queryKey: ['velero', 'deletion', base, namespace, name, uid, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<VeleroDeletion>(
      `/velero/backups/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/deletion?uid=${encodeURIComponent(uid)}`, signal)),
    enabled: enabled && !!uid && !!namespace && !!name,
    refetchInterval: (query) => {
      const status = (query.state.error as { status?: number } | null)?.status
      if (status && status >= 400 && status < 500) return false
      return query.state.data && SETTLED.includes(query.state.data.state) ? false : 2_000
    },
    retry: (count, error) => {
      const status = (error as { status?: number } | null)?.status
      return status && status >= 400 && status < 500 ? false : shouldRetryRadarQuery(count, error)
    },
  })
}
