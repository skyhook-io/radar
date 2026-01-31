import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type {
  Topology,
  ClusterInfo,
  Capabilities,
  ContextInfo,
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
  ValuesPreviewResponse,
  HelmRepository,
  ChartSearchResult,
  ChartDetail,
  InstallChartRequest,
  ArtifactHubSearchResult,
  ArtifactHubChartDetail,
} from '../types'
import type { GitOpsOperationResponse } from '../types/gitops'

const API_BASE = '/api'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// ============================================================================
// Dashboard
// ============================================================================

export interface DashboardCluster {
  name: string
  platform: string
  version: string
  connected: boolean
}

export interface DashboardHealth {
  healthy: number
  warning: number
  error: number
  warningEvents: number
}

export interface DashboardProblem {
  kind: string
  namespace: string
  name: string
  status: string
  reason: string
  message: string
  age: string
  ageSeconds: number
}

export interface WorkloadCount {
  total: number
  ready: number
  unready: number
}

export interface DashboardMetrics {
  cpu?: MetricSummary
  memory?: MetricSummary
}

export interface MetricSummary {
  usageMillis: number
  requestsMillis: number
  capacityMillis: number
  usagePercent: number
  requestPercent: number
}

export interface DashboardResourceCounts {
  pods: { total: number; running: number; pending: number; failed: number; succeeded: number }
  deployments: { total: number; available: number; unavailable: number }
  statefulSets: WorkloadCount
  daemonSets: WorkloadCount
  services: number
  ingresses: number
  nodes: { total: number; ready: number; notReady: number }
  namespaces: number
  jobs: { total: number; active: number; succeeded: number; failed: number }
  cronJobs: { total: number; active: number; suspended: number }
  configMaps: number
  secrets: number
  pvcs: { total: number; bound: number; pending: number; unbound: number }
  helmReleases: number
}

export interface DashboardEvent {
  type: string
  reason: string
  message: string
  involvedObject: string
  namespace: string
  timestamp: string
}

export interface DashboardChange {
  kind: string
  namespace: string
  name: string
  changeType: string
  summary: string
  timestamp: string
}

export interface DashboardTopologySummary {
  nodeCount: number
  edgeCount: number
}

export interface DashboardTopFlow {
  src: string
  dst: string
  requestsPerSec?: number
  connections: number
}

export interface DashboardTrafficSummary {
  source: string
  flowCount: number
  topFlows: DashboardTopFlow[]
}

export interface DashboardHelmRelease {
  name: string
  namespace: string
  chart: string
  chartVersion: string
  status: string
  resourceHealth?: string
}

export interface DashboardHelmSummary {
  total: number
  releases: DashboardHelmRelease[]
}

export interface DashboardCRDCount {
  kind: string
  name: string
  group: string
  count: number
}

export interface DashboardResponse {
  cluster: DashboardCluster
  health: DashboardHealth
  problems: DashboardProblem[]
  resourceCounts: DashboardResourceCounts
  recentEvents: DashboardEvent[]
  recentChanges: DashboardChange[]
  topologySummary: DashboardTopologySummary
  trafficSummary: DashboardTrafficSummary | null
  helmReleases: DashboardHelmSummary
  metrics: DashboardMetrics | null
  topCRDs: DashboardCRDCount[]
}

export function useDashboard(namespace?: string) {
  const params = namespace ? `?namespace=${namespace}` : ''
  return useQuery<DashboardResponse>({
    queryKey: ['dashboard', namespace],
    queryFn: () => fetchJSON(`/dashboard${params}`),
    staleTime: 15000, // 15 seconds
    refetchInterval: 30000, // Refresh every 30 seconds
  })
}

// Cluster info
export function useClusterInfo() {
  return useQuery<ClusterInfo>({
    queryKey: ['cluster-info'],
    queryFn: () => fetchJSON('/cluster-info'),
    staleTime: 60000, // 1 minute
  })
}

