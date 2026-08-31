import type { CapacityIntegrationState, CapacitySourceCoverage } from './capacity'

// Topology types matching the Go backend

// Per-resource-type RBAC permissions. Field names must match the JSON keys
// produced by ResourcePermissions in internal/k8s/capabilities.go — there
// is no automated check across the Go/TS boundary.
export interface ResourcePermissions {
  pods: boolean
  services: boolean
  deployments: boolean
  daemonSets: boolean
  statefulSets: boolean
  replicaSets: boolean
  ingresses: boolean
  ingressClasses: boolean
  configMaps: boolean
  secrets: boolean
  events: boolean
  persistentVolumeClaims: boolean
  nodes: boolean
  namespaces: boolean
  jobs: boolean
  cronJobs: boolean
  horizontalPodAutoscalers: boolean
  persistentVolumes: boolean
  storageClasses: boolean
  podDisruptionBudgets: boolean
  networkPolicies: boolean
  serviceAccounts: boolean
  roles: boolean
  clusterRoles: boolean
  roleBindings: boolean
  clusterRoleBindings: boolean
  limitRanges: boolean
  gateways: boolean
  httpRoutes: boolean
  verticalPodAutoscalers: boolean
}

// Keys in ResourcePermissions that represent optional CRDs Radar can monitor
// when they're installed in the cluster. A `false` here means "CRD not
// installed (or RBAC denied)" — NOT "Radar is missing data the user expects",
// so banners about RBAC restrictions should ignore these keys.
//
// Keep in sync with dynamicCapabilityKinds in internal/k8s/capabilities_alignment_test.go.
export const OPTIONAL_RESOURCE_KINDS: ReadonlyArray<keyof ResourcePermissions> = [
  'gateways',
  'httpRoutes',
  'verticalPodAutoscalers',
]

// Per-workload write permissions. Field names must match
// WorkloadWritePermissions in internal/k8s/capabilities.go.
export interface WorkloadWritePermissions {
  deployments: boolean
  daemonSets: boolean
  statefulSets: boolean
  rollouts: boolean
}

export type WorkloadContainerType = 'container' | 'initContainer'

export interface WorkloadContainerImage {
  type: WorkloadContainerType
  name: string
  image: string
}

export interface WorkloadImageTarget {
  group: string
  resource: string
  kind: string
  namespace: string
  name: string
}

export type WorkloadUpdateBehaviorType =
  | 'rolling'
  | 'recreate'
  | 'paused'
  | 'onDelete'
  | 'partitioned'
  | 'canary'
  | 'blueGreen'

export interface WorkloadUpdateBehavior {
  type: WorkloadUpdateBehaviorType
  partition?: number
  autoPromote?: boolean
  gated?: boolean
}

export interface WorkloadImageInventory {
  target: WorkloadImageTarget
  containers: WorkloadContainerImage[]
  behavior: WorkloadUpdateBehavior
}

export interface WorkloadImageUpdate {
  type: WorkloadContainerType
  name: string
  previousImage: string
  image: string
}

export interface SetWorkloadImagesResult extends WorkloadImageInventory {
  object: Record<string, unknown>
}

export interface IntegrationCapability {
  state: CapacityIntegrationState
  reasonCode?: string
  cacheUnavailable?: boolean
}

