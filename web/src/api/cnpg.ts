import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { CNPGWorkspaceResponse, TimelineEvent } from '@skyhook-io/k8s-ui'
import { ApiError, fetchJSON } from './client'

// /api/cnpg/workspace
//
// Every CloudNativePG object the caller may read, with per-kind coverage, the
// CNPG issues on them and the no-schedule audit finding. One query feeds the
// workspace screens, the Resources sidebar counts and the composed summaries,
// so they can never disagree about what they count.
export function useCNPGWorkspace(namespaces: string[], options?: { enabled?: boolean }) {
  const ns = [...namespaces].sort().join(',')
  return useQuery<CNPGWorkspaceResponse>({
    queryKey: ['cnpg', 'workspace', ns],
    queryFn: ({ signal }) => fetchJSON<CNPGWorkspaceResponse>(`/cnpg/workspace${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}

export interface CNPGOperatorCoverage {
  state: 'full' | 'partial' | 'denied' | 'syncing' | 'error'
  deniedNamespaces?: string[]
}

export interface CNPGOperatorComponent {
  role: 'operator' | 'plugin'
  pluginName?: string
  namespace: string
  deployment: string
  image?: string
  version?: string
  readyReplicas: number | null
  replicas: number | null
}

export interface CNPGOperatorConfig {
  kind: 'ConfigMap' | 'Secret'
  namespace: string
  name: string
  purpose: 'operator' | 'monitoring'
  exists?: boolean | null
  readable?: boolean
  reason?: string
  data?: Record<string, string>
}

export interface CNPGOperatorResponse {
  coverage: { deployments: CNPGOperatorCoverage; services: CNPGOperatorCoverage }
  components: CNPGOperatorComponent[]
  config: CNPGOperatorConfig[]
}

// /api/cnpg/operator
//
// Operator and plugin workloads plus where the operator's configuration lives.
// Deliberately not filtered by the namespace view filter: the operator runs in
// its own namespace, which users rarely have selected.
export function useCNPGOperator(options?: { enabled?: boolean }) {
  return useQuery<CNPGOperatorResponse>({
    queryKey: ['cnpg', 'operator'],
    queryFn: ({ signal }) => fetchJSON<CNPGOperatorResponse>('/cnpg/operator', signal),
    enabled: options?.enabled ?? true,
    staleTime: 30_000,
    refetchInterval: 60_000,
  })
}

export interface CNPGClusterActivityResponse {
  events: TimelineEvent[]
  oldest: string | null
  attributionSince: string | null
  truncated: boolean
}

// /api/cnpg/clusters/{ns}/{name}/activity
//
// The Cluster's history together with its instance Pods and every CNPG object
// attributed to it, including ones since deleted.
export function useCNPGClusterActivity(namespace: string, name: string, sinceHours = 24) {
  return useQuery<CNPGClusterActivityResponse>({
    queryKey: ['cnpg', 'activity', namespace, name, sinceHours],
    queryFn: ({ signal }) => {
      const since = new Date(Date.now() - sinceHours * 3600_000).toISOString()
      return fetchJSON<CNPGClusterActivityResponse>(
        `/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/activity?since=${encodeURIComponent(since)}&limit=500`,
        signal,
      )
    },
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export interface CNPGActionCapability {
  allowed: boolean
  reason?: string
  permission: 'allowed' | 'denied' | 'unknown'
  grant?: string
}

export interface CNPGInstanceFact {
  pod: string
  podUID: string
  role: 'primary' | 'standby'
  ready: boolean
  healthy: boolean
  fenced: boolean
  podReadable: boolean
  podExists: boolean
}

export interface CNPGBackupMethod {
  method: 'plugin' | 'volumeSnapshot' | 'barmanObjectStore'
  pluginName?: string
  capability: 'backup' | 'unknown' | 'none'
  reason?: string
  deprecated?: boolean
}

export interface CNPGClusterFacts {
  currentPrimary?: string
  targetPrimary?: string
  phase?: string
  phaseReason?: string
  hibernation: string
  hibernated: boolean
  fencedInstances: { raw: string; all: boolean; instances: string[]; malformed?: boolean }
  instances: CNPGInstanceFact[]
  backupMethods: CNPGBackupMethod[]
  backupTarget?: string
  isReplicaCluster: boolean
  terminating: boolean
}

export type CNPGClusterActionName =
  | 'backup'
  | 'switchover'
  | 'restart'
  | 'restartInstance'
  | 'reload'
  | 'fence'
  | 'unfence'
  | 'hibernate'
  | 'rehydrate'

export interface CNPGClusterCapabilities {
  uid: string
  resourceVersion: string
  context: string
  facts: CNPGClusterFacts
  actions: Record<CNPGClusterActionName, CNPGActionCapability>
  instanceActions: Record<string, { restart: CNPGActionCapability; switchoverTarget: CNPGActionCapability; fence: CNPGActionCapability; unfence: CNPGActionCapability }>
  restartPlan?: {
    primaryUpdateStrategy?: string
    primaryUpdateMethod?: string
    steps: { instance: string; role: string; effect: 'recreate' | 'skipped_fenced' | 'switchover' | 'restart' | 'wait_for_user' | 'restart_only_instance' }[]
  }
  hibernateEffects?: {
    poolers: CNPGEffectList
    unsuspendedScheduledBackups: CNPGEffectList
    databases: CNPGEffectList
    publications: CNPGEffectList
    subscriptions: CNPGEffectList
    volumes: { available: boolean; reason?: string; items: { name: string; instance: string; role: string; capacity?: string; requested?: string }[] }
  }
}

export interface CNPGEffectList {
  available: boolean
  reason?: string
  names: string[]
}

export type CNPGScheduleActionName = 'suspend' | 'resume' | 'run'

export interface CNPGScheduleCapabilities {
  uid: string
  resourceVersion: string
  context: string
  facts: {
    generation: number
    cluster: string
    suspended: boolean
    nextScheduleTime?: string
    method?: string
    pluginName?: string
    target?: string
    clusterState: 'ok' | 'missing' | 'hibernated' | 'unreadable'
    terminating: boolean
    catchUp: boolean
  }
  actions: Record<CNPGScheduleActionName, CNPGActionCapability>
}

function cnpgPath(kind: 'clusters' | 'scheduledbackups', namespace: string, name: string) {
  return `/cnpg/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`
}

export function useCNPGClusterCapabilities(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGClusterCapabilities>({
    queryKey: ['cnpg', 'capabilities', 'clusters', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGClusterCapabilities>(`${cnpgPath('clusters', namespace, name)}/capabilities`, signal),
    enabled: enabled && !!name,
    staleTime: 5_000,
    retry: false,
  })
}

export function useCNPGScheduleCapabilities(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGScheduleCapabilities>({
    queryKey: ['cnpg', 'capabilities', 'scheduledbackups', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGScheduleCapabilities>(`${cnpgPath('scheduledbackups', namespace, name)}/capabilities`, signal),
    enabled: enabled && !!name,
    staleTime: 5_000,
    retry: false,
  })
}

export interface CNPGActionRequest {
  reviewedContext: string
  uid: string
  facts: Record<string, unknown>
  params?: Record<string, unknown>
}

export interface CNPGActionResult {
  action: string
  message: string
  backup?: string
  resolvedAfterTimeout?: boolean
  catchUp?: boolean
}

// Errors stay with the dialog (shown inline so the user can adjust and retry);
// only success goes to the global toast.
export function useCNPGAction(kind: 'clusters' | 'scheduledbackups', namespace: string, name: string) {
  const queryClient = useQueryClient()
  return useMutation<CNPGActionResult, Error, { action: string; request: CNPGActionRequest; successMessage: string }>({
    mutationFn: ({ action, request }) =>
      fetchJSON<CNPGActionResult>(`${cnpgPath(kind, namespace, name)}/actions/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['cnpg'] })
      queryClient.invalidateQueries({ queryKey: ['resource'] })
    },
    // A refusal over changed facts re-reads them, so the dialog shows what
    // the user would now be confirming.
    onError: (err) => {
      if (cnpgActionErrorCode(err) !== undefined) queryClient.invalidateQueries({ queryKey: ['cnpg'] })
    },
  })
}

