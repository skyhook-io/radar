import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, useEffect, useCallback, useRef } from 'react'
import type {
  Topology,
  ClusterInfo,
  ContextInfo,
  Namespace,
  TimelineEvent,
  TimelineResponse,
  TimelineGroupingMode,
  TimelineStats,
  FilterPreset,
  TimeRange,
  EventGroup,
  ResourceWithRelationships,
  HelmRelease,
  HelmReleaseDetail,
  HelmValues,
  ManifestDiff,
  UpgradeInfo,
  BatchUpgradeInfo,
  ValuesPreviewResponse,
  HelmRepository,
  ChartSearchResult,
  ChartDetail,
  InstallChartRequest,
  ArtifactHubSearchResult,
  ArtifactHubChartDetail,
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
  filter?: string // Filter preset name ('default', 'all', 'warnings-only', 'workloads')
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
  const { namespace, kind, timeRange = '1h', filter = 'all', includeK8sEvents = true, includeManaged = false, limit = 200 } = options

  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (kind) params.set('kind', kind)
  if (filter) params.set('filter', filter)
  if (!includeK8sEvents) params.set('include_k8s_events', 'false')
  if (includeManaged) params.set('include_managed', 'true')
  params.set('limit', String(limit))

  const sinceDate = getTimeRangeDate(timeRange)
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  const queryString = params.toString()

  return useQuery<TimelineEvent[]>({
    queryKey: ['changes', namespace, kind, timeRange, filter, includeK8sEvents, includeManaged, limit],
    queryFn: () => fetchJSON(`/changes${queryString ? `?${queryString}` : ''}`),
    refetchInterval: 60000, // SSE handles real-time updates; this is a fallback
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

// ============================================================================
// New Timeline API (with grouping and filter presets)
// ============================================================================

export interface UseTimelineOptions {
  namespace?: string
  kinds?: string[]
  timeRange?: TimeRange
  groupBy?: TimelineGroupingMode
  filter?: string // Filter preset name (default, all, warnings-only, workloads)
  includeManaged?: boolean
  includeK8sEvents?: boolean
  limit?: number
}

// New timeline API with grouping support
export function useTimeline(options: UseTimelineOptions = {}) {
  const {
    namespace,
    kinds,
    timeRange = '1h',
    groupBy = 'none',
    filter = 'default',
    includeManaged = false,
    includeK8sEvents = true,
    limit = 200,
  } = options

  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (kinds?.length) params.set('kinds', kinds.join(','))
  if (groupBy !== 'none') params.set('group_by', groupBy)
  if (filter) params.set('filter', filter)
  if (includeManaged) params.set('include_managed', 'true')
  if (!includeK8sEvents) params.set('include_k8s_events', 'false')
  params.set('limit', String(limit))

  const sinceDate = getTimeRangeDate(timeRange)
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  const queryString = params.toString()

  return useQuery<TimelineResponse>({
    queryKey: ['timeline', namespace, kinds, timeRange, groupBy, filter, includeManaged, includeK8sEvents, limit],
    queryFn: () => fetchJSON(`/timeline${queryString ? `?${queryString}` : ''}`),
    refetchInterval: 60000, // SSE handles real-time updates; this is a fallback
  })
}

// Get timeline with flat events (no grouping) - convenience wrapper
export function useTimelineFlat(options: Omit<UseTimelineOptions, 'groupBy'> = {}) {
  const result = useTimeline({ ...options, groupBy: 'none' })

  return {
    ...result,
    // Flatten the response to just events for easier consumption
    data: result.data?.ungrouped ?? [],
  }
}

// Get available filter presets
export function useTimelineFilters() {
  return useQuery<Record<string, FilterPreset>>({
    queryKey: ['timeline-filters'],
    queryFn: () => fetchJSON('/timeline/filters'),
    staleTime: 3600000, // 1 hour - filters don't change often
  })
}

// Get timeline store statistics
export function useTimelineStats() {
  return useQuery<TimelineStats>({
    queryKey: ['timeline-stats'],
    queryFn: () => fetchJSON('/timeline/stats'),
    staleTime: 60000, // 1 minute
  })
}

// Get child events for a resource using new timeline API
export function useTimelineChildren(kind: string, namespace: string, name: string, timeRange: TimeRange = '1h') {
  const sinceDate = getTimeRangeDate(timeRange)
  const params = new URLSearchParams()
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  return useQuery<TimelineEvent[]>({
    queryKey: ['timeline-children', kind, namespace, name, timeRange],
    queryFn: () => fetchJSON(`/timeline/${kind}/${namespace}/${name}/children?${params.toString()}`),
    enabled: Boolean(kind && namespace && name),
    refetchInterval: 15000,
  })
}

// Timeline SSE stream event types
export interface TimelineStreamEvent {
  event: 'initial' | 'event' | 'group_update' | 'heartbeat'
  data: TimelineResponse | { event: TimelineEvent; groupId?: string } | { groupId: string; healthState: string } | { time: number }
}

// Create SSE connection for timeline stream
export function createTimelineStream(options?: {
  namespace?: string
  groupBy?: TimelineGroupingMode
  filter?: string
  onInitial?: (response: TimelineResponse) => void
  onEvent?: (event: TimelineEvent, groupId?: string) => void
  onGroupUpdate?: (groupId: string, healthState: string) => void
  onError?: (error: Error) => void
}): () => void {
  const params = new URLSearchParams()
  if (options?.namespace) params.set('namespace', options.namespace)
  if (options?.groupBy && options.groupBy !== 'none') params.set('group_by', options.groupBy)
  if (options?.filter) params.set('filter', options.filter)

  const queryString = params.toString()
  const url = `${API_BASE}/timeline/stream${queryString ? `?${queryString}` : ''}`

  const eventSource = new EventSource(url)

  eventSource.addEventListener('initial', (e) => {
    try {
      const data = JSON.parse(e.data) as TimelineResponse
      options?.onInitial?.(data)
    } catch (err) {
      console.error('Failed to parse initial timeline data:', err)
    }
  })

  eventSource.addEventListener('event', (e) => {
    try {
      const data = JSON.parse(e.data) as { event: TimelineEvent; groupId?: string }
      options?.onEvent?.(data.event, data.groupId)
    } catch (err) {
      console.error('Failed to parse timeline event:', err)
    }
  })

  eventSource.addEventListener('group_update', (e) => {
    try {
      const data = JSON.parse(e.data) as { groupId: string; healthState: string }
      options?.onGroupUpdate?.(data.groupId, data.healthState)
    } catch (err) {
      console.error('Failed to parse group update:', err)
    }
  })

  eventSource.onerror = () => {
    options?.onError?.(new Error('Timeline SSE connection error'))
  }

  // Return cleanup function
  return () => {
    eventSource.close()
  }
}

// React hook for timeline stream with automatic reconnection and state management
export interface UseTimelineStreamOptions {
  namespace?: string
  groupBy?: TimelineGroupingMode
  filter?: string
  enabled?: boolean
}

export interface UseTimelineStreamResult {
  groups: EventGroup[]
  ungrouped: TimelineEvent[]
  isConnected: boolean
  error: Error | null
  totalEvents: number
}

export function useTimelineStream(options: UseTimelineStreamOptions = {}): UseTimelineStreamResult {
  const { namespace, groupBy = 'none', filter, enabled = true } = options

  const [groups, setGroups] = useState<EventGroup[]>([])
  const [ungrouped, setUngrouped] = useState<TimelineEvent[]>([])
  const [isConnected, setIsConnected] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const [totalEvents, setTotalEvents] = useState(0)

  // Use ref to track cleanup function
  const cleanupRef = useRef<(() => void) | null>(null)

  // Handle initial data from SSE
  const handleInitial = useCallback((response: TimelineResponse) => {
    setGroups(response.groups || [])
    setUngrouped(response.ungrouped || [])
    setTotalEvents(response.meta.totalEvents)
    setIsConnected(true)
    setError(null)
  }, [])

  // Handle new events from SSE
  const handleEvent = useCallback((event: TimelineEvent, groupId?: string) => {
    if (groupId && groupBy !== 'none') {
      // Find and update the appropriate group
      setGroups(prevGroups => {
        const newGroups = [...prevGroups]
        const groupIndex = newGroups.findIndex(g => g.id === groupId)

        if (groupIndex >= 0) {
          // Add event to existing group
          const group = { ...newGroups[groupIndex] }
          group.events = [event, ...group.events]
          group.eventCount = group.events.length
          newGroups[groupIndex] = group
        } else {
          // Create new group for this event
          const newGroup: EventGroup = {
            id: groupId,
            kind: event.kind,
            name: event.name,
            namespace: event.namespace,
            events: [event],
            eventCount: 1,
            healthState: event.healthState,
          }
          newGroups.unshift(newGroup)
        }

        return newGroups
      })
    } else {
      // Add to ungrouped list
      setUngrouped(prev => [event, ...prev])
    }

    setTotalEvents(prev => prev + 1)
  }, [groupBy])

  // Handle group updates (health state changes)
  const handleGroupUpdate = useCallback((groupId: string, healthState: string) => {
    setGroups(prevGroups => {
      return prevGroups.map(group => {
        if (group.id === groupId) {
          return { ...group, healthState: healthState as EventGroup['healthState'] }
        }
        return group
      })
    })
  }, [])

  // Handle errors
  const handleError = useCallback((err: Error) => {
    setError(err)
    setIsConnected(false)
  }, [])

  // Set up SSE connection
  useEffect(() => {
    if (!enabled) {
      return
    }

    // Clean up any existing connection
    if (cleanupRef.current) {
      cleanupRef.current()
    }

    const cleanup = createTimelineStream({
      namespace,
      groupBy,
      filter,
      onInitial: handleInitial,
      onEvent: handleEvent,
      onGroupUpdate: handleGroupUpdate,
      onError: handleError,
    })

    cleanupRef.current = cleanup

    return () => {
      cleanup()
      cleanupRef.current = null
    }
  }, [namespace, groupBy, filter, enabled, handleInitial, handleEvent, handleGroupUpdate, handleError])

  return {
    groups,
    ungrouped,
    isConnected,
    error,
    totalEvents,
  }
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
// CronJob operations
// ============================================================================

// Trigger a CronJob (create a Job from it)
export function useTriggerCronJob() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name }: { namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/cronjobs/${namespace}/${name}/trigger`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'cronjobs'] })
      queryClient.invalidateQueries({ queryKey: ['resources', 'jobs'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// Suspend a CronJob
export function useSuspendCronJob() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name }: { namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/cronjobs/${namespace}/${name}/suspend`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'cronjobs'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// Resume a suspended CronJob
export function useResumeCronJob() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name }: { namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/cronjobs/${namespace}/${name}/resume`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'cronjobs'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// ============================================================================
// Workload operations
// ============================================================================

// Restart a workload (Deployment, StatefulSet, DaemonSet, Rollout)
export function useRestartWorkload() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ kind, namespace, name }: { kind: string; namespace: string; name: string }) => {
      const response = await fetch(`${API_BASE}/workloads/${kind}/${namespace}/${name}/restart`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: (_, variables) => {
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

// Preview values change (dry-run upgrade)
export function useHelmPreviewValues() {
  return useMutation<ValuesPreviewResponse, Error, { namespace: string; name: string; values: Record<string, unknown> }>({
    mutationFn: async ({ namespace, name, values }) => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}/values/preview`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ values }),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
  })
}

