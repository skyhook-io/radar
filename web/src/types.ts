// Topology types matching the Go backend

// Per-resource-type RBAC permissions (matches backend k8s.ResourcePermissions)
export interface ResourcePermissions {
  pods: boolean
  services: boolean
  deployments: boolean
  daemonSets: boolean
  statefulSets: boolean
  replicaSets: boolean
  ingresses: boolean
  configMaps: boolean
  secrets: boolean
  events: boolean
  pvcs: boolean
  nodes: boolean
  namespaces: boolean
  jobs: boolean
  cronJobs: boolean
  hpas: boolean
  gateways: boolean
  httpRoutes: boolean
}

// Feature capabilities based on RBAC permissions
export interface Capabilities {
  exec: boolean        // Terminal feature (pods/exec)
  logs: boolean        // Log viewer (pods/log)
  portForward: boolean // Port forwarding (pods/portforward)
  secrets: boolean     // List secrets
  helmWrite: boolean   // Helm write operations (install, upgrade, rollback, uninstall, apply values)
  resources?: ResourcePermissions // Per-resource-type permissions
}

// Core node kinds that have specific UI handling
//
// When adding a new kind here, also update:
// - ALL_NODE_KINDS in App.tsx
// - TopologyFilterSidebar.tsx RESOURCE_KINDS array
// - index.css .topology-icon-* class
// - K8sResourceNode.tsx NODE_DIMENSIONS
// - resource-icons.ts KIND_ICON_MAP
// - kindToPlural in navigation.ts (if irregular plural)
// - badge-colors.ts KIND_BADGE_COLORS + KIND_BADGE_BORDERED
// - layout.ts kindPriority in pickGroupName()
// - resource-hierarchy.ts kindPriority maps + appLabelEligibleKinds
export type CoreNodeKind =
  | 'Internet'
  | 'Ingress'
  | 'Gateway'
  | 'HTTPRoute'
  | 'GRPCRoute'
  | 'TCPRoute'
  | 'TLSRoute'
  | 'Service'
  | 'Deployment'
  | 'Rollout'
  | 'Application' // ArgoCD Application
  | 'Kustomization' // FluxCD Kustomization
  | 'HelmRelease' // FluxCD HelmRelease
  | 'GitRepository' // FluxCD GitRepository
  | 'DaemonSet'
  | 'StatefulSet'
  | 'ReplicaSet'
  | 'Pod'
  | 'PodGroup'
  | 'ConfigMap'
  | 'Secret'
  | 'HorizontalPodAutoscaler'
  | 'Job'
  | 'CronJob'
  | 'PersistentVolumeClaim'
  | 'Namespace'

// NodeKind can be a core kind or any arbitrary CRD kind string
export type NodeKind = CoreNodeKind | (string & {})

/** Short display names for verbose K8s kind names */
export function displayKind(kind: string): string {
  const shortNames: Record<string, string> = {
    HorizontalPodAutoscaler: 'HPA',
    PersistentVolumeClaim: 'PVC',
  }
  return shortNames[kind] || kind
}

export type HealthStatus = 'healthy' | 'degraded' | 'unhealthy' | 'unknown'

export type EdgeType = 'routes-to' | 'exposes' | 'manages' | 'uses' | 'configures'

export interface TopologyNode {
  id: string
  kind: NodeKind
  name: string
  status: HealthStatus
  data: Record<string, unknown>
}

export interface TopologyEdge {
  id: string
  source: string
  target: string
  type: EdgeType
  label?: string
  skipIfKindVisible?: string // Hide this edge if this kind is visible (for shortcut edges)
}

export interface Topology {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  warnings?: string[] // Warnings about resources that failed to load
  truncated?: boolean // True if topology was truncated due to size limit
  totalNodes?: number // Total nodes before truncation (only set if truncated)
  largeCluster?: boolean // True if cluster exceeds large cluster threshold
  hiddenKinds?: string[] // Resource kinds auto-hidden for performance
  crdDiscoveryStatus?: 'idle' | 'discovering' | 'ready' // CRD discovery status
}