// Feature capabilities based on RBAC permissions
export interface Capabilities {
  exec: boolean           // Terminal feature (pods/exec)
  localTerminal: boolean  // Local terminal available (not in-cluster, not disabled)
  logs: boolean           // Log viewer (pods/log)
  portForward: boolean    // Port forwarding (pods/portforward)
  secrets: boolean        // List secrets
  secretsUpdate: boolean  // Update secrets (inline editing)
  helmWrite: boolean      // Helm write operations (install, upgrade, rollback, uninstall, apply values)
  nodeWrite: boolean      // Node write operations (cordon, uncordon, drain)
  workloadWrites?: WorkloadWritePermissions // Workload patch permissions (restart/scale controls)
  mcpEnabled: boolean     // MCP server is running
  // Karpenter discovery and NodePool read state. Optional on the wire for the
  // same newer-frontend/older-backend reason as `deployment` below — consumers
  // must treat absence as "unknown" and fall back to discovery signals.
  karpenter?: IntegrationCapability
  resourceDiscovery?: CapacitySourceCoverage
  // How / where this Radar binary is running. Optional on the wire so a
  // newer frontend (e.g. radar-hub-web bundling a fresher @skyhook-io/radar-app)
  // doesn't crash against an older backend that hasn't shipped the field yet —
  // consumers should default to { mode: 'local' } when absent.
  deployment?: Deployment
  // Optional because Radar Hub can embed a newer frontend against an older
  // in-cluster Radar agent. Features require an explicit server advertisement.
  features?: FeatureCapabilities
  resources?: ResourcePermissions // Per-resource-type permissions
  authEnabled?: boolean   // Auth is enabled on the backend
  username?: string       // Authenticated user's username (when auth enabled)
  // Which Cloud-connect lane this deployment gets. Optional on the wire:
  // older backends don't advertise it — consumers fall back to wizard links.
  cloudConnect?: CloudConnectCapability
}

// CloudConnectCapability picks the Cloud funnel's connect lane: 'driver'
// means the in-product connect flow can run on this server (local, no auth,
// no existing tunnel); 'wizard' routes to the Hub's connect wizard at appUrl.
export interface CloudConnectCapability {
  lane: 'driver' | 'wizard'
  appUrl: string
  // Hub API origin the connect dialog reads its live copy from. Absent means
  // the server decided this deployment must not fetch — consumers render their
  // compiled-in copy instead.
  apiUrl?: string
}

export interface FeatureCapabilities {
  yamlReview?: boolean
  yamlSchemas?: boolean
  workloadImages?: boolean
}

// DeploymentMode is the closed set of topologies Radar can run in.
// `local` is a developer's machine with a kubeconfig (most OSS use).
// `in-cluster` is a Radar pod inside the cluster, no kubeconfig.
// `cloud` is in-cluster + tunneled to Radar Cloud's hub — UI suppresses
// chrome the hub already renders (cluster headline, local-MCP card).
export type DeploymentMode = 'local' | 'in-cluster' | 'cloud'

export interface Deployment {
  mode: DeploymentMode
}

// Core node kinds that have specific UI handling
//
// When adding a new kind here, also update:
// - ALL_NODE_KINDS in App.tsx
// - TopologyFilterSidebar.tsx RESOURCE_KINDS array
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
  | 'ServiceAccount'
  | 'SealedSecret'
  | 'ServiceMonitor'
  | 'PodMonitor'
  | 'HorizontalPodAutoscaler'
  | 'Job'
  | 'CronJob'
  | 'Workflow'
  | 'CronWorkflow'
  | 'WorkflowTemplate'
  | 'ClusterWorkflowTemplate'
  | 'PersistentVolumeClaim'
  | 'Node'
  | 'Namespace'
  | 'KnativeService'
  | 'KnativeConfiguration'
  | 'KnativeRevision'
  | 'KnativeRoute'
  | 'Broker'
  | 'Trigger'
  | 'PingSource'
  | 'ApiServerSource'
  | 'ContainerSource'
  | 'SinkBinding'
  | 'Channel'
  | 'IngressRoute'       // Traefik IngressRoute
  | 'IngressRouteTCP'    // Traefik IngressRouteTCP
  | 'IngressRouteUDP'    // Traefik IngressRouteUDP
  | 'Middleware'          // Traefik Middleware
  | 'MiddlewareTCP'      // Traefik MiddlewareTCP
  | 'TraefikService'     // Traefik TraefikService
  | 'ServersTransport'   // Traefik ServersTransport
  | 'ServersTransportTCP' // Traefik ServersTransportTCP
  | 'TLSOption'          // Traefik TLSOption
  | 'TLSStore'           // Traefik TLSStore
  | 'HTTPProxy'          // Contour HTTPProxy
  | 'CAPICluster'        // Cluster API Cluster
  | 'MachineDeployment'  // Cluster API MachineDeployment
  | 'MachineSet'         // Cluster API MachineSet
  | 'Machine'            // Cluster API Machine
  | 'MachinePool'        // Cluster API MachinePool
  | 'KubeadmControlPlane' // Cluster API KubeadmControlPlane
  | 'ClusterClass'       // Cluster API ClusterClass
  | 'MachineHealthCheck' // Cluster API MachineHealthCheck
  | 'CalicoNetworkPolicy'
  | 'CalicoGlobalNetworkPolicy'
  | 'CalicoStagedNetworkPolicy'
  | 'CalicoStagedGlobalNetworkPolicy'
  | 'CalicoStagedKubernetesNetworkPolicy'

