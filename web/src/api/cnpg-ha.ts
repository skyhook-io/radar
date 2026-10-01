import { useQuery } from '@tanstack/react-query'
import { cnpgFormatLag, cnpgReplicationTone, type CNPGClusterHA, type CNPGFleetRow, type CNPGInstanceLive, type CNPGReplicationLive } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'
import type { CNPGRuntimeResponse } from './cnpg'

// /api/cnpg/clusters/{ns}/{name}/ha — quorum, disruption budgets, Leases,
// failure domains, Jobs, image drift and certificate renewal ownership. Each
// part is authorized on its own and says when it could not be read.
export function useCNPGClusterHA(namespace: string, name: string, options?: { enabled?: boolean; refetchInterval?: number | false }) {
  return useQuery<CNPGClusterHA>({
    queryKey: ['cnpg', 'ha', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGClusterHA>(`/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/ha`, signal),
    enabled: (options?.enabled ?? true) && !!name,
    staleTime: 10_000,
    refetchInterval: options?.refetchInterval ?? 30_000,
    refetchIntervalInBackground: false,
    retry: false,
  })
}

/** The instance-manager facts the HA section shows, from a runtime read. */
export function cnpgInstanceLive(rt: CNPGRuntimeResponse | undefined): CNPGInstanceLive[] | undefined {
  if (!rt || rt.permission.proxy === 'denied') return undefined
  return rt.instances.map((i) => ({
    pod: i.pod,
    state: i.status.state,
    pendingRestart: i.status.pendingRestart,
    pendingRestartForDecrease: i.status.pendingRestartForDecrease,
    roleDetail: i.status.roleDetail,
    instanceManagerVersion: i.status.instanceManagerVersion,
    timeline: i.status.timeline,
    incomplete: i.status.incomplete,
    reason: i.status.error ?? i.status.reason,
  }))
}

/** Why the HA section has no instance-manager facts at all; undefined when it has them. */
export function cnpgInstanceLiveUnavailable(rt: CNPGRuntimeResponse | undefined, error: unknown): string | undefined {
  if (rt?.permission.proxy === 'denied') return `needs ${rt.permission.grant ?? 'get pods/proxy'}`
  if (rt) return undefined
  return error instanceof Error ? `the runtime read failed: ${error.message}` : 'instance managers not read yet'
}

/** Why cnpgReplicationLive has no answer; undefined when it has one. */
export function cnpgReplicationGap(rt: CNPGRuntimeResponse | undefined, error: unknown): string | undefined {
  const unavailable = cnpgInstanceLiveUnavailable(rt, error)
  if (unavailable) return unavailable
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary) return 'no instance reports being the primary'
  if (primary.status.state !== 'ok' && primary.status.state !== 'partial') {
    return `${primary.pod} did not report (${primary.status.error ?? primary.status.reason ?? primary.status.state})`
  }
  if (!primary.status.replication) return `${primary.pod} did not report its replication rows`
  return undefined
}

/** Streaming standbys and the worst replay lag, from the primary's pg_stat_replication. */
export function cnpgReplicationLive(rt: CNPGRuntimeResponse | undefined): CNPGReplicationLive | undefined {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || (primary.status.state !== 'ok' && primary.status.state !== 'partial')) return undefined
  const reps = primary.status.replication
  if (!reps) return undefined
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  return {
    streaming: reps.filter((r) => r.state === 'streaming').length,
    standbys: rt!.instances.filter((i) => i.role !== 'primary').length,
    maxReplayLagSeconds: lags.length ? Math.max(...lags) : undefined,
  }
}

// Replaces the Kubernetes-only replication fact with the primary's
// pg_stat_replication when it has been read; otherwise keeps "lag unknown".
// Expected standbys are spec.instances − 1: a standby whose Pod is gone is
// missing, not absent from the count.
export function withLiveReplication(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGFleetRow {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || primary.status.state !== 'ok' || row.replication.text === 'Single instance' || row.replication.text === 'Hibernated') return row
  const reps = primary.status.replication
  if (!reps) return row
  const streaming = reps.filter((r) => r.state === 'streaming').length
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  const maxLag = lags.length ? Math.max(...lags) : undefined
  const lagText = maxLag !== undefined ? ` · max replay delay ${cnpgFormatLag(maxLag)}` : ''
  const source = 'From the primary’s pg_stat_replication via the instance manager'
  if (row.instances.desired === null) {
    return { ...row, replication: { text: `${streaming} streaming${lagText}`, tone: 'unknown', source: `${source}; spec.instances is not reported, so the expected standbys are unknown`, at: primary.status.capturedAt } }
  }
  const expected = Math.max(0, row.instances.desired - 1)
  const tone = cnpgReplicationTone(streaming, expected, maxLag)
  const text = streaming < expected ? `${streaming} of ${expected} expected standbys streaming` : `${streaming}/${expected} streaming`
  return { ...row, replication: { text: `${text}${lagText}`, tone, source, at: primary.status.capturedAt } }
}
