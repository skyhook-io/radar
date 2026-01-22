import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type {
  Topology,
  ClusterInfo,
  Namespace,
  TimelineEvent,
  TimeRange,
  ResourceWithRelationships,
  HelmRelease,
  HelmReleaseDetail,
  HelmValues,
  ManifestDiff,
  UpgradeInfo,
  BatchUpgradeInfo,
} from '../types'

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

// Pod logs types
export interface LogsResponse {
  podName: string
  namespace: string
  containers: string[]
  logs: Record<string, string> // container -> logs
}

export interface LogStreamEvent {
  event: 'connected' | 'log' | 'end' | 'error'
  data: {
    timestamp?: string
    content?: string
    container?: string
    pod?: string
    namespace?: string
    reason?: string
    error?: string
  }
}

// Fetch pod logs (non-streaming)
export function usePodLogs(namespace: string, podName: string, options?: {
  container?: string
  tailLines?: number
  previous?: boolean
}) {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.previous) params.set('previous', 'true')
  const queryString = params.toString()

  return useQuery<LogsResponse>({
    queryKey: ['pod-logs', namespace, podName, options?.container, options?.tailLines, options?.previous],
    queryFn: () => fetchJSON(`/pods/${namespace}/${podName}/logs${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(namespace && podName),
    staleTime: 5000, // Allow refetch after 5 seconds
  })
}

// Create SSE connection for streaming logs
export function createLogStream(
  namespace: string,
  podName: string,
  options?: {
    container?: string
    tailLines?: number
    previous?: boolean
  }
): EventSource {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.previous) params.set('previous', 'true')
  const queryString = params.toString()

  return new EventSource(`${API_BASE}/pods/${namespace}/${podName}/logs/stream${queryString ? `?${queryString}` : ''}`)
}

// ============================================================================
// Port Forwarding
// ============================================================================

export interface AvailablePort {
  port: number
  protocol: string
  containerName?: string
  name?: string
}

export function useAvailablePorts(type: 'pod' | 'service', namespace: string, name: string) {
  return useQuery<{ ports: AvailablePort[] }>({
    queryKey: ['available-ports', type, namespace, name],
    queryFn: () => fetchJSON(`/portforwards/available/${type}/${namespace}/${name}`),
    enabled: Boolean(namespace && name),
    staleTime: 30000,
  })
}

// ============================================================================
// Resource Update/Delete mutations
// ============================================================================

// Update a resource with new YAML
export function useUpdateResource() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ kind, namespace, name, yaml }: { kind: string; namespace: string; name: string; yaml: string }) => {
      const response = await fetch(`${API_BASE}/resources/${kind}/${namespace}/${name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'text/plain' },
        body: yaml,
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: (_, variables) => {
      // Invalidate queries to refresh data
      queryClient.invalidateQueries({ queryKey: ['resource', variables.kind, variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['resources', variables.kind] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// Delete a resource
export function useDeleteResource() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ kind, namespace, name }: { kind: string; namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/resources/${kind}/${namespace}/${name}`, {
        method: 'DELETE',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      // DELETE returns 204 No Content, no body to parse
      return { success: true }
    },
    onSuccess: (_, variables) => {
      // Invalidate queries to refresh data
      queryClient.invalidateQueries({ queryKey: ['resources', variables.kind] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// ============================================================================
// Helm API hooks
// ============================================================================

// List all Helm releases
export function useHelmReleases(namespace?: string) {
  const params = namespace ? `?namespace=${namespace}` : ''
  return useQuery<HelmRelease[]>({
    queryKey: ['helm-releases', namespace],
    queryFn: () => fetchJSON(`/helm/releases${params}`),
    staleTime: 30000, // 30 seconds
  })
}

// Get details for a specific Helm release
export function useHelmRelease(namespace: string, name: string) {
  return useQuery<HelmReleaseDetail>({
    queryKey: ['helm-release', namespace, name],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}`),
    enabled: Boolean(namespace && name),
    staleTime: 30000,
  })
}

// Get manifest for a Helm release (optionally at a specific revision)
export function useHelmManifest(namespace: string, name: string, revision?: number) {
  const params = revision ? `?revision=${revision}` : ''
  return useQuery<string>({
    queryKey: ['helm-manifest', namespace, name, revision],
    queryFn: async () => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}/manifest${params}`)
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.text()
    },
    enabled: Boolean(namespace && name),
    staleTime: 60000, // 1 minute
  })
}

// Get values for a Helm release
export function useHelmValues(namespace: string, name: string, allValues?: boolean) {
  const params = allValues ? '?all=true' : ''
  return useQuery<HelmValues>({
    queryKey: ['helm-values', namespace, name, allValues],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}/values${params}`),
    enabled: Boolean(namespace && name),
    staleTime: 60000,
  })
}

// Get diff between two revisions
export function useHelmManifestDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number
) {
  return useQuery<ManifestDiff>({
    queryKey: ['helm-diff', namespace, name, revision1, revision2],
    queryFn: () =>
      fetchJSON(`/helm/releases/${namespace}/${name}/diff?revision1=${revision1}&revision2=${revision2}`),
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2),
    staleTime: 60000,
  })
}

// Check for upgrade availability (lazy - called when drawer opens)
export function useHelmUpgradeInfo(namespace: string, name: string, enabled = true) {
  return useQuery<UpgradeInfo>({
    queryKey: ['helm-upgrade-info', namespace, name],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}/upgrade-info`),
    enabled: Boolean(namespace && name && enabled),
    staleTime: 300000, // 5 minutes - upgrade info doesn't change frequently
    retry: false, // Don't retry on failure - repo might not be configured
  })
}

// Batch check for upgrade availability (for list view)
export function useHelmBatchUpgradeInfo(namespace?: string, enabled = true) {
  const params = namespace ? `?namespace=${namespace}` : ''
  return useQuery<BatchUpgradeInfo>({
    queryKey: ['helm-batch-upgrade-info', namespace],
    queryFn: () => fetchJSON(`/helm/upgrade-check${params}`),
    enabled,
    staleTime: 300000, // 5 minutes
    retry: false,
  })
}

// ============================================================================
// Helm Actions (mutations)
// ============================================================================

// Rollback a release to a previous revision
export function useHelmRollback() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name, revision }: { namespace: string; name: string; revision: number }) => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}/rollback?revision=${revision}`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: (_, variables) => {
      // Invalidate queries to refresh data
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-release', variables.namespace, variables.name] })
    },
  })
}

// Uninstall a release
export function useHelmUninstall() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name }: { namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}`, {
        method: 'DELETE',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      // Invalidate releases list
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-batch-upgrade-info'] })
    },
  })
}

// Upgrade a release to a new version
export function useHelmUpgrade() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name, version }: { namespace: string; name: string; version: string }) => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}/upgrade?version=${encodeURIComponent(version)}`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: (_, variables) => {
      // Invalidate queries to refresh data
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-release', variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-upgrade-info', variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-batch-upgrade-info'] })
    },
  })
}
