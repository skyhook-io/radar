import { useQuery } from '@tanstack/react-query'
import type { Topology, ClusterInfo, Namespace, TimelineEvent, TimeRange, ResourceWithRelationships } from '../types'

const API_BASE = '/api'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// Cluster info
export function useClusterInfo() {
  return useQuery<ClusterInfo>({
    queryKey: ['cluster-info'],
    queryFn: () => fetchJSON('/cluster-info'),
    staleTime: 60000, // 1 minute
  })
}

// Namespaces
export function useNamespaces() {
  return useQuery<Namespace[]>({
    queryKey: ['namespaces'],
    queryFn: () => fetchJSON('/namespaces'),
    staleTime: 30000, // 30 seconds
  })
}

// Topology (for manual refresh)
export function useTopology(namespace: string, viewMode: string = 'resources') {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (viewMode) params.set('view', viewMode)
  const queryString = params.toString()

  return useQuery<Topology>({
    queryKey: ['topology', namespace, viewMode],
    queryFn: () => fetchJSON(`/topology${queryString ? `?${queryString}` : ''}`),
    staleTime: 5000, // 5 seconds
  })
}

// Generic resource fetching - returns resource with relationships
export function useResource<T>(kind: string, namespace: string, name: string) {
  const query = useQuery<ResourceWithRelationships<T>>({
    queryKey: ['resource', kind, namespace, name],
    queryFn: () => fetchJSON(`/resources/${kind}/${namespace}/${name}`),
    enabled: Boolean(kind && namespace && name),
  })

  // Extract resource and relationships from the response
  return {
    ...query,
    data: query.data?.resource,
    relationships: query.data?.relationships,
  }
}

// Hook that returns full response with relationships explicitly
export function useResourceWithRelationships<T>(kind: string, namespace: string, name: string) {
  return useQuery<ResourceWithRelationships<T>>({
    queryKey: ['resource', kind, namespace, name],
    queryFn: () => fetchJSON(`/resources/${kind}/${namespace}/${name}`),
    enabled: Boolean(kind && namespace && name),
  })
}

// List resources
export function useResources<T>(kind: string, namespace?: string) {
  const params = namespace ? `?namespace=${namespace}` : ''
  return useQuery<T[]>({
    queryKey: ['resources', kind, namespace],
    queryFn: () => fetchJSON(`/resources/${kind}${params}`),
  })
}

// Timeline changes (unified view of changes + K8s events)
export interface UseChangesOptions {
  namespace?: string
  kind?: string
  timeRange?: TimeRange
  includeK8sEvents?: boolean
  includeManaged?: boolean
  limit?: number
}

function getTimeRangeDate(range: TimeRange): Date | null {
  if (range === 'all') return null
  const now = new Date()
  switch (range) {
    case '5m':
      return new Date(now.getTime() - 5 * 60 * 1000)
    case '30m':
      return new Date(now.getTime() - 30 * 60 * 1000)
    case '1h':
      return new Date(now.getTime() - 60 * 60 * 1000)
    case '6h':
      return new Date(now.getTime() - 6 * 60 * 60 * 1000)
    case '24h':
      return new Date(now.getTime() - 24 * 60 * 60 * 1000)
    default:
      return null
  }
}

export function useChanges(options: UseChangesOptions = {}) {
  const { namespace, kind, timeRange = '1h', includeK8sEvents = true, includeManaged = false, limit = 200 } = options

  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (kind) params.set('kind', kind)
  if (!includeK8sEvents) params.set('include_k8s_events', 'false')
  if (includeManaged) params.set('include_managed', 'true')
  params.set('limit', String(limit))

  const sinceDate = getTimeRangeDate(timeRange)
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  const queryString = params.toString()

  return useQuery<TimelineEvent[]>({
    queryKey: ['changes', namespace, kind, timeRange, includeK8sEvents, includeManaged, limit],
    queryFn: () => fetchJSON(`/changes${queryString ? `?${queryString}` : ''}`),
    refetchInterval: 10000, // Refresh every 10 seconds
  })
}

// Children changes for a parent workload (e.g., ReplicaSets and Pods under a Deployment)
export function useResourceChildren(kind: string, namespace: string, name: string, timeRange: TimeRange = '1h') {
  const sinceDate = getTimeRangeDate(timeRange)
  const params = new URLSearchParams()
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  return useQuery<TimelineEvent[]>({
    queryKey: ['resource-children', kind, namespace, name, timeRange],
    queryFn: () => fetchJSON(`/changes/${kind}/${namespace}/${name}/children?${params.toString()}`),
    enabled: Boolean(kind && namespace && name),
    refetchInterval: 15000, // Refresh every 15 seconds
  })
}

// Resource-specific events (filtered by resource name)
export function useResourceEvents(kind: string, namespace: string, name: string) {
  const params = new URLSearchParams()
  params.set('namespace', namespace)
  params.set('kind', kind)
  params.set('limit', '50')

  // Get events from last 24 hours
  const since = new Date(Date.now() - 24 * 60 * 60 * 1000)
  params.set('since', since.toISOString())

  return useQuery<TimelineEvent[]>({
    queryKey: ['resource-events', kind, namespace, name],
    queryFn: async () => {
      const events = await fetchJSON<TimelineEvent[]>(`/changes?${params.toString()}`)
      // Filter to only events for this specific resource
      return events.filter(e => e.name === name)
    },
    enabled: Boolean(kind && namespace && name),
    refetchInterval: 15000, // Refresh every 15 seconds
  })
}