// K8s Event (from SSE stream)
export interface K8sEvent {
  kind: string
  namespace: string
  name: string
  operation: 'add' | 'update' | 'delete'
  timestamp?: number
  diff?: DiffInfo
}

// Diff information for resource changes
export interface DiffInfo {
  fields: FieldChange[]
  summary: string
}

export interface FieldChange {
  path: string
  oldValue: unknown
  newValue: unknown
}

// Owner information for managed resources
export interface OwnerInfo {
  kind: string
  name: string
}

// Event source types for the new timeline API
export type EventSource = 'informer' | 'k8s_event' | 'historical'

// Event types for the new timeline API
export type EventType = 'add' | 'update' | 'delete' | 'Normal' | 'Warning'

// Unified timeline event (from /api/changes and /api/timeline)
// Uses the canonical format from timeline.TimelineEvent in the backend
export interface TimelineEvent {
  id: string
  timestamp: string // ISO date string
  source: EventSource // Where event originated: 'informer', 'k8s_event', 'historical'

  // Resource identity
  kind: string
  namespace: string
  name: string
  uid?: string

  // Resource metadata - when the resource was actually created in K8s
  // This is different from timestamp which is when we observed the event
  createdAt?: string // ISO date string

  // Event details
  eventType: EventType // 'add', 'update', 'delete', 'Normal', 'Warning'
  reason?: string
  message?: string

  // Rich context
  diff?: DiffInfo
  healthState?: HealthStatus
  owner?: OwnerInfo
  labels?: Record<string, string> // For app-label grouping

  // K8s Event specific
  count?: number

  // Correlation
  correlationId?: string
}

// Helper to check if event is a change (vs K8s event)
export function isChangeEvent(event: TimelineEvent): boolean {
  return event.source === 'informer' || event.source === 'historical'
}

// Helper to check if event is a K8s Event object
export function isK8sEvent(event: TimelineEvent): boolean {
  return event.source === 'k8s_event'
}

// Helper to check if event is historical (reconstructed from metadata)
export function isHistoricalEvent(event: TimelineEvent): boolean {
  return event.source === 'historical'
}

// Helper to check if event is an add/update/delete operation
export function isOperation(eventType: EventType): eventType is 'add' | 'update' | 'delete' {
  return eventType === 'add' || eventType === 'update' || eventType === 'delete'
}

// Check if a resource kind is a top-level workload (representative in timeline)
// These are the "root" resources that own/manage others
export function isWorkloadKind(kind: string): boolean {
  return [
    'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet',
    'Service', 'Job', 'CronJob',
    'Workflow', 'CronWorkflow', // Argo Workflows
    'Application', // ArgoCD Application
    'Kustomization', 'HelmRelease', // FluxCD controllers
    'GitRepository', 'OCIRepository', 'HelmRepository', // FluxCD sources
  ].includes(kind)
}

// Check if a resource kind is typically managed by another
export function isManagedKind(kind: string): boolean {
  return ['ReplicaSet', 'Pod', 'Event'].includes(kind)
}

// Timeline filter options
export interface TimelineFilters {
  namespace: string
  kinds: string[]
  eventTypes: ('change' | 'k8s_event')[]
  healthStates: string[]
  timeRange: TimeRange
}

export type TimeRange = '5m' | '30m' | '1h' | '6h' | '24h' | 'all'

// Cluster info
export interface ClusterInfo {
  context: string
  cluster: string
  platform: string
  kubernetesVersion: string
  nodeCount: number
  podCount: number
  namespaceCount: number
  inCluster: boolean
  crdDiscoveryStatus?: 'idle' | 'discovering' | 'ready'
}

// Context info for context switching
export interface ContextInfo {
  name: string
  cluster: string
  user: string
  namespace: string
  isCurrent: boolean
}

// Namespace
export interface Namespace {
  name: string
  status: string
}

// Main view type (which screen we're on)
export type MainView = 'home' | 'topology' | 'resources' | 'timeline' | 'helm'

