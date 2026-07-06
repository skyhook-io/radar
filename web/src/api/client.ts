import { useEffect, useRef } from 'react'
import type { AppHistory, AppRow } from '@skyhook-io/k8s-ui'
import { useQuery, useMutation, useQueryClient, skipToken } from '@tanstack/react-query'
import { showApiError, showApiSuccess } from '../components/ui/Toast'
import { useCanHelmWrite } from '../contexts/CapabilitiesContext'
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
  NotesDiff,
  HooksDiff,
  ResourceDiff,
  UpgradeInfo,
  BatchUpgradeInfo,
  ValuesDiff,
  ValuesPreviewResponse,
  HelmRepository,
  ChartSearchResult,
  ChartDetail,
  InstallChartRequest,
  ArtifactHubSearchResult,
  ArtifactHubChartDetail,
  GitOpsResourceTree,
  GitOpsInsight,
} from '../types'
import type { GitOpsOperationResponse } from '../types/gitops'
import { getApiBase, getAuthHeaders, getCredentialsMode, getBasename, routePath } from './config'
import { pluralToKind } from '../utils/navigation'

// Auto-refresh cadences (ms) — named constants for each polled hook's
// refetchInterval below, so the poll rate reads clearly at each call site.
const DASHBOARD_REFRESH_INTERVAL_MS = 30_000
const AUDIT_REFRESH_INTERVAL_MS = 60_000
const ISSUES_REFRESH_INTERVAL_MS = 30_000
const COST_REFRESH_INTERVAL_MS = 60_000
const CHANGES_REFRESH_INTERVAL_MS = 60_000
const APPLICATIONS_REFRESH_INTERVAL_MS = 60_000

// Wrapper around fetch that always includes credentials (for session cookies)
// and handles 401 responses globally. Merges caller-provided headers with
// auth headers from the config module so library consumers (Radar Hub) can
// inject Authorization bearer tokens without each call site knowing.
export function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers)
  for (const [k, v] of Object.entries(getAuthHeaders())) {
    if (!headers.has(k)) headers.set(k, v)
  }
  return fetch(input, { credentials: getCredentialsMode(), ...init, headers }).then(async response => {
    const authPrefix = `${getBasename()}/auth`
    if (response.status === 401 && !window.location.pathname.startsWith(authPrefix)) {
      // Save current location so user returns to where they were after re-auth.
      // Editor draft is auto-saved by EditableYamlView via sessionStorage.
      try { sessionStorage.setItem('radar_return_path', window.location.pathname + window.location.search) } catch { /* best-effort */ }

      let authMode: string | undefined
      try {
        const body = await response.clone().json()
        authMode = body.authMode
      } catch {
        console.warn('Authentication required (unable to determine auth mode)')
      }

      if (authMode === 'oidc') {
        window.location.href = routePath('/auth/login')
      } else {
        // Proxy mode or unknown — reload is safe for both (proxy re-injects headers,
        // unknown avoids redirecting to /auth/login which doesn't exist in proxy mode).
        // Guard against infinite reload if proxy is misconfigured and keeps returning 401.
        const lastReload = sessionStorage.getItem('radar_proxy_reload')
        const now = Date.now()
        if (!lastReload || now - parseInt(lastReload) > 5000) {
          try { sessionStorage.setItem('radar_proxy_reload', String(now)) } catch { /* best-effort */ }
          window.location.reload()
        }
      }
    }
    return response
  })
}

// ApiError preserves HTTP status code for callers to distinguish 403/404/500 etc.
export class ApiError extends Error {
  status: number
  data?: Record<string, unknown>
  constructor(message: string, status: number, data?: Record<string, unknown>) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.data = data
  }
}

export function isForbiddenError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 403
}

const METRICS_API_GROUP_TOKENS = ['metrics', 'k8s', 'io'] as const

function mentionsMetricsAPIGroup(message: string): boolean {
  const tokens = message.split(/[^a-z0-9]+/).filter(Boolean)
  return tokens.some((token, index) => (
    token === METRICS_API_GROUP_TOKENS[0] &&
    tokens[index + 1] === METRICS_API_GROUP_TOKENS[1] &&
    tokens[index + 2] === METRICS_API_GROUP_TOKENS[2]
  ))
}

function hasMetricsUnavailablePhrase(message: string): boolean {
  return (
    message.includes('may not be installed') ||
    message.includes('not found') ||
    message.includes('could not find the requested resource') ||
    message.includes('no matches for kind') ||
    message.includes('no resource matches') ||
    message.includes('no metrics known') ||
    message.includes('not available') ||
    message.includes('unable to fetch metrics') ||
    message.includes('currently unable to handle the request')
  )
}

export function isMetricsUnavailableError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  if (error.status !== 404 && error.status !== 500) return false
  return [error.message, error.data?.error].some((message) => {
    if (typeof message !== 'string') return false
    const normalized = message.toLowerCase()
    const hasMetricsSignal = (
      normalized.includes('metrics-server') ||
      mentionsMetricsAPIGroup(normalized) ||
      normalized.includes('pod metrics') ||
      normalized.includes('node metrics')
    )
    return hasMetricsSignal && hasMetricsUnavailablePhrase(normalized)
  })
}

export async function fetchJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await apiFetch(`${getApiBase()}${path}`, signal ? { signal } : undefined)
  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new ApiError(errorData.error || `HTTP ${response.status}`, response.status, errorData)
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
  group?: string
  severity: 'critical' | 'high' | 'medium' | 'warning' | 'info'
  reason: string
  message: string
  age: string
  ageSeconds: number
  duration: string
  durationSeconds: number
  podCount?: number
}

export interface WorkloadCount {
  total: number
  ready: number
  unready: number
}

export interface DashboardMetrics {
  cpu?: MetricSummary
  memory?: MetricSummary
  // When false, only requests/capacity are meaningful — live usage (from
  // metrics-server) is unavailable and usage fields are zero.
  usageAvailable: boolean
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
  gateways?: number
  routes?: number
  nodes: { total: number; ready: number; notReady: number; cordoned: number }
  namespaces: number
  jobs: { total: number; active: number; succeeded: number; failed: number }
  cronJobs: { total: number; active: number; suspended: number }
  configMaps: number
  secrets: number
  pvcs: { total: number; bound: number; pending: number; unbound: number }
  restricted?: string[] // Resource kinds the user cannot list due to RBAC
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
  restricted?: boolean // True when user lacks permissions to list Helm releases (RBAC-denied)
  // error + errorCode populated when the Helm read failed for a non-RBAC
  // reason (client not initialized, unconfigured, network). Surfaced
  // via the dashboard widget so empty results aren't mistaken for
  // "this cluster has zero releases."
  error?: string
  errorCode?: string
}

export interface DashboardCRDCount {
  kind: string
  name: string
  group: string
  count: number
}

// Re-export shared types from k8s-ui — single source of truth
import type { AuditCardData, AuditFinding, ResourceGroup, CheckMeta, Check, Issue, IssueRecentChange } from '@skyhook-io/k8s-ui'
export type DashboardAudit = AuditCardData
export type { AuditFinding, ResourceGroup, CheckMeta, Check }

export interface AuditResponse {
  summary: DashboardAudit
  findings: AuditFinding[]
  groups: ResourceGroup[]
  checks: Record<string, CheckMeta>
  // Remediation-queue rollup: findings grouped by check, prioritized. Present
  // on the non-raw scan (standalone + embedded per-cluster views); the Checks
  // queue renders this.
  groupedChecks?: Check[]
}

export interface DashboardCertificateHealth {
  total: number
  healthy: number
  warning: number
  critical: number
  expired: number
}

export interface DashboardNetworkPolicyCoverage {
  totalPolicies: number
  coveredWorkloads: number
  totalWorkloads: number
}

export interface DashboardGitOpsControllers {
  // Aggregate roll-up across all detected controllers.
  status: 'healthy' | 'degraded' | 'crashing'
  controllers: DashboardGitOpsController[]
}

export interface DashboardGitOpsController {
  name: string
  // Tool vocabulary aligns with the backend `ctrlTool*` constants in
  // internal/server/dashboard_gitops.go and the GitOps tree-builder
  // tags in pkg/gitops/tree/graph.go (`gitopsTool`). Keep these three
  // surfaces in sync — diverging vocabulary across surfaces was a real
  // source of confusion until consolidated.
  tool: 'argocd' | 'fluxcd'
  namespace: string
  ready: number
  total: number
  // Per-controller status (aggregate is in the parent and excludes
  // 'pending' — it normalizes into 'degraded' there).
  status: 'healthy' | 'degraded' | 'crashing' | 'pending'
  // Reason for the crash when status === 'crashing'. Common values:
  // "CrashLoopBackOff", "Error". Empty for non-crashing states.
  crashReason?: string
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
  metrics: DashboardMetrics | null
  metricsServerAvailable: boolean
  certificateHealth: DashboardCertificateHealth | null
  networkPolicyCoverage: DashboardNetworkPolicyCoverage | null
  audit: DashboardAudit | null
  gitopsControllers: DashboardGitOpsControllers | null
  nodeVersionSkew: { versions: Record<string, string[]>; minVersion: string; maxVersion: string } | null
  deferredLoading?: boolean // True while deferred informers (secrets, events, etc.) are still syncing
  partialData?: string[] // Critical kinds promoted at first paint that haven't yet finished syncing (live-filtered)
  accessRestricted?: boolean // True when user has no namespace access (RBAC)
}

export interface DashboardCRDsResponse {
  topCRDs: DashboardCRDCount[]
}

export function useDashboard(namespaces: string[] = [], options?: { enabled?: boolean }) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<DashboardResponse>({
    queryKey: ['dashboard', namespaces],
    queryFn: () => fetchJSON(`/dashboard${params}`),
    enabled: options?.enabled ?? true,
    staleTime: 15000, // 15 seconds
    refetchInterval: DASHBOARD_REFRESH_INTERVAL_MS,
  })
}

// Best practices
export function useAudit(namespaces: string[] = []) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<AuditResponse>({
    queryKey: ['audit', namespaces],
    queryFn: () => fetchJSON(`/audit${params}`),
    staleTime: 30000,
    refetchInterval: AUDIT_REFRESH_INTERVAL_MS,
    placeholderData: (prev) => prev,
  })
}

// Live cluster Issues — the grouped triage queue (radar's /api/issues =
// internal/issues.Compose+Classify+Group). Single-cluster here; the Hub fleet
// view fans the same shape across clusters. Do not carry old data across query
// keys: issues are scope-sensitive, so namespace changes must not show the
// previous scope's rows while the new scope fetches.
// total = rows returned (after the cap); total_matched = rows that matched
// before the cap. total_matched > total means the queue was truncated — surface
// that honestly rather than presenting a capped list as if it were complete.
export interface IssuesResponse {
  issues: Issue[]
  total?: number
  total_matched?: number
  recent_changes?: IssueRecentChange[]
  recent_changes_reason?: string
  // Present only when RBAC visibility is incomplete (absent = full access).
  // state 'degraded' means core workload reads are denied, so an empty list may
  // mean "can't see" rather than "nothing broken" — the UI must say so.
  visibility?: { state?: string; impact?: string }
}

export function useIssues(namespaces: string[] = []) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<IssuesResponse>({
    queryKey: ['issues', namespaces],
    queryFn: () => fetchJSON(`/issues${params}`),
    staleTime: 30000,
    refetchInterval: ISSUES_REFRESH_INTERVAL_MS,
  })
}

export function useResourceAudit(kind: string, namespace: string, name: string) {
  return useQuery<AuditFinding[]>({
    queryKey: ['audit', 'resource', kind, namespace, name],
    queryFn: () => fetchJSON(`/audit/resource/${kind}/${namespace}/${name}`),
    staleTime: 30000,
  })
}

