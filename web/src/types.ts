// Topology types matching the Go backend

export type NodeKind =
  | 'Internet'
  | 'Ingress'
  | 'Service'
  | 'Deployment'
  | 'DaemonSet'
  | 'StatefulSet'
  | 'ReplicaSet'
  | 'Pod'
  | 'PodGroup'
  | 'ConfigMap'
  | 'Secret'
  | 'HPA'
  | 'Job'
  | 'CronJob'
  | 'PVC'
  | 'Namespace'

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

// Unified timeline event (from /api/changes)
export interface TimelineEvent {
  id: string
  type: 'change' | 'k8s_event'
  timestamp: string // ISO date string
  kind: string
  namespace: string
  name: string
  // For changes
  operation?: 'add' | 'update' | 'delete'
  diff?: DiffInfo
  healthState?: 'healthy' | 'degraded' | 'unhealthy' | 'unknown'
  owner?: OwnerInfo // Owner/controller for managed resources
  isHistorical?: boolean // True if extracted from resource metadata/status (not observed in real-time)
  // For k8s_event and historical events
  reason?: string // Event reason or historical event description (e.g., "created", "started")
  message?: string
  eventType?: 'Normal' | 'Warning'
  count?: number
}

// Check if a resource kind is a top-level workload (not managed)
export function isWorkloadKind(kind: string): boolean {
  return ['Deployment', 'DaemonSet', 'StatefulSet', 'Service', 'Ingress', 'ConfigMap', 'Secret', 'Job', 'CronJob'].includes(kind)
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
export type MainView = 'topology' | 'resources' | 'timeline' | 'helm'

// Topology view mode (for backwards compatibility, also exported as ViewMode)
export type TopologyMode = 'full' | 'traffic'
export type ViewMode = 'full' | 'traffic'

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
}

// Resource reference (for relationships)
export interface ResourceRef {
  kind: string
  namespace: string
  name: string
}

// Computed relationships for a resource
export interface Relationships {
  owner?: ResourceRef
  children?: ResourceRef[]
  services?: ResourceRef[]
  ingresses?: ResourceRef[]
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