// Apply new values to a release
export function useHelmApplyValues() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name, values }: { namespace: string; name: string; values: Record<string, unknown> }) => {
      const response = await fetch(`${API_BASE}/helm/releases/${namespace}/${name}/values`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ values }),
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
      queryClient.invalidateQueries({ queryKey: ['helm-values', variables.namespace, variables.name] })
    },
  })
}

// ============================================================================
// Chart Browser API hooks
// ============================================================================

// List configured Helm repositories
export function useHelmRepositories() {
  return useQuery<HelmRepository[]>({
    queryKey: ['helm-repositories'],
    queryFn: () => fetchJSON('/helm/repositories'),
  })
}

// Update a repository index
export function useUpdateRepository() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (repoName: string) => {
      const response = await fetch(`${API_BASE}/helm/repositories/${repoName}/update`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['helm-repositories'] })
      queryClient.invalidateQueries({ queryKey: ['helm-charts'] })
    },
  })
}

// Search charts across all repositories
export function useSearchCharts(query: string, allVersions = false, enabled = true) {
  return useQuery<ChartSearchResult>({
    queryKey: ['helm-charts', query, allVersions],
    queryFn: () => {
      const params = new URLSearchParams()
      if (query) params.set('query', query)
      if (allVersions) params.set('allVersions', 'true')
      return fetchJSON(`/helm/charts?${params.toString()}`)
    },
    enabled,
  })
}

