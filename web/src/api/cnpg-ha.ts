import { useQuery } from '@tanstack/react-query'
import type { CNPGClusterHA, CNPGInstanceLive, CNPGReplicationLive } from '@skyhook-io/k8s-ui'
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
  }))
}

/** Streaming standbys and the worst replay lag, from the primary's pg_stat_replication. */
export function cnpgReplicationLive(rt: CNPGRuntimeResponse | undefined): CNPGReplicationLive | undefined {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || (primary.status.state !== 'ok' && primary.status.state !== 'partial')) return undefined
  const reps = primary.status.replication ?? []
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  return {
    streaming: reps.filter((r) => r.state === 'streaming').length,
    standbys: rt!.instances.filter((i) => i.role !== 'primary').length,
    maxReplayLagSeconds: lags.length ? Math.max(...lags) : undefined,
  }
}