// NodeKind can be a core kind or any arbitrary CRD kind string
export type NodeKind = CoreNodeKind | (string & {})

/** Short display names for verbose K8s kind names */
export function displayKind(kind: string): string {
  const shortNames: Record<string, string> = {
    HorizontalPodAutoscaler: 'HPA',
    PersistentVolumeClaim: 'PVC',
    ServiceAccount: 'Service Account',
    SealedSecret: 'Sealed Secret',
    ServiceMonitor: 'Service Monitor',
    PodMonitor: 'Pod Monitor',
    EC2NodeClass: 'NodeClass',
    KnativeService: 'Knative Svc',
    KnativeConfiguration: 'Knative Config',
    KnativeRevision: 'Revision',
    KnativeRoute: 'Route',
    ApiServerSource: 'ApiSrc',
    ContainerSource: 'ContainerSrc',
    IngressRouteTCP: 'TCP Route',
    IngressRouteUDP: 'UDP Route',
    MiddlewareTCP: 'MW TCP',
    TraefikService: 'Traefik Svc',
    ServersTransport: 'Transport',
    ServersTransportTCP: 'Transport TCP',
    CAPICluster: 'Cluster',
    MachineDeployment: 'Machine Deploy',
    MachineSet: 'MachineSet',
    Machine: 'Machine',
    MachinePool: 'MachinePool',
    KubeadmControlPlane: 'Control Plane',
    ClusterClass: 'ClusterClass',
    MachineHealthCheck: 'Health Check',
    CalicoNetworkPolicy: 'Calico NetworkPolicy',
    CalicoGlobalNetworkPolicy: 'Calico GlobalNetworkPolicy',
    CalicoStagedNetworkPolicy: 'Calico StagedNetworkPolicy',
    CalicoStagedGlobalNetworkPolicy: 'Calico StagedGlobalNetworkPolicy',
    CalicoStagedKubernetesNetworkPolicy: 'Calico StagedKubernetesNetworkPolicy',
  }
  return shortNames[kind] || kind
}

export type HealthStatus = 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown'

export type EdgeType = 'routes-to' | 'exposes' | 'manages' | 'uses' | 'configures' | 'protects'

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
  partial?: boolean
  /** Hover tooltip for the edge label. Used by the Reachability view to keep the
   *  DECLARED route path available when the label shows an overridden tested path. */
  labelTitle?: string
  skipIfKindVisible?: string // Hide this edge if this kind is visible (for shortcut edges)
  policyEffect?: 'allowed' | 'blocked' | 'unprotected'
  /** Reachability outcome for THIS route/hop edge (distinct from policyEffect, a
   *  NetworkPolicy concept). When set, the topology renderer colors the edge by
   *  reachability: verified/reached = green, unreachable = red, blocked (downstream
   *  of a break) = dashed gray, not-tested = neutral. Used by the Reachability view. */
  reachOutcome?: 'verified' | 'reached' | 'unreachable' | 'blocked' | 'not-tested'
}

export interface Topology {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  warnings?: string[] // Warnings about resources that failed to load
  largeCluster?: boolean // True if cluster exceeds large cluster threshold
  hiddenKinds?: string[] // Resource kinds auto-hidden for performance
  requiresNamespaceFilter?: boolean // True if cluster is too large for all-namespace topology
  estimatedNodes?: number // Pre-build node count estimate
  summaryMode?: boolean // True when the pod tier was collapsed into per-workload/service counts
  crdDiscoveryStatus?: 'idle' | 'discovering' | 'ready' // CRD discovery status
}

