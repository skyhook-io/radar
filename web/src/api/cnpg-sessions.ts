import { useQuery } from '@tanstack/react-query'
import type { Grant } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'
import type { CNPGActionCapability, CNPGClusterFacts, CNPGRuntimeSourceState } from './cnpg'

const enc = encodeURIComponent

export interface CNPGBackend {
  pid: number
  blockedBy: number[]
  /** PostgreSQL's own backend_start; a cancel or terminate echoes it verbatim. */
  backendStart: string
  state?: string
  waitEventType?: string
  waitEvent?: string
  user?: string
  database?: string
  application?: string
  clientAddr?: string
  backendType?: string
  backendAgeSeconds?: number
  xactAgeSeconds?: number
  queryAgeSeconds?: number
  stateAgeSeconds?: number
  query?: string
  queryTruncated?: boolean
}

export interface CNPGSessionInstance {
  pod: string
  role: 'primary' | 'replica' | 'unknown'
  cpuRequest?: string
  cpuLimit?: string
  memoryRequest?: string
  memoryLimit?: string
}

export interface CNPGSessionsResponse {
  cluster: { namespace: string; name: string; uid: string }
  pod: string
  podUID?: string
  role?: string
  sampledAt: string
  permission: { exec: 'allowed' | 'denied' | 'unknown'; grant?: Grant }
  state: CNPGRuntimeSourceState
  error?: string
  capturedAt?: string
  serverTime?: string
  maxConnections?: number
  superuserReservedConnections?: number
  clientBackends?: number
  involvedTotal?: number
  truncated?: boolean
  /** Only the backends in a blocking relation: blocking another, or waiting on one. */
  sessions?: CNPGBackend[]
  instances: CNPGSessionInstance[]
}

// /api/cnpg/clusters/{ns}/{name}/sessions — who blocks whom on one instance,
// read with fixed SQL over the caller's pods/exec. Denied is a state, not an error.
export function useCNPGSessions(namespace: string, name: string, pod?: string, enabled = true) {
  return useQuery<CNPGSessionsResponse>({
    queryKey: ['cnpg', 'sessions', namespace, name, pod ?? ''],
    queryFn: ({ signal }) =>
      fetchJSON<CNPGSessionsResponse>(`/cnpg/clusters/${enc(namespace)}/${enc(name)}/sessions${pod ? `?pod=${enc(pod)}` : ''}`, signal),
    enabled: enabled && !!name,
    refetchInterval: (q) => (q.state.data?.state === 'denied' ? false : 10_000),
    refetchIntervalInBackground: false,
    staleTime: 5_000,
    retry: false,
  })
}

export interface CNPGDestroyPVC {
  name: string
  uid: string
  role?: string
  tablespace?: string
  owned: boolean
  detached: boolean
  capacity?: string
  storageClass?: string
}

export interface CNPGDestroyPlan {
  uid: string
  context: string
  facts: CNPGClusterFacts
  pod: string
  podUID: string
  role: string
  pvcsReadable: boolean
  pvcReason?: string
  pvcs: CNPGDestroyPVC[]
  jobsReadable: boolean
  jobs: string[]
  actions: { delete: CNPGActionCapability; keep: CNPGActionCapability }
}

export function useCNPGDestroyPlan(namespace: string, name: string, pod: string, enabled = true) {
  return useQuery<CNPGDestroyPlan>({
    queryKey: ['cnpg', 'destroy-plan', namespace, name, pod],
    queryFn: ({ signal }) => fetchJSON<CNPGDestroyPlan>(`/cnpg/clusters/${enc(namespace)}/${enc(name)}/instances/${enc(pod)}/destroy-plan`, signal),
    enabled: enabled && !!pod,
    staleTime: 0,
    retry: false,
  })
}

export interface CNPGPoolerFacts {
  generation: number
  cluster: string
  type: string
  instances?: number
  /** Desired state (spec.pgbouncer.paused); each PgBouncer applies it on its own. */
  paused: boolean
  poolMode?: string
  parameters: Record<string, string>
  terminating: boolean
  deployment: {
    name: string
    state: 'ok' | 'missing' | 'unreadable' | 'foreign'
    replicas?: number
    readyReplicas?: number
    updatedReplicas?: number
    availableReplicas?: number
  }
  service: { name: string; state: 'ok' | 'missing' | 'unreadable' | 'foreign'; type?: string; port?: number }
}

export interface CNPGPoolerCapabilities {
  uid: string
  resourceVersion: string
  context: string
  facts: CNPGPoolerFacts
  actions: { pause: CNPGActionCapability; resume: CNPGActionCapability; observeState: CNPGActionCapability }
}

export function useCNPGPoolerCapabilities(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGPoolerCapabilities>({
    queryKey: ['cnpg', 'capabilities', 'poolers', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGPoolerCapabilities>(`/cnpg/poolers/${enc(namespace)}/${enc(name)}/capabilities`, signal),
    enabled: enabled && !!name,
    staleTime: 5_000,
    refetchInterval: 30_000,
    retry: false,
  })
}

export interface CNPGPgBouncerStateResponse {
  pooler: { namespace: string; name: string; uid: string }
  sampledAt: string
  permission: { exec: 'allowed' | 'denied' | 'unknown'; grant?: Grant }
  pods: { pod: string; state: CNPGRuntimeSourceState; error?: string; paused?: boolean; suspended?: boolean; active?: boolean }[]
}

// /api/cnpg/poolers/{ns}/{name}/pgbouncer-state — each PgBouncer's own SHOW
// STATE, the only place "paused" is observed rather than requested.
export function useCNPGPgBouncerState(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGPgBouncerStateResponse>({
    queryKey: ['cnpg', 'pgbouncer-state', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGPgBouncerStateResponse>(`/cnpg/poolers/${enc(namespace)}/${enc(name)}/pgbouncer-state`, signal),
    enabled: enabled && !!name,
    refetchInterval: (q) => (q.state.data?.permission.exec === 'denied' ? false : 15_000),
    refetchIntervalInBackground: false,
    staleTime: 5_000,
    retry: false,
  })
}