// Topology view mode (for backwards compatibility, also exported as ViewMode)
// NOTE: Must match Go backend constants in internal/topology/types.go
export type TopologyMode = 'resources' | 'traffic'
export type ViewMode = 'resources' | 'traffic'

// Grouping mode
export type GroupingMode = 'none' | 'namespace' | 'app' | 'label'

// Group info for topology
export interface TopologyGroup {
  id: string
  type: 'namespace' | 'app' | 'label'
  name: string
  label?: string // for label-based grouping
  nodeCount: number
  collapsed?: boolean
}

// Selected resource (for resources view drawer)
export interface SelectedResource {
  kind: string
  namespace: string
  name: string
  group?: string  // API group for CRDs (e.g., 'metrics.k8s.io')
}

// Resource reference (for relationships)
export interface ResourceRef {
  kind: string
  namespace: string
  name: string
  group?: string  // API group for CRDs (e.g., 'cert-manager.io')
}

// Computed relationships for a resource
export interface Relationships {
  owner?: ResourceRef
  children?: ResourceRef[]
  services?: ResourceRef[]
  ingresses?: ResourceRef[]
  gateways?: ResourceRef[]
  routes?: ResourceRef[]
  configRefs?: ResourceRef[]
  hpa?: ResourceRef
  scaleTarget?: ResourceRef
  pods?: ResourceRef[]
}

// Resource with computed relationships (API response wrapper)
export interface ResourceWithRelationships<T = unknown> {
  resource: T
  relationships?: Relationships
}

// API Resource (from discovery endpoint)
export interface APIResource {
  group: string
  version: string
  kind: string
  name: string // Plural name (e.g., "deployments")
  namespaced: boolean
  isCrd: boolean
  verbs: string[]
}

// Helm release types
export interface HelmRelease {
  name: string
  namespace: string
  chart: string
  chartVersion: string
  appVersion: string
  status: string
  revision: number
  updated: string // ISO date string
  // Health summary from owned resources
  resourceHealth?: 'healthy' | 'degraded' | 'unhealthy' | 'unknown'
  healthIssue?: string    // Primary issue if unhealthy (e.g., "OOMKilled")
  healthSummary?: string  // Brief summary like "2/3 pods ready"
}

export interface HelmRevision {
  revision: number
  status: string
  chart: string
  appVersion: string
  description: string
  updated: string // ISO date string
}

export interface HelmReleaseDetail {
  name: string
  namespace: string
  chart: string
  chartVersion: string
  appVersion: string
  status: string
  revision: number
  updated: string
  description: string
  notes: string
  history: HelmRevision[]
  resources: HelmOwnedResource[]
  hooks?: HelmHook[]
  readme?: string
  dependencies?: ChartDependency[]
}

export interface HelmHook {
  name: string
  kind: string
  events: string[]
  weight: number
  status?: string
}

export interface ChartDependency {
  name: string
  version: string
  repository?: string
  condition?: string
  enabled: boolean
}

export interface HelmOwnedResource {
  kind: string
  name: string
  namespace: string
  status?: string   // Running, Pending, Failed, Active, etc.
  ready?: string    // e.g., "3/3" for deployments
  message?: string  // Status message or reason
  summary?: string  // Brief status like "0/3 OOMKilled"
  issue?: string    // Primary issue if unhealthy (e.g., "OOMKilled")
}

export interface HelmValues {
  userSupplied: Record<string, unknown>
  computed?: Record<string, unknown>
}

export interface ManifestDiff {
  revision1: number
  revision2: number
  diff: string
}

// Selected Helm release (for drawer state)
export interface SelectedHelmRelease {
  namespace: string
  name: string
}

// Upgrade availability info
export interface UpgradeInfo {
  currentVersion: string
  latestVersion?: string
  updateAvailable: boolean
  repositoryName?: string
  error?: string
}

// Batch upgrade info (map of "namespace/name" to UpgradeInfo)
export interface BatchUpgradeInfo {
  releases: Record<string, UpgradeInfo>
}

// Request body for applying new values to a release
export interface ApplyValuesRequest {
  values: Record<string, unknown>
}