export type CNPGActionErrorCode = 'context_changed' | 'changed' | 'blocked' | 'all_fenced' | 'operator_webhook_unavailable' | 'outcome_unknown'

export function cnpgActionErrorCode(err: unknown): CNPGActionErrorCode | undefined {
  const code = err instanceof ApiError ? err.data?.code : undefined
  return typeof code === 'string' ? (code as CNPGActionErrorCode) : undefined
}

export type CNPGRuntimeSourceState = 'ok' | 'denied' | 'unreachable' | 'error' | 'partial'

export interface CNPGRuntimeReplication {
  applicationName: string
  state?: string
  syncState?: string
  syncPriority?: number
  writeLag?: number
  flushLag?: number
  replayLag?: number
  sentLsn?: string
  replayLsn?: string
}

export interface CNPGRuntimeInstance {
  pod: string
  role: 'primary' | 'replica' | 'unknown'
  status: {
    state: CNPGRuntimeSourceState
    error?: string
    capturedAt?: string
    isPrimary?: boolean
    currentLsn?: string
    receivedLsn?: string
    replayLsn?: string
    timeline?: number
    replayPaused?: boolean
    pendingRestart?: boolean
    isWalReceiverActive?: boolean
    archiving?: { lastArchivedWal?: string; lastArchivedAt?: string; lastFailedWal?: string; lastFailedAt?: string; readyWalFiles?: number }
    replication?: CNPGRuntimeReplication[]
    slots?: { name: string; type?: string; active?: boolean; database?: string; restartLsn?: string; walStatus?: string; retainedBytes?: number }[]
  }
  metrics: {
    state: CNPGRuntimeSourceState
    error?: string
    capturedAt?: string
    missing?: string[]
    maxConnections?: number
    sessions?: { state: string; database: string; user: string; application: string; count: number }[]
    sessionsTotal?: number
    waitingBackends?: number
    oldestXactSeconds?: number
    xidAge?: { database: string; age: number }[]
    databaseSizes?: { database: string; bytes: number }[]
    archiver?: { archivedCount?: number; failedCount?: number; secondsSinceLastArchival?: number; secondsSinceLastFailure?: number }
    xactCommitTotal?: number
    xactRollbackTotal?: number
    blksHit?: number
    blksRead?: number
    deadlocksTotal?: number
  }
}

