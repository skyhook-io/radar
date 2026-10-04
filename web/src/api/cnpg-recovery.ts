import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { Grant } from '@skyhook-io/k8s-ui'
import { apiFetch, ApiError, fetchJSON, useRadarFeature } from './client'
import type { ActionCapability } from './actions'
import { getApiBase } from './config'
import { shouldRetryRadarQuery } from './radarFeatures'

export type CNPGReadState = 'ok' | 'denied' | 'notFound' | 'error' | 'skipped' | 'partial'

export interface CNPGReadCoverage {
  state: CNPGReadState
  grant?: Grant
  reason?: string
}

export interface CNPGContainerState {
  name: string
  state: 'waiting' | 'running' | 'terminated' | 'unknown'
  reason?: string
  message?: string
  exitCode?: number
  restarts: number
  ready: boolean
}

export interface CNPGRecoveryPod {
  name: string
  uid: string
  kind: 'job' | 'instance'
  job?: string
  ownerVerified: boolean
  phase: string
  ready: boolean
  startedAt?: string
  initContainers: CNPGContainerState[]
  containers: CNPGContainerState[]
}

export interface CNPGRecoveryJob {
  name: string
  active: number
  succeeded: number
  failed: number
  complete: boolean
  failedWith?: string
  startedAt?: string
  completedAt?: string
}

export interface CNPGRecoveryEvent {
  type: string
  reason: string
  message: string
  kind: string
  name: string
  count: number
  lastSeen?: string
}

export interface CNPGRestoreValidationRef {
  namespace: string
  name: string
  uid?: string
  verified: boolean
}

export interface CNPGRestoreValidation {
  version: number
  recordedAt: string
  recordedBy?: string
  checked: string
  targetTime?: string
  source?: CNPGRestoreValidationRef
  target: CNPGRestoreValidationRef
}

export interface CNPGRecoverySpec {
  sourceKind: 'objectStore' | 'barmanObjectStore' | 'backup' | 'volumeSnapshots' | 'unknown'
  source?: string
  objectStore?: string
  serverName?: string
  backup?: string
  target?: Record<string, unknown>
}

export interface CNPGRecoveryResponse {
  cluster: {
    uid: string
    phase?: string
    phaseReason?: string
    instances: number | null
    readyInstances: number | null
    currentPrimary?: string
    createdAt?: string
    ready?: { status: string; reason?: string; message?: string }
  }
  recovery: CNPGRecoverySpec | null
  pods: CNPGRecoveryPod[]
  jobs: CNPGRecoveryJob[]
  events: CNPGRecoveryEvent[]
  coverage: Record<'pods' | 'jobs' | 'events', CNPGReadCoverage>
  validation?: CNPGRestoreValidation
  validationError?: string
  capturedAt: string
}

const clusterPath = (namespace: string, name: string) => `/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`

// /api/cnpg/clusters/{ns}/{name}/recovery — the restored Cluster's progress:
// phase, the recovery Job's Pods and their init containers, the instances and
// Warning events about them. Polled every 5s until the Cluster reports healthy.
export function useCNPGRecovery(namespace: string, name: string, options?: { enabled?: boolean }) {
  const { guard, gatedKey } = useRadarFeature('cnpgWorkspace')
  return useQuery<CNPGRecoveryResponse>({
    queryKey: ['cnpg', 'recovery', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<CNPGRecoveryResponse>(`${clusterPath(namespace, name)}/recovery`, signal)),
    enabled: options?.enabled ?? true,
    staleTime: 3_000,
    refetchInterval: (query) => {
      const d = query.state.data
      return d?.recovery && d.cluster.phase !== 'Cluster in healthy state' ? 5_000 : 30_000
    },
    retry: (count, err) => shouldRetryRadarQuery(count, err) && !(err instanceof ApiError && (err.status === 403 || err.status === 404)) && count < 2,
  })
}

export interface CNPGRestoreValidationRequest {
  reviewedContext: string
  uid: string
  params: { checked: string; targetTime?: string; source?: { namespace: string; name: string } }
}

