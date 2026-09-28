import { useQuery } from '@tanstack/react-query'
import type { CNPGWorkspaceResponse, TimelineEvent } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'

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