// Response for previewing values changes
export interface ValuesPreviewResponse {
  currentValues: Record<string, unknown>
  newValues: Record<string, unknown>
  manifestDiff: string
}

// ============================================================================
// Chart Browser Types
// ============================================================================

// Configured Helm repository
export interface HelmRepository {
  name: string
  url: string
  lastUpdated?: string // ISO date string
}

// Basic chart information
export interface ChartInfo {
  name: string
  version: string
  appVersion?: string
  description?: string
  icon?: string
  repository: string
  home?: string
  deprecated?: boolean
}

// Detailed chart information
export interface ChartDetail extends ChartInfo {
  readme?: string
  values?: Record<string, unknown>
  valuesSchema?: string
  maintainers?: ChartMaintainer[]
  sources?: string[]
  keywords?: string[]
}

// Chart maintainer
export interface ChartMaintainer {
  name: string
  email?: string
  url?: string
}

// Chart search result
export interface ChartSearchResult {
  charts: ChartInfo[]
  total: number
}

// Request body for installing a new chart
export interface InstallChartRequest {
  releaseName: string
  namespace: string
  chartName: string
  version: string
  repository: string
  values?: Record<string, unknown>
  createNamespace?: boolean
}

// ============================================================================
// ArtifactHub Types
// ============================================================================

// ArtifactHub chart with rich metadata
export interface ArtifactHubChart {
  packageId: string
  name: string
  version: string
  appVersion?: string
  description?: string
  logoUrl?: string
  homeUrl?: string
  deprecated?: boolean
  repository: ArtifactHubRepository
  stars: number
  license?: string
  createdAt?: number // Unix timestamp
  updatedAt?: number // Unix timestamp
  signed?: boolean
  security?: ArtifactHubSecurity
  productionOrgsCount?: number
  hasValuesSchema?: boolean
  keywords?: string[]
}

// ArtifactHub repository info
export interface ArtifactHubRepository {
  name: string
  url: string
  official?: boolean
  verifiedPublisher?: boolean
  organizationName?: string
}

// ArtifactHub security report summary
export interface ArtifactHubSecurity {
  critical?: number
  high?: number
  medium?: number
  low?: number
  unknown?: number
}

// ArtifactHub search result
export interface ArtifactHubSearchResult {
  charts: ArtifactHubChart[]
  total: number
}

// ArtifactHub chart detail (extended)
export interface ArtifactHubChartDetail extends ArtifactHubChart {
  readme?: string
  values?: string // Default values as YAML string
  valuesSchema?: string
  maintainers?: ArtifactHubMaintainer[]
  links?: ArtifactHubLink[]
  availableVersions?: ArtifactHubVersionSummary[]
  install?: string // Install instructions
}

// ArtifactHub maintainer
export interface ArtifactHubMaintainer {
  name: string
  email?: string
}

// ArtifactHub link
export interface ArtifactHubLink {
  name: string
  url: string
}

// ArtifactHub version summary
export interface ArtifactHubVersionSummary {
  version: string
  ts?: number // Unix timestamp
}

// Chart source type for UI toggling
export type ChartSource = 'local' | 'artifacthub'

// ============================================================================
// Traffic Types
// ============================================================================

// Traffic endpoint (source or destination in a flow)
export interface TrafficEndpoint {
  name: string
  namespace: string
  kind: string // Pod, Service, External
  ip?: string
  labels?: Record<string, string>
  workload?: string
  port?: number
}

// Traffic flow between two endpoints
export interface TrafficFlow {
  source: TrafficEndpoint
  destination: TrafficEndpoint
  protocol: string // tcp, udp, http, grpc
  port: number
  l7Protocol?: string // HTTP, gRPC, DNS
  httpMethod?: string
  httpPath?: string
  httpStatus?: number
  bytesSent: number
  bytesRecv: number
  connections: number
  verdict: string // forwarded, dropped, error
  lastSeen: string // ISO date string
}

