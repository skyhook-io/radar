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
    // backupName and submission (8–64 letters, digits or hyphens) are chosen
    // when the confirmation opens. Sending the same confirmation again, with
    // both unchanged, returns the backup it already created; without
    // backupName each request creates a new backup.
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

/** internal/velero RunRow: one Backup or Restore as its list and pages show it. */
export interface VeleroRunRow {
  namespace: string
  name: string
  uid: string
  phase?: string
  created?: string
  started?: string
  completed?: string
  expiration?: string
  /** A Backup's velero.io/schedule-name label: the schedule that submitted it. Never set on a Restore. */
  schedule?: string
  storageLocation?: string
  /** Velero's progress: items processed so far (a Restore's itemsRestored also counts skipped and failed items), of itemsTotal. Absent until Velero reports them. */
  itemsDone?: number
  itemsTotal?: number
  errors: number
  warnings: number
  /** A Backup created by Radar's Back up now from its schedule. */
  manual?: boolean
  reason?: string
  backupName?: string
  /** A Restore's spec.scheduleName: restore from that schedule's newest completed backup. Unrelated to schedule. */
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
  /** The runs that passed the schedule or backup filter, before the cap; runs holds the newest. */
  total: number
  truncated?: boolean
}

// GET /api/velero/runs/{backups|restores}?namespace=&schedule=&backup=
//
// Every Backup or Restore the caller can read, newest first, read through the
// same per-kind access as the Velero views; `coverage` says what it covered.
// `schedule` and `backup` narrow the answer on the server: one schedule's
// runs, or the restores made from one backup.
export function useVeleroRuns(kind: 'restores', namespace?: string, filter?: { backup?: string }, options?: { enabled?: boolean }): UseQueryResult<VeleroRunsResponse<VeleroRestoreRow>>
export function useVeleroRuns(kind: 'backups', namespace?: string, filter?: { schedule?: string }, options?: { enabled?: boolean }): UseQueryResult<VeleroRunsResponse>
export function useVeleroRuns(kind: 'backups' | 'restores', namespace?: string, filter?: { schedule?: string; backup?: string }, options?: { enabled?: boolean }) {
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
    enabled: options?.enabled ?? true,
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
  /** Absent when no request for the backup is retained: Velero removes a successful one with the backup, and any processed one after a day. */
  request?: string
  /** When the request was made; absent with request, or when it has no creation time. */
  created?: string
  /** A waiting request whose server Deployment has no pods (status.replicas is 0). Sent only when true, and checked only while waiting. */
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
export interface VeleroRunContentsResponse {
  groups: { kind: string; count: number; matched: number; items: { namespace: string; name: string; action?: string }[] }[]
  total: number
  matched: number
  namespaces: string[]
  actions: Record<string, number>
  truncated: boolean
}

export type VeleroLogLevel = 'error' | 'warning' | 'info' | 'debug' | 'other'
export interface VeleroRunLogResponse {
  lines: { number: number; level: VeleroLogLevel; text: string }[]
  counts: Record<VeleroLogLevel, number>
  matched: number
  truncated: boolean
  partial: boolean
  decodedBytes: number
  limit?: string
  /** The levels the lines were selected from; with levels=auto, the server's choice. */
  levels: VeleroLogLevel[]
}

function useVeleroRunArtifact<T>(kind: 'backups' | 'restores', namespace: string, name: string, artifact: 'contents' | 'log' | 'volumes', params: URLSearchParams) {
  const { guard, gatedKey } = useRadarFeature(artifact === 'volumes' ? 'veleroRunVolumes' : 'veleroRunArtifacts')
  const base = getApiBase()
  const search = params.toString()
  return useQuery<T>({
    queryKey: ['velero', artifact, base, kind, namespace, name, search, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<T>(`/velero/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/${artifact}?${search}`, { method: 'POST', signal })),
    enabled: !!namespace && !!name,
    // A tab or filter is the user's request; don't create DownloadRequests on focus or a timer.
    staleTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })
}

export function useVeleroRunContents(kind: 'backups' | 'restores', namespace: string, name: string, filters: { q: string; action: string; namespace: string }) {
  return useVeleroRunArtifact<VeleroRunContentsResponse>(kind, namespace, name, 'contents', new URLSearchParams(filters))
}

export function useVeleroRunLog(kind: 'backups' | 'restores', namespace: string, name: string, levels: string, q: string) {
  return useVeleroRunArtifact<VeleroRunLogResponse>(kind, namespace, name, 'log', new URLSearchParams({ levels, q }))
}

export interface VeleroVolumeDetails {
  snapshotHandle?: string
  size?: number
  incrementalSize?: number
  driver?: string
  vscName?: string
  operationID?: string
  readyToUse?: boolean
  volumeGroupSnapshotHandle?: string
  dataMover?: string
  uploaderType?: string
  retainedSnapshot?: string
  volumeType?: string
  volumeAZ?: string
  iops?: string
  volumeName?: string
  podName?: string
  podNamespace?: string
  nodeName?: string
  phase?: string
}

export interface VeleroVolumeRecord {
  pvcName?: string
  pvcNamespace?: string
  pvName?: string
  method?: string
  snapshotDataMoved: boolean
  preserveLocalSnapshot?: boolean
  result?: string
  skipped?: boolean
  skippedReason?: string
  started?: string
  completed?: string
  csiSnapshotInfo?: VeleroVolumeDetails
  snapshotDataMovementInfo?: VeleroVolumeDetails
  nativeSnapshotInfo?: VeleroVolumeDetails
  podVolumeInfo?: VeleroVolumeDetails
}

export interface VeleroRunVolumesResponse {
  volumes: VeleroVolumeRecord[]
  total: number
  truncated: boolean
  /** The run had finished when Radar read it, just before fetching the file; otherwise Velero may still rewrite the record. */
  finishedAtRead: boolean
}

export interface VeleroVolumeOperation {
  kind: string
  name: string
  pod?: string
  podNamespace?: string
  volume?: string
  pvc?: string
  pvcNamespace?: string
  node?: string
  phase?: string
  bytesDone?: number
  bytesTotal?: number
  message?: string
  started?: string
  completed?: string
  longRunning: boolean
  nodeAgent?: 'running' | 'notRunning' | 'unknown'
}

export interface VeleroVolumeOperationsResponse {
  observedAt: string
  runPhase: string
  coverage: Record<string, import('@skyhook-io/k8s-ui').VeleroKindCoverage>
  operations: VeleroVolumeOperation[]
}

export function useVeleroRunVolumes(kind: 'backups' | 'restores', namespace: string, name: string) {
  return useVeleroRunArtifact<VeleroRunVolumesResponse>(kind, namespace, name, 'volumes', new URLSearchParams())
}

export function isVeleroRunFinished(phase: string | undefined): boolean {
  return phase !== undefined && ['Completed', 'Failed', 'PartiallyFailed', 'FailedValidation', 'Deleting'].includes(phase)
}

export function useVeleroVolumeOperations(kind: 'backups' | 'restores', namespace: string, name: string) {
  const { guard, gatedKey } = useRadarFeature('veleroRunVolumes')
  const base = getApiBase()
  return useQuery<VeleroVolumeOperationsResponse>({
    queryKey: ['velero', 'volume-operations', base, kind, namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<VeleroVolumeOperationsResponse>(`/velero/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/volume-operations`, signal)),
    enabled: !!namespace && !!name,
    staleTime: 10_000,
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return false
      return data.operations.length > 0 || Object.values(data.coverage).some((coverage) => coverage.state === 'syncing') || !isVeleroRunFinished(data.runPhase) ? 10_000 : false
    },
    retry: shouldRetryRadarQuery,
  })
}