// Get chart detail
export function useChartDetail(repo: string, chart: string, version?: string, enabled = true) {
  return useQuery<ChartDetail>({
    queryKey: ['helm-chart-detail', repo, chart, version],
    queryFn: () => {
      const path = version
        ? `/helm/charts/${repo}/${chart}/${version}`
        : `/helm/charts/${repo}/${chart}`
      return fetchJSON(path)
    },
    enabled: enabled && Boolean(repo && chart),
  })
}

// Install a new chart (non-streaming)
export function useInstallChart() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (req: InstallChartRequest) => {
      const response = await fetch(`${API_BASE}/helm/releases`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json() as Promise<HelmRelease>
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
    },
  })
}

// Install progress event types
export interface InstallProgressEvent {
  type: 'progress' | 'complete' | 'error'
  phase?: string
  message?: string
  detail?: string
  release?: HelmRelease
}

// Install a chart with progress streaming via SSE
export function installChartWithProgress(
  req: InstallChartRequest,
  onProgress: (event: InstallProgressEvent) => void
): Promise<HelmRelease> {
  return new Promise((resolve, reject) => {
    // Use fetch with streaming response since we need to POST
    fetch(`${API_BASE}/helm/releases/install-stream`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
      .then(async (response) => {
        if (!response.ok) {
          const error = await response.json().catch(() => ({ error: 'Unknown error' }))
          reject(new Error(error.error || `HTTP ${response.status}`))
          return
        }

        const reader = response.body?.getReader()
        if (!reader) {
          reject(new Error('No response body'))
          return
        }

        const decoder = new TextDecoder()
        let buffer = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })

          // Parse SSE events from buffer
          const lines = buffer.split('\n')
          buffer = lines.pop() || '' // Keep incomplete line in buffer

          for (const line of lines) {
            if (line.startsWith('data: ')) {
              try {
                const data = JSON.parse(line.slice(6)) as InstallProgressEvent
                onProgress(data)

                if (data.type === 'complete' && data.release) {
                  resolve(data.release)
                } else if (data.type === 'error') {
                  reject(new Error(data.message || 'Install failed'))
                }
              } catch {
                // Ignore parse errors
              }
            }
          }
        }
      })
      .catch(reject)
  })
}