export interface CNPGRuntimeResponse {
  cluster: { namespace: string; name: string; uid: string }
  sampledAt: string
  permission: { proxy: 'allowed' | 'denied'; grant?: string }
  instances: CNPGRuntimeInstance[]
}

// /api/cnpg/clusters/{ns}/{name}/runtime — live instance-manager status and
// exporter metrics read through pods/proxy. Polled only while visible.
export function useCNPGRuntime(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGRuntimeResponse>({
    queryKey: ['cnpg', 'runtime', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGRuntimeResponse>(`/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/runtime`, signal),
    enabled: enabled && !!name,
    refetchInterval: (q) => (q.state.data?.permission.proxy === 'denied' ? false : 5_000),
    refetchIntervalInBackground: false,
    staleTime: 4_000,
    retry: false,
  })
}

export interface CNPGPoolerRuntimeResponse {
  pooler: { namespace: string; name: string; uid: string }
  sampledAt: string
  permission: { proxy: 'allowed' | 'denied'; grant?: string }
  pods: {
    pod: string
    state: CNPGRuntimeSourceState
    error?: string
    reason?: string
    missing?: string[]
    pools?: { database: string; user: string; clActive?: number; clWaiting?: number; svActive?: number; svIdle?: number; svUsed?: number; maxwaitSeconds?: number }[]
  }[]
}

export function useCNPGPoolerRuntime(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGPoolerRuntimeResponse>({
    queryKey: ['cnpg', 'pooler-runtime', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGPoolerRuntimeResponse>(`/cnpg/poolers/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/runtime`, signal),
    enabled: enabled && !!name,
    refetchInterval: (q) => (q.state.data?.permission.proxy === 'denied' ? false : 30_000),
    refetchIntervalInBackground: false,
    staleTime: 25_000,
    retry: false,
  })
}