// Live Issues that touch ONE resource — its own issues plus, for a workload, its
// owned pods' issues (server-side owner rollup via issues.RelatedIssues). Backs
// the "Operational Issues" section in the resource detail. Cluster-scoped
// resources pass "_" for namespace; namespaced ones also scope the scan via
// ?namespaces= for a cheap, bounded Compose.
export function useResourceIssues(kind: string, group: string | undefined, namespace: string, name: string, enabled = true) {
  const clusterScoped = !namespace
  const pathNs = clusterScoped ? '_' : encodeURIComponent(namespace)
  const params = new URLSearchParams()
  if (group) params.set('group', group)
  const path = `/issues/resource/${encodeURIComponent(kind)}/${pathNs}/${encodeURIComponent(name)}`
  const qs = params.toString()
  return useQuery<Issue[]>({
    queryKey: ['issues', 'resource', kind, group ?? '', namespace, name],
    queryFn: () => fetchJSON(`${path}${qs ? `?${qs}` : ''}`),
    // No refetchInterval: a drawer doesn't need to poll; staleTime keeps it fresh
    // on reopen without re-running an uncapped Compose every 30s.
    staleTime: 30000,
    enabled: enabled && !!kind && !!name,
  })
}

// Audit settings
export interface AuditSettings {
  ignoredNamespaces: string[]
  disabledChecks: string[]
}

export function useAuditSettings() {
  return useQuery<AuditSettings>({
    queryKey: ['audit-settings'],
    queryFn: () => fetchJSON('/settings/audit'),
    staleTime: 60000,
  })
}

export function useUpdateAuditSettings() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (settings: AuditSettings) => {
      const resp = await apiFetch(`${getApiBase()}/settings/audit`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings),
      })
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(body.error || `HTTP ${resp.status}`)
      }
      return resp.json()
    },
    meta: {
      errorMessage: 'Failed to save audit settings',
      successMessage: 'Audit settings saved',
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['audit-settings'] })
      queryClient.invalidateQueries({ queryKey: ['audit'] })
      queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    },
  })
}

// Certificate expiry for TLS secrets (used in secrets list view)
export interface CertExpiry {
  daysLeft: number
  expired?: boolean
}

