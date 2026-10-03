import { useQuery } from '@tanstack/react-query'
import type { CNPGFleetMetricsReading } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'
import type { CNPGClusterActivityResponse } from './cnpg'

export type CNPGHistoryRange = '15m' | '1h' | '6h' | '24h'

export const CNPG_HISTORY_RANGES: { id: CNPGHistoryRange; label: string }[] = [
  { id: '15m', label: '15 min' },
  { id: '1h', label: '1 h' },
  { id: '6h', label: '6 h' },
  { id: '24h', label: '24 h' },
]

export interface CNPGHistoryPoint {
  timestamp: number
  value: number | null
}

export interface CNPGHistorySeries {
  labels: Record<string, string>
  dataPoints: CNPGHistoryPoint[]
}

export interface CNPGHistoryChart {
  id: string
  title: string
  unit: string
  /** The PromQL families the chart is built from. */
  source: string
  /** The label naming each series (pod, state, datname, persistentvolumeclaim or series). */
  seriesBy: string
  /** ok | empty (scraped, nothing to plot) | noSeries (not scraped) | denied | error | notRead */
  state: string
  reason?: string
  grant?: string
  thresholds?: { value: number; label: string }[]
  series: CNPGHistorySeries[]
  omitted?: number
  steps: number
  covered: number
}

export interface CNPGClusterHistoryResponse {
  cluster: { namespace: string; name: string; uid: string }
  /** prometheus | none (Radar has no Prometheus; `reason` says why) */
  source: 'prometheus' | 'none'
  /** ok | ambiguous | scopeMismatch | error, for the query as a whole */
  state?: string
  reason?: string
  range: CNPGHistoryRange
  start?: string
  end?: string
  stepSeconds?: number
  selector?: string
  isolation?: { mode: 'configured' | 'verified' | 'unverified'; labels?: Record<string, string>; note: string }
  /** The volume chart's own match: claims are tied to the cluster apart from Pods. */
  pvcIsolation?: { mode: 'configured' | 'verified' | 'unverified'; labels?: Record<string, string>; note: string }
  sampledAt: string
  charts: CNPGHistoryChart[]
}

// /api/cnpg/clusters/{ns}/{name}/history — server-built Prometheus range
// queries for the Runtime Trends section.
export function useCNPGClusterHistory(namespace: string, name: string, range: CNPGHistoryRange, enabled = true) {
  return useQuery<CNPGClusterHistoryResponse>({
    queryKey: ['cnpg', 'history', namespace, name, range],
    queryFn: ({ signal }) =>
      fetchJSON<CNPGClusterHistoryResponse>(
        `/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/history?range=${range}`,
        signal,
      ),
    enabled: enabled && !!name,
    refetchInterval: range === '15m' ? 30_000 : 60_000,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
    retry: false,
    placeholderData: (prev) => (prev?.range === range ? prev : undefined),
  })
}

export interface CNPGFleetMetricsResponse {
  sampledAt: string
  source: 'prometheus' | 'none'
  reason?: string
  lagSource: string
  growthSource: string
  clusters: CNPGFleetMetricsReading[]
}

// /api/cnpg/fleet-metrics — each visible Cluster's largest standby replay lag
// and volume growth, read with the workspace's namespace set.
export function useCNPGFleetMetrics(namespaces: string[], enabled = true) {
  const ns = [...namespaces].sort().join(',')
  return useQuery<CNPGFleetMetricsResponse>({
    queryKey: ['cnpg', 'fleet-metrics', ns],
    queryFn: ({ signal }) => fetchJSON<CNPGFleetMetricsResponse>(`/cnpg/fleet-metrics${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal),
    enabled,
    staleTime: 20_000,
    refetchInterval: 60_000,
    retry: false,
    placeholderData: (prev) => prev,
  })
}

// The Cluster's activity inside one interval, both bounds applied server-side.
export function useCNPGClusterActivityWindow(namespace: string, name: string, since: string, until: string, enabled = true) {
  return useQuery<CNPGClusterActivityResponse>({
    queryKey: ['cnpg', 'activity-window', namespace, name, since, until],
    queryFn: ({ signal }) =>
      fetchJSON<CNPGClusterActivityResponse>(
        `/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/activity?since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}&limit=500`,
        signal,
      ),
    enabled: enabled && !!name,
    staleTime: 30_000,
  })
}