// ============================================================================
// ArtifactHub API hooks
// ============================================================================

// Sort options for ArtifactHub search
export type ArtifactHubSortOption = 'relevance' | 'stars' | 'last_updated'

// Search charts on ArtifactHub
export function useArtifactHubSearch(
  query: string,
  options?: { offset?: number; limit?: number; official?: boolean; verified?: boolean; sort?: ArtifactHubSortOption },
  enabled = true
) {
  const params = new URLSearchParams()
  if (query) params.set('query', query)
  if (options?.offset) params.set('offset', String(options.offset))
  if (options?.limit) params.set('limit', String(options.limit))
  if (options?.official) params.set('official', 'true')
  if (options?.verified) params.set('verified', 'true')
  if (options?.sort && options.sort !== 'relevance') params.set('sort', options.sort)

  return useQuery<ArtifactHubSearchResult>({
    queryKey: ['artifacthub-search', query, options?.offset, options?.limit, options?.official, options?.verified, options?.sort],
    queryFn: () => fetchJSON(`/helm/artifacthub/search?${params.toString()}`),
    enabled: enabled && query.length > 0,
    staleTime: 60000, // 1 minute
  })
}

// Get chart detail from ArtifactHub
export function useArtifactHubChart(repoName: string, chartName: string, version?: string, enabled = true) {
  const path = version
    ? `/helm/artifacthub/charts/${repoName}/${chartName}/${version}`
    : `/helm/artifacthub/charts/${repoName}/${chartName}`

  return useQuery<ArtifactHubChartDetail>({
    queryKey: ['artifacthub-chart', repoName, chartName, version],
    queryFn: () => fetchJSON(path),
    enabled: enabled && Boolean(repoName && chartName),
    staleTime: 60000,
  })
}

// ============================================================================
// Context Switching API hooks
// ============================================================================

// List all available kubeconfig contexts
export function useContexts() {
  return useQuery<ContextInfo[]>({
    queryKey: ['contexts'],
    queryFn: () => fetchJSON('/contexts'),
    staleTime: 30000, // 30 seconds
  })
}

// Session counts for context switch confirmation
export interface SessionCounts {
  portForwards: number
  execSessions: number
  total: number
}

// Fetch current session counts (port forwards + exec sessions)
export async function fetchSessionCounts(): Promise<SessionCounts> {
  return fetchJSON('/sessions')
}

// Context switch timeout in milliseconds (should be longer than backend timeout)
const CONTEXT_SWITCH_TIMEOUT = 45000 // 45 seconds

// Switch to a different context
export function useSwitchContext() {
  const queryClient = useQueryClient()

  return useMutation<ClusterInfo, Error, { name: string }>({
    mutationFn: async ({ name }) => {
      const controller = new AbortController()
      const timeoutId = setTimeout(() => controller.abort(), CONTEXT_SWITCH_TIMEOUT)

      try {
        const response = await fetch(`${API_BASE}/contexts/${encodeURIComponent(name)}`, {
          method: 'POST',
          signal: controller.signal,
        })
        clearTimeout(timeoutId)

        if (!response.ok) {
          const error = await response.json().catch(() => ({ error: 'Unknown error' }))
          throw new Error(error.error || `HTTP ${response.status}`)
        }
        return response.json()
      } catch (error) {
        clearTimeout(timeoutId)
        if (error instanceof Error && error.name === 'AbortError') {
          throw new Error('Context switch timed out. The cluster may be unreachable.')
        }
        throw error
      }
    },
    onSuccess: () => {
      // Clear all query cache to ensure fresh data from new context
      // Using removeQueries + invalidateQueries ensures no stale data is served
      queryClient.removeQueries()
      queryClient.invalidateQueries()
    },
  })
}