export function useSecretCertExpiry(namespaces: string[] = [], enabled = true) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<Record<string, CertExpiry>>({
    queryKey: ['secret-cert-expiry', namespaces],
    queryFn: () => fetchJSON(`/secrets/certificate-expiry${params}`),
    enabled,
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// CRD counts - loaded lazily after main dashboard
export function useDashboardCRDs(namespaces: string[] = []) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<DashboardCRDsResponse>({
    queryKey: ['dashboard-crds', namespaces],
    queryFn: () => fetchJSON(`/dashboard/crds${params}`),
    staleTime: 30000, // 30 seconds - less frequent updates
    refetchInterval: 60000, // Refresh every minute
  })
}

// Helm summary - loaded lazily after main dashboard (Helm SDK lists K8s secrets, ~2-3s)
export function useDashboardHelm(namespaces: string[] = []) {
  const params = namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
  return useQuery<DashboardHelmSummary>({
    queryKey: ['dashboard-helm', namespaces],
    queryFn: () => fetchJSON(`/dashboard/helm${params}`),
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// ============================================================================
// OpenCost
// ============================================================================

export interface OpenCostNamespaceCost {
  name: string
  hourlyCost: number
  cpuCost: number
  memoryCost: number
  storageCost?: number
  cpuUsageCost?: number
  memoryUsageCost?: number
  efficiency?: number
  idleCost?: number
}

export type CostUnavailableReason = 'no_prometheus' | 'no_metrics' | 'query_error'

export interface OpenCostSummary {
  available: boolean
  reason?: CostUnavailableReason
  currency?: string
  window?: string
  totalHourlyCost?: number
  totalStorageCost?: number
  totalIdleCost?: number
  clusterEfficiency?: number
  namespaces?: OpenCostNamespaceCost[]
}

export function useOpenCostSummary() {
  return useQuery<OpenCostSummary>({
    queryKey: ['opencost-summary'],
    queryFn: () => fetchJSON('/opencost/summary'),
    refetchInterval: COST_REFRESH_INTERVAL_MS,
    staleTime: 30000,
    placeholderData: (prev) => prev, // Keep previous data visible during refetch
  })
}

// Workload-level cost breakdown for a namespace
export interface OpenCostWorkloadCost {
  name: string
  kind: string
  hourlyCost: number
  cpuCost: number
  memoryCost: number
  replicas: number
  cpuUsageCost?: number
  memoryUsageCost?: number
  efficiency?: number
  idleCost?: number
}

export interface OpenCostWorkloadResponse {
  available: boolean
  reason?: CostUnavailableReason
  namespace: string
  workloads: OpenCostWorkloadCost[]
}

export function useOpenCostWorkloads(namespace: string, options?: { enabled?: boolean }) {
  return useQuery<OpenCostWorkloadResponse>({
    queryKey: ['opencost-workloads', namespace],
    queryFn: () => fetchJSON(`/opencost/workloads?namespace=${encodeURIComponent(namespace)}`),
    enabled: (options?.enabled ?? true) && Boolean(namespace),
    staleTime: 30000,
  })
}

// Cost trend over time
export type CostTimeRange = '6h' | '24h' | '7d'

export interface OpenCostTrendDataPoint {
  timestamp: number
  value: number
}

export interface OpenCostTrendSeries {
  namespace: string
  dataPoints: OpenCostTrendDataPoint[]
}

export interface OpenCostTrendResponse {
  available: boolean
  reason?: CostUnavailableReason
  range: string
  series?: OpenCostTrendSeries[]
}

export function useOpenCostTrend(range_: CostTimeRange = '24h') {
  return useQuery<OpenCostTrendResponse>({
    queryKey: ['opencost-trend', range_],
    queryFn: () => fetchJSON(`/opencost/trend?range=${range_}`),
    staleTime: 60000,
    refetchInterval: 120000, // Refresh every 2 minutes
    placeholderData: (prev) => prev,
  })
}

// Node cost breakdown
export interface OpenCostNodeCost {
  name: string
  instanceType?: string
  region?: string
  hourlyCost: number
  cpuCost: number
  memoryCost: number
}

export interface OpenCostNodeResponse {
  available: boolean
  reason?: CostUnavailableReason
  nodes?: OpenCostNodeCost[]
}

export function useOpenCostNodes() {
  return useQuery<OpenCostNodeResponse>({
    queryKey: ['opencost-nodes'],
    queryFn: () => fetchJSON('/opencost/nodes'),
    staleTime: 60000,
    refetchInterval: 120000,
    placeholderData: (prev) => prev,
  })
}

// Cluster info
export function useClusterInfo() {
  const query = useQuery<ClusterInfo>({
    queryKey: ['cluster-info'],
    queryFn: () => fetchJSON('/cluster-info'),
    staleTime: 60000, // 1 minute
    // Poll faster when CRD discovery is in progress
    refetchInterval: (query) => {
      const status = query.state.data?.crdDiscoveryStatus
      return status === 'discovering' ? 2000 : false
    },
  })
  return query
}

// Version check
export type InstallMethod = 'homebrew' | 'krew' | 'scoop' | 'direct' | 'desktop'

export interface VersionInfo {
  currentVersion: string
  latestVersion?: string
  updateAvailable: boolean
  releaseUrl?: string
  releaseNotes?: string
  installMethod: InstallMethod
  updateCommand?: string
  error?: string
}

export function useVersionCheck() {
  return useQuery<VersionInfo>({
    queryKey: ['version-check'],
    queryFn: () => fetchJSON('/version-check'),
    staleTime: 60 * 60 * 1000, // 1 hour
    retry: false, // Don't retry on failure
  })
}

// ============================================================================
// Desktop Update API hooks
// ============================================================================

export type DesktopUpdateState = 'idle' | 'downloading' | 'ready' | 'applying' | 'error'

export interface DesktopUpdateStatus {
  state: DesktopUpdateState
  progress?: number // 0.0 - 1.0 during download
  version?: string
  error?: string
}

export function useStartDesktopUpdate() {
  return useMutation({
    mutationFn: async () => {
      const response = await apiFetch(`${getApiBase()}/desktop/update`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to start update',
    },
  })
}

export function useDesktopUpdateStatus(enabled: boolean) {
  return useQuery<DesktopUpdateStatus>({
    queryKey: ['desktop-update-status'],
    queryFn: () => fetchJSON('/desktop/update/status'),
    enabled,
    refetchInterval: 500, // Poll every 500ms during active update
    staleTime: 0, // Always refetch
  })
}

export function useApplyDesktopUpdate() {
  return useMutation({
    mutationFn: async () => {
      const response = await apiFetch(`${getApiBase()}/desktop/update/apply`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to apply update',
      successMessage: 'Update applied — restarting...',
    },
  })
}

// Runtime stats for debug overlay
export interface RuntimeStats {
  heapMB: number
  heapObjectsK: number
  goroutines: number
  uptimeSeconds: number
  typedInformers?: number
  dynamicInformers?: number
}

// ============================================================================
// Resource search (GET /api/search) — the existing search engine, RBAC-filtered
// and ranked server-side. Mirrors internal/search.Hit / .Result.
// ============================================================================

export interface SearchMatchedField {
  token: string
  /** "name" | "namespace" | "label:k" | "annotation:k" | "image" | "kind" | "content:path" */
  site: string
  score: number
}

export interface SearchSummaryContext {
  health?: string
  issueCount?: number
  managedBy?: { kind?: string; name?: string } | null
}

export interface SearchHit {
  score: number
  kind: string
  group?: string
  namespace?: string
  name: string
  matched?: SearchMatchedField[]
  summaryContext?: SearchSummaryContext
  /** Embedder (Radar Hub) only: the cluster this hit belongs to, for
   *  cross-cluster fleet search. Standalone Radar (single-cluster) leaves these
   *  unset — the omnibar keys + displays the cluster only when present. */
  cluster?: string
  clusterName?: string
}

export interface SearchResult {
  hits: SearchHit[]
  total: number
  searched: number
  total_matched: number
}

const SEARCH_MIN_QUERY = 2

// useSearch hits the resource-search engine. The caller supplies the (already
// debounced) query; the hook is enabled only past the min length. include=none
// keeps the per-hit payload identity-only; context=summary attaches
// health/issueCount per hit (rich rows). React Query's AbortSignal cancels
// overlapping scans on a new query. keepPreviousData avoids flicker while the
// next query resolves.
export function useSearch(query: string, opts?: { limit?: number; context?: 'summary' | 'none'; enabled?: boolean; globalNs?: boolean }) {
  const trimmed = query.trim()
  const enabled = (opts?.enabled ?? true) && trimmed.length >= SEARCH_MIN_QUERY
  const limit = opts?.limit ?? 20
  const context = opts?.context ?? 'summary'
  // globalNs makes search ignore the per-user namespace-switcher pick and scan
  // the user's full RBAC ceiling (scope then comes only from the query's `ns:`
  // tokens). The omnibar opts in so ⌘K is a genuinely global lookup.
  const globalNs = opts?.globalNs ?? false
  return useQuery<SearchResult>({
    queryKey: ['search', trimmed, limit, context, globalNs],
    queryFn: ({ signal }) =>
      fetchJSON<SearchResult>(`/search?q=${encodeURIComponent(trimmed)}&limit=${limit}&include=none&context=${context}${globalNs ? '&globalNs=1' : ''}`, signal),
    enabled,
    staleTime: 2000,
    placeholderData: (prev) => prev, // keepPreviousData
  })
}

export interface HealthResponse {
  status: string
  resourceCount: number
  runtime: RuntimeStats
}

export function useRuntimeStats(enabled: boolean = true) {
  return useQuery<HealthResponse>({
    queryKey: ['health'],
    queryFn: () => fetchJSON('/health'),
    staleTime: 2000, // 2 seconds
    refetchInterval: enabled ? 3000 : false, // Refresh every 3 seconds when enabled
    enabled,
  })
}

// Capabilities (RBAC-based feature flags)
export function useCapabilities() {
  return useQuery<Capabilities>({
    queryKey: ['capabilities'],
    queryFn: () => fetchJSON('/capabilities'),
    staleTime: 60000, // 1 minute - cached on backend too
    refetchInterval: 60000, // Re-check periodically so transient failures self-correct
  })
}

// Namespace-scoped capabilities. Users with namespace-scoped RoleBindings may
// have these permissions in specific namespaces.
export function useNamespaceCapabilities(namespace: string | undefined, globalCaps: Capabilities | undefined) {
  const needsCheck = namespace && globalCaps
  return useQuery<Capabilities>({
    queryKey: ['capabilities', namespace],
    queryFn: () => fetchJSON(`/capabilities?namespace=${encodeURIComponent(namespace!)}`),
    enabled: !!needsCheck,
    staleTime: 60000,
  })
}

// Auth
export type CloudRole = 'owner' | 'member' | 'viewer'

export interface AuthMe {
  authEnabled: boolean
  authMode?: string
  username?: string
  groups?: string[]
  /** Pre-computed Cloud tier from `cloud:<tier>` group prefix.
   *  Absent when not running under Cloud (OSS, OIDC, no role group). */
  cloudRole?: CloudRole
  /** Proxy mode only: whether an upstream sign-out URL is configured.
   *  When false, logout clears Radar's cookie but the proxy may re-auth
   *  the same user on the next request. */
  proxyLogoutConfigured?: boolean
}

export function useAuthMe() {
  return useQuery<AuthMe>({
    queryKey: ['auth-me'],
    queryFn: () => fetchJSON('/auth/me'),
    staleTime: 300000, // 5 minutes
  })
}

// Tier ordering for Cloud-role gates. Mirrors radar OSS pkg/auth
// CloudRole.AtLeast — the frontend must agree with the backend on what
// "member-or-higher" means; otherwise we'd hide a button the
// backend would happily honor (or vice versa).
const CLOUD_ROLE_RANK: Record<string, number> = { viewer: 1, member: 2, owner: 3 }

/**
 * useCloudRole returns the caller's Cloud tier (`owner` / `member` /
 * `viewer`) and a `canAtLeast(min)` gate. When no Cloud role is
 * present (OSS, OIDC, no role group, OR auth/me is still loading),
 * `canAtLeast` returns true — the gate is strictly additive for
 * Cloud-attributed users, mirroring the backend's `requireCloudRole`
 * semantics. Use for passive content gating (panels, sections); use
 * `useCanHelmAct` (or similar) for *click-prone* surfaces where you
 * need fail-closed behavior during the auth/me round-trip to prevent
 * a viewer from clicking through during the loading window.
 *
 * Why optimistic during load: the gated empty state ("Your role can't
 * view…") rendered briefly to OSS / kubectl-plugin users before
 * auth/me resolves is a worse regression than a Cloud viewer seeing
 * a content tab populate for a tick before being gated out. Click-
 * prevention belongs in the action-button hook, not here.
 */
export function useCloudRole() {
  const { data, isLoading } = useAuthMe()
  const role = data?.cloudRole
  return {
    role,
    isLoading,
    isCloudUser: !!role,
    canAtLeast: (min: CloudRole) => {
      if (!role) return true // not Cloud-attributed (incl. still-loading) → no gate
      return (CLOUD_ROLE_RANK[role] ?? 0) >= (CLOUD_ROLE_RANK[min] ?? 0)
    },
  }
}

/**
 * useCanHelmAct combines the K8s capability gate (rbac.helm=true) and
 * the Cloud role gate (member+) into a single answer for any Helm
 * write or sensitive-read button. Returns { allowed, reason } so the
 * tooltip can explain which gate failed.
 *
 * Cloud role check runs FIRST so the message is actionable for Cloud
 * users — telling them "Helm write permissions required" is wrong if
 * the chart is fine and the actual gate is their viewer role.
 */
export function useCanHelmAct(): { allowed: boolean; reason?: string } {
  const helmWrite = useCanHelmWrite()
  const { role, canAtLeast, isLoading } = useCloudRole()
  // Fail-closed for action buttons during the auth/me round-trip:
  // a Cloud viewer who clicks during loading would otherwise fire a
  // real request that gets 403'd. For OSS / kubectl-plugin the
  // round-trip is sub-ms so this is imperceptible; for Cloud it
  // prevents the click-through window. Distinct from useCloudRole's
  // canAtLeast (which is optimistic during loading) because passive
  // content gates don't have a click-handler to misfire.
  if (isLoading) {
    return { allowed: false, reason: 'Loading permissions…' }
  }
  if (!canAtLeast('member')) {
    return {
      allowed: false,
      reason: `Your Radar Cloud role (${role ?? 'unknown'}) cannot run Helm operations. Ask a member or owner.`,
    }
  }
  if (!helmWrite) {
    return {
      allowed: false,
      reason: 'Helm write permissions required. Set rbac.helm=true in the Radar Helm chart values.',
    }
  }
  return { allowed: true }
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
export function useTopology(namespaces: string[], viewMode: string = 'resources', options?: { enabled?: boolean }) {
  const params = new URLSearchParams()
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  if (viewMode) params.set('view', viewMode)
  const queryString = params.toString()

  return useQuery<Topology>({
    queryKey: ['topology', namespaces, viewMode],
    queryFn: () => fetchJSON(`/topology${queryString ? `?${queryString}` : ''}`),
    staleTime: 5000, // 5 seconds
    enabled: options?.enabled !== false,
  })
}

export function useApplications(namespaces: string[], options?: { enabled?: boolean }) {
  const params = new URLSearchParams()
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  const queryString = params.toString()

  return useQuery<{ applications: AppRow[] }>({
    queryKey: ['applications', namespaces],
    queryFn: () => fetchJSON(`/applications${queryString ? `?${queryString}` : ''}`),
    enabled: options?.enabled ?? true,
    staleTime: 30_000,
    refetchInterval: APPLICATIONS_REFRESH_INTERVAL_MS,
  })
}

export function useApplicationHistory(appKey: string | undefined, namespaces: string[], options?: { enabled?: boolean }) {
  const params = new URLSearchParams()
  if (appKey) params.set('app', appKey)
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  const queryString = params.toString()

  return useQuery<AppHistory>({
    queryKey: ['application-history', appKey, namespaces],
    queryFn: appKey ? () => fetchJSON(`/applications/history?${queryString}`) : skipToken,
    enabled: Boolean(appKey) && (options?.enabled ?? true),
    staleTime: 15_000,
    refetchInterval: APPLICATIONS_REFRESH_INTERVAL_MS,
  })
}

export function useGitOpsTree(kind: string, namespace: string, name: string, group?: string, namespaces: string[] = []) {
  const ns = namespace || '_'
  const params = new URLSearchParams()
  if (group) params.set('group', group)
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  const queryString = params.toString()

  return useQuery<GitOpsResourceTree>({
    queryKey: ['gitops-tree', kind, namespace, name, group, namespaces],
    queryFn: () => fetchJSON(`/gitops/tree/${kind}/${ns}/${name}${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(kind && name),
    staleTime: 5000,
  })
}

// Poll fast (2s) while a sync/rollback is in flight so the user sees the
// outcome quickly; otherwise rely on staleTime + manual refetch. Argo flips
// operationState.phase from "Running" -> Succeeded/Failed when done, so this
// auto-quiesces on completion.
const INSIGHTS_RUNNING_POLL_MS = 2000

export function useGitOpsInsights(kind: string, namespace: string, name: string, group?: string, namespaces: string[] = []) {
  const ns = namespace || '_'
  const params = new URLSearchParams()
  if (group) params.set('group', group)
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  const queryString = params.toString()

  return useQuery<GitOpsInsight>({
    queryKey: ['gitops-insights', kind, namespace, name, group, namespaces],
    queryFn: () => fetchJSON(`/gitops/insights/${kind}/${ns}/${name}${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(kind && name),
    staleTime: 5000,
    refetchInterval: (query) => {
      const phase = query.state.data?.summary?.operationPhase
      return phase === 'Running' ? INSIGHTS_RUNNING_POLL_MS : false
    },
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
    certificateInfo: query.data?.certificateInfo,
    hpaDiagnosis: query.data?.hpaDiagnosis,
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
export function useResources<T>(
  kind: string,
  namespace?: string,
  group?: string,
  options?: { enabled?: boolean; refetchInterval?: number | false },
) {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (group) params.set('group', group)
  const queryString = params.toString()

  return useQuery<T[]>({
    queryKey: ['resources', kind, group, namespace],
    queryFn: () => fetchJSON(`/resources/${kind}${queryString ? `?${queryString}` : ''}`),
    enabled: (options?.enabled ?? true) && Boolean(kind),
    staleTime: 30000, // 30 seconds - matches refetchInterval in ResourcesView
    refetchInterval: options?.refetchInterval,
  })
}

// Timeline changes (unified view of changes + K8s events)
export interface UseChangesOptions {
  namespaces?: string[]
  kind?: string
  timeRange?: TimeRange
  filter?: string // Filter preset name ('default', 'all', 'warnings-only', 'workloads')
  includeK8sEvents?: boolean
  includeManaged?: boolean
  includeDeleted?: boolean
  limit?: number
  enabled?: boolean
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
  const { namespaces = [], kind, timeRange = '1h', filter = 'all', includeK8sEvents = true, includeManaged = false, includeDeleted = true, limit = 200, enabled = true } = options

  const params = new URLSearchParams()
  if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
  if (kind) params.set('kind', kind)
  if (filter) params.set('filter', filter)
  if (!includeK8sEvents) params.set('include_k8s_events', 'false')
  if (includeManaged) params.set('include_managed', 'true')
  if (!includeDeleted) params.set('include_deleted', 'false')
  params.set('limit', String(limit))

  const sinceDate = getTimeRangeDate(timeRange)
  if (sinceDate) {
    params.set('since', sinceDate.toISOString())
  }

  const queryString = params.toString()

  return useQuery<TimelineEvent[]>({
    queryKey: ['changes', namespaces, kind, timeRange, filter, includeK8sEvents, includeManaged, includeDeleted, limit],
    queryFn: () => fetchJSON(`/changes${queryString ? `?${queryString}` : ''}`),
    staleTime: 5000, // Consider data stale after 5 seconds to ensure fresh data on navigation
    refetchInterval: CHANGES_REFRESH_INTERVAL_MS, // SSE handles real-time updates; this is a fallback
    enabled,
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

export interface ResourceEventsResult {
  k8sEvents: TimelineEvent[]
  updates: TimelineEvent[]
  isLoading: boolean
  // Per-stream errors are surfaced separately so the UI can distinguish
  // "this stream failed" from "no data" — silent fallback to [] would
  // reproduce the exact failure mode #547 is about.
  k8sError: Error | null
  updatesError: Error | null
}

// K8s events and resource updates are fetched separately so a high-frequency
// informer update stream (e.g. a CrashLoop status field flapping every few
// seconds) can never starve out user-meaningful K8s events under a shared limit.
export function useResourceEvents(kind: string, namespace: string, name: string): ResourceEventsResult {
  // The timeline store keys events by their K8s Kind (singular PascalCase, e.g. "Pod"),
  // but callers pass the URL-form kind ("pods").
  const singularKind = pluralToKind(kind)
  const since = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()

  // Include managed resources — when viewing a specific resource (e.g. a Pod owned
  // by a ReplicaSet, or a K8s Event whose involvedObject is the Pod itself), the
  // default preset's IsManaged() filter would otherwise drop everything.
  const baseParams = () => {
    const p = new URLSearchParams()
    p.set('namespace', namespace)
    p.set('kind', singularKind)
    p.set('name', name)
    p.set('include_managed', 'true')
    p.set('since', since)
    return p
  }

  const enabled = Boolean(kind && namespace && name)

  // K8s events: high limit so the full set is always returned. The number of
  // distinct K8s events per resource is naturally bounded — kubelet/controllers
  // dedupe via Reason+InvolvedObject and bump count.
  const k8sQuery = useQuery<TimelineEvent[]>({
    queryKey: ['resource-events', 'k8s', singularKind, namespace, name],
    queryFn: async () => {
      const params = baseParams()
      params.set('sources', 'k8s_event')
      params.set('limit', '500')
      return fetchJSON<TimelineEvent[]>(`/changes?${params.toString()}`)
    },
    enabled,
    refetchInterval: 15000,
  })

  // Resource updates (informer diffs + historical): bounded so a flapping
  // resource doesn't return an unbounded payload.
  const updatesQuery = useQuery<TimelineEvent[]>({
    queryKey: ['resource-events', 'updates', singularKind, namespace, name],
    queryFn: async () => {
      const params = baseParams()
      params.set('sources', 'informer,historical')
      params.set('limit', '50')
      return fetchJSON<TimelineEvent[]>(`/changes?${params.toString()}`)
    },
    enabled,
    refetchInterval: 15000,
  })

  return {
    k8sEvents: k8sQuery.data ?? [],
    updates: updatesQuery.data ?? [],
    isLoading: k8sQuery.isLoading || updatesQuery.isLoading,
    k8sError: (k8sQuery.error as Error | null) ?? null,
    updatesError: (updatesQuery.error as Error | null) ?? null,
  }
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

async function fetchMetricsOrNull<T>(path: string): Promise<T | null> {
  try {
    return await fetchJSON<T>(path)
  } catch (error) {
    if (isMetricsUnavailableError(error)) return null
    throw error
  }
}

function retryMetricsQuery(failureCount: number, error: unknown): boolean {
  return !isMetricsUnavailableError(error) && failureCount < 1
}

// Fetch metrics for a specific pod
export function usePodMetrics(namespace: string, podName: string, options?: { enabled?: boolean }) {
  return useQuery<PodMetrics | null>({
    queryKey: ['pod-metrics', namespace, podName],
    queryFn: () => fetchMetricsOrNull<PodMetrics>(`/metrics/pods/${namespace}/${podName}`),
    enabled: Boolean(namespace && podName) && (options?.enabled ?? true),
    staleTime: 15000,
    refetchInterval: 30000,
    refetchOnMount: 'always',
    refetchOnReconnect: 'always',
    retry: retryMetricsQuery,
  })
}

// Fetch metrics for a specific node
export function useNodeMetrics(nodeName: string, options?: { enabled?: boolean }) {
  return useQuery<NodeMetrics | null>({
    queryKey: ['node-metrics', nodeName],
    queryFn: () => fetchMetricsOrNull<NodeMetrics>(`/metrics/nodes/${nodeName}`),
    enabled: Boolean(nodeName) && (options?.enabled ?? true),
    staleTime: 15000,
    refetchInterval: 30000,
    refetchOnMount: 'always',
    refetchOnReconnect: 'always',
    retry: retryMetricsQuery,
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
  collectionError?: string
  rawCollectionError?: string
  metricsUnavailableDiagnosis?: string
  metricsUnavailable?: boolean
  metricsUnavailableReason?: string
}

export interface NodeMetricsHistory {
  name: string
  dataPoints: MetricsDataPoint[]
  collectionError?: string
  rawCollectionError?: string
  metricsUnavailableDiagnosis?: string
  metricsUnavailable?: boolean
  metricsUnavailableReason?: string
}

function withoutCollectionError<T extends { collectionError?: string; rawCollectionError?: string }>(history: T): T {
  const next = { ...history }
  delete next.collectionError
  delete next.rawCollectionError
  return next
}

export function normalizePodMetricsHistory(history: PodMetricsHistory): PodMetricsHistory {
  if (history.metricsUnavailable !== true) return history
  return { ...withoutCollectionError(history), metricsUnavailable: true, metricsUnavailableReason: history.rawCollectionError || history.collectionError }
}

export function normalizeNodeMetricsHistory(history: NodeMetricsHistory): NodeMetricsHistory {
  if (history.metricsUnavailable !== true) return history
  return { ...withoutCollectionError(history), metricsUnavailable: true, metricsUnavailableReason: history.rawCollectionError || history.collectionError }
}

export function shouldFetchLiveMetrics(historySettled: boolean, metricsUnavailable: boolean): boolean {
  return historySettled && !metricsUnavailable
}

export function isLiveMetricsUnavailable(liveMetricsEnabled: boolean, metrics: unknown): boolean {
  return liveMetricsEnabled && metrics === null
}

export function getVisibleLiveMetrics<T>(liveMetricsEnabled: boolean, metricsUnavailable: boolean, metrics: T | null | undefined): T | undefined {
  if (!liveMetricsEnabled || metricsUnavailable) return undefined
  return metrics ?? undefined
}

// Fetch historical metrics for a pod (last ~1 hour)
export function usePodMetricsHistory(namespace: string, podName: string) {
  return useQuery<PodMetricsHistory>({
    queryKey: ['pod-metrics-history', namespace, podName],
    queryFn: async () => normalizePodMetricsHistory(await fetchJSON<PodMetricsHistory>(`/metrics/pods/${namespace}/${podName}/history`)),
    enabled: Boolean(namespace && podName),
    staleTime: 25000, // Slightly less than poll interval
    refetchInterval: 30000, // Match the backend poll interval
  })
}

// Fetch historical metrics for a node (last ~1 hour)
export function useNodeMetricsHistory(nodeName: string) {
  return useQuery<NodeMetricsHistory>({
    queryKey: ['node-metrics-history', nodeName],
    queryFn: async () => normalizeNodeMetricsHistory(await fetchJSON<NodeMetricsHistory>(`/metrics/nodes/${nodeName}/history`)),
    enabled: Boolean(nodeName),
    staleTime: 25000,
    refetchInterval: 30000,
  })
}

// Top metrics types (bulk, for resource table view)
export interface TopPodMetrics {
  namespace: string
  name: string
  cpu: number           // nanocores (usage)
  memory: number        // bytes (usage)
  cpuRequest: number    // nanocores (sum across containers)
  cpuLimit: number      // nanocores (sum across containers)
  memoryRequest: number // bytes (sum across containers)
  memoryLimit: number   // bytes (sum across containers)
}

export interface TopNodeMetrics {
  name: string
  cpu: number              // nanocores (usage)
  memory: number           // bytes (usage)
  podCount: number         // pods scheduled on this node
  cpuAllocatable: number   // nanocores
  memoryAllocatable: number // bytes
}

// Fetch bulk metrics for pods (for CPU/Memory columns in resource table)
export function useTopPodMetrics(options?: { enabled?: boolean; namespaces?: string[] }) {
  const namespacesParam = options?.namespaces?.join(',') ?? ''
  const params = new URLSearchParams()
  if (namespacesParam) params.set('namespaces', namespacesParam)
  const queryString = params.toString()

  return useQuery<TopPodMetrics[]>({
    queryKey: ['top-pod-metrics', namespacesParam],
    queryFn: () => fetchJSON(`/metrics/top/pods${queryString ? `?${queryString}` : ''}`),
    enabled: options?.enabled ?? true,
    staleTime: 25000,
    refetchInterval: 30000,
  })
}

// Fetch bulk metrics for all nodes (for CPU/Memory columns in resource table)
export function useTopNodeMetrics(options?: { enabled?: boolean }) {
  return useQuery<TopNodeMetrics[]>({
    queryKey: ['top-node-metrics'],
    queryFn: () => fetchJSON('/metrics/top/nodes'),
    enabled: options?.enabled ?? true,
    staleTime: 25000,
    refetchInterval: 30000,
  })
}

// ============================================================================
// Prometheus Metrics
// ============================================================================

// Prometheus types
export interface PrometheusStatus {
  available: boolean
  connected: boolean
  address?: string
  service?: {
    namespace: string
    name: string
    port: number
    basePath?: string
  }
  contextName?: string
  error?: string
}

// Time-series sample types live in @skyhook-io/k8s-ui (shared with library
// consumers). Re-export here so radar-app callers keep their existing import
// paths; the Prom-prefixed names are deprecated aliases.
export type {
  TimeSeriesPoint,
  TimeSeries,
  PrometheusDataPoint,
  PrometheusSeries,
} from '@skyhook-io/k8s-ui/components/charts'

import type { TimeSeries as ChartTimeSeries } from '@skyhook-io/k8s-ui/components/charts'

export interface PrometheusQueryResult {
  resultType: string
  series: ChartTimeSeries[]
}

export interface PrometheusResourceMetrics {
  kind: string
  namespace?: string
  name: string
  category: string
  unit: string
  range: string
  result: PrometheusQueryResult
  query?: string // PromQL query (included when result is empty, for diagnostics)
  hint?: string  // Contextual hint when results are empty (e.g. cri-docker label issues)
}

export type PrometheusMetricCategory = 'cpu' | 'memory' | 'network_rx' | 'network_tx' | 'filesystem' | 'restarts'
export type PrometheusTimeRange = '10m' | '30m' | '1h' | '3h' | '6h' | '12h' | '24h' | '48h' | '7d' | '14d'

// PVC usage at a moment in time, derived from kubelet_volume_stats_*.
// HasData=false silently indicates the CSI driver doesn't report or Prom
// isn't scraping kubelet endpoints — UI should hide the gauge in that case.
export interface PrometheusPVCUsage {
  namespace: string
  name: string
  used: number
  capacity: number
  ratio: number
  hasData: boolean
}

export type RightsizingTone = 'ok' | 'info' | 'warning' | 'alert' | 'critical'

export interface RightsizingRow {
  container: string
  resource: 'cpu' | 'memory'
  currentRequest?: string
  currentLimit?: string
  p95?: string
  recommendedRequest?: string
  tone: RightsizingTone
  message: string
}

export interface PrometheusRightsizing {
  kind: string
  namespace: string
  name: string
  window: string
  sampleAvailable: boolean
  rows: RightsizingRow[]
  reason?: string
}

// Check Prometheus availability
export function usePrometheusStatus() {
  return useQuery<PrometheusStatus>({
    queryKey: ['prometheus-status'],
    queryFn: () => fetchJSON('/prometheus/status'),
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// Connect to Prometheus (trigger discovery)
export function usePrometheusConnect() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const resp = await apiFetch(`${getApiBase()}/prometheus/connect`, { method: 'POST' })
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(body.error || `HTTP ${resp.status}`)
      }
      return resp.json() as Promise<PrometheusStatus>
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['prometheus-status'] })
    },
    meta: {
      errorMessage: 'Failed to connect to Prometheus',
      successMessage: 'Connected to Prometheus',
    },
  })
}

// Auto-discover Prometheus on first mount of any Prom-backed view, and
// auto-reconnect across radar restarts on subsequent mounts.
//
// Two paths through this hook, both running once per cluster context per
// component-instance:
//   1. Cached path — localStorage flag means "Prom was discovered before on
//      this context". Probe fires immediately on mount; the user sees
//      charts populate without manual interaction.
//   2. First-time path — no flag yet. Probe fires after a small delay so the
//      initial workload-view render lands before we hit the cluster network.
//      Behavior matches Lens / Headlamp defaults; the trade-off is one
//      cluster probe per session per fresh kubeconfig context.
//
// On success either way we set the flag, so subsequent mounts take path 1.
// On failure we clear the flag (path 1) or leave it cleared (path 2) and
// reset attemptedRef, so the existing "Discover Prometheus" CTA renders
// once status refreshes. Manual interaction stays available as the fallback.
//
// localStorage is the right surface: connection intent is browser-local,
// not a server-side preference, and we want it to persist across radar
// restarts on the same port.
const PROM_AUTOCONNECT_PREFIX = 'radar.prometheus.autoConnect:'
// First-mount delay before probing the cluster. Chosen short enough that the
// CTA → charts transition feels prompt, long enough that the probe doesn't
// race the initial workload-view render.
const PROM_FIRSTLAUNCH_PROBE_DELAY_MS = 500

function promAutoConnectKey(contextName: string): string {
  return `${PROM_AUTOCONNECT_PREFIX}${contextName}`
}

export function useAutoPromConnect(): void {
  const queryClient = useQueryClient()
  const { data: clusterInfo } = useClusterInfo()
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const attemptedRef = useRef<string | null>(null)

  useEffect(() => {
    if (typeof window === 'undefined') return
    const context = clusterInfo?.context
    if (!context || statusLoading) return

    // Persist the "we've connected here before" signal once a connection lands.
    if (status?.connected) {
      try { window.localStorage.setItem(promAutoConnectKey(context), '1') } catch {
        // localStorage can throw in some restricted browser modes — fail open.
      }
      return
    }

    if (attemptedRef.current === context) return
    let cached: string | null = null
    try { cached = window.localStorage.getItem(promAutoConnectKey(context)) } catch {
      // keep the null fallback
    }

    attemptedRef.current = context

    // Cached path probes immediately; first-time path defers briefly so the
    // initial UI render isn't competing with the cluster network call.
    const delay = cached === '1' ? 0 : PROM_FIRSTLAUNCH_PROBE_DELAY_MS
    const timeout = window.setTimeout(() => {
      // Direct apiFetch (not via the usePrometheusConnect mutation) so the
      // meta-driven toast handler stays silent — the user didn't click anything.
      apiFetch(`${getApiBase()}/prometheus/connect?optional=true`, { method: 'POST' })
        .then(async resp => {
          if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
          const nextStatus = await resp.json() as PrometheusStatus
          queryClient.setQueryData(['prometheus-status'], nextStatus)
          if (!nextStatus.connected) throw new Error(nextStatus.error || 'Prometheus unavailable')
          queryClient.invalidateQueries({ queryKey: ['prometheus-status'] })
        })
        .catch(() => {
          try { window.localStorage.removeItem(promAutoConnectKey(context)) } catch {
            // ignore — manual CTA will render once status refreshes
          }
          attemptedRef.current = null
        })
    }, delay)
    return () => window.clearTimeout(timeout)
  }, [clusterInfo?.context, status?.connected, statusLoading, queryClient])
}

// Fetch Prometheus metrics for a resource
export function usePrometheusResourceMetrics(
  kind: string,
  namespace: string,
  name: string,
  category: PrometheusMetricCategory = 'cpu',
  range: PrometheusTimeRange = '1h',
  enabled = true,
) {
  return useQuery<PrometheusResourceMetrics>({
    queryKey: ['prometheus-resource-metrics', kind, namespace, name, category, range],
    queryFn: () =>
      fetchJSON(
        namespace
          ? `/prometheus/resources/${kind}/${namespace}/${name}?category=${category}&range=${range}`
          : `/prometheus/resources/${kind}/${name}?category=${category}&range=${range}`,
      ),
    enabled,
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// Fetch Prometheus metrics for a namespace
export function usePrometheusNamespaceMetrics(
  namespace: string,
  category: PrometheusMetricCategory = 'cpu',
  range: PrometheusTimeRange = '1h',
  enabled = true,
) {
  return useQuery<PrometheusResourceMetrics>({
    queryKey: ['prometheus-namespace-metrics', namespace, category, range],
    queryFn: () =>
      fetchJSON(`/prometheus/namespace/${namespace}?category=${category}&range=${range}`),
    enabled,
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// Fetch Prometheus metrics for the entire cluster
export function usePrometheusClusterMetrics(
  category: PrometheusMetricCategory = 'cpu',
  range: PrometheusTimeRange = '1h',
  enabled = true,
) {
  return useQuery<PrometheusResourceMetrics>({
    queryKey: ['prometheus-cluster-metrics', category, range],
    queryFn: () =>
      fetchJSON(`/prometheus/cluster?category=${category}&range=${range}`),
    enabled,
    staleTime: 30000,
    refetchInterval: 60000,
  })
}

// Fetch PVC usage. hasData=false when no series — UI should hide the gauge.
export function usePrometheusPVCUsage(namespace: string, name: string, enabled = true) {
  return useQuery<PrometheusPVCUsage>({
    queryKey: ['prometheus-pvc-usage', namespace, name],
    queryFn: () => fetchJSON(`/prometheus/pvc/${namespace}/${name}`),
    enabled: enabled && Boolean(namespace && name),
    staleTime: 60000,
    refetchInterval: 120000,
  })
}

// Fetch rightsizing recommendations for a workload (Deployment / StatefulSet / DaemonSet).
export function usePrometheusRightsizing(kind: string, namespace: string, name: string, enabled = true) {
  return useQuery<PrometheusRightsizing>({
    queryKey: ['prometheus-rightsizing', kind, namespace, name],
    queryFn: () => fetchJSON(`/prometheus/rightsizing/${kind}/${namespace}/${name}`),
    enabled: enabled && Boolean(kind && namespace && name),
    staleTime: 5 * 60 * 1000, // P95 over 24h is slow to shift; cache aggressively
    refetchInterval: 10 * 60 * 1000,
  })
}

// Raw PromQL query (range). Used by HPA charts for status_current_replicas etc.
export function usePromQLRange(query: string, range: PrometheusTimeRange = '1h', enabled = true) {
  return useQuery<PrometheusQueryResult>({
    queryKey: ['promql-range', query, range],
    queryFn: () =>
      fetchJSON(`/prometheus/query?query=${encodeURIComponent(query)}&range=${range}`),
    enabled: enabled && Boolean(query),
    staleTime: 30000,
    refetchInterval: 60000,
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
  sinceSeconds?: number
}) {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.previous) params.set('previous', 'true')
  if (options?.sinceSeconds) params.set('sinceSeconds', String(options.sinceSeconds))
  const queryString = params.toString()

  return useQuery<LogsResponse>({
    queryKey: ['pod-logs', namespace, podName, options?.container, options?.tailLines, options?.previous, options?.sinceSeconds],
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
    sinceSeconds?: number
  }
): EventSource {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.previous) params.set('previous', 'true')
  if (options?.sinceSeconds) params.set('sinceSeconds', String(options.sinceSeconds))
  const queryString = params.toString()

  return new EventSource(`${getApiBase()}/pods/${namespace}/${podName}/logs/stream${queryString ? `?${queryString}` : ''}`, {
    withCredentials: getCredentialsMode() === 'include',
  })
}

// ============================================================================
// Port Forwarding
// ============================================================================

export interface AvailablePort {
  port: number
  protocol: string
  containerName?: string
  name?: string
  scheme?: 'http' | 'https'
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
    mutationFn: async ({ kind, namespace, name, yaml, force = true }: { kind: string; namespace: string; name: string; yaml: string; force?: boolean }) => {
      const url = new URL(`${getApiBase()}/resources/${kind}/${namespace}/${name}`, window.location.origin)
      if (!force) {
        url.searchParams.set('force', 'false')
      }
      const response = await apiFetch(url.toString(), {
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
    onSuccess: (updated: any, variables) => {
      // The PUT goes straight to the apiserver and returns the authoritative
      // object, but the GET behind this query reads Radar's informer cache,
      // which lags a write by one watch round-trip. Seed the detail cache with
      // the PUT response so the edit shows immediately. Invalidating here
      // instead would trigger a refetch that races the seed and re-reads the
      // lagging cache — the change appears not to have taken effect.
      if (updated && typeof updated === 'object' && updated.metadata) {
        queryClient.setQueriesData(
          { queryKey: ['resource', variables.kind, variables.namespace, variables.name] },
          (old: any) =>
            old && typeof old === 'object' && 'resource' in old
              ? { ...old, resource: updated }
              : { resource: updated }
        )
      } else {
        queryClient.invalidateQueries({ queryKey: ['resource', variables.kind, variables.namespace, variables.name] })
      }
      queryClient.invalidateQueries({ queryKey: ['resources', variables.kind] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// Cascade delete preview — shows resources that will be garbage-collected
export interface CascadeDeletePreview {
  root: { kind: string; namespace: string; name: string; group?: string }
  dependents: { kind: string; namespace: string; name: string; group?: string }[]
}

export function useCascadeDeletePreview(kind: string, namespace: string, name: string, enabled: boolean) {
  return useQuery<CascadeDeletePreview>({
    queryKey: ['cascade-preview', kind, namespace, name],
    queryFn: () => fetchJSON<CascadeDeletePreview>(`/resources/${kind}/${namespace}/${name}/cascade-preview`),
    enabled,
    staleTime: 30_000,
  })
}

// Delete a resource
export function useDeleteResource() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ kind, group, namespace, name, force }: { kind: string; group?: string; namespace: string; name: string; force?: boolean }) => {
      const url = new URL(`${getApiBase()}/resources/${kind}/${namespace}/${name}`, window.location.origin)
      if (group) {
        url.searchParams.set('group', group)
      }
      if (force) {
        url.searchParams.set('force', 'true')
      }
      const response = await apiFetch(url.toString(), {
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

export function useBulkDeleteResources() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ items, force }: { items: Array<{ kind: string; group?: string; namespace: string; name: string }>; force?: boolean }) => {
      const results = await Promise.allSettled(
        items.map(async ({ kind, group, namespace, name }) => {
          const url = new URL(`${getApiBase()}/resources/${kind}/${namespace}/${name}`, window.location.origin)
          if (group) url.searchParams.set('group', group)
          if (force) url.searchParams.set('force', 'true')
          const response = await apiFetch(url.toString(), { method: 'DELETE' })
          if (!response.ok) {
            const error = await response.json().catch(() => ({ error: 'Unknown error' }))
            throw new Error(error.error || `Failed to delete ${namespace}/${name}`)
          }
          return { kind, namespace, name }
        })
      )
      const failed = results.filter(r => r.status === 'rejected')
      if (failed.length > 0) {
        throw new Error(`Failed to delete ${failed.length} of ${items.length} resources`)
      }
      return { deleted: items.length }
    },
    meta: {
      errorMessage: 'Failed to delete some resources',
      successMessage: 'Resources deleted',
    },
    // onSettled, not onSuccess — a partial failure still deleted some
    // resources, and the table must refetch to drop them.
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      queryClient.invalidateQueries({ queryKey: ['resource-counts'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

interface BulkWorkloadItem {
  kind: string
  namespace: string
  name: string
}

interface BulkWorkloadMutationResult {
  requested: number
  succeeded: number
  failedMessages: string[]
}

function failedBulkWorkloadMessages(results: PromiseSettledResult<unknown>[]): string[] {
  return results.flatMap(r => r.status === 'rejected'
    ? [r.reason instanceof Error ? r.reason.message : String(r.reason)]
    : []
  )
}

function bulkWorkloadFailureMessage(action: string, failed: number, total: number, messages: string[]): string {
  return `Failed to ${action} ${failed} of ${total} workloads:\n${messages.join('\n')}`
}

export function useBulkRestartWorkloads() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ items }: { items: BulkWorkloadItem[] }): Promise<BulkWorkloadMutationResult> => {
      if (items.length === 0) {
        return { requested: 0, succeeded: 0, failedMessages: [] }
      }
      const results = await Promise.allSettled(
        items.map(async ({ kind, namespace, name }) => {
          const response = await apiFetch(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/restart`, {
            method: 'POST',
          })
          if (!response.ok) {
            const error = await response.json().catch(() => ({ error: 'Unknown error' }))
            throw new Error(`${namespace}/${name}: ${error.error || `HTTP ${response.status}`}`)
          }
          return { kind, namespace, name }
        })
      )
      const failedMessages = failedBulkWorkloadMessages(results)
      if (failedMessages.length === items.length) {
        throw new Error(bulkWorkloadFailureMessage('restart', failedMessages.length, items.length, failedMessages))
      }
      return { requested: items.length, succeeded: items.length - failedMessages.length, failedMessages }
    },
    meta: {
      errorMessage: 'Failed to restart some workloads',
    },
    onSuccess: (result) => {
      if (result.failedMessages.length > 0) {
        showApiError(
          `Restarted ${result.succeeded} of ${result.requested} workloads`,
          result.failedMessages.join('\n'),
        )
      } else {
        showApiSuccess('Workloads restarting')
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

export function useBulkScaleWorkloads() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ items, replicas }: { items: BulkWorkloadItem[]; replicas: number }): Promise<BulkWorkloadMutationResult> => {
      if (items.length === 0) {
        return { requested: 0, succeeded: 0, failedMessages: [] }
      }
      const results = await Promise.allSettled(
        items.map(async ({ kind, namespace, name }) => {
          const response = await apiFetch(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/scale`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ replicas }),
          })
          if (!response.ok) {
            const error = await response.json().catch(() => ({ error: 'Unknown error' }))
            throw new Error(`${namespace}/${name}: ${error.error || `HTTP ${response.status}`}`)
          }
          return { kind, namespace, name }
        })
      )
      const failedMessages = failedBulkWorkloadMessages(results)
      if (failedMessages.length === items.length) {
        throw new Error(bulkWorkloadFailureMessage('scale', failedMessages.length, items.length, failedMessages))
      }
      return { requested: items.length, succeeded: items.length - failedMessages.length, failedMessages }
    },
    meta: {
      errorMessage: 'Failed to scale some workloads',
    },
    onSuccess: (result) => {
      if (result.failedMessages.length > 0) {
        showApiError(
          `Scaled ${result.succeeded} of ${result.requested} workloads`,
          result.failedMessages.join('\n'),
        )
      } else {
        showApiSuccess('Workloads scaled')
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// Apply (create or update) a resource from YAML
export interface ApplyResourceResult {
  name: string
  namespace: string
  kind: string
  created: boolean
}

export function useApplyResource() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ yaml, mode = 'apply', dryRun = false, force = false }: { yaml: string; mode?: 'apply' | 'create'; dryRun?: boolean; force?: boolean }) => {
      const url = new URL(`${getApiBase()}/resources/apply`, window.location.origin)
      url.searchParams.set('mode', mode)
      if (dryRun) {
        url.searchParams.set('dryRun', 'true')
      }
      if (force) {
        url.searchParams.set('force', 'true')
      }
      const response = await apiFetch(url.toString(), {
        method: 'POST',
        headers: { 'Content-Type': 'text/plain' },
        body: yaml,
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json() as Promise<ApplyResourceResult[]>
    },
    // No meta errorMessage/successMessage — the CreateResourceDialog
    // handles all feedback inline to avoid duplicate toasts.
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] })
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
      const response = await apiFetch(`${getApiBase()}/cronjobs/${namespace}/${name}/trigger`, {
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
      const response = await apiFetch(`${getApiBase()}/cronjobs/${namespace}/${name}/suspend`, {
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
      const response = await apiFetch(`${getApiBase()}/cronjobs/${namespace}/${name}/resume`, {
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
      const response = await apiFetch(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/restart`, {
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

// Scale a workload (Deployment, StatefulSet)
export function useScaleWorkload() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ kind, namespace, name, replicas }: { kind: string; namespace: string; name: string; replicas: number }) => {
      const response = await apiFetch(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/scale`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ replicas }),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to scale workload',
      successMessage: 'Workload scaled',
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', variables.kind] })
      queryClient.invalidateQueries({ queryKey: ['resource', variables.kind, variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// ============================================================================
// Workload rollback
// ============================================================================

// Workload revision history
export interface WorkloadRevision {
  number: number
  createdAt: string
  image: string
  isCurrent: boolean
  replicas: number
  template?: string // Pod template spec as YAML (for revision diff)
}

export function useWorkloadRevisions(kind: string, namespace: string, name: string, enabled = true) {
  return useQuery<WorkloadRevision[]>({
    queryKey: ['workload-revisions', kind, namespace, name],
    queryFn: () => fetchJSON(`/workloads/${kind}/${namespace}/${name}/revisions`),
    enabled: Boolean(kind && namespace && name && enabled),
  })
}

export function useRollbackWorkload() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ kind, namespace, name, revision }: { kind: string; namespace: string; name: string; revision: number }) => {
      const response = await apiFetch(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ revision }),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to rollback workload',
      successMessage: 'Rollback initiated',
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', variables.kind] })
      queryClient.invalidateQueries({ queryKey: ['resource', variables.kind, variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['workload-revisions', variables.kind, variables.namespace, variables.name] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

// ============================================================================
// Node operations (cordon, uncordon, drain)
// ============================================================================

export function useCordonNode() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name }: { name: string }) => {
      const response = await apiFetch(`${getApiBase()}/nodes/${name}/cordon`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to cordon node',
      successMessage: 'Node cordoned',
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'nodes'] })
      queryClient.invalidateQueries({ queryKey: ['resource', 'nodes', '', variables.name] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

export function useUncordonNode() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name }: { name: string }) => {
      const response = await apiFetch(`${getApiBase()}/nodes/${name}/uncordon`, {
        method: 'POST',
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to uncordon node',
      successMessage: 'Node uncordoned',
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'nodes'] })
      queryClient.invalidateQueries({ queryKey: ['resource', 'nodes', '', variables.name] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

export interface DrainNodeOptions {
  deleteEmptyDirData?: boolean
  force?: boolean
}

export function useDrainNode() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name, options }: { name: string; options?: DrainNodeOptions }) => {
      const response = await apiFetch(`${getApiBase()}/nodes/${name}/drain`, {
        method: 'POST',
        headers: options ? { 'Content-Type': 'application/json' } : {},
        body: options ? JSON.stringify(options) : undefined,
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to drain node',
      // No static successMessage — handled in onSuccess to distinguish partial failures
    },
    onSuccess: (data: { evictedPods?: string[]; errors?: string[] }, variables) => {
      queryClient.invalidateQueries({ queryKey: ['resources', 'nodes'] })
      queryClient.invalidateQueries({ queryKey: ['resource', 'nodes', '', variables.name] })
      queryClient.invalidateQueries({ queryKey: ['topology'] })

      const evicted = data?.evictedPods?.length ?? 0
      const errors = data?.errors?.length ?? 0
      if (errors > 0) {
        showApiError(
          `Drain completed with ${errors} error(s)`,
          `${evicted} pods evicted. Errors: ${data.errors!.join('; ')}`,
        )
      } else {
        showApiSuccess(`Node drained: ${evicted} pods evicted`)
      }
    },
  })
}

// ============================================================================
// Helm API hooks
// ============================================================================

function helmNamespaceParams(namespaces: string[] = []) {
  return namespaces.length > 0 ? `?namespaces=${namespaces.join(',')}` : ''
}

// List all Helm releases
export function useHelmReleases(namespaces: string[] = []) {
  const params = helmNamespaceParams(namespaces)
  return useQuery<HelmRelease[]>({
    queryKey: ['helm-releases', namespaces],
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
    staleTime: 5000,
    refetchInterval: 10000, // Poll for live resource status updates (post-upgrade/rollback)
  })
}

// Get manifest for a Helm release (optionally at a specific revision).
// `enabled` lets callers skip the query when the user's Cloud role
// would 403 the read — saves a round-trip and avoids a transient
// "error" state that the role-gated empty panel doesn't need.
export function useHelmManifest(namespace: string, name: string, revision?: number, enabled = true) {
  const params = revision ? `?revision=${revision}` : ''
  return useQuery<string>({
    queryKey: ['helm-manifest', namespace, name, revision],
    queryFn: async () => {
      const response = await apiFetch(`${getApiBase()}/helm/releases/${namespace}/${name}/manifest${params}`)
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.text()
    },
    enabled: Boolean(namespace && name && enabled),
    staleTime: 60000, // 1 minute
  })
}

// Get values for a Helm release. `enabled` see useHelmManifest.
export function useHelmValues(namespace: string, name: string, allValues?: boolean, enabled = true, revision?: number) {
  const params = new URLSearchParams()
  if (allValues) params.set('all', 'true')
  if (revision && revision > 0) params.set('revision', String(revision))
  const query = params.toString() ? `?${params.toString()}` : ''
  return useQuery<HelmValues>({
    queryKey: ['helm-values', namespace, name, allValues, revision],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}/values${query}`),
    enabled: Boolean(namespace && name && enabled),
    staleTime: 60000,
  })
}

// Get diff between two revisions. `enabled` see useHelmManifest.
export function useHelmManifestDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number,
  enabled = true,
) {
  return useQuery<ManifestDiff>({
    queryKey: ['helm-diff', namespace, name, revision1, revision2],
    queryFn: () =>
      fetchJSON(`/helm/releases/${namespace}/${name}/diff?revision1=${revision1}&revision2=${revision2}`),
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2 && enabled),
    staleTime: 60000,
  })
}

export function useHelmValuesDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number,
  allValues = false,
  enabled = true,
) {
  return useQuery<ValuesDiff>({
    queryKey: ['helm-values-diff', namespace, name, revision1, revision2, allValues],
    queryFn: () => {
      const params = new URLSearchParams({
        revision1: String(revision1),
        revision2: String(revision2),
      })
      if (allValues) params.set('all', 'true')
      return fetchJSON(`/helm/releases/${namespace}/${name}/values/diff?${params.toString()}`)
    },
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2 && enabled),
    staleTime: 60000,
  })
}

export function useHelmNotesDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number,
  enabled = true,
) {
  return useQuery<NotesDiff>({
    queryKey: ['helm-notes-diff', namespace, name, revision1, revision2],
    queryFn: () =>
      fetchJSON(`/helm/releases/${namespace}/${name}/notes/diff?revision1=${revision1}&revision2=${revision2}`),
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2 && enabled),
    staleTime: 60000,
  })
}

export function useHelmHooksDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number,
  enabled = true,
) {
  return useQuery<HooksDiff>({
    queryKey: ['helm-hooks-diff', namespace, name, revision1, revision2],
    queryFn: () =>
      fetchJSON(`/helm/releases/${namespace}/${name}/hooks/diff?revision1=${revision1}&revision2=${revision2}`),
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2 && enabled),
    staleTime: 60000,
  })
}

export function useHelmResourceDiff(
  namespace: string,
  name: string,
  revision1: number,
  revision2: number,
  enabled = true,
) {
  return useQuery<ResourceDiff>({
    queryKey: ['helm-resource-diff', namespace, name, revision1, revision2],
    queryFn: () =>
      fetchJSON(`/helm/releases/${namespace}/${name}/resources/diff?revision1=${revision1}&revision2=${revision2}`),
    enabled: Boolean(namespace && name && revision1 > 0 && revision2 > 0 && revision1 !== revision2 && enabled),
    staleTime: 60000,
  })
}

// Check for upgrade availability (lazy - called when drawer opens)
export function useHelmUpgradeInfo(namespace: string, name: string, enabled = true) {
  return useQuery<UpgradeInfo>({
    queryKey: ['helm-upgrade-info', namespace, name],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}/upgrade-info`),
    enabled: Boolean(namespace && name && enabled),
    staleTime: 30000, // 30 seconds - keep in sync with release list
    retry: false, // Don't retry on failure - repo might not be configured
  })
}

// Available chart versions for a release (newest-first), for the upgrade dialog's
// version picker. Empty when the source can't be resolved — the dialog then falls
// back to the latest version from upgrade-info.
export function useHelmReleaseVersions(namespace: string, name: string, enabled = true) {
  return useQuery<string[]>({
    queryKey: ['helm-release-versions', namespace, name],
    queryFn: () => fetchJSON(`/helm/releases/${namespace}/${name}/versions`),
    enabled: Boolean(namespace && name && enabled),
    staleTime: 30000,
    retry: false,
  })
}

// Batch check for upgrade availability (for list view)
export function useHelmBatchUpgradeInfo(namespaces: string[] = [], enabled = true) {
  const params = helmNamespaceParams(namespaces)
  return useQuery<BatchUpgradeInfo>({
    queryKey: ['helm-batch-upgrade-info', namespaces],
    queryFn: () => fetchJSON(`/helm/upgrade-check${params}`),
    enabled,
    staleTime: 30000, // 30 seconds - keep in sync with release list
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
      const response = await apiFetch(`${getApiBase()}/helm/releases/${namespace}/${name}/rollback?revision=${revision}`, {
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
      const response = await apiFetch(`${getApiBase()}/helm/releases/${namespace}/${name}`, {
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

// Stream SSE progress events from a Helm operation endpoint.
// Resolves on 'complete', rejects on 'error'. Returns the complete event data for install (which includes release).
function streamHelmProgress(
  url: string,
  options: RequestInit,
  onProgress: (event: InstallProgressEvent) => void,
  failureLabel: string,
): Promise<InstallProgressEvent> {
  const headers = new Headers(options.headers)
  for (const [k, v] of Object.entries(getAuthHeaders())) {
    if (!headers.has(k)) headers.set(k, v)
  }
  return new Promise((resolve, reject) => {
    fetch(url, { credentials: getCredentialsMode(), ...options, headers })
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

          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            if (line.startsWith('data: ')) {
              try {
                const data = JSON.parse(line.slice(6)) as InstallProgressEvent
                onProgress(data)

                if (data.type === 'complete') {
                  resolve(data)
                  return
                } else if (data.type === 'error') {
                  reject(new Error(data.message || failureLabel))
                  return
                }
              } catch (err) {
                reject(err instanceof Error ? err : new Error(`${failureLabel}: invalid progress event`))
                return
              }
            }
          }
        }

        reject(new Error(`${failureLabel}: stream ended before completion`))
      })
      .catch(reject)
  })
}

// Upgrade a release with progress streaming via SSE
export function upgradeWithProgress(
  namespace: string,
  name: string,
  version: string,
  repositoryName: string | undefined,
  onProgress: (event: InstallProgressEvent) => void
): Promise<void> {
  const params = new URLSearchParams({ version })
  if (repositoryName) params.set('repository', repositoryName)
  return streamHelmProgress(
    `${getApiBase()}/helm/releases/${namespace}/${name}/upgrade-stream?${params.toString()}`,
    { method: 'POST' },
    onProgress,
    'Upgrade failed',
  ).then(() => {})
}

// Rollback a release with progress streaming via SSE
export function rollbackWithProgress(
  namespace: string,
  name: string,
  revision: number,
  onProgress: (event: InstallProgressEvent) => void
): Promise<void> {
  return streamHelmProgress(
    `${getApiBase()}/helm/releases/${namespace}/${name}/rollback-stream?revision=${revision}`,
    { method: 'POST' },
    onProgress,
    'Rollback failed',
  ).then(() => {})
}

// Preview values change (dry-run upgrade)
export function useHelmPreviewValues() {
  return useMutation<ValuesPreviewResponse, Error, { namespace: string; name: string; values: Record<string, unknown> }>({
    mutationFn: async ({ namespace, name, values }) => {
      const response = await apiFetch(`${getApiBase()}/helm/releases/${namespace}/${name}/values/preview`, {
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
      const response = await apiFetch(`${getApiBase()}/helm/releases/${namespace}/${name}/values`, {
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

// Shared mutation fn + cache invalidation for the helm-repo
// update endpoint. Hoisted so useUpdateRepository and
// useUpdateRepositorySilent can't drift on URL, error parsing, or
// invalidation keys — only the toast meta differs.
async function updateRepositoryFn(repoName: string): Promise<unknown> {
  const response = await apiFetch(`${getApiBase()}/helm/repositories/${repoName}/update`, {
    method: 'POST',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

function invalidateHelmAfterRepoUpdate(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: ['helm-repositories'] })
  queryClient.invalidateQueries({ queryKey: ['helm-charts'] })
}

// Update a repository index
export function useUpdateRepository() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: updateRepositoryFn,
    meta: {
      errorMessage: 'Failed to update repository',
      successMessage: 'Repository updated',
    },
    onSuccess: () => invalidateHelmAfterRepoUpdate(queryClient),
  })
}

// Same endpoint as useUpdateRepository but without meta — the
// caller surfaces ONE aggregate toast for the whole batch, so the
// global MutationCache must NOT fire a per-call toast (otherwise
// N failures = N identical "Failed to update repository" toasts
// with no repo name).
export function useUpdateRepositorySilent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: updateRepositoryFn,
    onSuccess: () => invalidateHelmAfterRepoUpdate(queryClient),
  })
}

// Registered OCI chart sources (the OCI analog of `helm repo add`). Used to
// track upgrades for the user's own OCI-published charts.
export function useHelmOCISources() {
  return useQuery<string[]>({
    queryKey: ['helm-oci-sources'],
    queryFn: () => fetchJSON('/helm/oci-sources'),
  })
}

async function mutateOCISource(method: 'POST' | 'DELETE', source: string): Promise<string[]> {
  const response = await apiFetch(`${getApiBase()}/helm/oci-sources`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source }),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// Invalidate the upgrade-info queries so a newly-registered source is probed
// immediately and "source not tracked" re-resolves.
function invalidateHelmAfterSourceChange(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: ['helm-oci-sources'] })
  queryClient.invalidateQueries({ queryKey: ['helm-upgrade-info'] })
  queryClient.invalidateQueries({ queryKey: ['helm-batch-upgrade-info'] })
}

export function useAddOCISource() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (source: string) => mutateOCISource('POST', source),
    meta: { errorMessage: 'Failed to add chart source', successMessage: 'Chart source added' },
    onSuccess: () => invalidateHelmAfterSourceChange(queryClient),
  })
}

export function useRemoveOCISource() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (source: string) => mutateOCISource('DELETE', source),
    meta: { errorMessage: 'Failed to remove chart source', successMessage: 'Chart source removed' },
    onSuccess: () => invalidateHelmAfterSourceChange(queryClient),
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
      const response = await apiFetch(`${getApiBase()}/helm/releases`, {
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
  return streamHelmProgress(
    `${getApiBase()}/helm/releases/install-stream`,
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(req) },
    onProgress,
    'Install failed',
  ).then((event) => event.release as HelmRelease)
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
  getBody?: (variables: TVariables) => unknown
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
        const response = await apiFetch(`${getApiBase()}${config.getPath(variables)}`, {
          method: 'POST',
          headers: config.getBody ? { 'Content-Type': 'application/json' } : undefined,
          body: config.getBody ? JSON.stringify(config.getBody(variables)) : undefined,
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
// ArgoAppVars identifies the target Application. Used by mutations that don't
// take a body (terminate, suspend, resume, refresh).
type ArgoAppVars = { namespace: string; name: string }
// ArgoSyncVars extends ArgoAppVars with the sync request body fields. Only
// useArgoSync sends these — splitting the type prevents callers from passing
// resources/revision/prune to mutations that would silently drop them.
type ArgoSyncVars = ArgoAppVars & {
  resources?: Array<{ group?: string; kind: string; namespace?: string; name: string }>
  revision?: string
  prune?: boolean
  dryRun?: boolean
  force?: boolean
  applyOnly?: boolean
  // Free-form Argo SyncOption strings, e.g. "Replace=true",
  // "ServerSideApply=true", "PruneLast=true". Caller is responsible for
  // spelling.
  syncOptions?: string[]
}

// ArgoRollbackVars targets a specific Argo history entry by ID. Prune and
// DryRun mirror the sync flags so the rollback dialog can offer the same
// safety net.
type ArgoRollbackVars = ArgoAppVars & {
  id: number
  prune?: boolean
  dryRun?: boolean
}

// Standard invalidation patterns
const fluxInvalidateKeys = (v: FluxResourceVars) => [
  ['resources', v.kind],
  ['resource', v.kind, v.namespace, v.name],
  ['gitops-tree', v.kind, v.namespace, v.name],
  ['gitops-insights', v.kind, v.namespace, v.name],
]
const argoInvalidateKeys = (v: ArgoAppVars) => [
  ['resources', 'applications'],
  ['resource', 'applications', v.namespace, v.name],
  ['gitops-tree', 'applications', v.namespace, v.name],
  ['gitops-insights', 'applications', v.namespace, v.name],
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

export const useArgoSync = createGitOpsMutation<ArgoSyncVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/sync`,
  getBody: (v) => ({
    resources: v.resources,
    revision: v.revision,
    prune: v.prune,
    dryRun: v.dryRun,
    force: v.force,
    applyOnly: v.applyOnly,
    syncOptions: v.syncOptions,
  }),
  errorMessage: 'Failed to trigger sync',
  successMessage: 'Sync initiated',
  getInvalidateKeys: argoInvalidateKeys,
})

export const useArgoRollback = createGitOpsMutation<ArgoRollbackVars>({
  getPath: (v) => `/argo/applications/${v.namespace}/${v.name}/rollback`,
  getBody: (v) => ({ id: v.id, prune: v.prune, dryRun: v.dryRun }),
  errorMessage: 'Failed to roll back application',
  successMessage: 'Rollback initiated',
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
      const response = await apiFetch(`${getApiBase()}/argo/applications/${namespace}/${name}/refresh${params}`, {
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
      // Match the standard Argo invalidation set so the GitOps detail page
      // (insights strip, resource tree) refetches after Refresh / Hard
      // Refresh — without these two extra keys the user clicks Refresh and
      // sees stale insight/tree data until the next staleTime tick.
      argoInvalidateKeys(variables).forEach((key) =>
        queryClient.invalidateQueries({ queryKey: key })
      )
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
        const response = await apiFetch(`${getApiBase()}/contexts/${encodeURIComponent(name)}`, {
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
          throw new Error('Context switch timed out. The cluster may be unreachable.', { cause: error })
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
    onError: () => {
      // Invalidate contexts so the dropdown checkmark reflects the backend's
      // current context after a failed switch (backend has already switched
      // the in-memory context even though connectivity failed).
      queryClient.invalidateQueries({ queryKey: ['contexts'] })
    },
  })
}

// ============================================================================
// Active namespace switcher
// ============================================================================

export interface NamespaceScope {
  actives: string[]
  kubeconfigNamespace: string
  /**
   * 'cluster-wide' — no per-user pick; user can list across namespaces.
   * 'namespace'    — per-user view filter pinned to one or more namespaces.
   * 'restricted'   — user can't list namespaces and isn't pinned to any.
   */
  mode: 'cluster-wide' | 'namespace' | 'restricted'
  accessibleNamespaces: string[]
  /** false when accessibleNamespaces is a best-effort short list (no list perm). */
  authoritative: boolean
  /** false when clearing would leave no usable namespace fallback. */
  canClearNamespace: boolean
  /** true when the backend informer cache is pinned to a namespace. */
  cacheScoped: boolean
  cacheScopeNamespace?: string
  /** true when this client may rebuild the local cache for another namespace. */
  namespaceRescope: boolean
}

export function useNamespaceScope() {
  return useQuery<NamespaceScope>({
    queryKey: ['namespace-scope'],
    queryFn: () => fetchJSON('/cluster/namespace-scope'),
    staleTime: 30000,
  })
}

const NAMESPACE_SWITCH_TIMEOUT = 5000
const NAMESPACE_RESCOPE_TIMEOUT = 120000

export function debugNamespaceLog(label: string, payload?: Record<string, unknown>) {
  if (typeof window === 'undefined') return
  const enabled = window.localStorage.getItem('radar:debug:namespaces')
  if (enabled !== '1' && enabled !== 'true') return
  console.log(`[namespace-debug] ${label}`, {
    t: Math.round(performance.now()),
    href: window.location.href,
    ...payload,
  })
}

export function useSetActiveNamespace() {
  const queryClient = useQueryClient()
  return useMutation<NamespaceScope, Error, { namespaces: string[] }>({
    meta: {
      // Surface 403s (RBAC drift, denied bookmark) and network errors via the
      // global toast. Without this, App.tsx call sites that mutate without
      // their own onError (bookmark reconciliation, back-nav, topology
      // maximize/clear, command palette) silently revert when the scope
      // refetches and the mirror effect overwrites local state.
      errorMessage: 'Failed to update namespace selection',
    },
    mutationFn: async ({ namespaces }) => {
      debugNamespaceLog('mutation:start', { namespaces })
      const controller = new AbortController()
      const currentScope = queryClient.getQueryData<NamespaceScope>(['namespace-scope'])
      // cacheScoped is a stable per-process property (the server's --namespace-scope
      // flag). If the scope query is missing/stale we can't yet tell a cheap
      // view-filter change from a cache-rebuilding rescope, so bias to the long
      // timeout — only a confirmed non-scoped session gets the fast switch timeout.
      // Aborting a real rebuild at 5s surfaces a spurious failure while the server
      // keeps going.
      const isRescope = currentScope?.cacheScoped !== false
      const timeoutMs = isRescope ? NAMESPACE_RESCOPE_TIMEOUT : NAMESPACE_SWITCH_TIMEOUT
      const timeoutId = setTimeout(() => controller.abort(), timeoutMs)
      const startedAt = performance.now()
      try {
        const response = await apiFetch(`${getApiBase()}/cluster/namespace`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ namespaces }),
          signal: controller.signal,
        })
        clearTimeout(timeoutId)
        debugNamespaceLog('mutation:response', {
          namespaces,
          status: response.status,
          durationMs: Math.round(performance.now() - startedAt),
        })
        if (!response.ok) {
          const error = await response.json().catch(() => ({ error: 'Unknown error' }))
          throw new Error(error.error || `HTTP ${response.status}`)
        }
        return response.json()
      } catch (error) {
        clearTimeout(timeoutId)
        debugNamespaceLog('mutation:error', {
          namespaces,
          durationMs: Math.round(performance.now() - startedAt),
          error: error instanceof Error ? error.message : String(error),
        })
        if (error instanceof Error && error.name === 'AbortError') {
          throw new Error(isRescope
            ? 'Namespace rescope timed out. The cluster may still be loading.'
            : 'Namespace switch timed out. The cluster may be unreachable.', { cause: error })
        }
        throw error
      }
    },
    onSuccess: (scope) => {
      debugNamespaceLog('mutation:success-before-scope-cache-write', {
        actives: scope.actives,
        mode: scope.mode,
        accessibleCount: scope.accessibleNamespaces.length,
      })
      if (scope.cacheScoped) {
        queryClient.removeQueries({ predicate: query => query.queryKey[0] !== 'namespace-scope' })
      }
      queryClient.setQueryData<NamespaceScope>(['namespace-scope'], scope)
      if (scope.cacheScoped) {
        queryClient.invalidateQueries()
      }
      debugNamespaceLog('mutation:success-after-scope-cache-write')
    },
    onError: () => {
      // A failed switch can leave the server's stored pick out of sync
      // with the cached scope (network timeout after the server wrote;
      // partial mutation). Refetch so the displayed picker matches what
      // the server actually persisted instead of what we assumed.
      debugNamespaceLog('mutation:on-error-invalidate-scope')
      queryClient.invalidateQueries({ queryKey: ['namespace-scope'] })
    },
  })
}

// ============================================================================
// Image Filesystem Inspection
// ============================================================================

import type { ImageFilesystem, ImageMetadata, WorkloadPodInfo } from '../types'

// Fetch image metadata (lightweight, checks if cached)
export function useImageMetadata(
  image: string,
  namespace: string,
  podName: string,
  pullSecrets: string[],
  enabled = true
) {
  const params = new URLSearchParams()
  params.set('image', image)
  if (namespace) params.set('namespace', namespace)
  if (podName) params.set('pod', podName)
  if (pullSecrets.length > 0) params.set('pullSecrets', pullSecrets.join(','))

  return useQuery<ImageMetadata>({
    queryKey: ['image-metadata', image, namespace, podName, pullSecrets.join(',')],
    queryFn: () => fetchJSON(`/images/metadata?${params.toString()}`),
    enabled: enabled && Boolean(image),
    staleTime: 60000, // 1 minute - metadata is lightweight
    retry: false,
  })
}

// Fetch full image filesystem (downloads layers if not cached)
export function useImageFilesystem(
  image: string,
  namespace: string,
  podName: string,
  pullSecrets: string[],
  enabled = true
) {
  const params = new URLSearchParams()
  params.set('image', image)
  if (namespace) params.set('namespace', namespace)
  if (podName) params.set('pod', podName)
  if (pullSecrets.length > 0) params.set('pullSecrets', pullSecrets.join(','))

  const shouldFetch = enabled && Boolean(image)

  return useQuery<ImageFilesystem>({
    queryKey: ['image-filesystem', image, namespace, podName, pullSecrets.join(',')],
    // Use skipToken to completely prevent the query from running when disabled
    queryFn: shouldFetch
      ? () => fetchJSON(`/images/inspect?${params.toString()}`)
      : skipToken,
    staleTime: 300000, // 5 minutes - image content doesn't change
    retry: false, // Don't retry on auth errors
  })
}

// ============================================================================
// Workload Logs (aggregated from all pods)
// ============================================================================

// Response from workload pods endpoint
export interface WorkloadPodsResponse {
  pods: WorkloadPodInfo[]
}

// Response from workload logs endpoint (non-streaming)
export interface WorkloadLogsResponse {
  pods: WorkloadPodInfo[]
  logs: {
    pod: string
    container: string
    timestamp: string
    content: string
  }[]
}

// Fetch pods for a workload
export function useWorkloadPods(kind: string, namespace: string, name: string) {
  return useQuery<WorkloadPodsResponse>({
    queryKey: ['workload-pods', kind, namespace, name],
    queryFn: () => fetchJSON(`/workloads/${kind}/${namespace}/${name}/pods`),
    enabled: Boolean(kind && namespace && name),
    staleTime: 10000, // 10 seconds - pods can change
  })
}

// Fetch logs for a workload (non-streaming)
export function useWorkloadLogs(
  kind: string,
  namespace: string,
  name: string,
  options?: {
    container?: string
    tailLines?: number
    sinceSeconds?: number
  }
) {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.sinceSeconds) params.set('sinceSeconds', String(options.sinceSeconds))
  const queryString = params.toString()

  return useQuery<WorkloadLogsResponse>({
    queryKey: ['workload-logs', kind, namespace, name, options?.container, options?.tailLines, options?.sinceSeconds],
    queryFn: () => fetchJSON(`/workloads/${kind}/${namespace}/${name}/logs${queryString ? `?${queryString}` : ''}`),
    enabled: Boolean(kind && namespace && name),
    staleTime: 5000,
  })
}

// Create SSE connection for streaming workload logs
export function createWorkloadLogStream(
  kind: string,
  namespace: string,
  name: string,
  options?: {
    container?: string
    tailLines?: number
    sinceSeconds?: number
  }
): EventSource {
  const params = new URLSearchParams()
  if (options?.container) params.set('container', options.container)
  if (options?.tailLines) params.set('tailLines', String(options.tailLines))
  if (options?.sinceSeconds) params.set('sinceSeconds', String(options.sinceSeconds))
  const queryString = params.toString()

  return new EventSource(`${getApiBase()}/workloads/${kind}/${namespace}/${name}/logs/stream${queryString ? `?${queryString}` : ''}`, {
    withCredentials: getCredentialsMode() === 'include',
  })
}

// ============================================================================
// Diagnostics
// ============================================================================

export interface DiagMetricsSourceHealth {
  collecting: boolean
  lastSuccess?: string
  consecutiveErrors: number
  lastError?: string
  trackedCount: number
  totalDataPoints: number
}

export interface DiagDropRecord {
  kind: string
  namespace: string
  name: string
  reason: string
  operation: string
  time: string
}

export interface DiagErrorEntry {
  time: string
  source: string
  message: string
  level: string
}

export type DiagSyncPhase = 'not_started' | 'syncing_critical' | 'syncing_deferred' | 'complete'

export interface DiagInformerSyncStatus {
  kind: string
  key: string
  deferred: boolean
  synced: boolean
  syncedAt?: string
  items: number
  lastError?: string
  lastErrorAt?: string
  forbiddenSeen?: boolean
}

export interface DiagCacheSyncStatus {
  phase: DiagSyncPhase
  syncStarted?: string
  elapsedSec: number
  criticalTotal: number
  criticalSynced: number
  deferredTotal: number
  deferredSynced: number
  informers: DiagInformerSyncStatus[]
  pendingCritical?: string[]
  pendingDeferred?: string[]
  promotedKinds?: string[]
}

export interface DiagSampleWindow {
  count: number
  last: number
  min: number
  p50: number
  p95: number
  p99: number
  max: number
}

export interface DiagPerfSnapshot {
  topology: {
    totalBuilds: number
    durationUs: DiagSampleWindow
    nodeCount: DiagSampleWindow
    edgeCount: DiagSampleWindow
    payloadBytes: DiagSampleWindow
    estimatedNodes: DiagSampleWindow
  }
  sse: {
    totalBroadcasts: number
    totalDrops: number
  }
}

export interface DiagnosticsSnapshot {
  timestamp: string
  radarVersion: string
  goVersion: string
  goos: string
  goarch: string
  uptime: string
  uptimeSec: number

  connection?: {
    state: string
    context: string
    clusterName?: string
    error?: string
    errorType?: string
  }
  kubeconfig?: {
    mode: '' | 'in-cluster' | 'single' | 'multi-env' | 'multi-dir'
    fileCount: number
    contextCount: number
    enrichedFromShell: boolean
    currentContextUsesExec: boolean
    execPluginsPresent?: string[]
    execPluginsMissing?: string[]
  }
  cluster?: {
    platform: string
    kubernetesVersion: string
    nodeCount: number
    namespaceCount: number
    inCluster: boolean
  }
  cache?: {
    watchedKinds: string[]
    totalResources: number
  }
  metrics?: {
    podMetrics: DiagMetricsSourceHealth
    nodeMetrics: DiagMetricsSourceHealth
    lastAttempt?: string
    totalCollections: number
    bufferSize: number
    pollIntervalSec: number
  }
  timeline?: {
    storageType: string
    totalEvents: number
    oldestEvent?: string
    newestEvent?: string
    storeErrors: number
    totalDrops: number
  }
  eventPipeline?: {
    received: Record<string, number>
    dropped: Record<string, number>
    recorded: Record<string, number>
    recentDrops: DiagDropRecord[]
    uptime: string
  }
  informers?: {
    typedCount: number
    dynamicCount: number
    watchedCRDs: string[]
    syncStatus?: DiagCacheSyncStatus
  }
  prometheus?: {
    connected: boolean
    address?: string
    serviceName?: string
    serviceNamespace?: string
  }
  traffic?: {
    activeSource: string
    detected: string[]
    notDetected: string[]
  }
  permissions?: {
    exec: boolean
    logs: boolean
    portForward: boolean
    secrets: boolean
    helmWrite: boolean
    namespaceScoped: boolean
    namespace?: string
    restricted?: string[]
  }
  apiDiscovery?: {
    totalResources: number
    crdCount: number
    lastRefresh?: string
  }
  sse?: {
    connectedClients: number
  }
  perf?: DiagPerfSnapshot
  runtime?: {
    heapMB: number
    heapObjectsK: number
    goroutines: number
    numCPU: number
  }
  config?: {
    port: number
    devMode: boolean
    namespace?: string
    timelineStorage: string
    historyLimit: number
    debugEvents: boolean
    mcpEnabled: boolean
    hasPrometheusURL: boolean
    hasPrometheusHeaders: boolean
  }
  recentErrors?: DiagErrorEntry[]
  totalErrorsRecorded?: number
  errors?: string[]
}

export function useDiagnostics(enabled: boolean) {
  return useQuery<DiagnosticsSnapshot>({
    queryKey: ['diagnostics'],
    queryFn: enabled ? () => fetchJSON('/diagnostics') : skipToken,
    staleTime: 0,
    gcTime: 0,
  })
}