export function useRecordCNPGRestoreValidation(namespace: string, name: string) {
  const { guard } = useRadarFeature('cnpgWorkspace')
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CNPGRestoreValidationRequest) => guard(() =>
      fetchJSON<CNPGRestoreValidation>(`${clusterPath(namespace, name)}/restore-validation`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'recovery', namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'workspace'] })
    },
    meta: { errorMessage: 'Could not record the validation note', successMessage: 'Validation note recorded' },
  })
}

export interface CNPGReportOptions {
  logs: boolean
  queryText: boolean
  tailLines: number
}

/** Fetches the report zip as the caller; returns the blob and its file name. */
export async function downloadCNPGReport(namespace: string, name: string, opts: CNPGReportOptions): Promise<{ blob: Blob; filename: string }> {
  const q = new URLSearchParams()
  if (opts.logs) {
    q.set('logs', 'true')
    q.set('tailLines', String(opts.tailLines))
    if (opts.queryText) q.set('queryText', 'true')
  }
  const qs = q.toString()
  const res = await apiFetch(`${getApiBase()}${clusterPath(namespace, name)}/report${qs ? `?${qs}` : ''}`)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new ApiError(err.error || `HTTP ${res.status}`, res.status, err)
  }
  const disposition = res.headers.get('Content-Disposition') ?? ''
  const match = /filename="([^"]+)"/.exec(disposition)
  return { blob: await res.blob(), filename: match?.[1] ?? `report_cluster_${name}.zip` }
}

// Operator diagnosis — additive fields on /api/cnpg/operator.

export interface CNPGOperatorPod {
  name: string
  uid: string
  phase: string
  ready: boolean
  startedAt?: string
  restarts: number
  leader: boolean
  lastTermination?: { container: string; reason: string; exitCode: number; finishedAt?: string }
}

export interface CNPGOperatorLeader extends Omit<CNPGReadCoverage, 'state'> {
  state: CNPGReadState | 'disabled'
  lease?: string
  holder?: string
  holderPod?: string
  holderIsCurrentPod: boolean
  renewTime?: string
  acquireTime?: string
  leaseDurationSeconds?: number
  transitions?: number
  stale: boolean
}

export interface CNPGOperatorWebhookConfig extends CNPGReadCoverage {
  kind: string
  name: string
  webhooks: { name: string; failurePolicy: string; caBundleSet: boolean; service?: string; url: boolean }[]
}

export interface CNPGOperatorWebhookService extends CNPGReadCoverage {
  namespace: string
  name: string
  readyEndpoints: number | null
  notReadyEndpoints: number | null
}

export interface CNPGOperatorReconcilePod {
  pod: string
  leader: boolean
  startedAt?: string
  state: 'ok' | 'partial' | 'denied' | 'unreachable' | 'error'
  error?: string
  reason?: string
  capturedAt?: string
  controllers: { controller: string; errors: number | null; total: number | null; results: Record<string, number> }[]
}

export interface CNPGOperatorDiagnosis {
  namespace: string
  deployment: string
  pods: CNPGOperatorPod[]
  podCoverage: CNPGReadCoverage
  leader: CNPGOperatorLeader
  watch: { all: boolean; namespaces: string[]; source: string; unresolved?: string }
  webhooks: CNPGOperatorWebhookConfig[]
  webhookServices: CNPGOperatorWebhookService[]
  metricsPort: number
  reconcile: CNPGOperatorReconcilePod[]
  events: CNPGReadCoverage & { items: CNPGRecoveryEvent[] }
}

// /api/cnpg/restore/capability — whether the caller may create the restored
// Cluster in `namespace` (create clusters, and the operator's webhook admits
// writes), for every way into the restore dialog.
export function useCNPGRestoreCapability(namespace: string) {
  const { guard, gatedKey } = useRadarFeature('cnpgWorkspace')
  return useQuery<ActionCapability>({
    queryKey: ['cnpg', 'restore-capability', namespace, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<ActionCapability>(`/cnpg/restore/capability?namespace=${encodeURIComponent(namespace)}`, signal)),
    enabled: !!namespace,
    staleTime: 15_000,
    retry: false,
  })
}