// PodSummary is stamped onto a workload or service node's data in summary mode.
export interface PodSummary {
  total: number
  healthy: number
  degraded: number
  unhealthy: number
}

// K8s Event (from SSE stream)
export interface K8sEvent {
  kind: string
  group?: string
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
  // Store-assigned arrival number (monotonic per store instance). The delta
  // cursor keys on it — arrival order, not event time, so late-arriving
  // events can't slip behind a client's cursor.
  seq?: number

  // Resource identity
  kind: string
  apiVersion?: string // e.g. "apps/v1", "cluster.x-k8s.io/v1beta1"
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
    'KnativeService', // Knative Serving
  ].includes(kind)
}

export function isDeploymentLikeWorkloadKind(kind: string, group = ''): boolean {
  switch (kind) {
    case 'Deployment':
    case 'ReplicaSet':
    case 'StatefulSet':
    case 'DaemonSet':
      return group === '' || group === 'apps'
    case 'Rollout':
      return group === '' || group === 'argoproj.io'
    default:
      return false
  }
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

export type TimeRange = '5m' | '30m' | '1h' | '6h' | '24h' | '7d' | '30d' | 'all'

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
  /** Original context name inside its source file. Set for isolated sources
   *  so display code need not infer backend-added collision qualifiers. */
  originalName?: string
  cluster: string
  user: string
  namespace: string
  isCurrent: boolean
  /** Source kubeconfig label (e.g. "kube-cluster-paris"). Set by the backend
   *  for contexts loaded through the isolated source registry. */
  source?: string
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
export type TopologyMode = 'resources' | 'traffic' | 'fleet'
export type ViewMode = 'resources' | 'traffic' | 'fleet'

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

// Resolved envFrom ConfigMap/Secret data for pod env var expansion
export interface ResolvedEnvFromEntry {
  keys: string[]
  values: Record<string, string>
  isSecret: boolean
}
export type ResolvedEnvFromKey = `configmap:${string}` | `secret:${string}`
export type ResolvedEnvFrom = Partial<Record<ResolvedEnvFromKey, ResolvedEnvFromEntry>>

export type PodEnvironmentValueState = 'resolved' | 'masked' | 'unavailable' | 'missing' | 'denied'

export interface PodEnvironmentSource {
  kind: string
  name?: string
  key?: string
  variable?: string
}

export interface PodEnvironmentEvidence {
  kind: 'modified' | 'removed' | 'added'
  changedAt: string
  message?: string
}

export interface PodEnvironmentRow {
  name: string
  value?: string
  state: PodEnvironmentValueState
  sensitive?: boolean
  source: PodEnvironmentSource
  dependencies?: PodEnvironmentSource[]
  shadowedSources?: PodEnvironmentSource[]
  message?: string
  optional?: boolean
  missingImpact?: 'startupBlocked' | 'restartBlocked'
  runtimeDependent?: boolean
  currentPodValue?: boolean
  placeholder?: boolean
  evidence?: PodEnvironmentEvidence
}

export interface PodEnvironmentContainer {
  name: string
  role: 'container' | 'init' | 'sidecar'
  rows: PodEnvironmentRow[]
  truncated?: boolean
}

export interface PodEnvironmentResponse {
  containers: PodEnvironmentContainer[]
  coverage: {
    observedSince?: string
    degraded?: boolean
    degradedReason?: string
    saturated?: boolean
  }
  partial?: boolean
  truncated?: boolean
}

export interface PodEnvironmentRevealResponse {
  value: string
  encoding: 'utf8' | 'base64'
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
  deployment?: ResourceRef   // Grandparent Deployment (for Pods owned by ReplicaSets)
  managedBy?: ResourceRef[]  // Topmost meaningful manager(s): GitOps controller (ArgoCD Application / Flux Kustomization / Flux HelmRelease), Helm release, or the topmost K8s owner. Synthesized server-side; replaces client-side detectGitOpsOwner.
  children?: ResourceRef[]
  services?: ResourceRef[]
  ingresses?: ResourceRef[]
  gateways?: ResourceRef[]
  routes?: ResourceRef[]
  configRefs?: ResourceRef[]
  consumers?: ResourceRef[]
  scalers?: ResourceRef[]
  storageRefs?: ResourceRef[]
  scaleTarget?: ResourceRef
  pdbs?: ResourceRef[]              // PodDisruptionBudgets protecting this workload
  networkPolicies?: ResourceRef[]   // NetworkPolicy / CiliumNetworkPolicy / ClusterNetworkPolicy variants selecting this workload
  pods?: ResourceRef[]
  serviceAccount?: ResourceRef      // For Pods: derived from pod.spec.serviceAccountName
  node?: ResourceRef                // For scheduled Pods: derived from pod.spec.nodeName
  resourceClaims?: ResourceRef[]    // For Pods: DRA ResourceClaims (direct + template-generated)
}

// Parsed X.509 certificate metadata (from backend cert parsing)
export interface CertificateInfo {
  subject: string
  sans?: string[]
  issuer: string
  selfSigned?: boolean
  keyType: string
  serialNumber: string
  notBefore: string
  notAfter: string
  daysLeft: number
  expired?: boolean
}

export interface SecretCertificateInfo {
  certificates: CertificateInfo[]
}

export type HPADiagnosisState =
  | 'ok'
  | 'scaling_up'
  | 'scaling_down'
  | 'limited_max'
  | 'limited_min'
  | 'metrics_unavailable'
  | 'metrics_incomplete'
  | 'unable_to_scale'
  | 'scaled_to_zero'
  | 'disabled'
  | 'pinned'
  | 'stale'
  | 'stabilized'
  | 'unknown'

export interface HPADiagnosis {
  state: HPADiagnosisState
  summary: string
  target: {
    apiVersion?: string
    kind?: string
    name?: string
  }
  bounds: {
    min: number
    max: number
    current: number
    desired: number
    observedGeneration?: number
    generation?: number
  }
  metrics?: HPAMetricSummary[]
  reasons?: HPAReasonSummary[]
}

export interface HPAReasonSummary {
  id: string
  message: string
  detail?: string
  conditionType?: string
  conditionReason?: string
}

export interface HPAMetricSummary {
  type: string
  name: string
  current?: string
  target?: string
  status: string
}

// Resource with computed relationships and optional certificate info (API response wrapper)
export interface ResourceWithRelationships<T = unknown> {
  resource: T
  relationships?: Relationships
  certificateInfo?: SecretCertificateInfo
  hpaDiagnosis?: HPADiagnosis
}

// API Resource (from discovery endpoint)
export type DynamicObservationState =
  | 'unwatched'
  | 'deferred'
  | 'syncing'
  | 'watched'
  | 'denied'
  | 'unsupported'

export type DynamicObservationOrigin = 'warmup' | 'small_eager' | 'on_demand'
export type DynamicObservationScope = 'cluster' | 'explicit_namespaces'

export interface DynamicResourceObservation {
  state: DynamicObservationState
  origin?: DynamicObservationOrigin
  observationStart?: string
  scope?: DynamicObservationScope
  namespaces?: string[]
  namespacePartial?: boolean
  truncated?: boolean
  reasonCode?: string
}

export interface APIResource {
  group: string
  version: string
  kind: string
  name: string // Plural name (e.g., "deployments")
  namespaced: boolean
  isCrd: boolean
  featured?: boolean
  verbs: string[]
  observation?: DynamicResourceObservation
}

// Helm release types
export interface HelmRelease {
  name: string
  namespace: string
  // Empty means Helm stores release metadata in namespace.
  storageNamespace?: string
  chart: string
  chartVersion: string
  appVersion: string
  status: string
  revision: number
  updated: string // ISO date string
  lastOperation?: HelmOperation
  operations?: HelmOperation[]
  // Health summary from owned resources
  resourceHealth?: 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown'
  healthIssue?: string    // Primary issue if unhealthy (e.g., "OOMKilled")
  healthSummary?: string  // Brief summary like "2/3 pods ready"
  // When set, this release was installed by Flux's helm-controller — the
  // user should manage it via the named HelmRelease CR (GitOps tab) rather
  // than helm CLI / Radar's Helm view, since changes here would get
  // reverted at the next reconcile. Format: "namespace/name".
  managedByFluxHelmRelease?: string
}

export interface HelmRevision {
  revision: number
  status: string
  chart: string
  appVersion: string
  description: string
  updated: string // ISO date string
}

export type HelmOperationKind = 'release_failed' | 'upgrade_failed' | 'upgrade_rolled_back' | 'rollback' | 'pending'
export type HelmOperationStatus = 'failed' | 'rolled_back' | 'completed' | 'stuck_pending'
export type HelmOperationConfidence = 'high' | 'medium' | 'low'
export type HelmOperationSource = 'helm_status' | 'helm_history'

export interface HelmOperation {
  kind: HelmOperationKind
  status: HelmOperationStatus
  source: HelmOperationSource
  confidence: HelmOperationConfidence
  message: string
  rawMessage?: string
  evidence?: string
  failureDescription?: string
  revision?: number
  failedRevision?: number
  rollbackRevision?: number
  targetRevision?: number
  pendingStatus?: string
  updated?: string
}

export type HelmOperationInsightState = 'active' | 'recovered'

export interface HelmSuggestedCompare {
  revision1: number
  revision2: number
  reason?: string
}

export interface HelmOperationInsight {
  state: HelmOperationInsightState
  primaryResource?: HelmOwnedResource
  relatedResources?: HelmOwnedResource[]
  signalCount?: number
  suggestedCompare?: HelmSuggestedCompare
}

export interface HelmReleaseDetail {
  name: string
  namespace: string
  // Empty means Helm stores release metadata in namespace.
  storageNamespace?: string
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
  resourceHealth?: 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown'
  healthIssue?: string
  healthSummary?: string
  hooks?: HelmHook[]
  hookDiagnostics?: HookDiagnostic[]
  readme?: string
  dependencies?: ChartDependency[]
  lastOperation?: HelmOperation
  operations?: HelmOperation[]
  operationInsight?: HelmOperationInsight
  // When set, this release was installed by Flux's helm-controller — see
  // HelmRelease.managedByFluxHelmRelease for context. Format: "namespace/name".
  managedByFluxHelmRelease?: string
}

export interface HelmHook {
  name: string
  namespace?: string
  kind: string
  path?: string
  manifestChanged?: boolean
  events: string[]
  weight: number
  status?: string
  startedAt?: string
  completedAt?: string
  deletePolicies?: string[]
  outputLogPolicies?: string[]
}

export interface HookDiagnostic {
  name: string
  namespace?: string
  kind: string
  events?: string[]
  phase: string
  message: string
  evidence?: HookEvidence
  evidenceUnavailable?: boolean
  evidenceUnavailableReason?: string
}

export interface HookEvidence {
  summary?: string
  jobs?: HookJobEvidence[]
  pods?: HookPodEvidence[]
  events?: HookEventEvidence[]
  logs?: HookLogEvidence[]
  errors?: string[]
}

export interface HookJobEvidence {
  name: string
  namespace?: string
  status?: string
  active?: number
  succeeded?: number
  failed?: number
  conditions?: string[]
}

export interface HookPodEvidence {
  name: string
  namespace?: string
  phase?: string
  ready?: string
  restartCount?: number
  reason?: string
  message?: string
}

export interface HookEventEvidence {
  involvedKind: string
  involvedName: string
  type?: string
  reason?: string
  message?: string
  count?: number
  lastSeen?: string
}

export interface HookLogEvidence {
  pod: string
  container: string
  previous?: boolean
  lines?: string[]
  totalLines?: number
  matchedLines?: number
  fallback?: boolean
  error?: string
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
  apiVersion?: string // e.g. "apps/v1", "cluster.x-k8s.io/v1beta1"
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

export interface ValuesDiff {
  revision1: number
  revision2: number
  allValues: boolean
  diff: string
}

export interface ManifestDiff {
  revision1: number
  revision2: number
  diff: string
}

export interface NotesDiff {
  revision1: number
  revision2: number
  diff: string
}

export interface HooksDiff {
  revision1: number
  revision2: number
  added: HelmHook[]
  removed: HelmHook[]
  modified: HelmHook[]
  unchanged: HelmHook[]
}

export interface HelmResourceRef {
  kind: string
  apiVersion?: string
  name: string
  namespace: string
}

export interface HelmResourceFieldChange {
  path: string
  oldValue: unknown
  newValue: unknown
}

export interface HelmResourceChange extends HelmResourceRef {
  summary?: string
  fieldCount: number
  fields: HelmResourceFieldChange[]
}

export interface ResourceDiff {
  revision1: number
  revision2: number
  added: HelmResourceRef[]
  removed: HelmResourceRef[]
  modified: HelmResourceChange[]
  unchanged: HelmResourceRef[]
  parseErrorCount?: number
}

// Selected Helm release (for drawer state)
export interface SelectedHelmRelease {
  namespace: string
  name: string
  storageNamespace?: string
}

// Upgrade availability info
export interface UpgradeInfo {
  currentVersion: string
  latestVersion?: string
  updateAvailable: boolean
  repositoryName?: string
  // 'repository' for classic HTTP-repo matches, 'oci' when discovered via a
  // registered OCI chart source. Absent when the source couldn't be determined.
  sourceType?: 'repository' | 'oci'
  // oci:// chart reference an OCI-sourced upgrade lives at (display only).
  chartRef?: string
  error?: string
  // Machine-readable source-resolution failure, used to make the Helm drawer
  // explain whether tracking an OCI source can help.
  sourceIssue?: 'untracked' | 'repo_index_error' | 'ambiguous_repository'
  // True only when the error is a genuinely untracked source (registering a
  // chart source could fix it). Kept for compatibility; prefer sourceIssue.
  untracked?: boolean
}

// Batch upgrade info keyed by "storageNamespace/name".
export interface BatchUpgradeInfo {
  releases: Record<string, UpgradeInfo>
}

// Request body for applying new values to a release
export interface ApplyValuesRequest {
  values: Record<string, unknown>
  version?: string
  repository?: string
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
// Metrics Types
// ============================================================================

// Top metrics types (bulk, for resource table view)
export interface ContainerResourceMetrics {
  name: string
  cpu: number           // nanocores (usage)
  cpuRequest: number    // nanocores
  cpuLimit: number      // nanocores
  memory: number        // bytes (usage)
  memoryRequest: number // bytes
  memoryLimit: number   // bytes
}

export interface TopPodMetrics {
  namespace: string
  name: string
  cpu: number           // nanocores (usage)
  memory: number        // bytes (usage)
  cpuRequest: number    // nanocores (sum across running containers)
  cpuLimit: number      // nanocores (sum across running containers)
  memoryRequest: number // bytes (sum across running containers)
  memoryLimit: number   // bytes (sum across running containers)
  // Per-container breakdown; present only for pods with more than one running
  // container (regular + native sidecars). Absent for single-container pods.
  containers?: ContainerResourceMetrics[]
}

export interface TopNodeMetrics {
  name: string
  cpu: number              // nanocores (usage)
  memory: number           // bytes (usage)
  observedAt?: string      // exact metrics sample time; absent when no sample exists
  podCount: number         // pods scheduled on this node
  cpuAllocatable: number   // nanocores
  memoryAllocatable: number // bytes
}

export interface MetricsDataPoint {
  timestamp: string
  cpu: number      // CPU in nanocores
  memory: number   // Memory in bytes
}

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
  latencyNs?: number
  l7Type?: string // REQUEST, RESPONSE, SAMPLE
  httpProtocol?: string // HTTP/1.1, HTTP/2
  httpHeaders?: string[] // allowlisted headers as "key: value"
  dnsQuery?: string
  dnsIPs?: string[]
  dnsTTL?: number
  dnsRCode?: number
  dnsQTypes?: string[]
  trafficDirection?: string // ingress, egress
  dropReasonDesc?: string
  sourceService?: string
  destService?: string
  bytesSent: number
  bytesRecv: number
  connections: number
  /** The conversation's initiator could not be established, so source and
   *  destination are ordered arbitrarily rather than describing a caller. */
  directionUnknown?: boolean
  /** 5xx responses per second. How a rate-based source reports failures: it
   *  measures a rate rather than observing individual responses, so it has a
   *  status code for no single flow. */
  errorRate?: number
  verdict: string // forwarded, dropped, error
  lastSeen: string // ISO date string
}

// HTTP path statistics for aggregated flows
export interface HTTPPathStat {
  method: string
  path: string
  count: number
  avgMs?: number
  errorPct?: number // 4xx+5xx percentage
}

// DNS query statistics for aggregated flows
export interface DNSQueryStat {
  query: string
  count: number
  nxCount?: number // NXDOMAIN responses
  avgTTL?: number
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
  /** The conversation's initiator could not be established, so the endpoints are
   *  ordered arbitrarily and the edge is drawn without an arrowhead. */
  directionUnknown?: boolean
  l7Protocol?: string // HTTP, gRPC, DNS
  requestCount?: number
  errorCount?: number
  avgLatencyMs?: number
  latencyP50Ms?: number
  latencyP95Ms?: number
  latencyP99Ms?: number
  httpStatusCounts?: Record<string, number> // "2xx": 150, "5xx": 3
  topHTTPPaths?: HTTPPathStat[]
  topDNSQueries?: DNSQueryStat[]
  verdictCounts?: Record<string, number> // "forwarded": 500, "dropped": 3
  dropReasons?: Record<string, number>
}

// Cluster info for traffic detection
export interface TrafficClusterInfo {
  platform: string // rke2, gke, eks, aks, minikube, kind, docker-desktop, openshift, rancher, generic
  cni: string // cilium, canal, calico, flannel, vpc-cni, azure-cni, gke-native, unknown
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
  defaultValues?: Record<string, unknown>
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
  active: string
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
  /** 'transient' (or absent) means the condition may clear on its own and a
   *  retry is worthwhile. 'partial' means the flows are correct but incomplete
   *  for a reason retrying cannot change, so show the warning next to them and
   *  do not refetch. */
  warningKind?: 'transient' | 'partial'
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

// Main view type now includes 'traffic', 'cost', 'capacity', 'checks', 'gitops'.
// Library consumers (Radar Hub) get all GitOps surfaces — the package
// IS the public surface, so adding new top-level views must extend
// this type rather than rely on app-local extensions.
export type ExtendedMainView = MainView | 'traffic' | 'cost' | 'capacity' | 'checks' | 'gitops' | 'issues' | 'applications'

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
export interface WorkloadPodContainerInfo {
  name: string
  init?: boolean
  ready: boolean
  restartCount: number
}

export interface WorkloadPodInfo {
  name: string
  containers: string[]
  ready: boolean
  phase?: string
  nodeName?: string
  healthLevel?: HealthStatus
  reason?: string
  message?: string
  restartCount?: number
  lastTerminationReason?: string
  createdAt?: string
  containerStatuses?: WorkloadPodContainerInfo[]
  stepID?: string
  stepName?: string
  stepPhase?: string
  revisionIdentity?: string
  updatedRevision?: boolean
}

// SSE event types for workload log streaming
export type WorkloadLogEventType =
  | 'connected'
  | 'log'
  | 'pod_added'
  | 'pod_removed'
  | 'end'
  | 'error'

// Workload revision (for rollback)
export interface WorkloadRevision {
  number: number
  createdAt: string
  image: string
  isCurrent: boolean
  replicas: number
  template?: string // Pod template spec as YAML (for revision diff)
  // Rollouts only: mid-canary isCurrent (rolling out) and isStable (serving traffic) differ.
  isStable?: boolean
  podHash?: string
}

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