// Aggregated flow by service pair
export interface AggregatedFlow {
  source: TrafficEndpoint
  destination: TrafficEndpoint
  protocol: string
  port: number
  flowCount: number
  bytesSent: number
  bytesRecv: number
  connections: number
  lastSeen: string
  requestCount?: number
  errorCount?: number
  avgLatencyMs?: number
}

// Cluster info for traffic detection
export interface TrafficClusterInfo {
  platform: string // gke, eks, aks, generic
  cni: string // cilium, calico, flannel, vpc-cni, azure-cni
  dataplaneV2: boolean
  clusterName?: string
  k8sVersion?: string
}

// Traffic source status
export interface TrafficSourceStatus {
  name: string
  status: 'available' | 'not_found' | 'error'
  version?: string
  native: boolean
  message?: string
}

// Helm chart info for one-click install
export interface TrafficHelmChartInfo {
  repo: string
  repoUrl: string
  chartName: string
  version?: string
}

// Recommendation for installing a traffic source
export interface TrafficRecommendation {
  name: string
  reason: string
  installCommand?: string // For non-Helm installs (e.g., gcloud commands)
  docsUrl?: string
  // Helm chart info (for one-click install via Helm view)
  helmChart?: TrafficHelmChartInfo
  // Alternative option (for cases with two good choices)
  alternativeName?: string
  alternativeReason?: string
  alternativeDocsUrl?: string
}

// Response from GET /api/traffic/sources
export interface TrafficSourcesResponse {
  cluster: TrafficClusterInfo
  detected: TrafficSourceStatus[]
  notDetected: string[]
  recommended?: TrafficRecommendation
}

// Response from GET /api/traffic/flows
export interface TrafficFlowsResponse {
  source: string
  timestamp: string
  flows: TrafficFlow[]
  aggregated: AggregatedFlow[]
  warning?: string  // Non-fatal warning (e.g., query errors)
}

// Wizard state for traffic setup
export type TrafficWizardState = 'detecting' | 'not_found' | 'wizard' | 'checking' | 'ready'

// Traffic view filter options
export interface TrafficFilters {
  hideSystem: boolean
  hideExternal: boolean
  minConnections: number
  focusedNamespaces: Set<string>
  showNamespaceGroups: boolean
  aggregateExternal: boolean
  timeRange: string
}

// Main view type now includes 'traffic'
export type ExtendedMainView = MainView | 'traffic'

// ============================================================================
// Image Filesystem Types
// ============================================================================

// File or directory node in image filesystem
export interface FileNode {
  name: string
  path: string
  type: 'file' | 'dir' | 'symlink'
  size?: number
  permissions?: string
  mode?: number
  modTime?: string
  linkTarget?: string
  children?: FileNode[]
}

// Image layer information
export interface LayerInfo {
  digest: string
  size: number
  mediaType: string
}

// Complete image filesystem response
export interface ImageFilesystem {
  image: string
  digest?: string
  platform?: string
  root: FileNode
  totalFiles: number
  totalSize: number
  layers?: LayerInfo[]
  error?: string
}

// Lightweight image metadata (for pre-download check)
export interface ImageMetadata {
  image: string
  digest: string
  platform: string
  totalSize: number    // Total compressed size of all layers
  layerCount: number
  cached: boolean      // Whether filesystem is already cached
  filesystem?: ImageFilesystem  // Included if cached
  authMethod: string   // "anonymous", "google", "credentials", etc.
}

// ============================================================================
// Workload Logs Types
// ============================================================================

// Pod info returned from workload pods endpoint
export interface WorkloadPodInfo {
  name: string
  containers: string[]
  ready: boolean
}

// SSE event types for workload log streaming
export type WorkloadLogEventType =
  | 'connected'
  | 'log'
  | 'pod_added'
  | 'pod_removed'
  | 'end'
  | 'error'

// Workload log stream event data
export interface WorkloadLogStreamEvent {
  event: WorkloadLogEventType
  // connected event
  workload?: string
  namespace?: string
  kind?: string
  pods?: WorkloadPodInfo[]
  // log event
  pod?: string
  container?: string
  timestamp?: string
  content?: string
  // pod_added/pod_removed events
  reason?: string
  // error event
  message?: string
}
