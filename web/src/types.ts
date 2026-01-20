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
  // For k8s_event
  reason?: string
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

// Namespace
export interface Namespace {
  name: string
  status: string
}

// Main view type (which screen we're on)
export type MainView = 'topology' | 'resources' | 'events'

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