// Capabilities (RBAC-based feature flags)
export function useCapabilities() {
  return useQuery<Capabilities>({
    queryKey: ['capabilities'],
    queryFn: () => fetchJSON('/capabilities'),
    staleTime: 60000, // 1 minute - cached on backend too
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
// Uses '_' as placeholder for cluster-scoped resources (empty namespace)
export function useResource<T>(kind: string, namespace: string, name: string, group?: string) {
  // For cluster-scoped resources, use '_' as namespace placeholder
  const ns = namespace || '_'
  const params = new URLSearchParams()
  if (group) params.set('group', group)
  const queryString = params.toString()

  const query = useQuery<ResourceWithRelationships<T>>({
    queryKey: ['resource', kind, namespace, name, group],
    queryFn: () => fetchJSON(`/resources/${kind}/${ns}/${name}${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(kind && name),  // namespace can be empty for cluster-scoped resources
  })

  // Extract resource and relationships from the response
  return {
    ...query,
    data: query.data?.resource,
    relationships: query.data?.relationships,
  }
}

// Hook that returns full response with relationships explicitly
export function useResourceWithRelationships<T>(kind: string, namespace: string, name: string, group?: string) {
  const ns = namespace || '_'
  const params = new URLSearchParams()
  if (group) params.set('group', group)
  const queryString = params.toString()

  return useQuery<ResourceWithRelationships<T>>({
    queryKey: ['resource', kind, namespace, name, group],
    queryFn: () => fetchJSON(`/resources/${kind}/${ns}/${name}${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(kind && name),
  })
}

// List resources - queryKey includes group for cache sharing with ResourcesView
export function useResources<T>(kind: string, namespace?: string, group?: string) {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (group) params.set('group', group)
  const queryString = params.toString()

  return useQuery<T[]>({
    queryKey: ['resources', kind, group, namespace],
    queryFn: () => fetchJSON(`/resources/${kind}${queryString ? `?${queryString}` : ''}`),
    staleTime: 30000, // 30 seconds - matches refetchInterval in ResourcesView
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
    staleTime: 5000, // Consider data stale after 5 seconds to ensure fresh data on navigation
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
// Metrics (from metrics.k8s.io)
// ============================================================================

export interface ContainerMetrics {
  name: string
  usage: {
    cpu: string      // e.g., "10m" (millicores)
    memory: string   // e.g., "128Mi"
  }
}

export interface PodMetrics {
  metadata: {
    name: string
    namespace: string
    creationTimestamp: string
  }
  timestamp: string
  window: string
  containers: ContainerMetrics[]
}

export interface NodeMetrics {
  metadata: {
    name: string
    creationTimestamp: string
  }
  timestamp: string
  window: string
  usage: {
    cpu: string
    memory: string
  }
}

// Fetch metrics for a specific pod
export function usePodMetrics(namespace: string, podName: string) {
  return useQuery<PodMetrics>({
    queryKey: ['pod-metrics', namespace, podName],
    queryFn: () => fetchJSON(`/metrics/pods/${namespace}/${podName}`),
    enabled: Boolean(namespace && podName),
    staleTime: 15000, // Metrics are fresh for 15 seconds
    refetchInterval: 30000, // Refresh every 30 seconds
  })
}

// Fetch metrics for a specific node
export function useNodeMetrics(nodeName: string) {
  return useQuery<NodeMetrics>({
    queryKey: ['node-metrics', nodeName],
    queryFn: () => fetchJSON(`/metrics/nodes/${nodeName}`),
    enabled: Boolean(nodeName),
    staleTime: 15000,
    refetchInterval: 30000,
  })
}

// ============================================================================
// Metrics History (local collection)
// ============================================================================

export interface MetricsDataPoint {
  timestamp: string
  cpu: number      // CPU in nanocores
  memory: number   // Memory in bytes
}

export interface ContainerMetricsHistory {
  name: string
  dataPoints: MetricsDataPoint[]
}

export interface PodMetricsHistory {
  namespace: string
  name: string
  containers: ContainerMetricsHistory[]
}

export interface NodeMetricsHistory {
  name: string
  dataPoints: MetricsDataPoint[]
}

// Fetch historical metrics for a pod (last ~1 hour)
export function usePodMetricsHistory(namespace: string, podName: string) {
  return useQuery<PodMetricsHistory>({
    queryKey: ['pod-metrics-history', namespace, podName],
    queryFn: () => fetchJSON(`/metrics/pods/${namespace}/${podName}/history`),
    enabled: Boolean(namespace && podName),
    staleTime: 25000, // Slightly less than poll interval
    refetchInterval: 30000, // Match the backend poll interval
  })
}

// Fetch historical metrics for a node (last ~1 hour)
export function useNodeMetricsHistory(nodeName: string) {
  return useQuery<NodeMetricsHistory>({
    queryKey: ['node-metrics-history', nodeName],
    queryFn: () => fetchJSON(`/metrics/nodes/${nodeName}/history`),
    enabled: Boolean(nodeName),
    staleTime: 25000,
    refetchInterval: 30000,
  })
}

// ============================================================================
// Pod Logs
// ============================================================================

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
    meta: {
      errorMessage: 'Failed to update resource',
      successMessage: 'Resource updated',
    },
    onSuccess: (_, variables) => {
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
    meta: {
      errorMessage: 'Failed to delete resource',
      successMessage: 'Resource deleted',
    },
    onSuccess: (_, variables) => {
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
    meta: {
      errorMessage: 'Failed to trigger CronJob',
      successMessage: 'CronJob triggered',
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
    meta: {
      errorMessage: 'Failed to suspend CronJob',
      successMessage: 'CronJob suspended',
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
    meta: {
      errorMessage: 'Failed to resume CronJob',
      successMessage: 'CronJob resumed',
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
    meta: {
      errorMessage: 'Failed to restart workload',
      successMessage: 'Workload restarting',
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
    meta: {
      errorMessage: 'Rollback failed',
      successMessage: 'Release rolled back',
    },
    onSuccess: (_, variables) => {
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
    meta: {
      errorMessage: 'Uninstall failed',
      successMessage: 'Release uninstalled',
    },
    onSuccess: () => {
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
    meta: {
      errorMessage: 'Upgrade failed',
      successMessage: 'Release upgraded',
    },
    onSuccess: (_, variables) => {
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
    meta: {
      errorMessage: 'Failed to apply values',
      successMessage: 'Values applied',
    },
    onSuccess: (_, variables) => {
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
    meta: {
      errorMessage: 'Failed to update repository',
      successMessage: 'Repository updated',
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
    meta: {
      errorMessage: 'Installation failed',
      successMessage: 'Chart installed',
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
// GitOps Mutation Factory
// ============================================================================

interface GitOpsMutationConfig<TVariables> {
  getPath: (variables: TVariables) => string
  errorMessage: string
  successMessage: string
  getInvalidateKeys: (variables: TVariables) => (string | undefined)[][]
}

/**
 * Factory function for creating GitOps mutation hooks with consistent patterns.
 * Handles fetch, error handling, meta messages, and query invalidation.
 */
function createGitOpsMutation<TVariables>(config: GitOpsMutationConfig<TVariables>) {
  return function useGitOpsMutation() {
    const queryClient = useQueryClient()
    return useMutation<GitOpsOperationResponse, Error, TVariables>({
      mutationFn: async (variables: TVariables): Promise<GitOpsOperationResponse> => {
        const response = await fetch(`${API_BASE}${config.getPath(variables)}`, {
          method: 'POST',
        })
        if (!response.ok) {
          const error = await response.json().catch(() => ({ error: 'Unknown error' }))
          throw new Error(error.error || `HTTP ${response.status}`)
        }
        return response.json() as Promise<GitOpsOperationResponse>
      },
      meta: {
        errorMessage: config.errorMessage,
        successMessage: config.successMessage,
      },
      onSuccess: (_, variables) => {
        config.getInvalidateKeys(variables).forEach(key =>
          queryClient.invalidateQueries({ queryKey: key })
        )
      },
    })
  }
}

// Common variable types
type FluxResourceVars = { kind: string; namespace: string; name: string }
type ArgoAppVars = { namespace: string; name: string }

// Standard invalidation patterns
const fluxInvalidateKeys = (v: FluxResourceVars) => [
  ['resources', v.kind],
  ['resource', v.kind, v.namespace, v.name],
]
const argoInvalidateKeys = (v: ArgoAppVars) => [
  ['resources', 'applications'],
  ['resource', 'applications', v.namespace, v.name],
]

// ============================================================================
// FluxCD API hooks
// ============================================================================

export const useFluxReconcile = createGitOpsMutation<FluxResourceVars>({
  getPath: (v) => `/flux/${v.kind}/${v.namespace}/${v.name}/reconcile`,
  errorMessage: 'Failed to trigger reconciliation',
  successMessage: 'Reconciliation triggered',
  getInvalidateKeys: fluxInvalidateKeys,
})

export const useFluxSuspend = createGitOpsMutation<FluxResourceVars>({
  getPath: (v) => `/flux/${v.kind}/${v.namespace}/${v.name}/suspend`,
  errorMessage: 'Failed to suspend resource',
  successMessage: 'Resource suspended',
  getInvalidateKeys: fluxInvalidateKeys,
})

export const useFluxResume = createGitOpsMutation<FluxResourceVars>({
  getPath: (v) => `/flux/${v.kind}/${v.namespace}/${v.name}/resume`,
  errorMessage: 'Failed to resume resource',
  successMessage: 'Resource resumed',
  getInvalidateKeys: fluxInvalidateKeys,
})

export const useFluxSyncWithSource = createGitOpsMutation<FluxResourceVars>({
  getPath: (v) => `/flux/${v.kind}/${v.namespace}/${v.name}/sync-with-source`,
  errorMessage: 'Failed to sync with source',
  successMessage: 'Sync with source triggered',
  getInvalidateKeys: (v) => [
    ...fluxInvalidateKeys(v),
    // Also invalidate source resources as they were reconciled too
    ['resources', 'gitrepositories'],
    ['resources', 'ocirepositories'],
    ['resources', 'helmrepositories'],
  ],
})

// ============================================================================
// ArgoCD API hooks
// ============================================================================

export const useArgoSync = createGitOpsMutation<ArgoAppVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/sync`,
  errorMessage: 'Failed to trigger sync',
  successMessage: 'Sync initiated',
  getInvalidateKeys: argoInvalidateKeys,
})

export const useArgoTerminate = createGitOpsMutation<ArgoAppVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/terminate`,
  errorMessage: 'Failed to terminate sync',
  successMessage: 'Sync terminated',
  getInvalidateKeys: argoInvalidateKeys,
})

export const useArgoSuspend = createGitOpsMutation<ArgoAppVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/suspend`,
  errorMessage: 'Failed to suspend application',
  successMessage: 'Application suspended',
  getInvalidateKeys: argoInvalidateKeys,
})

export const useArgoResume = createGitOpsMutation<ArgoAppVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/resume`,
  errorMessage: 'Failed to resume application',
  successMessage: 'Application resumed',
  getInvalidateKeys: argoInvalidateKeys,
})

// useArgoRefresh has a unique parameter (hard), so it's defined separately
export function useArgoRefresh() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ namespace, name, hard = false }: { namespace: string; name: string; hard?: boolean }) => {
      const params = hard ? '?type=hard' : ''
      const response = await fetch(`${API_BASE}/argo/applications/${namespace}/${name}/refresh${params}`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to refresh application',
      successMessage: 'Application refreshed',
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'applications'] })
      queryClient.invalidateQueries({ queryKey: ['resource', 'applications', variables.namespace, variables.name] })
    },
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
