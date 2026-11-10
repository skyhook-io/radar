import { useQuery } from '@tanstack/react-query'
import type { Grant } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import type { CNPGRuntimeSourceState } from './cnpg'

const enc = encodeURIComponent

interface CNPGInspectSource {
  state: CNPGRuntimeSourceState
  error?: string
  capturedAt?: string
}

export interface CNPGTimelineSwitch {
  from: number
  to: number
  switchLsn: string
  /** PostgreSQL's own text, e.g. "before 2026-10-01 12:00:00+00" or "no recovery target specified". */
  reason: string
}

export interface CNPGDatabaseContents {
  tables: number
  /** The planner's estimate (pg_class.reltuples); it can be stale. */
  estimatedRows: number
  /** Tables without an estimate (never analyzed; before PostgreSQL 14 also an empty one), not counted as zero. */
  noEstimate: number
  largest: { name: string; estimatedRows?: number; bytes: number }[]
}

export interface CNPGRestoreChecksResponse extends CNPGInspectSource {
  cluster: { namespace: string; name: string; uid: string }
  pod: string
  sampledAt: string
  permission: { exec: 'allowed' | 'denied' | 'unknown'; grant?: Grant }
  /** spec.bootstrap.recovery.recoveryTarget as declared. */
  target?: Record<string, unknown>
  database: string
  inRecovery?: boolean
  timeline?: number
  history?: CNPGTimelineSwitch[]
  historyMissing?: boolean
  databaseCount?: number
  databases?: { name: string; bytes: number }[]
  roleCount?: number
  roles?: { name: string; canLogin: boolean }[]
  contents?: CNPGDatabaseContents
  contentsSource?: CNPGInspectSource
}

// /api/cnpg/clusters/{ns}/{name}/restore-checks — what Radar reads in a
// restored cluster's primary over the caller's pods/exec. Denied is a state.
export function useCNPGRestoreChecks(namespace: string, name: string, enabled = true) {
  const { guard, gatedKey } = useRadarFeature('cnpgWorkspace')
  return useQuery<CNPGRestoreChecksResponse>({
    queryKey: ['cnpg', 'restore-checks', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<CNPGRestoreChecksResponse>(`/cnpg/clusters/${enc(namespace)}/${enc(name)}/restore-checks`, signal)),
    enabled: enabled && !!name,
    staleTime: 60_000,
    retry: false,
  })
}

export interface CNPGParameterSetting {
  name: string
  /** PostgreSQL's display; null when the connection itself sets the parameter. */
  value: string | null
  setByClient?: boolean
  source: string
  /** pg_settings.context: when a change takes effect. */
  context: string
  pendingRestart: boolean
}

export interface CNPGInstanceSettings extends CNPGInspectSource {
  pod: string
  role: 'primary' | 'replica' | 'unknown'
  settings?: CNPGParameterSetting[]
}

export interface CNPGParametersResponse extends CNPGInspectSource {
  cluster: { namespace: string; name: string; uid: string }
  sampledAt: string
  permission: { exec: 'allowed' | 'denied' | 'unknown'; grant?: Grant }
  declared: { name: string; value: string }[]
  omitted?: number
  skipped?: string[]
  instances: CNPGInstanceSettings[]
}

// /api/cnpg/clusters/{ns}/{name}/parameters — the declared parameters as each
// instance's PostgreSQL reports them. Observation only.
export function useCNPGParameters(namespace: string, name: string, enabled = true) {
  const { guard, gatedKey } = useRadarFeature('cnpgWorkspace')
  return useQuery<CNPGParametersResponse>({
    queryKey: ['cnpg', 'parameters', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<CNPGParametersResponse>(`/cnpg/clusters/${enc(namespace)}/${enc(name)}/parameters`, signal)),
    enabled: enabled && !!name,
    staleTime: 30_000,
    retry: false,
  })
}
