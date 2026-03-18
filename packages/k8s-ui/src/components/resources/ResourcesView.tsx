import React, { useState, useMemo, useEffect, useCallback, useRef, useContext } from 'react'
import { TableVirtuoso, type TableVirtuosoHandle } from 'react-virtuoso'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import type { TopPodMetrics, TopNodeMetrics } from '../../types'
import {
  Search,
  RefreshCw,
  AlertTriangle,
  Globe,
  Shield,
  ChevronDown,
  ChevronUp,
  ArrowUpDown,
  Clock,
  ListFilter,
  X,
  Columns3,
  RotateCcw,
  Trash2,
  Tag,
  Copy,
  Check,
} from 'lucide-react'
import { clsx } from 'clsx'
import { ResourceBar } from '../ui/ResourceBar'
import type { SelectedResource, APIResource } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { categorizeResources, CORE_RESOURCES } from '../../utils/api-resources'
import {
  getPodStatus,
  getPodRestarts,
  getPodProblems,
  getContainerSquareStates,
  getWorkloadImages,
  getWorkloadConditions,
  getReplicaSetOwner,
  isReplicaSetActive,
  getServiceStatus,
  getServicePorts,
  getServiceExternalIP,
  getServiceSelector,
  getServiceEndpointsStatus,
  getIngressHosts,
  getIngressClass,
  hasIngressTLS,
  getIngressAddress,
  getIngressRules,
  getConfigMapKeys,
  getConfigMapSize,
  getSecretType,
  getSecretKeyCount,
  getJobStatus,
  getJobCompletions,
  getJobDuration,
  getCronJobStatus,
  getCronJobSchedule,
  getCronJobLastRun,
  getHPAStatus,
  getHPAReplicas,
  getHPATarget,
  getHPAMetrics,
  getNodeStatus,
  getNodeRoles,
  getNodeConditions,
  getNodeTaints,
  getNodeVersion,
  getPVCStatus,
  getPVCCapacity,
  getPVCAccessModes,
  getRolloutStatus,
  getRolloutStrategy,
  getRolloutReady,
  getRolloutStep,
  getWorkflowStatus,
  getWorkflowDuration,
  getWorkflowProgress,
  getWorkflowTemplate,
  getPVStatus,
  getPVAccessModes,
  getPVClaim,
  getStorageClassProvisioner,
  getStorageClassReclaimPolicy,
  getStorageClassBindingMode,
  getStorageClassExpansion,
  getGatewayStatus,
  getGatewayClass,
  getGatewayListeners,
  getGatewayAttachedRoutes,
  getGatewayAddresses,
  getGatewayClassStatus,
  getGatewayClassController,
  getGatewayClassDescription,
  getRouteStatus,
  getRouteParents,
  getRouteHostnames,
  getRouteBackends,
  getRouteRulesCount,
  getSealedSecretStatus,
  getSealedSecretKeyCount,
  getWorkflowTemplateCount,
  getWorkflowTemplateEntrypoint,
  getNetworkPolicyTypes,
  getNetworkPolicyRuleCount,
  getNetworkPolicySelector,
  getPDBStatus,
  getPDBBudget,
  getPDBHealthy,
  getPDBAllowed,
  getServiceAccountAutomount,
  getServiceAccountSecretCount,
  getRoleRuleCount,
  formatAge,
  formatDuration,
  truncate,
  getCellFilterValue,
  parseColumnFilters,
  serializeColumnFilters,
} from './resource-utils'
import { Tooltip } from '../ui/Tooltip'
// CRD-specific cell components (extracted)
import { GitRepositoryCell, OCIRepositoryCell, HelmRepositoryCell, KustomizationCell, FluxHelmReleaseCell, FluxAlertCell } from './renderers/flux-cells'
import { ArgoApplicationCell, ArgoApplicationSetCell, ArgoAppProjectCell } from './renderers/argo-cells'
import { VulnerabilityReportCell, ConfigAuditReportCell, ExposedSecretReportCell, RbacAssessmentReportCell, ClusterComplianceReportCell, SbomReportCell } from './renderers/trivy-cells'
import { CertificateCell, CertificateRequestCell, ClusterIssuerCell, IssuerCell, OrderCell, ChallengeCell } from './renderers/certmanager-cells'
import { NodePoolCell, NodeClaimCell, EC2NodeClassCell } from './renderers/karpenter-cells'
import { ScaledObjectCell, ScaledJobCell, TriggerAuthenticationCell, ClusterTriggerAuthenticationCell } from './renderers/keda-cells'
import { ServiceMonitorCell, PrometheusRuleCell, PodMonitorCell } from './renderers/prometheus-cells'
import { PolicyReportCell, ClusterPolicyReportCell, KyvernoPolicyCell, ClusterPolicyCell } from './renderers/kyverno-cells'
import { ExternalSecretCell, ClusterExternalSecretCell, SecretStoreCell, ClusterSecretStoreCell } from './renderers/eso-cells'
import { BackupCell, RestoreCell, ScheduleCell, BackupStorageLocationCell } from './renderers/velero-cells'
import { CNPGClusterCell, CNPGBackupCell, CNPGScheduledBackupCell, CNPGPoolerCell } from './renderers/cnpg-cells'
import { VirtualServiceCell, DestinationRuleCell, IstioGatewayCell, ServiceEntryCell, PeerAuthenticationCell, AuthorizationPolicyCell } from './renderers/istio-cells'
import { KnativeServiceCell, ConfigurationCell as KnativeConfigurationCell, RevisionCell as KnativeRevisionCell, RouteCell as KnativeRouteCell, BrokerCell, TriggerCell, EventTypeCell, PingSourceCell, ApiServerSourceCell, ContainerSourceCell, SinkBindingCell, ChannelCell, InMemoryChannelCell, SubscriptionCell, SequenceCell, ParallelCell, DomainMappingCell, ServerlessServiceCell, KnativeIngressCell, KnativeCertificateCell } from './renderers/knative-cells'
import { IngressRouteCell, MiddlewareCell, TraefikServiceCell, ServersTransportCell, TLSOptionCell } from './renderers/traefik-cells'
import { HTTPProxyCell } from './renderers/contour-cells'
import { useRegisterShortcut, useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { ResourcesSidebar } from './ResourcesSidebar'
import type { SelectedKindInfo } from './ResourcesSidebar'

// Pod problem filter options (special multi-select, not a single column value)
const POD_PROBLEMS = ['CrashLoopBackOff', 'ImagePullBackOff', 'OOMKilled', 'Unschedulable', 'Not Ready', 'High Restarts'] as const

// Columns to skip for auto-detected filters (high cardinality, text-like, or non-filterable)
const SKIP_FILTER_COLUMNS = new Set([
  'name', 'namespace', 'age', 'keys', 'size', 'images', 'domains', 'hosts', 'rules',
  'ports', 'message', 'url', 'ref', 'revision', 'path', 'selector', 'ready', 'restarts',
  'completions', 'duration', 'schedule', 'lastRun', 'target', 'replicas', 'metrics',
  'capacity', 'accessModes', 'volume', 'step', 'progress', 'template', 'expires',
  'issuer', 'domain', 'presented', 'listeners', 'routes', 'addresses', 'hostnames',
  'parents', 'backends', 'controller', 'description', 'externalIP', 'address',
  'conditions', 'taints', 'desired', 'upToDate', 'available', 'owner',
  'tls', 'endpoints', 'object', 'count', 'lastSeen', 'reason', 'source', 'inventory',
  'lastUpdated', 'chart', 'events', 'repo',
  'generators', 'applications', 'destinations', 'sources', 'budget', 'healthy', 'allowed',
  'secrets', 'subjects', 'role', 'entrypoint', 'templates',
])

// Column definitions per resource kind
interface Column {
  key: string
  label: string
  width?: string
  hideOnMobile?: boolean
  tooltip?: string // Explanation of what this column means
  defaultVisible?: boolean // false = hidden by default, shown via column picker
  defaultWidth?: number // default width in px (used for resizable columns)
  minWidth?: number // minimum width in px
}

// Tailwind width class → pixel minimum mapping for CSS Grid column sizing
const TAILWIND_WIDTH_TO_PX: Record<string, number> = {
  'w-12': 48, 'w-14': 56, 'w-16': 64, 'w-20': 80, 'w-24': 96,
  'w-28': 112, 'w-32': 128, 'w-36': 144, 'w-40': 160, 'w-44': 176,
  'w-48': 192, 'w-56': 224, 'w-64': 256,
}

function getColumnMinWidth(col: Column): number {
  if (col.minWidth) return col.minWidth
  if (!col.width) return 200 // Name column (no width class) gets wider minimum
  const match = col.width.match(/(?:min-)?w-(\d+)/)
  if (match) return TAILWIND_WIDTH_TO_PX[`w-${match[1]}`] || 80
  return 80
}

// Default columns for unknown resource types (CRDs)
const DEFAULT_COLUMNS: Column[] = [
  { key: 'name', label: 'Name' },
  { key: 'namespace', label: 'Namespace', width: 'w-48' },
  { key: 'status', label: 'Status', width: 'w-28' },
  { key: 'age', label: 'Age', width: 'w-24' },
]

const KNOWN_COLUMNS: Record<string, Column[]> = {
  pods: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'containers', label: 'Containers', width: 'w-28' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'cpu', label: 'CPU', width: 'w-40', tooltip: 'CPU usage / limit (marker = request)' },
    { key: 'memory', label: 'Memory', width: 'w-40', tooltip: 'Memory usage / limit (marker = request)' },
    { key: 'restarts', label: 'Restarts', width: 'w-24' },
    { key: 'podIP', label: 'Pod IP', width: 'w-32', defaultVisible: false },
    { key: 'node', label: 'Node', width: 'w-44', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-14' },
  ],
  deployments: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Ready pods / Desired replicas' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true, tooltip: 'Number of pods running the current pod template' },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true, tooltip: 'Number of pods available (ready for minReadySeconds)' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  daemonsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'desired', label: 'Desired', width: 'w-20', tooltip: 'Number of nodes that should run the daemon pod (based on node selector)' },
    { key: 'ready', label: 'Ready', width: 'w-20', tooltip: 'Number of pods that are ready (passing readiness probes)' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true, tooltip: 'Number of pods running the current pod template spec' },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true, tooltip: 'Number of pods available (ready for minReadySeconds duration)' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  statefulsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Ready pods / Desired replicas' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true, tooltip: 'Number of pods running the current pod template' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  replicasets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'owner', label: 'Owner', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  services: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'selector', label: 'Selector', width: 'w-48', hideOnMobile: true },
    { key: 'endpoints', label: 'Endpoints', width: 'w-24' },
    { key: 'ports', label: 'Ports', width: 'w-40' },
    { key: 'externalIP', label: 'External', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  ingresses: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'class', label: 'Class', width: 'w-24 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-48' },
    { key: 'rules', label: 'Rules', width: 'min-w-56', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'address', label: 'Address', width: 'min-w-32', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  nodes: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-44' },
    { key: 'roles', label: 'Roles', width: 'w-28' },
    { key: 'cpu', label: 'CPU', width: 'w-40', tooltip: 'Current CPU usage / allocatable' },
    { key: 'memory', label: 'Memory', width: 'w-40', tooltip: 'Current memory usage / allocatable' },
    { key: 'pods', label: 'Pods', width: 'w-28', tooltip: 'Pods running / allocatable' },
    { key: 'conditions', label: 'Conditions', width: 'w-40', hideOnMobile: true },
    { key: 'taints', label: 'Taints', width: 'w-24', hideOnMobile: true },
    { key: 'version', label: 'Version', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  configmaps: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'keys', label: 'Keys', width: 'w-48' },
    { key: 'size', label: 'Size', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  secrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'keys', label: 'Keys', width: 'w-20' },
    { key: 'expires', label: 'Expires', width: 'w-24', tooltip: 'Certificate expiry for TLS secrets' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  jobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'completions', label: 'Completions', width: 'w-28' },
    { key: 'duration', label: 'Duration', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  cronjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'schedule', label: 'Schedule', width: 'w-40' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  hpas: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'target', label: 'Target', width: 'w-48' },
    { key: 'replicas', label: 'Replicas', width: 'w-32' },
    { key: 'metrics', label: 'Metrics', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  horizontalpodautoscalers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'target', label: 'Target', width: 'w-48' },
    { key: 'replicas', label: 'Replicas', width: 'w-32' },
    { key: 'metrics', label: 'Metrics', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  persistentvolumeclaims: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'capacity', label: 'Capacity', width: 'w-24' },
    { key: 'storageClass', label: 'Storage Class', width: 'w-36', hideOnMobile: true },
    { key: 'accessModes', label: 'Access', width: 'w-20', tooltip: 'Access modes: RWO=ReadWriteOnce, RWX=ReadWriteMany, ROX=ReadOnlyMany' },
    { key: 'volume', label: 'Volume', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  rollouts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Phase', width: 'w-28' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Available / Desired replicas' },
    { key: 'strategy', label: 'Strategy', width: 'w-24' },
    { key: 'step', label: 'Step', width: 'w-20', hideOnMobile: true, tooltip: 'Current canary step / Total steps' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  workflows: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Phase', width: 'w-28' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'progress', label: 'Progress', width: 'w-24', hideOnMobile: true, tooltip: 'Succeeded steps / Total steps' },
    { key: 'template', label: 'Template', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  certificates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'domains', label: 'Domains', width: 'w-48' },
    { key: 'issuer', label: 'Issuer', width: 'w-36', hideOnMobile: true },
    { key: 'expires', label: 'Expires', width: 'w-24', tooltip: 'Days until certificate expires' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  persistentvolumes: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'capacity', label: 'Capacity', width: 'w-24' },
    { key: 'accessModes', label: 'Access', width: 'w-20', tooltip: 'RWO=ReadWriteOnce, ROX=ReadOnlyMany, RWX=ReadWriteMany' },
    { key: 'reclaimPolicy', label: 'Reclaim', width: 'w-20' },
    { key: 'storageClass', label: 'Storage Class', width: 'w-36', hideOnMobile: true },
    { key: 'claim', label: 'Claim', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  storageclasses: [
    { key: 'name', label: 'Name' },
    { key: 'provisioner', label: 'Provisioner', width: 'w-48' },
    { key: 'reclaimPolicy', label: 'Reclaim', width: 'w-20' },
    { key: 'bindingMode', label: 'Binding Mode', width: 'w-36' },
    { key: 'expansion', label: 'Expansion', width: 'w-24', tooltip: 'Whether volumes can be expanded after creation' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  certificaterequests: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'issuer', label: 'Issuer', width: 'w-36' },
    { key: 'approved', label: 'Approved', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterissuers: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'issuerType', label: 'Type', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  issuers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'issuerType', label: 'Type', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  orders: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'state', label: 'State', width: 'w-24' },
    { key: 'domains', label: 'Domains', width: 'w-48' },
    { key: 'issuer', label: 'Issuer', width: 'w-36', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  challenges: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'challengeType', label: 'Type', width: 'w-20' },
    { key: 'state', label: 'State', width: 'w-24' },
    { key: 'domain', label: 'Domain', width: 'w-48' },
    { key: 'presented', label: 'Presented', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  gateways: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'class', label: 'Class', width: 'w-36' },
    { key: 'listeners', label: 'Listeners', width: 'w-40', tooltip: 'Protocol:Port for each listener' },
    { key: 'routes', label: 'Routes', width: 'w-20', tooltip: 'Total attached routes across all listeners' },
    { key: 'addresses', label: 'Addresses', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  httproutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-16', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  gatewayclasses: [
    { key: 'name', label: 'Name' },
    { key: 'controller', label: 'Controller', width: 'w-64', tooltip: 'Gateway controller implementation (spec.controllerName)' },
    { key: 'description', label: 'Description', width: 'w-64', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  nodepools: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'nodeClass', label: 'Node Class', width: 'w-36' },
    { key: 'limits', label: 'Limits', width: 'w-36', tooltip: 'CPU and memory limits' },
    { key: 'disruption', label: 'Disruption', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  nodeclaims: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'instanceType', label: 'Instance Type', width: 'w-32' },
    { key: 'nodePool', label: 'Node Pool', width: 'w-32' },
    { key: 'nodeName', label: 'Node', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  ec2nodeclasses: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'ami', label: 'AMI', width: 'w-36', tooltip: 'AMI selector alias or ID' },
    { key: 'role', label: 'IAM Role', width: 'w-48' },
    { key: 'volumeSize', label: 'Volume', width: 'w-24', tooltip: 'Root volume size' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  scaledobjects: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'target', label: 'Target', width: 'w-48', tooltip: 'Scale target workload' },
    { key: 'replicas', label: 'Replicas', width: 'w-28', tooltip: 'Min-Max replica range' },
    { key: 'triggerTypes', label: 'Trigger Types', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  scaledjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'target', label: 'Job Target', width: 'w-48' },
    { key: 'strategy', label: 'Strategy', width: 'w-28' },
    { key: 'triggerTypes', label: 'Trigger Types', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  triggerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'secretTargetRef', label: 'Secret Refs', width: 'w-20' },
    { key: 'env', label: 'Env Vars', width: 'w-20' },
    { key: 'hashiCorpVault', label: 'Vault', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clustertriggerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'secretTargetRef', label: 'Secret Refs', width: 'w-20' },
    { key: 'env', label: 'Env Vars', width: 'w-20' },
    { key: 'hashiCorpVault', label: 'Vault', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  servicemonitors: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'endpoints', label: 'Endpoints', width: 'w-20', tooltip: 'Number of scrape endpoints' },
    { key: 'jobLabel', label: 'Job Label', width: 'w-32' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  prometheusrules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'groups', label: 'Groups', width: 'w-20' },
    { key: 'rules', label: 'Rules', width: 'w-20', tooltip: 'Total alert + recording rules' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  podmonitors: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'endpoints', label: 'Endpoints', width: 'w-20', tooltip: 'Number of pod metrics endpoints' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // ============================================================================
  // KYVERNO / POLICY REPORT RESOURCES
  // ============================================================================
  policyreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'warn', label: 'Warn', width: 'w-16' },
    { key: 'error', label: 'Err', width: 'w-16' },
    { key: 'skip', label: 'Skip', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterpolicyreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'warn', label: 'Warn', width: 'w-16' },
    { key: 'error', label: 'Err', width: 'w-16' },
    { key: 'skip', label: 'Skip', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  kyvernopolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-24', tooltip: 'Validation failure action (Enforce or Audit)' },
    { key: 'rules', label: 'Rules', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-24', tooltip: 'Validation failure action (Enforce or Audit)' },
    { key: 'rules', label: 'Rules', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  grpcroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-16', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  tcproutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-16', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  tlsroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48', tooltip: 'SNI hostnames for TLS routing' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-16', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  sealedsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Synced', width: 'w-24' },
    { key: 'keys', label: 'Keys', width: 'w-20' },
    { key: 'type', label: 'Type', width: 'w-36', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  workflowtemplates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'entrypoint', label: 'Entrypoint', width: 'w-36' },
    { key: 'templates', label: 'Templates', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  networkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'policyTypes', label: 'Types', width: 'w-28' },
    { key: 'selector', label: 'Pod Selector', width: 'w-48' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  poddisruptionbudgets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'budget', label: 'Budget', width: 'w-36' },
    { key: 'healthy', label: 'Healthy', width: 'w-24' },
    { key: 'allowed', label: 'Allowed', width: 'w-24', tooltip: 'Number of disruptions currently allowed' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  serviceaccounts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'automount', label: 'Automount', width: 'w-24', tooltip: 'Whether token is automatically mounted in pods' },
    { key: 'secrets', label: 'Secrets', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  roles: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterroles: [
    { key: 'name', label: 'Name' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  rolebindings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'role', label: 'Role', width: 'w-48' },
    { key: 'subjects', label: 'Subjects', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterrolebindings: [
    { key: 'name', label: 'Name' },
    { key: 'role', label: 'Role', width: 'w-48' },
    { key: 'subjects', label: 'Subjects', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  ingressclasses: [
    { key: 'name', label: 'Name' },
    { key: 'controller', label: 'Controller', width: 'w-64' },
    { key: 'default', label: 'Default', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  leases: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'holder', label: 'Holder', width: 'w-48' },
    { key: 'renewTime', label: 'Last Renewed', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  priorityclasses: [
    { key: 'name', label: 'Name' },
    { key: 'value', label: 'Value', width: 'w-24' },
    { key: 'globalDefault', label: 'Global Default', width: 'w-28' },
    { key: 'preemptionPolicy', label: 'Preemption', width: 'w-32' },
    { key: 'description', label: 'Description', width: 'w-64', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  runtimeclasses: [
    { key: 'name', label: 'Name' },
    { key: 'handler', label: 'Handler', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  mutatingwebhookconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'webhooks', label: 'Webhooks', width: 'w-20' },
    { key: 'failurePolicy', label: 'Failure Policy', width: 'w-28' },
    { key: 'target', label: 'Target', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  validatingwebhookconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'webhooks', label: 'Webhooks', width: 'w-20' },
    { key: 'failurePolicy', label: 'Failure Policy', width: 'w-28' },
    { key: 'target', label: 'Target', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  events: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-20' },
    { key: 'reason', label: 'Reason', width: 'w-28' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'object', label: 'Object', width: 'w-48', hideOnMobile: true },
    { key: 'count', label: 'Count', width: 'w-16' },
    { key: 'lastSeen', label: 'Last Seen', width: 'w-24' },
  ],
  // ============================================================================
  // FLUXCD GITOPS RESOURCES
  // ============================================================================
  gitrepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'ref', label: 'Ref', width: 'w-32', tooltip: 'Branch, tag, or semver' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Revision', width: 'w-24', hideOnMobile: true, tooltip: 'Last fetched commit SHA' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  ocirepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'ref', label: 'Tag', width: 'w-24', tooltip: 'OCI tag or semver' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Digest', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  helmrepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'type', label: 'Type', width: 'w-20', tooltip: 'default (Helm) or oci' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  kustomizations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'source', label: 'Source', width: 'w-48', tooltip: 'Source GitRepository or OCIRepository' },
    { key: 'path', label: 'Path', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Revision', width: 'w-48', hideOnMobile: true, tooltip: 'Applied git revision' },
    { key: 'inventory', label: 'Resources', width: 'w-24', tooltip: 'Number of managed resources' },
    { key: 'lastUpdated', label: 'Last Updated', width: 'w-28', tooltip: 'Time since last successful reconciliation' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  helmreleases: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'chart', label: 'Chart', width: 'w-40' },
    { key: 'version', label: 'Version', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Rev', width: 'w-16', hideOnMobile: true, tooltip: 'Helm release revision number' },
    { key: 'lastUpdated', label: 'Last Updated', width: 'w-28', tooltip: 'Time since last successful reconciliation' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  alerts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-40' },
    { key: 'events', label: 'Events', width: 'w-24', tooltip: 'Number of event sources' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // ============================================================================
  // ARGOCD GITOPS RESOURCES
  // ============================================================================
  applications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'project', label: 'Project', width: 'w-28' },
    { key: 'sync', label: 'Sync', width: 'w-24' },
    { key: 'health', label: 'Health', width: 'w-24' },
    { key: 'repo', label: 'Repository', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  applicationsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'generators', label: 'Generators', width: 'w-32' },
    { key: 'template', label: 'Template', width: 'w-40', hideOnMobile: true },
    { key: 'applications', label: 'Apps', width: 'w-20', tooltip: 'Number of generated applications' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  appprojects: [
    { key: 'name', label: 'Name' },
    { key: 'description', label: 'Description', width: 'w-64' },
    { key: 'destinations', label: 'Destinations', width: 'w-24', tooltip: 'Allowed cluster/namespace destinations' },
    { key: 'sources', label: 'Sources', width: 'w-20', tooltip: 'Allowed source repositories' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Trivy Operator
  vulnerabilityreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical vulnerabilities' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High vulnerabilities' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium vulnerabilities' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low vulnerabilities' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  configauditreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  exposedsecretreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical secrets' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High secrets' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium secrets' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low secrets' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  rbacassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clusterrbacassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clustercompliancereports: [
    { key: 'name', label: 'Name' },
    { key: 'title', label: 'Framework', width: 'w-64' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  sbomreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'components', label: 'Components', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clustersbomreports: [
    { key: 'name', label: 'Name' },
    { key: 'components', label: 'Components', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  infraassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clusterinfraassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  // External Secrets Operator
  externalsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'store', label: 'Store', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-28' },
    { key: 'refreshInterval', label: 'Refresh', width: 'w-24' },
    { key: 'lastSync', label: 'Last Sync', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clusterexternalsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'namespaces', label: 'Namespaces', width: 'w-24' },
    { key: 'failed', label: 'Failed', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  secretstores: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'provider', label: 'Provider', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  clustersecretstores: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'provider', label: 'Provider', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // ============================================================================
  // VELERO BACKUP & DISASTER RECOVERY
  // ============================================================================
  backups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'storageLocation', label: 'Storage', width: 'w-36' },
    { key: 'namespaces', label: 'Scope', width: 'w-24', tooltip: 'Included namespaces (* = all)' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'expiry', label: 'Expires', width: 'w-24' },
    { key: 'errors', label: 'Errors', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  restores: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'backupName', label: 'Backup', width: 'w-40' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'errors', label: 'Errors', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  schedules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'schedule', label: 'Schedule', width: 'w-32' },
    { key: 'lastBackup', label: 'Last Backup', width: 'w-28' },
    { key: 'paused', label: 'Paused', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  backupstoragelocations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'provider', label: 'Provider', width: 'w-24' },
    { key: 'bucket', label: 'Bucket', width: 'w-40' },
    { key: 'default', label: 'Default', width: 'w-16' },
    { key: 'lastValidation', label: 'Validated', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // ============================================================================
  // CLOUDNATIVEPG (CNPG) POSTGRESQL
  // ============================================================================
  clusters: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'instances', label: 'Instances', width: 'w-24', tooltip: 'Ready/Total' },
    { key: 'primary', label: 'Primary', width: 'w-36' },
    { key: 'image', label: 'Image', width: 'w-28' },
    { key: 'storage', label: 'Storage', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  scheduledbackups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'schedule', label: 'Schedule', width: 'w-36' },
    { key: 'lastSchedule', label: 'Last Run', width: 'w-24' },
    { key: 'suspended', label: 'Suspended', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  poolers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-16' },
    { key: 'poolMode', label: 'Pool Mode', width: 'w-28' },
    { key: 'instances', label: 'Instances', width: 'w-24', tooltip: 'Ready/Total' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // ============================================================================
  // ISTIO SERVICE MESH
  // ============================================================================
  virtualservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-48' },
    { key: 'gateways', label: 'Gateways', width: 'w-40' },
    { key: 'routes', label: 'Routes', width: 'w-20', tooltip: 'HTTP + TCP + TLS routes' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  destinationrules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'host', label: 'Host', width: 'w-48' },
    { key: 'subsets', label: 'Subsets', width: 'w-20' },
    { key: 'loadBalancer', label: 'LB Policy', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  serviceentries: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-48' },
    { key: 'location', label: 'Location', width: 'w-28' },
    { key: 'ports', label: 'Ports', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  peerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'mode', label: 'mTLS Mode', width: 'w-28' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  authorizationpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-24' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Serving
  knativeservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'latestRevision', label: 'Latest Revision', width: 'w-44' },
    { key: 'traffic', label: 'Traffic', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  knativeconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'latestCreated', label: 'Latest Created', width: 'w-48' },
    { key: 'latestReady', label: 'Latest Ready', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  knativerevisions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'routing', label: 'Traffic', width: 'w-20', tooltip: 'Whether this revision is receiving traffic' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'concurrency', label: 'Concurrency', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  knativeroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'traffic', label: 'Traffic', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Eventing
  brokers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  triggers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'broker', label: 'Broker', width: 'w-36' },
    { key: 'subscriber', label: 'Subscriber', width: 'w-48' },
    { key: 'filter', label: 'Filter', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  eventtypes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-48' },
    { key: 'source', label: 'Source', width: 'w-44' },
    { key: 'reference', label: 'Reference', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Sources
  pingsources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'schedule', label: 'Schedule', width: 'w-36' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'data', label: 'Data', width: 'w-36' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  apiserversources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  containersources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  sinkbindings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'subject', label: 'Subject', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Messaging
  channels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  inmemorychannels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  subscriptions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'channel', label: 'Channel', width: 'w-44' },
    { key: 'subscriber', label: 'Subscriber', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Flows
  sequences: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'steps', label: 'Steps', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  parallels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'branches', label: 'Branches', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Knative Networking
  knativeingresses: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'ingressClass', label: 'Class', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-56' },
    { key: 'visibility', label: 'Visibility', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  knativecertificates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'dnsNames', label: 'DNS Names', width: 'w-56' },
    { key: 'secretName', label: 'Secret', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  serverlessservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'mode', label: 'Mode', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  domainmappings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  // Traefik
  ingressroutes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'min-w-48', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'middlewares', label: 'MW', width: 'w-14 shrink-0', tooltip: 'Unique middleware count' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  ingressroutetcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'min-w-48', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'middlewares', label: 'MW', width: 'w-14 shrink-0', tooltip: 'Unique middleware count' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  ingressrouteudps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'routes', label: 'Routes', width: 'min-w-48' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  middlewares: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  middlewaretcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  traefikservices: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-36 shrink-0' },
    { key: 'targets', label: 'Targets', width: 'min-w-48' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  serverstransports: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'serverName', label: 'Server Name', width: 'w-40' },
    { key: 'insecure', label: 'Skip Verify', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  serverstransporttcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'serverName', label: 'Server Name', width: 'w-40' },
    { key: 'insecure', label: 'Skip Verify', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  tlsoptions: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'minVersion', label: 'Min TLS', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  tlsstores: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
  // Contour
  httpproxies: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'fqdn', label: 'FQDN', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'w-20 shrink-0' },
    { key: 'includes', label: 'Includes', width: 'w-24 shrink-0' },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-16 shrink-0' },
  ],
}

// Map (plural, group) → KNOWN_COLUMNS key for kinds that collide with core K8s
const GROUP_QUALIFIED_COLUMN_KEYS: Record<string, Record<string, string>> = {
  services: { 'serving.knative.dev': 'knativeservices' },
  configurations: { 'serving.knative.dev': 'knativeconfigurations' },
  revisions: { 'serving.knative.dev': 'knativerevisions' },
  routes: { 'serving.knative.dev': 'knativeroutes' },
  ingresses: { 'networking.internal.knative.dev': 'knativeingresses' },
  certificates: { 'networking.internal.knative.dev': 'knativecertificates' },
}

// Normalize a kind name to its plural API form used in KNOWN_COLUMNS keys.
// Handles CRD singular names from URLs: 'ScaledObject' → 'scaledobjects', 'NodePool' → 'nodepools'
// When group is provided, resolves collisions (e.g., 'services' + 'serving.knative.dev' → 'knativeservices')
function normalizeKindToPlural(kind: string, group?: string): string {
  const lower = kind.toLowerCase()
  // Check group-qualified mapping first for collision resolution
  if (group && GROUP_QUALIFIED_COLUMN_KEYS[lower]?.[group]) {
    return GROUP_QUALIFIED_COLUMN_KEYS[lower][group]
  }
  if (KNOWN_COLUMNS[lower]) return lower
  // Try adding 's' but avoid double-s (e.g., "ingress" → "ingresss")
  if (!lower.endsWith('s') && KNOWN_COLUMNS[lower + 's']) return lower + 's'
  // Try 'es' for kinds ending in s/sh/ch/x/z (e.g., "ingress" → "ingresses")
  if (KNOWN_COLUMNS[lower + 'es']) return lower + 'es'
  // Try 'ies' for kinds ending in y (e.g., "httpproxy" → "httpproxies")
  if (lower.endsWith('y') && KNOWN_COLUMNS[lower.slice(0, -1) + 'ies']) return lower.slice(0, -1) + 'ies'
  return lower
}

function getColumnsForKind(kind: string, group?: string): Column[] {
  return KNOWN_COLUMNS[normalizeKindToPlural(kind, group)] || DEFAULT_COLUMNS
}

// Get the default visible columns for a kind
function getDefaultVisibleColumns(columns: Column[]): Set<string> {
  return new Set(columns.filter(c => c.defaultVisible !== false).map(c => c.key))
}

// localStorage helpers for column settings
const COLUMN_SETTINGS_PREFIX = 'radar-columns-'

interface ColumnSettings {
  visible: string[]
  widths: Record<string, number>
}

function loadColumnSettings(kind: string, group?: string): ColumnSettings | null {
  try {
    const key = COLUMN_SETTINGS_PREFIX + normalizeKindToPlural(kind, group)
    const raw = localStorage.getItem(key)
    if (raw) return JSON.parse(raw)
  } catch { /* ignore */ }
  return null
}

function saveColumnSettings(kind: string, group: string | undefined, settings: ColumnSettings) {
  try {
    const key = COLUMN_SETTINGS_PREFIX + normalizeKindToPlural(kind, group)
    localStorage.setItem(key, JSON.stringify(settings))
  } catch { /* ignore */ }
}

function clearColumnSettings(kind: string, group?: string) {
  try {
    const key = COLUMN_SETTINGS_PREFIX + normalizeKindToPlural(kind, group)
    localStorage.removeItem(key)
  } catch { /* ignore */ }
}

// Metrics context for passing top metrics to cell renderers without prop drilling
interface MetricsLookup {
  pods: Map<string, TopPodMetrics>   // key: "namespace/name"
  nodes: Map<string, TopNodeMetrics> // key: node name
}

const MetricsContext = React.createContext<MetricsLookup>({ pods: new Map(), nodes: new Map() })

// Context for deeply nested sub-components (PodCell, SecretCell) to access injected platform data
interface ResourcesViewData {
  onNavigate?: (path: string, options?: { replace?: boolean }) => void
  certExpiry?: Record<string, { expired?: boolean; daysLeft: number }>
  certExpiryError?: boolean
  onOpenLogs?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  onOpenWorkloadLogs?: (params: { namespace: string; workloadKind: string; workloadName: string }) => void
}

export const ResourcesViewDataContext = React.createContext<ResourcesViewData>({})

// Inline helper replacing ApiError/isForbiddenError from the removed api/client import
function isForbiddenError(error: any): boolean {
  return error?.status === 403
}

export interface ResourceQueryResult {
  data?: any[]
  isLoading: boolean
  error?: any
  refetch?: () => void
  dataUpdatedAt?: number
}

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: (resource: SelectedResource | null) => void
  onResourceClickYaml?: NavigateToResource
  onKindChange?: () => void // Called when user changes resource type in sidebar
  // Injected data (replacing hooks)
  apiResources?: APIResource[]
  /** @deprecated Use resourceCounts + resourceForbidden + selectedKindQuery instead */
  resourceQueries?: ResourceQueryResult[]
  // Lightweight counts for sidebar badges (from /api/resource-counts)
  resourceCounts?: Record<string, number>
  resourceForbidden?: string[]
  // Single query for the currently selected kind's full data
  selectedKindQuery?: ResourceQueryResult
  topPodMetrics?: TopPodMetrics[]
  topNodeMetrics?: TopNodeMetrics[]
  certExpiry?: Record<string, { expired?: boolean; daysLeft: number }>
  certExpiryError?: boolean
  // Pinned kinds
  pinned?: Array<{ name: string; kind: string; group: string }>
  togglePin?: (kind: { name: string; kind: string; group: string }) => void
  isPinned?: (kind: string, group?: string) => boolean
  // Navigation
  locationSearch?: string
  locationPathname?: string
  onNavigate?: (path: string, options?: { replace?: boolean }) => void
  /** URL prefix for this view (default: '/resources'). Must match the route path. */
  basePath?: string
  // Dock actions
  onOpenLogs?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  onOpenWorkloadLogs?: (params: { namespace: string; workloadKind: string; workloadName: string }) => void
  // Callback when selected kind changes — used by parent to fetch data for the selected kind
  onSelectedKindChange?: (kind: { name: string; kind: string; group: string }) => void
  /** When true, the sidebar is not rendered. Useful when a standalone ResourcesSidebar is used externally. */
  hideSidebar?: boolean
}

// Default selected kind
const DEFAULT_KIND_INFO: SelectedKindInfo = { name: 'pods', kind: 'Pod', group: '' }

// Read initial state from URL — kind is in the path: {basePath}/{kind}
function getInitialKindFromURL(basePath: string = '/resources'): SelectedKindInfo {
  // Read kind from URL path segment after basePath: {basePath}/{kind}
  const pathname = window.location.pathname
  const base = basePath.replace(/\/$/, '') // strip trailing slash
  let kind: string | null = null
  if (pathname.startsWith(base + '/')) {
    kind = pathname.slice(base.length + 1).split('/')[0] || null
  }
  const group = new URLSearchParams(window.location.search).get('apiGroup') || ''
  if (kind) {
    // Find matching resource from CORE_RESOURCES or use as-is
    // Only match core resources when no apiGroup is specified (avoids collisions like KNative Service)
    if (!group) {
      const coreMatch = CORE_RESOURCES.find(r => r.kind === kind || r.name === kind)
      if (coreMatch) {
        return { name: coreMatch.name, kind: coreMatch.kind, group: coreMatch.group }
      }
    }
    return { name: kind, kind: kind, group }
  }
  return DEFAULT_KIND_INFO
}

// Get initial filters from URL
function getInitialFiltersFromURL() {
  const params = new URLSearchParams(window.location.search)
  // Parse generic column filters
  const columnFilters = parseColumnFilters(params.get('filters'))
  const result = {
    search: params.get('search') || '',
    columnFilters,
    problemFilters: params.get('problems')?.split(',').filter(Boolean) || [],
    showInactive: params.get('showInactive') === 'true',
    labelSelector: params.get('labels') || '', // e.g., "app=caretta,version=v1"
    ownerKind: params.get('ownerKind') || '', // e.g., "DaemonSet"
    ownerName: params.get('ownerName') || '', // e.g., "app-caretta"
  }
  return result
}

// Sort state type
type SortDirection = 'asc' | 'desc' | null

export function ResourcesView({
  namespaces, selectedResource, onResourceClick, onResourceClickYaml, onKindChange,
  apiResources: apiResourcesProp,
  resourceQueries: resourceQueriesProp,
  resourceCounts: resourceCountsProp,
  resourceForbidden: resourceForbiddenProp,
  selectedKindQuery: selectedKindQueryProp,
  topPodMetrics,
  topNodeMetrics,
  certExpiry,
  certExpiryError,
  pinned = [],
  togglePin = () => {},
  isPinned = () => false,
  locationSearch = '',
  locationPathname = '',
  onNavigate,
  basePath = '/resources',
  onOpenLogs,
  onOpenWorkloadLogs,
  onSelectedKindChange,
  hideSidebar = false,
}: ResourcesViewProps) {
  const location = useMemo(() => ({ search: locationSearch, pathname: locationPathname }), [locationSearch, locationPathname])
  const initialFilters = getInitialFiltersFromURL()
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(() => getInitialKindFromURL(basePath))
  // Sync selectedKind from URL when locationPathname changes (e.g., browser back, external sidebar navigation)
  useEffect(() => {
    const kindFromURL = getInitialKindFromURL(basePath)
    if (kindFromURL.name !== selectedKind.name || kindFromURL.group !== selectedKind.group) {
      setSelectedKind(kindFromURL)
    }
  }, [locationPathname]) // eslint-disable-line react-hooks/exhaustive-deps
  // Notify parent of selected kind changes (including initial mount)
  useEffect(() => {
    onSelectedKindChange?.(selectedKind)
  }, [selectedKind.name, selectedKind.group]) // eslint-disable-line react-hooks/exhaustive-deps
  const [searchTerm, setSearchTerm] = useState(initialFilters.search)
  const [sortColumn, setSortColumn] = useState<string | null>(null)
  const [sortDirection, setSortDirection] = useState<SortDirection>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  // Filter state
  const [columnFilters, setColumnFilters] = useState<Record<string, string[]>>(initialFilters.columnFilters)
  const [problemFilters, setProblemFilters] = useState<string[]>(initialFilters.problemFilters)
  const [openColumnFilter, setOpenColumnFilter] = useState<string | null>(null)
  const [columnFilterSearch, setColumnFilterSearch] = useState('')
  const columnFilterDropdownRef = useRef<HTMLDivElement>(null)
  const [showProblemsDropdown, setShowProblemsDropdown] = useState(false)
  const problemsDropdownRef = useRef<HTMLDivElement>(null)
  const [showLabelsDropdown, setShowLabelsDropdown] = useState(false)
  const [labelSearch, setLabelSearch] = useState('')
  const labelsDropdownRef = useRef<HTMLDivElement>(null)
  // ReplicaSet-specific: hide inactive by default
  const [showInactiveReplicaSets, setShowInactiveReplicaSets] = useState(initialFilters.showInactive)
  // Column visibility and resize state
  const [visibleColumns, setVisibleColumns] = useState<Set<string>>(new Set())
  const [columnWidths, setColumnWidths] = useState<Record<string, number>>({})
  const [showColumnPicker, setShowColumnPicker] = useState(false)
  const columnPickerRef = useRef<HTMLDivElement>(null)
  // Label/owner filtering for deep-linking from workload details
  const [labelSelector, setLabelSelector] = useState<string>(initialFilters.labelSelector)
  const [ownerKind, setOwnerKind] = useState<string>(initialFilters.ownerKind)
  const [ownerName, setOwnerName] = useState<string>(initialFilters.ownerName)

  // Column filter helpers
  const clearColumnFilter = useCallback((key: string) => {
    setColumnFilters(prev => {
      const next = { ...prev }
      delete next[key]
      return next
    })
  }, [])

  const toggleColumnFilterValue = useCallback((key: string, value: string) => {
    setColumnFilters(prev => {
      const next = { ...prev }
      const current = next[key] || []
      const idx = current.indexOf(value)
      if (idx >= 0) {
        const updated = current.filter(v => v !== value)
        if (updated.length === 0) {
          delete next[key]
        } else {
          next[key] = updated
        }
      } else {
        next[key] = [...current, value]
      }
      return next
    })
  }, [])

  // Track if this is the initial mount to avoid re-syncing on first render
  const isInitialMount = useRef(true)
  const isSyncingFromURL = useRef(false)
  // Track whether the initial mount effect has processed the ?resource= param
  const hasProcessedInitialResource = useRef(false)
  // Set by sidebar kind change to push a browser history entry (vs replace for filter changes)
  const shouldPushHistory = useRef(false)

  // Ref to search input for keyboard shortcut
  const searchInputRef = useRef<HTMLInputElement>(null)
  // Resize state
  const resizingColumn = useRef<string | null>(null)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(0)
  const [resizeLineX, setResizeLineX] = useState<number | null>(null)
  const tableContainerRef = useRef<HTMLDivElement>(null)

  // Bulk metrics for table columns — provided via props
  const metricsLookup = useMemo<MetricsLookup>(() => {
    const pods = new Map<string, TopPodMetrics>()
    const nodes = new Map<string, TopNodeMetrics>()
    if (topPodMetrics) {
      for (const m of topPodMetrics) {
        pods.set(`${m.namespace}/${m.name}`, m)
      }
    }
    if (topNodeMetrics) {
      for (const m of topNodeMetrics) {
        nodes.set(m.name, m)
      }
    }
    return { pods, nodes }
  }, [topPodMetrics, topNodeMetrics])

  // Load column settings from localStorage when kind changes
  const allColumns = useMemo(() => getColumnsForKind(selectedKind.name, selectedKind.group), [selectedKind.name, selectedKind.group])

  useEffect(() => {
    const saved = loadColumnSettings(selectedKind.name, selectedKind.group)
    if (saved) {
      // If saved columns are just the defaults but this kind has specialized columns,
      // discard the stale save and use the specialized columns instead
      const defaultKeys = DEFAULT_COLUMNS.map(c => c.key)
      const isStaleDefaults = allColumns !== DEFAULT_COLUMNS &&
        saved.visible.length === defaultKeys.length &&
        saved.visible.every(v => defaultKeys.includes(v))
      if (isStaleDefaults) {
        clearColumnSettings(selectedKind.name, selectedKind.group)
        setVisibleColumns(getDefaultVisibleColumns(allColumns))
        setColumnWidths({})
      } else {
        setVisibleColumns(new Set(saved.visible))
        setColumnWidths(saved.widths || {})
      }
    } else {
      setVisibleColumns(getDefaultVisibleColumns(allColumns))
      setColumnWidths({})
    }
  }, [selectedKind.name, selectedKind.group, allColumns])

  // Save column settings when they change (skip initial load)
  const isColumnSettingsLoaded = useRef(false)
  useEffect(() => {
    if (visibleColumns.size === 0) return // not loaded yet
    if (!isColumnSettingsLoaded.current) {
      isColumnSettingsLoaded.current = true
      return
    }
    saveColumnSettings(selectedKind.name, selectedKind.group, {
      visible: Array.from(visibleColumns),
      widths: columnWidths,
    })
  }, [visibleColumns, columnWidths, selectedKind.name, selectedKind.group])

  // Close column picker on outside click or Escape
  useEffect(() => {
    if (!showColumnPicker) return
    const handleClick = (e: MouseEvent) => {
      if (columnPickerRef.current && !columnPickerRef.current.contains(e.target as Node)) {
        setShowColumnPicker(false)
      }
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setShowColumnPicker(false) }
    }
    document.addEventListener('mousedown', handleClick)
    document.addEventListener('keydown', handleKey, true)
    return () => { document.removeEventListener('mousedown', handleClick); document.removeEventListener('keydown', handleKey, true) }
  }, [showColumnPicker])

  // Whether any columns have been resized (triggers switch to fixed grid sizes + spacer)
  const hasResizedColumns = Object.keys(columnWidths).length > 0

  // Column resize handlers
  const handleResizeStart = useCallback((e: React.MouseEvent, colKey: string, currentWidth: number) => {
    e.preventDefault()
    e.stopPropagation()
    resizingColumn.current = colKey
    resizeStartX.current = e.clientX
    resizeStartWidth.current = currentWidth

    // On first resize, snapshot all column widths from the DOM to switch to fixed grid sizes
    const container = tableContainerRef.current
    setColumnWidths(prev => {
      const hasWidths = Object.keys(prev).length > 0
      if (hasWidths) return prev
      // Snapshot all visible <th> widths
      const ths = container?.querySelectorAll('thead th')
      if (!ths) return prev
      const snapped: Record<string, number> = {}
      const cols = allColumns.filter(c => visibleColumns.has(c.key))
      ths.forEach((th, i) => {
        if (cols[i]) {
          snapped[cols[i].key] = th.getBoundingClientRect().width
        }
      })
      return snapped
    })

    // Show the resize line at the initial position
    const containerRect = container?.getBoundingClientRect()
    if (containerRect) {
      setResizeLineX(e.clientX - containerRect.left + (container?.scrollLeft ?? 0))
    }

    const handleMouseMove = (me: MouseEvent) => {
      if (!resizingColumn.current) return
      const diff = me.clientX - resizeStartX.current

      // Allow shrinking to a small minimum — content truncates with ellipsis
      const minW = 48

      const newWidth = Math.max(minW, resizeStartWidth.current + diff)
      setColumnWidths(prev => ({ ...prev, [resizingColumn.current!]: newWidth }))
      // Update resize line position, clamped to the constrained width
      const rect = container?.getBoundingClientRect()
      if (rect) {
        const clampedClientX = resizeStartX.current + (newWidth - resizeStartWidth.current)
        setResizeLineX(clampedClientX - rect.left + (container?.scrollLeft ?? 0))
      }
    }

    const handleMouseUp = () => {
      resizingColumn.current = null
      setResizeLineX(null)
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      // Suppress the click event that fires after mouseup to prevent accidental sort toggle
      document.addEventListener('click', (e) => e.stopPropagation(), { capture: true, once: true })
    }

    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
  }, [allColumns, visibleColumns])

  // Toggle column visibility
  const toggleColumnVisibility = useCallback((colKey: string) => {
    setVisibleColumns(prev => {
      const next = new Set(prev)
      if (next.has(colKey)) {
        next.delete(colKey)
      } else {
        next.add(colKey)
      }
      return next
    })
  }, [])

  // Reset column settings to defaults
  const resetColumnSettings = useCallback(() => {
    clearColumnSettings(selectedKind.name, selectedKind.group)
    setVisibleColumns(getDefaultVisibleColumns(allColumns))
    setColumnWidths({})
    isColumnSettingsLoaded.current = false
  }, [selectedKind.name, selectedKind.group, allColumns])

  // Keyboard shortcut: / to focus search
  useRegisterShortcut({
    id: 'resources-search',
    keys: '/',
    description: 'Focus search',
    category: 'Search',
    scope: 'resources',
    handler: () => searchInputRef.current?.focus(),
  })

  // Keyboard navigation: highlighted row state
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const virtuosoRef = useRef<TableVirtuosoHandle>(null)

  // Reset highlight when kind, search, sort, or namespace changes
  const namespacesKey = namespaces.join(',')
  useEffect(() => { setHighlightedIndex(-1) }, [selectedKind.name, searchTerm, sortColumn, sortDirection, namespacesKey])

  // Scroll highlighted row into view
  useEffect(() => {
    if (highlightedIndex >= 0) {
      virtuosoRef.current?.scrollToIndex({ index: highlightedIndex, align: 'center', behavior: 'auto' })
    }
  }, [highlightedIndex])

  // Open logs for pod / workload resources — provided via props
  const openLogs = onOpenLogs
  const openWorkloadLogs = onOpenWorkloadLogs

  // Helper: get resource at highlighted index
  const getHighlightedResource = useCallback(() => {
    // filteredResources is computed later in the component — use a ref to access it
    return highlightedResourceRef.current
  }, [])

  // Register navigation shortcuts
  useRegisterShortcuts([
    {
      id: 'resources-nav-down',
      keys: 'j',
      description: 'Next row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => {
        const max = filteredResourceCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'resources-nav-down-arrow',
      keys: 'ArrowDown',
      description: 'Next row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => {
        const max = filteredResourceCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'resources-nav-up',
      keys: 'k',
      description: 'Previous row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'resources-nav-up-arrow',
      keys: 'ArrowUp',
      description: 'Previous row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'resources-nav-top',
      keys: 'g g',
      description: 'Jump to first row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(0),
    },
    {
      id: 'resources-nav-bottom',
      keys: 'G',
      description: 'Jump to last row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(Math.max(0, filteredResourceCountRef.current - 1)),
    },
    {
      id: 'resources-open',
      keys: 'Enter',
      description: 'Open resource detail',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res?.metadata?.name) return
        onResourceClick?.({ kind: selectedKind.name, namespace: res.metadata.namespace || '', name: res.metadata.name, group: selectedKind.group })
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'resources-open-detail',
      keys: 'd',
      description: 'Open resource detail',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res?.metadata?.name) return
        onResourceClick?.({ kind: selectedKind.name, namespace: res.metadata.namespace || '', name: res.metadata.name, group: selectedKind.group })
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'resources-open-yaml',
      keys: 'y',
      description: 'Open YAML view',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res?.metadata?.name) return
        const cb = onResourceClickYaml || onResourceClick
        cb?.({ kind: selectedKind.name, namespace: res.metadata.namespace || '', name: res.metadata.name, group: selectedKind.group })
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'resources-logs',
      keys: 'l',
      description: 'Open logs',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res) return
        const kindLower = selectedKind.name.toLowerCase()
        const ns = res.metadata?.namespace || ''
        const name = res.metadata?.name || ''
        if (kindLower === 'pods') {
          // For pods, use openLogs directly (not workload logs)
          const containers = (res.spec?.containers || []).map((c: { name: string }) => c.name)
          if (containers.length > 0) {
            openLogs?.({ namespace: ns, podName: name, containers })
          }
        } else if (['deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs'].includes(kindLower)) {
          openWorkloadLogs?.({ namespace: ns, workloadKind: selectedKind.kind, workloadName: name })
        }
      },
      enabled: highlightedIndex >= 0 && ['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'jobs'].includes(selectedKind.name.toLowerCase()),
    },
    {
      id: 'resources-sort-name',
      keys: 'N',
      description: 'Sort by name',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('name'),
    },
    {
      id: 'resources-sort-age',
      keys: 'A',
      description: 'Sort by age',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('age'),
    },
    {
      id: 'resources-sort-status',
      keys: 'S',
      description: 'Sort by status',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('status'),
    },
    {
      id: 'resources-clear-highlight',
      keys: 'Escape',
      description: 'Clear highlight / blur search',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        // Close dropdowns first, then clear highlight, then blur search
        if (showColumnPicker) { setShowColumnPicker(false); return }
        if (openColumnFilter) { setOpenColumnFilter(null); return }
        if (showProblemsDropdown) { setShowProblemsDropdown(false); return }
        if (showLabelsDropdown) { setShowLabelsDropdown(false); return }
        if (highlightedIndex >= 0) setHighlightedIndex(-1)
        else searchInputRef.current?.blur()
      },
    },
  ])

  // Refs for accessing filteredResources inside shortcuts (computed later in component)
  const filteredResourceCountRef = useRef(0)
  const highlightedResourceRef = useRef<any>(null)

  // Ref for flat kind list used by [ / ] sidebar navigation (populated from categories)
  const flatKindListRef = useRef<SelectedKindInfo[]>([])

  // Sidebar kind navigation: [ = previous kind, ] = next kind
  useRegisterShortcuts([
    {
      id: 'resources-prev-kind',
      keys: '[',
      description: 'Previous resource kind',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        const list = flatKindListRef.current
        if (list.length === 0) return
        const idx = list.findIndex(k => k.name === selectedKind.name && k.group === selectedKind.group)
        const prev = idx > 0 ? list[idx - 1] : list[list.length - 1]
        shouldPushHistory.current = true
        setSelectedKind(prev)
        onKindChange?.()
      },
    },
    {
      id: 'resources-next-kind',
      keys: ']',
      description: 'Next resource kind',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        const list = flatKindListRef.current
        if (list.length === 0) return
        const idx = list.findIndex(k => k.name === selectedKind.name && k.group === selectedKind.group)
        const next = idx < list.length - 1 ? list[idx + 1] : list[0]
        shouldPushHistory.current = true
        setSelectedKind(next)
        onKindChange?.()
      },
    },
  ])

  // Close dropdowns on outside click
  useEffect(() => {
    const anyOpen = showProblemsDropdown || showLabelsDropdown || openColumnFilter
    if (!anyOpen) return
    const handleClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement
      if (showProblemsDropdown && problemsDropdownRef.current && !problemsDropdownRef.current.contains(target)) {
        setShowProblemsDropdown(false)
      }
      if (showLabelsDropdown && labelsDropdownRef.current && !labelsDropdownRef.current.contains(target)) {
        setShowLabelsDropdown(false)
      }
      if (openColumnFilter && !target.closest('[data-column-filter-trigger]') && columnFilterDropdownRef.current && !columnFilterDropdownRef.current.contains(target)) {
        setOpenColumnFilter(null)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [showProblemsDropdown, showLabelsDropdown, openColumnFilter])

  // Sync state from URL when navigation occurs (e.g., deep linking from WorkloadRenderer)
  useEffect(() => {
    if (isInitialMount.current) {
      isInitialMount.current = false
      return
    }

    // Mark that we're syncing from URL to prevent URL write-back
    isSyncingFromURL.current = true

    // Re-read URL params and update state
    const newKind = getInitialKindFromURL(basePath)
    const newFilters = getInitialFiltersFromURL()

    // Update kind if it changed
    if (newKind.name !== selectedKind.name || newKind.group !== selectedKind.group) {
      setSelectedKind(newKind)
    }

    // Update owner filter if it changed
    if (newFilters.ownerKind !== ownerKind || newFilters.ownerName !== ownerName) {
      setOwnerKind(newFilters.ownerKind)
      setOwnerName(newFilters.ownerName)
    }

    // Update search if it changed
    if (newFilters.search !== searchTerm) {
      setSearchTerm(newFilters.search)
    }

    // Update column filters if changed
    const newFiltersStr = serializeColumnFilters(newFilters.columnFilters)
    const currentFiltersStr = serializeColumnFilters(columnFilters)
    if (newFiltersStr !== currentFiltersStr) {
      setColumnFilters(newFilters.columnFilters)
    }

    // Reset the flag after a tick to allow normal URL updates
    requestAnimationFrame(() => {
      isSyncingFromURL.current = false
    })
  }, [location.search, location.pathname]) // Re-run when URL path or search params change

  const navigate = useMemo(() => {
    if (!onNavigate) return (_pathOrObj: any, _opts?: any) => {}
    // Adapter: react-router-dom's navigate can be called as navigate(path) or navigate({ pathname, search }, { replace })
    return (pathOrObj: any, opts?: any) => {
      if (typeof pathOrObj === 'string') {
        onNavigate(pathOrObj, opts)
      } else if (pathOrObj && typeof pathOrObj === 'object') {
        const path = pathOrObj.pathname + (pathOrObj.search ? `?${pathOrObj.search}` : '')
        onNavigate(path, opts)
      }
    }
  }, [onNavigate])

  // Update URL with all state
  const updateURL = useCallback((
    kindInfo: SelectedKindInfo,
    search: string,
    colFilters: Record<string, string[]>,
    problems: string[],
    showInactive: boolean,
    resourceNs?: string,
    resourceName?: string,
    pushHistory?: boolean
  ) => {
    // Preserve existing params (like namespace from App)
    const params = new URLSearchParams(window.location.search)

    // Kind is now in the path (/resources/{kind}), not a query param
    params.delete('kind')
    if (kindInfo.group) {
      params.set('apiGroup', kindInfo.group)
    } else {
      params.delete('apiGroup')
    }
    if (search) {
      params.set('search', search)
    } else {
      params.delete('search')
    }
    // Write column filters as `filters` param; remove legacy `status` param
    const filtersStr = serializeColumnFilters(colFilters)
    if (filtersStr) {
      params.set('filters', filtersStr)
    } else {
      params.delete('filters')
    }
    if (problems.length > 0) {
      params.set('problems', problems.join(','))
    } else {
      params.delete('problems')
    }
    if (showInactive) {
      params.set('showInactive', 'true')
    } else {
      params.delete('showInactive')
    }
    if (resourceName) {
      // Namespaced: ns/name, cluster-scoped: just name
      params.set('resource', resourceNs ? `${resourceNs}/${resourceName}` : resourceName)
    } else {
      params.delete('resource')
    }

    const newPath = `${basePath}/${kindInfo.name}`
    const queryStr = params.toString()
    const newURL = queryStr ? `${newPath}?${queryStr}` : newPath

    if (pushHistory) {
      navigate({ pathname: newPath, search: queryStr }, { replace: false })
    } else {
      window.history.replaceState({}, '', newURL)
    }
  }, [navigate, basePath])

  // Update URL when any filter changes
  useEffect(() => {
    // Skip URL update if we're syncing FROM the URL (e.g., browser back button)
    if (isSyncingFromURL.current) {

      return
    }
    // Skip on initial mount so we don't strip ?resource= before the mount effect reads it
    if (!hasProcessedInitialResource.current) {

      return
    }
    // Skip URL update if selectedResource's kind doesn't match selectedKind (still syncing)
    if (selectedResource) {
      const resourceKindLower = selectedResource.kind.toLowerCase()
      if (selectedKind.name.toLowerCase() !== resourceKindLower) {

        return // Wait for kind sync effect to run first
      }
    }
    // Push history when kind changes (so browser back/forward works), replace for filter changes
    const pushHistory = shouldPushHistory.current
    shouldPushHistory.current = false

    updateURL(selectedKind, searchTerm, columnFilters, problemFilters, showInactiveReplicaSets, selectedResource?.namespace, selectedResource?.name, pushHistory)
  }, [selectedKind, searchTerm, columnFilters, problemFilters, showInactiveReplicaSets, selectedResource, updateURL])

  // Handle resource click from URL on mount
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const resourceParam = params.get('resource')
    if (resourceParam && onResourceClick) {
      const slashIndex = resourceParam.indexOf('/')
      if (slashIndex > 0) {
        // Namespaced: ?resource=namespace/name
        const ns = resourceParam.slice(0, slashIndex)
        const name = resourceParam.slice(slashIndex + 1)
        onResourceClick({ kind: selectedKind.name, namespace: ns, name, group: selectedKind.group })
      } else {
        // Cluster-scoped: ?resource=name (no namespace)
        onResourceClick({ kind: selectedKind.name, namespace: '', name: resourceParam, group: selectedKind.group })
      }
    }
    // Signal that initial resource param has been processed — URL update effect can now run
    hasProcessedInitialResource.current = true

    // If the URL has no kind segment (e.g., /resources), update to include the default kind
    const base = basePath.replace(/\/$/, '')
    const path = window.location.pathname
    if (path === base || path === base + '/') {
      const search = window.location.search
      window.history.replaceState({}, '', `${base}/${selectedKind.name}${search}`)
    }
  }, []) // Only on mount

  // API resources for dynamic sidebar — provided via props
  const apiResources = apiResourcesProp

  // Sync selectedKind when selectedResource changes from external navigation (e.g., from Helm view)
  // Also re-runs when apiResources loads, to correct CRD kinds that were initially resolved via fallback
  useEffect(() => {
    if (!selectedResource) return

    const resourceKindLower = selectedResource.kind.toLowerCase()

    // Prefer matching from resourcesToCount (deduped list used for queries) to ensure group consistency.
    // Raw apiResources can have duplicates (e.g., Event in both v1 and events.k8s.io) where find()
    // returns a different group than categorizeResources() deduped to, causing query index mismatch.
    // When selectedResource has a group, match on group too to handle collisions (e.g., KNative Service vs core Service).
    const resourceGroup = selectedResource.group ?? ''
    const countMatch = resourcesToCount.find(r =>
      (r.name.toLowerCase() === resourceKindLower || r.kind.toLowerCase() === resourceKindLower) &&
      r.group === resourceGroup
    ) ?? resourcesToCount.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )

    if (countMatch) {
      if (selectedKind.name === countMatch.name && selectedKind.kind === countMatch.kind && selectedKind.group === countMatch.group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: countMatch.name, kind: countMatch.kind, group: countMatch.group })
      return
    }

    // Fall back to raw API resources for kinds not yet in categories
    const apiMatch = apiResources?.find(r =>
      (r.name.toLowerCase() === resourceKindLower || r.kind.toLowerCase() === resourceKindLower) &&
      r.group === resourceGroup
    ) ?? apiResources?.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const coreMatch = CORE_RESOURCES.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const match = apiMatch || coreMatch

    if (match) {
      if (selectedKind.name === match.name && selectedKind.kind === match.kind && selectedKind.group === match.group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: match.name, kind: match.kind, group: match.group })
    } else {
      // Last resort fallback: derive singular, preserve group from navigation
      const singular = resourceKindLower.endsWith('s')
        ? resourceKindLower.slice(0, -1).charAt(0).toUpperCase() + resourceKindLower.slice(1, -1)
        : resourceKindLower.charAt(0).toUpperCase() + resourceKindLower.slice(1)
      const group = selectedResource.group ?? ''
      if (selectedKind.name === resourceKindLower && selectedKind.group === group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: resourceKindLower, kind: singular, group })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedResource, apiResources])

  // Categorize resources for sidebar
  const categories = useMemo(() => {
    if (!apiResources) return null
    return categorizeResources(apiResources)
  }, [apiResources])

  // Get resources to count - use kind as unique key since name can conflict (e.g., pods vs PodMetrics)
  const resourcesToCount = useMemo(() => {
    if (categories) {
      return categories.flatMap(c => c.resources).map(r => ({
        kind: r.kind,
        name: r.name,
        group: r.group,
      }))
    }
    return CORE_RESOURCES.map(r => ({
      kind: r.kind,
      name: r.name,
      group: r.group,
    }))
  }, [categories])

  // Correct selectedKind when apiResources loads (handles URL deep-links to CRD resources)
  // getInitialKindFromURL can't look up CRDs, so name may be wrong (e.g., 'HTTPRoute' instead of 'httproutes')
  useEffect(() => {
    if (!apiResources) return
    // Check if current selectedKind already matches a discovered resource
    const alreadyResolved = resourcesToCount.some(r =>
      r.name === selectedKind.name && r.group === selectedKind.group
    )
    if (alreadyResolved) return

    // Try to match by kind name (URL stores kind=HTTPRoute, API has name=httproutes)
    const match = apiResources.find(r =>
      r.kind === selectedKind.kind && r.group === selectedKind.group
    )
    if (match) {
      setSelectedKind({ name: match.name, kind: match.kind, group: match.group })
    }
  }, [apiResources, resourcesToCount, selectedKind.name, selectedKind.kind, selectedKind.group])

  // Resource data — prefer new lightweight props over legacy resourceQueries array
  const resourceQueries = resourceQueriesProp ?? []
  const useNewCountsMode = !!resourceCountsProp

  // Find the selected kind's query
  const selectedQueryIndex = useMemo(() => {
    return resourcesToCount.findIndex(r =>
      r.name === selectedKind.name && r.group === selectedKind.group
    )
  }, [resourcesToCount, selectedKind.name, selectedKind.group])

  const selectedQuery = selectedKindQueryProp ?? resourceQueries[selectedQueryIndex]
  const resources = selectedQuery?.data
  const isLoading = selectedQuery?.isLoading ?? true
  const selectedQueryError = selectedQuery?.error
  const isSelectedForbidden = isForbiddenError(selectedQueryError)
  const refetchFn = selectedQuery?.refetch
  const dataUpdatedAt = selectedQuery?.dataUpdatedAt

  const [refetch, isRefreshAnimating, refreshPhase] = useRefreshAnimation(() => refetchFn?.())

  // Track last updated time
  useEffect(() => {
    if (dataUpdatedAt) {
      setLastUpdated(new Date(dataUpdatedAt))
    }
  }, [dataUpdatedAt])

  // Derive counts — prefer lightweight resourceCounts prop over full query data
  const counts = useMemo(() => {
    if (useNewCountsMode) {
      // resourceCountsProp uses "group/Kind" keys for CRDs, "Kind" for core — same format as sidebar
      const results: Record<string, number> = {}
      for (const resource of resourcesToCount) {
        const key = resource.group ? `${resource.group}/${resource.kind}` : resource.kind
        results[key] = resourceCountsProp![key] ?? 0
      }
      return results
    }
    // Legacy: derive counts from full query data
    const results: Record<string, number> = {}
    resourcesToCount.forEach((resource, index) => {
      const data = resourceQueries[index]?.data
      const key = resource.group ? `${resource.group}/${resource.kind}` : resource.kind
      results[key] = Array.isArray(data) ? data.length : 0
    })
    return results
  }, [useNewCountsMode, resourcesToCount, resourceCountsProp, resourceQueries])

  // Track which resource kinds returned 403 Forbidden
  const forbiddenKinds = useMemo(() => {
    if (useNewCountsMode) {
      return new Set(resourceForbiddenProp ?? [])
    }
    // Legacy: derive from full query errors
    const result = new Set<string>()
    resourcesToCount.forEach((resource, index) => {
      if (isForbiddenError(resourceQueries[index]?.error)) {
        result.add(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)
      }
    })
    return result
  }, [useNewCountsMode, resourceForbiddenProp, resourcesToCount, resourceQueries])

  // Reset sort and filters when kind changes (but not when syncing from URL navigation)
  // Track previous kind to skip on mount (where the effect fires but kind hasn't actually changed)
  const prevKindRef = useRef(selectedKind.name)
  useEffect(() => {
    if (prevKindRef.current === selectedKind.name) {
      return
    }
    prevKindRef.current = selectedKind.name
    setSortColumn(null)
    setSortDirection(null)
    setOpenColumnFilter(null)
    if (!isSyncingFromURL.current) {
      setColumnFilters({})
    }
    setProblemFilters([])
  }, [selectedKind.name])

  // Toggle sort for a column
  const handleSort = useCallback((column: string) => {
    if (sortColumn === column) {
      // Cycle: asc -> desc -> null
      if (sortDirection === 'asc') {
        setSortDirection('desc')
      } else if (sortDirection === 'desc') {
        setSortColumn(null)
        setSortDirection(null)
      } else {
        setSortDirection('asc')
      }
    } else {
      setSortColumn(column)
      setSortDirection('asc')
    }
  }, [sortColumn, sortDirection])

  // Get sortable value from a resource for a given column
  const getSortValue = useCallback((resource: any, column: string, kind?: string): string | number => {
    const meta = resource.metadata || {}
    const status = resource.status || {}
    const kindLower = kind?.toLowerCase() || ''

    switch (column) {
      case 'name':
        return meta.name || ''
      case 'namespace':
        return meta.namespace || ''
      case 'age':
        return meta.creationTimestamp ? new Date(meta.creationTimestamp).getTime() : 0
      case 'status':
        return status.phase || ''
      case 'containers':
        // Pod containers column — sort by readiness ratio
        if (status.containerStatuses) {
          const ready = status.containerStatuses.filter((c: any) => c.ready).length
          const total = status.containerStatuses.length
          return total > 0 ? ready / total : 0
        }
        return 0
      case 'ready':
        // For DaemonSets, use numberReady/desiredNumberScheduled
        if (kindLower === 'daemonsets') {
          const desired = status.desiredNumberScheduled ?? 0
          const ready = status.numberReady ?? 0
          return desired > 0 ? ready / desired : 0
        }
        // For other workloads, use readyReplicas/replicas ratio
        const desiredReplicas = resource.spec?.replicas ?? 0
        const readyReplicas = status.readyReplicas ?? 0
        return desiredReplicas > 0 ? readyReplicas / desiredReplicas : 0
      case 'desired':
        // DaemonSet: desiredNumberScheduled
        return status.desiredNumberScheduled ?? 0
      case 'available':
        // DaemonSet: numberAvailable, others: availableReplicas
        return status.numberAvailable ?? status.availableReplicas ?? 0
      case 'upToDate':
        // DaemonSet: updatedNumberScheduled, others: updatedReplicas
        return status.updatedNumberScheduled ?? status.updatedReplicas ?? 0
      case 'restarts':
        return getPodRestarts(resource)
      case 'lastSeen': {
        const lastTs = resource.lastTimestamp || meta.creationTimestamp
        return lastTs ? new Date(lastTs).getTime() : 0
      }
      case 'count':
        return resource.count || 0
      case 'reason':
        return resource.reason || ''
      case 'object':
        return resource.involvedObject ? `${resource.involvedObject.kind}/${resource.involvedObject.name}` : ''
      case 'type':
        return resource.spec?.type || resource.type || ''
      case 'version':
        return status.nodeInfo?.kubeletVersion || ''
      case 'cpu': {
        if (kindLower === 'pods') {
          const key = `${meta.namespace}/${meta.name}`
          return metricsLookup.pods.get(key)?.cpu ?? 0
        }
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.cpu ?? 0
        }
        return 0
      }
      case 'memory': {
        if (kindLower === 'pods') {
          const key = `${meta.namespace}/${meta.name}`
          return metricsLookup.pods.get(key)?.memory ?? 0
        }
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.memory ?? 0
        }
        return 0
      }
      case 'pods': {
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.podCount ?? 0
        }
        return 0
      }
      default:
        return ''
    }
  }, [metricsLookup])

  // Helper to check if a pod matches problem filters
  const podMatchesProblemFilter = useCallback((pod: any, filters: string[]): boolean => {
    if (filters.length === 0) return true
    const problems = getPodProblems(pod)
    const problemMessages = problems.map(p => p.message)
    const restarts = getPodRestarts(pod)

    return filters.some(filter => {
      switch (filter) {
        case 'CrashLoopBackOff':
          return problemMessages.includes('CrashLoopBackOff')
        case 'ImagePullBackOff':
          return problemMessages.some(m => m.includes('ImagePull'))
        case 'OOMKilled':
          return problemMessages.includes('OOMKilled')
        case 'Unschedulable':
          return problemMessages.includes('Unschedulable')
        case 'Not Ready':
          return problemMessages.includes('Not Ready') || problemMessages.some(m => m.includes('Probe'))
        case 'High Restarts':
          return restarts > 5
        default:
          return false
      }
    })
  }, [])


  // Filter resources by search term, status, problems, and sort
  const filteredResources = useMemo(() => {
    if (!resources) return []

    let result = resources

    // Apply search filter
    if (searchTerm) {
      const term = searchTerm.toLowerCase()
      result = result.filter((r: any) =>
        r.metadata?.name?.toLowerCase().includes(term) ||
        r.metadata?.namespace?.toLowerCase().includes(term)
      )
    }

    // Apply column filters (generic, multi-select per column — OR within column, AND across columns)
    const activeColFilters = Object.entries(columnFilters).filter(([, vals]) => vals.length > 0)
    if (activeColFilters.length > 0) {
      const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)
      result = result.filter((r: any) =>
        activeColFilters.every(([col, vals]) =>
          vals.includes(getCellFilterValue(r, col, kindLower))
        )
      )
    }

    // Apply problem filters (pods only)
    if (problemFilters.length > 0 && selectedKind.name.toLowerCase() === 'pods') {
      result = result.filter((r: any) => podMatchesProblemFilter(r, problemFilters))
    }

    // Apply inactive ReplicaSet filter (default: hide inactive)
    if (selectedKind.name.toLowerCase() === 'replicasets' && !showInactiveReplicaSets) {
      result = result.filter((r: any) => isReplicaSetActive(r))
    }

    // Apply label selector filter (e.g., "app=caretta,version=v1") — OR logic: matches ANY selected label
    if (labelSelector) {
      const labelPairs = labelSelector.split(',').map(pair => {
        const [key, value] = pair.split('=')
        return { key: key?.trim(), value: value?.trim() }
      }).filter(p => p.key && p.value)

      result = result.filter((r: any) => {
        const labels = r.metadata?.labels || {}
        return labelPairs.some(({ key, value }) => labels[key] === value)
      })
    }

    // Apply owner filter (e.g., ownerKind=DaemonSet, ownerName=app-caretta)
    if (ownerKind && ownerName) {
      result = result.filter((r: any) => {
        const ownerRefs = r.metadata?.ownerReferences || []

        // For Deployment ownership: Pods are owned by ReplicaSets, not Deployments directly.
        // ReplicaSets created by Deployments are named "<deployment-name>-<hash>".
        if (ownerKind === 'Deployment') {
          return ownerRefs.some((ref: any) =>
            ref.kind === 'ReplicaSet' && ref.name.startsWith(ownerName + '-')
          )
        }

        // Direct owner match for other kinds (DaemonSet, StatefulSet, Job, etc.)
        return ownerRefs.some((ref: any) =>
          ref.kind === ownerKind && ref.name === ownerName
        )
      })
    }

    // Apply custom sorting if set
    if (sortColumn && sortDirection) {
      result = [...result].sort((a: any, b: any) => {
        const aVal = getSortValue(a, sortColumn, selectedKind.name)
        const bVal = getSortValue(b, sortColumn, selectedKind.name)
        let comparison = 0
        if (typeof aVal === 'number' && typeof bVal === 'number') {
          comparison = aVal - bVal
        } else {
          comparison = String(aVal).localeCompare(String(bVal))
        }
        return sortDirection === 'desc' ? -comparison : comparison
      })
    } else {
      // Default sort by kind
      const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)

      if (kindLower === 'pods') {
        // Completed pods at bottom, then sort by name for stability across refreshes
        result = [...result].sort((a: any, b: any) => {
          const aCompleted = a.status?.phase === 'Succeeded'
          const bCompleted = b.status?.phase === 'Succeeded'
          if (aCompleted && !bCompleted) return 1
          if (!aCompleted && bCompleted) return -1
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (kindLower === 'daemonsets') {
        // DaemonSets with 0 desired (empty/inactive) at bottom, then sort by ready desc
        result = [...result].sort((a: any, b: any) => {
          const aDesired = a.status?.desiredNumberScheduled ?? 0
          const bDesired = b.status?.desiredNumberScheduled ?? 0
          const aReady = a.status?.numberReady ?? 0
          const bReady = b.status?.numberReady ?? 0

          // Empty DaemonSets (0 desired) go to bottom
          if (aDesired === 0 && bDesired > 0) return 1
          if (aDesired > 0 && bDesired === 0) return -1

          // Then sort by health: unhealthy (ready < desired) first
          const aHealthy = aReady >= aDesired
          const bHealthy = bReady >= bDesired
          if (!aHealthy && bHealthy) return -1
          if (aHealthy && !bHealthy) return 1

          // Finally sort by name
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (kindLower === 'events') {
        // Events: most recently seen first, name tiebreaker for same-timestamp stability
        result = [...result].sort((a: any, b: any) => {
          const aTime = new Date(a.lastTimestamp || a.metadata?.creationTimestamp || 0).getTime()
          const bTime = new Date(b.lastTimestamp || b.metadata?.creationTimestamp || 0).getTime()
          if (bTime !== aTime) return bTime - aTime
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (['deployments', 'statefulsets', 'replicasets'].includes(kindLower)) {
        // Workloads: unhealthy first, scaled-to-zero at bottom
        result = [...result].sort((a: any, b: any) => {
          const aDesired = a.spec?.replicas ?? 0
          const bDesired = b.spec?.replicas ?? 0
          const aReady = a.status?.readyReplicas ?? 0
          const bReady = b.status?.readyReplicas ?? 0

          // Scaled-to-zero at bottom
          if (aDesired === 0 && bDesired > 0) return 1
          if (aDesired > 0 && bDesired === 0) return -1

          // Unhealthy (ready < desired) first
          const aHealthy = aReady >= aDesired
          const bHealthy = bReady >= bDesired
          if (!aHealthy && bHealthy) return -1
          if (aHealthy && !bHealthy) return 1

          // Finally sort by name
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else {
        // All other kinds: sort by name for stability across refreshes
        result = [...result].sort((a: any, b: any) =>
          (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        )
      }
    }

    return result
  }, [resources, searchTerm, columnFilters, problemFilters, showInactiveReplicaSets, labelSelector, ownerKind, ownerName, selectedKind.name, sortColumn, sortDirection, getSortValue, podMatchesProblemFilter])

  // For nodes table: compute the majority minor version so outliers can be highlighted
  const majorityNodeMinorVersion = useMemo(() => {
    if (selectedKind.name.toLowerCase() !== 'nodes') return ''
    const counts = new Map<string, number>()
    for (const r of filteredResources) {
      const full = r.status?.nodeInfo?.kubeletVersion || ''
      const match = full.match(/^v?(\d+\.\d+)/)
      if (match) counts.set(match[1], (counts.get(match[1]) || 0) + 1)
    }
    let best = ''
    let bestCount = 0
    for (const [v, c] of counts) {
      if (c > bestCount) { best = v; bestCount = c }
    }
    return counts.size > 1 ? best : '' // empty string means no skew
  }, [filteredResources, selectedKind.name])

  // Keep refs in sync for keyboard shortcuts (shortcuts can't capture filteredResources directly)
  filteredResourceCountRef.current = filteredResources.length
  highlightedResourceRef.current = highlightedIndex >= 0 ? filteredResources[highlightedIndex] ?? null : null

  // Scroll to selected row when selection changes or data loads
  const lastScrolledResource = useRef<string | null>(null)
  useEffect(() => {
    if (!selectedResource || filteredResources.length === 0) return
    const resourceKey = `${selectedResource.kind}/${selectedResource.namespace}/${selectedResource.name}`
    if (lastScrolledResource.current === resourceKey) return

    const timer = setTimeout(() => {
      const idx = filteredResources.findIndex((r: any) =>
        r.metadata?.name === selectedResource.name &&
        r.metadata?.namespace === (selectedResource.namespace || '')
      )
      if (idx >= 0) {
        lastScrolledResource.current = resourceKey
        virtuosoRef.current?.scrollToIndex({ index: idx, align: 'center', behavior: 'smooth' })
      }
    }, 100)

    return () => clearTimeout(timer)
  }, [selectedResource, filteredResources])

  // Build flat kind list for [ / ] sidebar navigation
  useEffect(() => {
    const list: SelectedKindInfo[] = []
    const seen = new Set<string>()
    const addKind = (k: SelectedKindInfo) => {
      const key = `${k.group}/${k.name}`
      if (!seen.has(key)) { seen.add(key); list.push(k) }
    }
    for (const p of pinned) addKind({ name: p.name, kind: p.kind, group: p.group })
    if (categories) {
      for (const cat of categories) {
        for (const r of cat.resources) {
          addKind({ name: r.name, kind: r.kind, group: r.group })
        }
      }
    }
    flatKindListRef.current = list
  }, [pinned, categories])

  // Filter columns by visibility
  const columns = useMemo(() => {
    if (visibleColumns.size === 0) return allColumns.filter(c => c.defaultVisible !== false)
    return allColumns.filter(c => visibleColumns.has(c.key))
  }, [allColumns, visibleColumns])

  // Stable virtuoso components — memoized to avoid remounting the table on every render
  const virtuosoComponents = useMemo(() => ({
    Table: React.forwardRef<HTMLTableElement, React.TableHTMLAttributes<HTMLTableElement>>(function VirtuosoTable(props, ref) {
      return (
        <table
          {...props}
          ref={ref}
          className="w-full"
          style={{ ...props.style, tableLayout: 'fixed' }}
        >
          <colgroup>
            {columns.map(col => (
              <col
                key={col.key}
                style={{
                  width: columnWidths[col.key]
                    ? `${columnWidths[col.key]}px`
                    : col.key === 'name' ? undefined : `${getColumnMinWidth(col)}px`,
                }}
              />
            ))}
            {hasResizedColumns && <col />}
          </colgroup>
          {props.children}
        </table>
      )
    }),
    TableRow: VirtuosoTableRow,
  }), [columns, columnWidths, hasResizedColumns])

  // Calculate filter options with counts based on current resources (before filtering)
  const filterOptions = useMemo(() => {
    if (!resources || resources.length === 0) return null

    const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)
    const columns = KNOWN_COLUMNS[kindLower] || DEFAULT_COLUMNS

    // Auto-detect filterable columns
    const filterableColumns: Array<{
      key: string
      label: string
      values: Array<{ value: string; count: number }>
    }> = []

    for (const col of columns) {
      if (SKIP_FILTER_COLUMNS.has(col.key)) continue

      // Count distinct values for this column
      const valueCounts: Record<string, number> = {}
      for (const r of resources) {
        const val = getCellFilterValue(r, col.key, kindLower)
        if (val) {
          valueCounts[val] = (valueCounts[val] || 0) + 1
        }
      }

      const distinctCount = Object.keys(valueCounts).length
      // Only include if 2-20 distinct values (too few = useless, too many = not a filter)
      // Node column gets a higher cap (50) since clusters commonly have 20-50 nodes
      const maxDistinct = col.key === 'node' ? 50 : 20
      if (distinctCount >= 2 && distinctCount <= maxDistinct) {
        filterableColumns.push({
          key: col.key,
          label: col.label,
          values: Object.entries(valueCounts)
            .map(([value, count]) => ({ value, count }))
            .sort((a, b) => b.count - a.count),
        })
      }
    }

    // Pod-specific: compute problem counts (multi-select, different semantics)
    let problems: Array<{ value: string; count: number }> | undefined
    if (kindLower === 'pods') {
      const problemCounts: Record<string, number> = {}
      POD_PROBLEMS.forEach(p => problemCounts[p] = 0)

      for (const pod of resources) {
        const podProblems = getPodProblems(pod)
        const msgs = podProblems.map(p => p.message)
        const restarts = getPodRestarts(pod)

        if (msgs.includes('CrashLoopBackOff')) problemCounts['CrashLoopBackOff']++
        if (msgs.some(m => m.includes('ImagePull'))) problemCounts['ImagePullBackOff']++
        if (msgs.includes('OOMKilled')) problemCounts['OOMKilled']++
        if (msgs.includes('Unschedulable')) problemCounts['Unschedulable']++
        if (msgs.includes('Not Ready') || msgs.some(m => m.includes('Probe'))) problemCounts['Not Ready']++
        if (restarts > 5) problemCounts['High Restarts']++
      }

      const activeProblems = POD_PROBLEMS
        .map(p => ({ value: p, count: problemCounts[p] }))
        .filter(p => p.count > 0)
      if (activeProblems.length > 0) {
        problems = activeProblems
      }
    }

    // Compute available labels for label filtering
    const labelCounts: Record<string, number> = {}
    for (const r of resources) {
      const labels = r.metadata?.labels || {}
      for (const [key, value] of Object.entries(labels)) {
        // Skip internal/noisy labels
        if (key.includes('pod-template-hash') || key.includes('controller-revision-hash')) continue
        const pair = `${key}=${value}`
        labelCounts[pair] = (labelCounts[pair] || 0) + 1
      }
    }
    const labelValues = Object.entries(labelCounts)
      .map(([pair, count]) => ({ value: pair, count }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 30) // cap at 30 most common labels


    if (filterableColumns.length === 0 && !problems && labelValues.length === 0) return null
    return { columns: filterableColumns, problems, labels: labelValues }
  }, [resources, selectedKind.name])

  // Map filterable columns by key for O(1) lookup in header rendering
  const filterableColumnMap = useMemo(() => {
    const map = new Map<string, { key: string; label: string; values: Array<{ value: string; count: number }> }>()
    if (filterOptions) {
      for (const col of filterOptions.columns) map.set(col.key, col)
    }
    return map
  }, [filterOptions])

  // Compute inactive ReplicaSet count for toggle display
  const inactiveReplicaSetCount = useMemo(() => {
    if (selectedKind.name.toLowerCase() !== 'replicasets' || !resources) return 0
    return resources.filter((r: any) => !isReplicaSetActive(r)).length
  }, [resources, selectedKind.name])

  // Check if any filters are active
  const hasOwnerFilter = ownerKind !== '' && ownerName !== ''


  // Toggle problem filter
  const toggleProblemFilter = useCallback((problem: string) => {
    setProblemFilters(prev =>
      prev.includes(problem)
        ? prev.filter(p => p !== problem)
        : [...prev, problem]
    )
  }, [])

  // Toggle a label pair in the label selector (e.g., "app=nginx")
  const toggleLabelFilter = useCallback((pair: string) => {
    setLabelSelector(prev => {
      const existing = prev ? prev.split(',').filter(Boolean) : []
      const newLabels = existing.includes(pair)
        ? existing.filter(p => p !== pair)
        : [...existing, pair]
      const newSelector = newLabels.join(',')
      // Sync to URL
      const params = new URLSearchParams(window.location.search)
      if (newSelector) {
        params.set('labels', newSelector)
      } else {
        params.delete('labels')
      }
      window.history.replaceState({}, '', `${window.location.pathname}?${params.toString()}`)
      return newSelector
    })
  }, [])

  // Parse active label pairs for display
  const activeLabelPairs = useMemo(() => {
    if (!labelSelector) return []
    return labelSelector.split(',').filter(Boolean)
  }, [labelSelector])

  const resourcesViewDataContextValue = useMemo<ResourcesViewData>(() => ({
    onNavigate,
    certExpiry,
    certExpiryError,
    onOpenLogs,
    onOpenWorkloadLogs,
  }), [onNavigate, certExpiry, certExpiryError, onOpenLogs, onOpenWorkloadLogs])

  return (
    <ResourcesViewDataContext.Provider value={resourcesViewDataContextValue}>
    <div className="flex h-full w-full">
      {/* Sidebar - Resource Types */}
      {!hideSidebar && (
        <ResourcesSidebar
          selectedKind={selectedKind}
          onSelectedKindChange={(kind) => {
            shouldPushHistory.current = true
            setSelectedKind(kind)
          }}
          onKindChange={onKindChange}
          apiResources={apiResourcesProp}
          resourceCounts={counts}
          resourceForbidden={Array.from(forbiddenKinds)}
          pinned={pinned}
          togglePin={togglePin}
          isPinned={isPinned}
        />
      )}

      {/* Main Content - Resource Table */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        {/* Toolbar */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border bg-theme-surface/50 shrink-0">
          <div className="flex-1 relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
            <input
              ref={searchInputRef}
              type="text"
              placeholder="Search... (press /)"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="w-full max-w-md pl-10 pr-4 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          {/* Problems dropdown (pods only) */}
          {filterOptions?.problems && filterOptions.problems.length > 0 && (
            <div className="relative" ref={problemsDropdownRef}>
              <button
                onClick={() => { setShowProblemsDropdown(!showProblemsDropdown); setShowLabelsDropdown(false) }}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-2 rounded-lg text-xs transition-colors',
                  problemFilters.length > 0
                    ? 'bg-red-500/20 text-red-700 dark:text-red-300 hover:bg-red-500/30'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <AlertTriangle className="w-3.5 h-3.5" />
                <span>Problems</span>
                {problemFilters.length > 0 && (
                  <span className="px-1.5 py-0.5 text-xs bg-red-500/30 text-red-700 dark:text-red-300 rounded">
                    {problemFilters.length}
                  </span>
                )}
              </button>
              {showProblemsDropdown && (
                <div className="absolute right-0 top-full mt-1 min-w-48 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50">
                  {problemFilters.length > 0 && (
                    <div className="flex items-center justify-between px-3 py-1.5 border-b border-theme-border">
                      <span className="text-xs font-medium text-theme-text-secondary">Problems</span>
                      <button onClick={() => { setProblemFilters([]); setShowProblemsDropdown(false) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 -mr-1 rounded transition-colors">Clear</button>
                    </div>
                  )}
                  <div className="py-1">
                    {filterOptions.problems.map(({ value, count }) => (
                      <button
                        key={value}
                        onClick={() => toggleProblemFilter(value)}
                        className={clsx(
                          'w-full text-left px-3 py-1.5 text-xs flex items-center justify-between gap-2 transition-colors',
                          problemFilters.includes(value)
                            ? 'bg-red-500/20 text-red-700 dark:text-red-300'
                            : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                        )}
                      >
                        <span className="truncate">{value}</span>
                        <span className="text-theme-text-disabled shrink-0">({count})</span>
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* Labels dropdown */}
          {filterOptions?.labels && filterOptions.labels.length > 0 && (
            <div className="relative" ref={labelsDropdownRef}>
              <button
                onClick={() => { setShowLabelsDropdown(!showLabelsDropdown); setShowProblemsDropdown(false); setLabelSearch('') }}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-2 rounded-lg text-xs transition-colors',
                  activeLabelPairs.length > 0
                    ? 'bg-green-500/20 text-green-700 dark:text-green-300 hover:bg-green-500/30'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <Tag className="w-3.5 h-3.5" />
                <span>Labels</span>
                {activeLabelPairs.length > 0 && (
                  <span className="px-1.5 py-0.5 text-xs bg-green-500/30 text-green-700 dark:text-green-300 rounded">
                    {activeLabelPairs.length}
                  </span>
                )}
              </button>
              {showLabelsDropdown && (() => {
                const labels = filterOptions.labels ?? []
                const filtered = labelSearch
                  ? labels.filter(l => l.value.toLowerCase().includes(labelSearch.toLowerCase()))
                  : labels
                return (
                  <div className="absolute right-0 top-full mt-1 min-w-64 max-w-80 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50">
                    <div className="flex items-center gap-2 p-2 border-b border-theme-border">
                      <div className="relative flex-1">
                        <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                        <input
                          type="text"
                          placeholder="Search labels..."
                          value={labelSearch}
                          onChange={(e) => setLabelSearch(e.target.value)}
                          autoFocus
                          className="w-full pl-7 pr-2 py-1.5 text-xs bg-theme-elevated border border-theme-border-light rounded text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-1 focus:ring-blue-500"
                        />
                      </div>
                      {activeLabelPairs.length > 0 && (
                        <button onClick={() => { setLabelSelector(''); setShowLabelsDropdown(false) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 rounded transition-colors shrink-0">Clear</button>
                      )}
                    </div>
                    <div className="py-1 max-h-64 overflow-y-auto">
                      {filtered.map(({ value, count }) => (
                        <button
                          key={value}
                          onClick={() => toggleLabelFilter(value)}
                          className={clsx(
                            'w-full text-left px-3 py-1.5 text-xs flex items-center justify-between gap-2 transition-colors',
                            activeLabelPairs.includes(value)
                              ? 'bg-green-500/20 text-green-700 dark:text-green-300'
                              : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                          )}
                        >
                          <span className="truncate" title={value}>{value}</span>
                          <span className="text-theme-text-disabled shrink-0">({count})</span>
                        </button>
                      ))}
                      {filtered.length === 0 && (
                        <div className="px-3 py-2 text-xs text-theme-text-disabled">No matches</div>
                      )}
                    </div>
                  </div>
                )
              })()}
            </div>
          )}

          {/* ReplicaSet inactive toggle */}
          {selectedKind.name.toLowerCase() === 'replicasets' && inactiveReplicaSetCount > 0 && (
            <label className="flex items-center gap-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary">
              <input
                type="checkbox"
                checked={showInactiveReplicaSets}
                onChange={(e) => setShowInactiveReplicaSets(e.target.checked)}
                className="w-3 h-3 rounded border-theme-border-light accent-blue-500"
              />
              Show inactive ({inactiveReplicaSetCount})
            </label>
          )}

          {/* Active filter badges — owner only (column filters shown on header, problems/labels on their buttons) */}
          {hasOwnerFilter && (
            <span className="flex items-center gap-1 px-2 py-1 text-xs bg-purple-500/20 text-purple-700 dark:text-purple-300 rounded">
              {ownerKind}: {ownerName}
              <button
                onClick={() => {
                  setOwnerKind('')
                  setOwnerName('')
                  const params = new URLSearchParams(window.location.search)
                  params.delete('ownerKind')
                  params.delete('ownerName')
                  window.history.replaceState({}, '', `${window.location.pathname}?${params.toString()}`)
                }}
                className="hover:text-theme-text-primary"
              >
                <X className="w-3 h-3" />
              </button>
            </span>
          )}

          {lastUpdated && (
            <div className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
              <Clock className="w-3.5 h-3.5" />
              <span>Updated {formatAge(lastUpdated.toISOString())}</span>
            </div>
          )}
          {/* Column picker */}
          <div className="relative" ref={columnPickerRef}>
            <button
              onClick={() => setShowColumnPicker(prev => !prev)}
              className={clsx(
                'p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg',
                showColumnPicker && 'bg-theme-elevated text-theme-text-primary'
              )}
              title="Configure columns"
            >
              <Columns3 className="w-4 h-4" />
            </button>
            {showColumnPicker && (
              <div className="absolute right-0 top-full mt-1 z-50 bg-theme-surface border border-theme-border rounded-lg shadow-lg py-1 min-w-[200px] max-h-[400px] overflow-auto">
                <div className="px-3 py-2 border-b border-theme-border flex items-center justify-between">
                  <span className="text-xs font-medium text-theme-text-secondary uppercase">Columns</span>
                  <button
                    onClick={resetColumnSettings}
                    className="text-xs text-theme-text-tertiary hover:text-theme-text-primary flex items-center gap-1"
                    title="Reset to defaults"
                  >
                    <RotateCcw className="w-3 h-3" />
                    Reset
                  </button>
                </div>
                {allColumns.map(col => (
                  <label
                    key={col.key}
                    className="flex items-center gap-2 px-3 py-1.5 hover:bg-theme-elevated"
                  >
                    <input
                      type="checkbox"
                      checked={visibleColumns.has(col.key)}
                      onChange={() => toggleColumnVisibility(col.key)}
                      disabled={col.key === 'name'}
                      className="rounded border-theme-border"
                    />
                    <span className={clsx(
                      'text-sm',
                      col.key === 'name' ? 'text-theme-text-tertiary' : 'text-theme-text-primary'
                    )}>
                      {col.label}
                    </span>
                  </label>
                ))}
              </div>
            )}
          </div>
          <button
            onClick={refetch}
            disabled={isRefreshAnimating}
            className={clsx(
              'p-2 hover:bg-theme-elevated rounded-lg disabled:opacity-50 transition-colors duration-500',
              refreshPhase === 'success' ? 'text-emerald-400' : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
            title="Refresh"
          >
            {refreshPhase === 'success'
              ? <Check className="w-4 h-4 stroke-[2.5]" />
              : <RefreshCw className={clsx('w-4 h-4', refreshPhase === 'spinning' && 'animate-spin')} />
            }
          </button>
        </div>

        {/* Table */}
        <div
          className="flex-1 overflow-y-auto overflow-x-hidden relative"
          ref={tableContainerRef}
          onClick={(e) => {
            if (e.target === e.currentTarget && selectedResource) {
              onResourceClick?.(null)
            }
          }}
        >
          {isLoading ? (
            <div className="absolute inset-0 flex items-center justify-center text-theme-text-tertiary">
              Loading...
            </div>
          ) : isSelectedForbidden ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center text-theme-text-tertiary">
              <Shield className="w-8 h-8 text-amber-400 mb-2" />
              <p className="text-theme-text-secondary font-medium">Access Restricted</p>
              <p className="text-sm mt-1">Insufficient permissions to list {selectedKind.kind} resources</p>
            </div>
          ) : filteredResources.length === 0 ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center text-theme-text-tertiary">
              <p>No {selectedKind.kind} found</p>
              {searchTerm && (
                <button
                  onClick={() => setSearchTerm('')}
                  className="flex items-center gap-1.5 text-sm mt-2 px-3 py-1.5 rounded-md bg-theme-elevated hover:bg-theme-border text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                >
                  No results for "{searchTerm}"
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
              {namespaces.length > 0 && <p className="text-sm mt-1 text-theme-text-disabled">Searching in {namespaces.length === 1 ? `namespace: ${namespaces[0]}` : `${namespaces.length} namespaces`}</p>}
              {/* Show active filters as dismissible badges so user can clear them */}
              {(() => {
                const activeColEntries = Object.entries(columnFilters).filter(([, vals]) => vals.length > 0)
                if (activeColEntries.length === 0 && problemFilters.length === 0 && !labelSelector) return null
                return (
                  <div className="flex flex-wrap items-center gap-1.5 mt-3">
                    {activeColEntries.map(([key, vals]) => (
                      <button
                        key={key}
                        onClick={() => clearColumnFilter(key)}
                        className="flex items-center gap-1 px-2 py-1 text-xs bg-blue-500/15 text-blue-700 dark:text-blue-300 rounded-md hover:bg-blue-500/25 transition-colors"
                      >
                        <ListFilter className="w-3 h-3" />
                        <span>{key}: {vals.join(', ')}</span>
                        <X className="w-3 h-3" />
                      </button>
                    ))}
                    {problemFilters.length > 0 && (
                      <button
                        onClick={() => setProblemFilters([])}
                        className="flex items-center gap-1 px-2 py-1 text-xs bg-red-500/15 text-red-700 dark:text-red-300 rounded-md hover:bg-red-500/25 transition-colors"
                      >
                        <AlertTriangle className="w-3 h-3" />
                        <span>Problems: {problemFilters.join(', ')}</span>
                        <X className="w-3 h-3" />
                      </button>
                    )}
                    {labelSelector && (
                      <button
                        onClick={() => setLabelSelector('')}
                        className="flex items-center gap-1 px-2 py-1 text-xs bg-green-500/15 text-green-700 dark:text-green-300 rounded-md hover:bg-green-500/25 transition-colors"
                      >
                        <Tag className="w-3 h-3" />
                        <span>{labelSelector}</span>
                        <X className="w-3 h-3" />
                      </button>
                    )}
                  </div>
                )
              })()}
            </div>
          ) : (
            <MetricsContext.Provider value={metricsLookup}>
            <TableVirtuoso
              ref={virtuosoRef}
              data={filteredResources}
              increaseViewportBy={400}
              overscan={200}
              computeItemKey={(index, resource) => resource.metadata?.uid || `${resource.metadata?.namespace}-${resource.metadata?.name}-${index}`}
              components={virtuosoComponents}
              fixedHeaderContent={() => (
                <tr>
                  {columns.map((col, colIdx) => {
                    const isSortable = ['name', 'namespace', 'age', 'status', 'ready', 'restarts', 'type', 'version', 'desired', 'available', 'upToDate', 'lastSeen', 'count', 'reason', 'object', 'cpu', 'memory'].includes(col.key)
                    const isSorted = sortColumn === col.key
                    const isLastCol = colIdx === columns.length - 1
                    const filterCol = filterableColumnMap.get(col.key)
                    const activeFilterValues = columnFilters[col.key] || []
                    const hasActiveFilter = activeFilterValues.length > 0
                    const isFilterOpen = openColumnFilter === col.key
                    return (
                      <th
                        key={col.key}
                        className={clsx(
                          'text-left px-4 py-3 text-xs font-medium uppercase tracking-wide relative group/th',
                          'bg-theme-surface border-b border-theme-border',
                          !isLastCol && 'border-r-subtle',
                          isSortable ? 'text-theme-text-secondary hover:text-theme-text-primary cursor-pointer select-none' : 'text-theme-text-secondary'
                        )}
                        onClick={isSortable ? () => handleSort(col.key) : undefined}
                      >
                        <div className="flex items-center gap-1 overflow-hidden">
                          {col.tooltip ? (
                            <Tooltip content={col.tooltip}>
                              <span className="border-b border-dotted border-theme-text-tertiary truncate">{col.label}</span>
                            </Tooltip>
                          ) : (
                            <span className="truncate">{col.label}</span>
                          )}
                          {isSortable && (
                            <span className="text-theme-text-tertiary shrink-0">
                              {isSorted ? (
                                sortDirection === 'asc' ? (
                                  <ChevronUp className="w-3.5 h-3.5" />
                                ) : (
                                  <ChevronDown className="w-3.5 h-3.5" />
                                )
                              ) : (
                                <ArrowUpDown className="w-3 h-3 opacity-50" />
                              )}
                            </span>
                          )}
                          {filterCol && (
                            <span className="shrink-0 flex items-center gap-0">
                              <button
                                data-column-filter-trigger
                                onClick={(e) => {
                                  e.stopPropagation()
                                  if (isFilterOpen) {
                                    setOpenColumnFilter(null)
                                  } else {
                                    setOpenColumnFilter(col.key)
                                    setColumnFilterSearch('')
                                  }
                                }}
                                className={clsx(
                                  'rounded-l transition-colors flex items-center gap-0.5',
                                  hasActiveFilter
                                    ? 'px-1.5 py-0.5 -my-0.5 bg-blue-500/20 text-blue-700 dark:text-blue-300 hover:bg-blue-500/30'
                                    : isFilterOpen
                                      ? 'p-0.5 text-theme-text-primary'
                                      : 'p-0.5 text-theme-text-disabled opacity-0 group-hover/th:opacity-100 hover:text-theme-text-primary'
                                )}
                              >
                                <ListFilter className="w-3 h-3" />
                                {hasActiveFilter && <span className="text-[10px] leading-none font-semibold">{activeFilterValues.length}</span>}
                              </button>
                              {hasActiveFilter && (
                                <button
                                  data-column-filter-trigger
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    clearColumnFilter(col.key)
                                    setOpenColumnFilter(null)
                                  }}
                                  className="rounded-r px-0.5 py-0.5 -my-0.5 bg-blue-500/20 text-blue-700 dark:text-blue-300 hover:bg-blue-500/30 transition-colors"
                                  title="Clear filter"
                                >
                                  <X className="w-3 h-3" />
                                </button>
                              )}
                            </span>
                          )}
                        </div>
                        {/* Column filter dropdown */}
                        {filterCol && isFilterOpen && (() => {
                          const values = filterCol.values
                          const filtered = columnFilterSearch
                            ? values.filter(v => v.value.toLowerCase().includes(columnFilterSearch.toLowerCase()))
                            : values
                          return (
                            <div
                              ref={columnFilterDropdownRef}
                              className={clsx(
                                'absolute top-full mt-1 min-w-48 max-w-64 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50',
                                isLastCol ? 'right-0' : 'left-0'
                              )}
                              onClick={(e) => e.stopPropagation()}
                            >
                              {values.length > 5 ? (
                                <div className="flex items-center gap-2 p-2 border-b border-theme-border">
                                  <div className="relative flex-1">
                                    <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                                    <input
                                      type="text"
                                      placeholder="Search..."
                                      value={columnFilterSearch}
                                      onChange={(e) => setColumnFilterSearch(e.target.value)}
                                      autoFocus
                                      className="w-full pl-7 pr-2 py-1.5 text-xs bg-theme-elevated border border-theme-border-light rounded text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-1 focus:ring-blue-500"
                                    />
                                  </div>
                                  {activeFilterValues.length > 0 && (
                                    <button onClick={() => { clearColumnFilter(col.key); setOpenColumnFilter(null) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 rounded transition-colors shrink-0">Clear</button>
                                  )}
                                </div>
                              ) : activeFilterValues.length > 0 ? (
                                <div className="flex items-center justify-between px-3 py-1.5 border-b border-theme-border">
                                  <span className="text-xs font-medium text-theme-text-secondary">{col.label}</span>
                                  <button onClick={() => { clearColumnFilter(col.key); setOpenColumnFilter(null) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 -mr-1 rounded transition-colors">Clear</button>
                                </div>
                              ) : null}
                              <div className="py-1 max-h-64 overflow-y-auto">
                                {filtered.map(({ value, count }) => {
                                  const isSelected = activeFilterValues.includes(value)
                                  return (
                                    <button
                                      key={value}
                                      onClick={() => toggleColumnFilterValue(col.key, value)}
                                      className={clsx(
                                        'w-full text-left px-3 py-1.5 text-xs flex items-center gap-2 transition-colors',
                                        isSelected
                                          ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300'
                                          : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                                      )}
                                    >
                                      <span className={clsx('w-3 h-3 shrink-0 rounded-sm border flex items-center justify-center', isSelected ? 'bg-blue-500 border-blue-500' : 'border-theme-border')}>
                                        {isSelected && <Check className="w-2 h-2 text-white" />}
                                      </span>
                                      <span className="truncate" title={value}>{value}</span>
                                      <span className="text-theme-text-disabled shrink-0 ml-auto">({count})</span>
                                    </button>
                                  )
                                })}
                                {filtered.length === 0 && (
                                  <div className="px-3 py-2 text-xs text-theme-text-disabled">No matches</div>
                                )}
                              </div>
                            </div>
                          )
                        })()}
                        {/* Resize handle with visible divider */}
                        <div
                          className="absolute right-0 top-0 bottom-0 w-3 cursor-col-resize flex items-center justify-center"
                          style={{ transform: 'translateX(50%)' , zIndex: 10 }}
                          onMouseDown={(e) => {
                            const th = e.currentTarget.parentElement!
                            handleResizeStart(e, col.key, th.getBoundingClientRect().width)
                          }}
                        >
                          <div className="w-px h-4 bg-theme-border group-hover/th:bg-theme-text-disabled transition-colors" />
                        </div>
                      </th>
                    )
                  })}
                  {hasResizedColumns && <th className="bg-theme-surface border-b border-theme-border p-0" />}
                </tr>
              )}
              itemContent={(index, resource) => {
                const isSelected = selectedResource?.kind === selectedKind.name &&
                  selectedResource?.namespace === resource.metadata?.namespace &&
                  selectedResource?.name === resource.metadata?.name
                const isHighlighted = index === highlightedIndex
                return (
                  <ResourceRowCells
                    resource={resource}
                    kind={selectedKind.name}
                    group={selectedKind.group}
                    columns={columns}
                    hasSpacerColumn={hasResizedColumns}
                    isSelected={isSelected}
                    isHighlighted={isHighlighted}
                    majorityNodeMinorVersion={majorityNodeMinorVersion}
                    onClick={() => {
                      const res = { kind: selectedKind.name, namespace: resource.metadata?.namespace || '', name: resource.metadata?.name, group: selectedKind.group }
                      onResourceClick?.(isSelected ? null : res)
                    }}
                    onMouseEnter={() => setHighlightedIndex(-1)}
                  />
                )
              }}
              className="h-full"
            />
            {/* Resize indicator line — full table height, shown only while dragging */}
            {resizeLineX !== null && (
              <div
                className="absolute top-0 bottom-0 w-0.5 bg-blue-500 pointer-events-none"
                style={{ left: resizeLineX, zIndex: 20 }}
              />
            )}
            </MetricsContext.Provider>
          )}
        </div>
      </div>
    </div>
    </ResourcesViewDataContext.Provider>
  )
}

interface ResourceRowCellsProps {
  resource: any
  kind: string
  group?: string
  columns: Column[]
  hasSpacerColumn: boolean
  isSelected?: boolean
  isHighlighted?: boolean
  majorityNodeMinorVersion?: string
  onClick?: () => void
  onMouseEnter?: () => void
}

function ResourceRowCells({ resource, kind, group, columns, hasSpacerColumn, isSelected, isHighlighted, majorityNodeMinorVersion, onClick, onMouseEnter }: ResourceRowCellsProps) {
  return (
    <>
      {columns.map((col) => (
        <td
          key={col.key}
          onClick={onClick}
          onMouseEnter={onMouseEnter}
          className={clsx(
            'px-4 py-3 border-b-subtle cursor-pointer transition-colors',
            col.key !== 'status' && 'overflow-hidden truncate',
            isSelected
              ? 'bg-blue-500/20 group-hover/row:bg-blue-500/30'
              : isHighlighted
                ? 'bg-blue-500/10 ring-1 ring-inset ring-blue-400/30'
                : 'group-hover/row:bg-theme-surface/50'
          )}
        >
          <CellContent resource={resource} kind={kind} group={group} column={col.key} majorityNodeMinorVersion={majorityNodeMinorVersion} />
        </td>
      ))}
      {hasSpacerColumn && <td className="border-b-subtle p-0" />}
    </>
  )
}

const VirtuosoTableRow = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(
  function VirtuosoTableRow(props, ref) {
    return <tr {...props} ref={ref} className="group/row" />
  }
)

function CopyNameButton({ name }: { name: string }) {
  const [copied, setCopied] = useState(false)
  if (!name) return null
  return (
    <button
      onClick={(e) => {
        e.stopPropagation()
        navigator.clipboard.writeText(name).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        }).catch(() => {})
      }}
      className="shrink-0 p-0.5 text-theme-text-tertiary hover:text-theme-text-primary opacity-0 group-hover/row:opacity-100 transition-opacity"
      title="Copy name"
    >
      {copied ? <Check className="w-3 h-3 text-green-400" /> : <Copy className="w-3 h-3" />}
    </button>
  )
}

interface CellContentProps {
  resource: any
  kind: string
  column: string
  group?: string
  majorityNodeMinorVersion?: string
}

function CellContent({ resource, kind, column, group, majorityNodeMinorVersion }: CellContentProps) {
  const meta = resource.metadata || {}

  // Common columns
  if (column === 'name') {
    const isTerminating = !!meta.deletionTimestamp
    return (
      <div className="flex items-center gap-1.5 min-w-0">
        <Tooltip content={meta.name}>
          <span className={clsx('text-sm font-medium truncate block', isTerminating ? 'text-theme-text-tertiary line-through' : 'text-theme-text-primary')}>
            {meta.name}
          </span>
        </Tooltip>
        <CopyNameButton name={meta.name} />
        {isTerminating && (
          <Tooltip content="Resource is being deleted (has deletionTimestamp set). May be stuck due to finalizers.">
            <span className="shrink-0 flex items-center gap-1 px-1.5 py-0.5 text-[10px] font-medium bg-red-500/15 text-red-600 dark:text-red-400 rounded">
              <Trash2 className="w-3 h-3" />
              Terminating
            </span>
          </Tooltip>
        )}
      </div>
    )
  }
  if (column === 'namespace') {
    return (
      <Tooltip content={meta.namespace}>
        <span className="text-sm text-theme-text-secondary truncate block">{meta.namespace || '-'}</span>
      </Tooltip>
    )
  }
  if (column === 'age') {
    return <span className="text-sm text-theme-text-secondary">{formatAge(meta.creationTimestamp)}</span>
  }

  // Kind-specific columns (normalize CRD singular names like 'ScaledObject' → 'scaledobjects')
  const kindLower = normalizeKindToPlural(kind, group)
  switch (kindLower) {
    case 'pods':
      return <PodCell resource={resource} column={column} />
    case 'deployments':
    case 'statefulsets':
      return <WorkloadCell resource={resource} kind={kind} column={column} />
    case 'daemonsets':
      return <DaemonSetCell resource={resource} column={column} />
    case 'replicasets':
      return <ReplicaSetCell resource={resource} column={column} />
    case 'services':
      return <ServiceCell resource={resource} column={column} />
    case 'ingresses':
      return <IngressCell resource={resource} column={column} />
    case 'configmaps':
      return <ConfigMapCell resource={resource} column={column} />
    case 'secrets':
      return <SecretCell resource={resource} column={column} />
    case 'jobs':
      return <JobCell resource={resource} column={column} />
    case 'cronjobs':
      return <CronJobCell resource={resource} column={column} />
    case 'hpas':
    case 'horizontalpodautoscalers':
      return <HPACell resource={resource} column={column} />
    case 'nodes':
      return <NodeCell resource={resource} column={column} majorityNodeMinorVersion={majorityNodeMinorVersion} />
    case 'persistentvolumeclaims':
      return <PVCCell resource={resource} column={column} />
    case 'rollouts':
      return <RolloutCell resource={resource} column={column} />
    case 'workflows':
      return <WorkflowCell resource={resource} column={column} />
    case 'certificates':
      return <CertificateCell resource={resource} column={column} />
    case 'persistentvolumes':
      return <PersistentVolumeCell resource={resource} column={column} />
    case 'storageclasses':
      return <StorageClassCell resource={resource} column={column} />
    case 'certificaterequests':
      return <CertificateRequestCell resource={resource} column={column} />
    case 'clusterissuers':
      return <ClusterIssuerCell resource={resource} column={column} />
    case 'issuers':
      return <IssuerCell resource={resource} column={column} />
    case 'orders':
      return <OrderCell resource={resource} column={column} />
    case 'challenges':
      return <ChallengeCell resource={resource} column={column} />
    case 'gateways':
      // Disambiguate Gateway API vs Istio Gateway by apiVersion
      if (resource.apiVersion?.includes('networking.istio.io')) {
        return <IstioGatewayCell resource={resource} column={column} />
      }
      return <GatewayCell resource={resource} column={column} />
    case 'httproutes':
    case 'grpcroutes':
    case 'tcproutes':
    case 'tlsroutes':
      return <RouteCell resource={resource} column={column} />
    case 'gatewayclasses':
      return <GatewayClassCell resource={resource} column={column} />
    case 'sealedsecrets':
      return <SealedSecretCell resource={resource} column={column} />
    case 'workflowtemplates':
      return <WorkflowTemplateCell resource={resource} column={column} />
    case 'networkpolicies':
      return <NetworkPolicyCell resource={resource} column={column} />
    case 'poddisruptionbudgets':
      return <PDBCell resource={resource} column={column} />
    case 'serviceaccounts':
      return <ServiceAccountCell resource={resource} column={column} />
    case 'roles':
    case 'clusterroles':
      return <RoleCell resource={resource} column={column} />
    case 'rolebindings':
    case 'clusterrolebindings':
      return <RoleBindingCell resource={resource} column={column} />
    case 'ingressclasses':
      return <IngressClassCell resource={resource} column={column} />
    case 'leases':
      return <LeaseCell resource={resource} column={column} />
    case 'priorityclasses':
      return <PriorityClassCell resource={resource} column={column} />
    case 'runtimeclasses':
      return <RuntimeClassCell resource={resource} column={column} />
    case 'mutatingwebhookconfigurations':
    case 'validatingwebhookconfigurations':
      return <WebhookConfigCell resource={resource} column={column} />
    case 'events':
      return <EventCell resource={resource} column={column} />
    // FluxCD GitOps resources
    case 'gitrepositories':
      return <GitRepositoryCell resource={resource} column={column} />
    case 'ocirepositories':
      return <OCIRepositoryCell resource={resource} column={column} />
    case 'helmrepositories':
      return <HelmRepositoryCell resource={resource} column={column} />
    case 'kustomizations':
      return <KustomizationCell resource={resource} column={column} />
    case 'helmreleases':
      return <FluxHelmReleaseCell resource={resource} column={column} />
    case 'alerts':
      return <FluxAlertCell resource={resource} column={column} />
    // Karpenter
    case 'nodepools':
      return <NodePoolCell resource={resource} column={column} />
    case 'nodeclaims':
      return <NodeClaimCell resource={resource} column={column} />
    case 'ec2nodeclasses':
      return <EC2NodeClassCell resource={resource} column={column} />
    // Prometheus Operator
    case 'servicemonitors':
      return <ServiceMonitorCell resource={resource} column={column} />
    case 'prometheusrules':
      return <PrometheusRuleCell resource={resource} column={column} />
    case 'podmonitors':
      return <PodMonitorCell resource={resource} column={column} />
    // KEDA
    case 'scaledobjects':
      return <ScaledObjectCell resource={resource} column={column} />
    case 'scaledjobs':
      return <ScaledJobCell resource={resource} column={column} />
    case 'triggerauthentications':
      return <TriggerAuthenticationCell resource={resource} column={column} />
    case 'clustertriggerauthentications':
      return <ClusterTriggerAuthenticationCell resource={resource} column={column} />
    // ArgoCD GitOps resources
    case 'applications':
      return <ArgoApplicationCell resource={resource} column={column} />
    case 'applicationsets':
      return <ArgoApplicationSetCell resource={resource} column={column} />
    case 'appprojects':
      return <ArgoAppProjectCell resource={resource} column={column} />
    // Kyverno / Policy Reports
    case 'policyreports':
      return <PolicyReportCell resource={resource} column={column} />
    case 'clusterpolicyreports':
      return <ClusterPolicyReportCell resource={resource} column={column} />
    case 'kyvernopolicies':
      return <KyvernoPolicyCell resource={resource} column={column} />
    case 'clusterpolicies':
      return <ClusterPolicyCell resource={resource} column={column} />
    // Trivy Operator
    case 'vulnerabilityreports':
      return <VulnerabilityReportCell resource={resource} column={column} />
    case 'configauditreports':
      return <ConfigAuditReportCell resource={resource} column={column} />
    case 'exposedsecretreports':
      return <ExposedSecretReportCell resource={resource} column={column} />
    case 'rbacassessmentreports':
    case 'clusterrbacassessmentreports':
    case 'infraassessmentreports':
    case 'clusterinfraassessmentreports':
      return <RbacAssessmentReportCell resource={resource} column={column} />
    case 'clustercompliancereports':
      return <ClusterComplianceReportCell resource={resource} column={column} />
    case 'sbomreports':
    case 'clustersbomreports':
      return <SbomReportCell resource={resource} column={column} />
    // External Secrets Operator
    case 'externalsecrets':
      return <ExternalSecretCell resource={resource} column={column} />
    case 'clusterexternalsecrets':
      return <ClusterExternalSecretCell resource={resource} column={column} />
    case 'secretstores':
      return <SecretStoreCell resource={resource} column={column} />
    case 'clustersecretstores':
      return <ClusterSecretStoreCell resource={resource} column={column} />
    // Velero
    case 'backups':
      // Disambiguate CNPG vs Velero backups by apiVersion
      if (resource.apiVersion?.includes('cnpg.io')) {
        return <CNPGBackupCell resource={resource} column={column} />
      }
      return <BackupCell resource={resource} column={column} />
    case 'restores':
      return <RestoreCell resource={resource} column={column} />
    case 'schedules':
      return <ScheduleCell resource={resource} column={column} />
    case 'backupstoragelocations':
      return <BackupStorageLocationCell resource={resource} column={column} />
    // CloudNativePG
    case 'clusters':
      return <CNPGClusterCell resource={resource} column={column} />
    case 'scheduledbackups':
      return <CNPGScheduledBackupCell resource={resource} column={column} />
    case 'poolers':
      return <CNPGPoolerCell resource={resource} column={column} />
    // Istio Service Mesh
    case 'virtualservices':
      return <VirtualServiceCell resource={resource} column={column} />
    case 'destinationrules':
      return <DestinationRuleCell resource={resource} column={column} />
    case 'serviceentries':
      return <ServiceEntryCell resource={resource} column={column} />
    case 'peerauthentications':
      return <PeerAuthenticationCell resource={resource} column={column} />
    case 'authorizationpolicies':
      return <AuthorizationPolicyCell resource={resource} column={column} />
    // Knative Serving
    case 'knativeservices':
      return <KnativeServiceCell resource={resource} column={column} />
    case 'knativeconfigurations':
      return <KnativeConfigurationCell resource={resource} column={column} />
    case 'knativerevisions':
      return <KnativeRevisionCell resource={resource} column={column} />
    case 'knativeroutes':
      return <KnativeRouteCell resource={resource} column={column} />
    // Knative Eventing
    case 'brokers':
      return <BrokerCell resource={resource} column={column} />
    case 'triggers':
      return <TriggerCell resource={resource} column={column} />
    case 'eventtypes':
      return <EventTypeCell resource={resource} column={column} />
    // Knative Sources
    case 'pingsources':
      return <PingSourceCell resource={resource} column={column} />
    case 'apiserversources':
      return <ApiServerSourceCell resource={resource} column={column} />
    case 'containersources':
      return <ContainerSourceCell resource={resource} column={column} />
    case 'sinkbindings':
      return <SinkBindingCell resource={resource} column={column} />
    // Knative Messaging
    case 'channels':
      return <ChannelCell resource={resource} column={column} />
    case 'inmemorychannels':
      return <InMemoryChannelCell resource={resource} column={column} />
    case 'subscriptions':
      return <SubscriptionCell resource={resource} column={column} />
    // Knative Flows
    case 'sequences':
      return <SequenceCell resource={resource} column={column} />
    case 'parallels':
      return <ParallelCell resource={resource} column={column} />
    // Knative Networking & Serving
    case 'serverlessservices':
      return <ServerlessServiceCell resource={resource} column={column} />
    case 'domainmappings':
      return <DomainMappingCell resource={resource} column={column} />
    case 'knativeingresses':
      return <KnativeIngressCell resource={resource} column={column} />
    case 'knativecertificates':
      return <KnativeCertificateCell resource={resource} column={column} />
    // Traefik
    case 'ingressroutes':
    case 'ingressroutetcps':
    case 'ingressrouteudps':
      return <IngressRouteCell resource={resource} column={column} />
    case 'middlewares':
    case 'middlewaretcps':
      return <MiddlewareCell resource={resource} column={column} />
    case 'traefikservices':
      return <TraefikServiceCell resource={resource} column={column} />
    case 'serverstransports':
    case 'serverstransporttcps':
      return <ServersTransportCell resource={resource} column={column} />
    case 'tlsoptions':
      return <TLSOptionCell resource={resource} column={column} />
    // Contour
    case 'httpproxies':
      return <HTTPProxyCell resource={resource} column={column} />
    default:
      // Generic cell for CRDs and unknown resources
      return <GenericCell resource={resource} column={column} />
  }
}

// Generic cell renderer for CRDs and unknown resources
function GenericCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      // Try to extract status from common patterns
      const status = resource.status
      if (!status) return <span className="text-sm text-theme-text-tertiary">-</span>

      // Check for phase (common in many CRDs)
      if (status.phase) {
        const phase = status.phase as string
        const isHealthy = ['Running', 'Active', 'Succeeded', 'Ready', 'Healthy', 'Available'].includes(phase)
        const isWarning = ['Pending', 'Progressing', 'Unknown'].includes(phase)
        return (
          <span className={clsx(
            'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
            isHealthy ? 'status-healthy' :
            isWarning ? 'status-degraded' :
            'status-unhealthy'
          )}>
            {phase}
          </span>
        )
      }

      // Check for conditions (common pattern)
      if (status.conditions && Array.isArray(status.conditions)) {
        const readyCondition = status.conditions.find((c: any) => c.type === 'Ready' || c.type === 'Available')
        if (readyCondition) {
          const isReady = readyCondition.status === 'True'
          return (
            <span className={clsx(
              'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
              isReady ? 'status-healthy' : 'status-degraded'
            )}>
              {isReady ? 'Ready' : 'Not Ready'}
            </span>
          )
        }
      }

      // Check for state field
      if (status.state) {
        return (
          <span className="text-sm text-theme-text-secondary truncate">
            {String(status.state)}
          </span>
        )
      }

      return <span className="text-sm text-theme-text-tertiary">-</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// ============================================================================
// KIND-SPECIFIC CELL RENDERERS
// ============================================================================

function PodCell({ resource, column }: { resource: any; column: string }) {
  const metrics = useContext(MetricsContext)
  const { onNavigate: navigate } = useContext(ResourcesViewDataContext)

  switch (column) {
    case 'containers': {
      const squares = getContainerSquareStates(resource)
      const hasInit = squares.some(s => s.isInit)
      return (
        <div className="flex items-center gap-1">
          {squares.map((sq, i) => {
            const showSeparator = hasInit && i > 0 && sq.isInit !== squares[i - 1].isInit
            const bgClass =
              sq.status === 'ready' ? 'bg-green-500' :
              sq.status === 'completed' ? 'bg-theme-text-tertiary/30 border border-theme-text-tertiary' :
              sq.status === 'running' ? 'bg-yellow-500' :
              sq.status === 'waiting' ? 'bg-red-500' :
              sq.status === 'terminated' ? 'bg-red-500' :
              'bg-theme-text-tertiary/30 border border-dashed border-theme-text-tertiary'
            const ringClass = sq.restarts > 0 ? 'ring-2 ring-orange-400' : ''
            const dotColor =
              sq.status === 'ready' ? 'bg-green-500' :
              sq.status === 'completed' ? 'bg-theme-text-tertiary' :
              sq.status === 'running' ? 'bg-yellow-500' :
              sq.status === 'waiting' || sq.status === 'terminated' ? 'bg-red-500' :
              'bg-theme-text-tertiary'
            // Relative time helper
            const timeAgo = (dateStr?: string) => {
              if (!dateStr) return null
              const ms = Date.now() - new Date(dateStr).getTime()
              if (isNaN(ms) || ms < 0) return null
              if (ms < 60000) return 'just now'
              return formatDuration(ms) + ' ago'
            }
            const runDuration = (start?: string, end?: string) => {
              if (!start || !end) return null
              const ms = new Date(end).getTime() - new Date(start).getTime()
              return ms > 0 ? formatDuration(ms, true) : null
            }
            const stateLabel =
              sq.status === 'ready' ? 'Running' :
              sq.status === 'completed' ? 'Completed' :
              sq.status === 'running' ? 'Running (not ready)' :
              sq.status === 'waiting' ? 'Waiting' :
              sq.status === 'terminated' ? 'Terminated' : 'Unknown'
            const uptime = sq.status === 'ready' || sq.status === 'running' ? timeAgo(sq.startedAt) : null
            const duration = (sq.status === 'completed' || sq.status === 'terminated') ? runDuration(sq.startedAt, sq.finishedAt) : null
            const lt = sq.lastTermination
            const restartRecencyMs = lt?.finishedAt ? Date.now() - new Date(lt.finishedAt).getTime() : null
            const restartColor = restartRecencyMs !== null && restartRecencyMs < 600000 ? 'text-red-400' : restartRecencyMs !== null && restartRecencyMs < 3600000 ? 'text-yellow-400' : 'text-orange-400'
            const tooltipContent = (
              <div className="whitespace-normal space-y-1">
                {/* Header: dot + name + state */}
                <div className="flex items-center gap-1.5">
                  <div className={clsx('w-2 h-2 rounded-full shrink-0', dotColor)} />
                  <span className="font-medium">{sq.isInit ? <span className="text-theme-text-tertiary font-normal">init · </span> : ''}{sq.name}</span>
                  <span className="text-theme-text-tertiary">·</span>
                  <span className="text-theme-text-secondary">{stateLabel}</span>
                </div>
                {/* Reason (when different from state label) */}
                {sq.reason && sq.reason !== stateLabel && (
                  <div className={clsx(
                    'font-medium',
                    (sq.status === 'waiting' || sq.status === 'terminated') ? 'text-red-400' : 'text-theme-text-secondary'
                  )}>{sq.reason}</div>
                )}
                {/* Message — truncated for tooltip */}
                {sq.message && (
                  <div className="text-theme-text-secondary text-[11px] leading-tight">
                    {sq.message.length > 120 ? sq.message.slice(0, 120) + '...' : sq.message}
                  </div>
                )}
                {/* Exit code + uptime/duration on same line */}
                {(sq.exitCode !== undefined && sq.status !== 'ready' && sq.status !== 'running') || uptime || duration ? (
                  <div className="text-theme-text-tertiary flex items-center gap-1.5">
                    {sq.exitCode !== undefined && sq.status !== 'ready' && sq.status !== 'running' && (
                      <span className={sq.exitCode !== 0 ? 'text-red-400' : ''}>exit {sq.exitCode}</span>
                    )}
                    {uptime && <span>up {uptime.replace(' ago', '')}</span>}
                    {duration && <span>ran {duration}</span>}
                  </div>
                ) : null}
                {/* Restarts + last crash info */}
                {sq.restarts > 0 && (
                  <div className={clsx('border-t border-theme-border/50 pt-1 space-y-0.5', restartColor)}>
                    <div className="flex items-center gap-1.5">
                      <span>{sq.restarts} restart{sq.restarts !== 1 ? 's' : ''}</span>
                      {lt?.finishedAt && <span className="text-theme-text-tertiary">· last {timeAgo(lt.finishedAt)}</span>}
                    </div>
                    {lt?.reason && (
                      <div className="text-theme-text-tertiary">
                        {lt.reason}{lt.exitCode !== undefined && lt.exitCode !== 0 ? ` (exit ${lt.exitCode})` : ''}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
            return (
              <React.Fragment key={i}>
                {showSeparator && <div className="w-px h-3 bg-theme-text-tertiary/40 mx-0.5" />}
                <Tooltip content={tooltipContent} className="whitespace-normal max-w-xs">
                  <div className={clsx('w-2.5 h-2.5 rounded-sm', bgClass, ringClass)} />
                </Tooltip>
              </React.Fragment>
            )
          })}
        </div>
      )
    }
    case 'status': {
      const status = getPodStatus(resource)
      const problems = getPodProblems(resource)
      return (
        <div className="flex items-center gap-2">
          <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
            {status.text}
          </span>
          {problems.length > 0 && (
            <Tooltip content={problems.map(p => p.message).join(', ')}>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'restarts': {
      const restarts = getPodRestarts(resource)
      return (
        <span className={clsx(
          'text-sm',
          restarts > 5 ? 'text-red-400 font-medium' : restarts > 0 ? 'text-yellow-400' : 'text-theme-text-secondary'
        )}>
          {restarts}
        </span>
      )
    }
    case 'node': {
      const nodeVal = resource.spec?.nodeName || '-'
      if (nodeVal === '-') {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      return (
        <Tooltip content={`Filter pods on ${nodeVal}`}>
          <button
            onClick={(e) => {
              e.stopPropagation()
              // Merge node filter into existing column filters via URL
              const params = new URLSearchParams(window.location.search)
              const existing = parseColumnFilters(params.get('filters'))
              existing['node'] = [nodeVal]
              params.set('filters', serializeColumnFilters(existing))
              navigate?.(`/resources/pods?${params.toString()}`)
            }}
            className="text-sm text-blue-400 hover:text-blue-300 hover:underline truncate block text-left"
          >
            {nodeVal}
          </button>
        </Tooltip>
      )
    }
    case 'podIP': {
      const ip = resource.status?.podIP || '-'
      return <span className="text-sm text-theme-text-secondary font-mono">{ip}</span>
    }
    case 'cpu': {
      const key = `${resource.metadata?.namespace}/${resource.metadata?.name}`
      const m = metrics.pods.get(key)
      if (!m || m.cpu === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const denom = m.cpuLimit || m.cpuRequest
      if (!denom) return <span className="text-sm text-theme-text-secondary font-mono">{formatCPU(m.cpu)}</span>
      const pct = (m.cpu / denom) * 100
      const marker = m.cpuLimit > 0 && m.cpuRequest > 0 ? (m.cpuRequest / m.cpuLimit) * 100 : undefined
      const tip = buildResourceTooltip('CPU', m.cpu, m.cpuRequest, m.cpuLimit, formatCPU)
      return <ResourceBar used={formatCPU(m.cpu)} total={formatCPU(denom)} percent={pct} colorScheme={getBulletBarScheme(pct, marker)} markerPercent={marker} tooltip={tip} />
    }
    case 'memory': {
      const key = `${resource.metadata?.namespace}/${resource.metadata?.name}`
      const m = metrics.pods.get(key)
      if (!m || m.memory === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const denom = m.memoryLimit || m.memoryRequest
      if (!denom) return <span className="text-sm text-theme-text-secondary font-mono">{formatMemoryShort(m.memory)}</span>
      const pct = (m.memory / denom) * 100
      const marker = m.memoryLimit > 0 && m.memoryRequest > 0 ? (m.memoryRequest / m.memoryLimit) * 100 : undefined
      const tip = buildResourceTooltip('Memory', m.memory, m.memoryRequest, m.memoryLimit, formatMemoryShort)
      return <ResourceBar used={formatMemoryShort(m.memory)} total={formatMemoryShort(denom)} percent={pct} colorScheme={getBulletBarScheme(pct, marker)} markerPercent={marker} tooltip={tip} />
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkloadCell({ resource, column }: { resource: any; kind: string; column: string }) {
  const status = resource.status || {}
  const spec = resource.spec || {}

  switch (column) {
    case 'ready': {
      const desired = spec.replicas ?? 0
      const ready = status.readyReplicas || 0
      const allReady = ready === desired && desired > 0
      return (
        <span className={clsx(
          'text-sm font-medium',
          desired === 0 ? 'text-theme-text-secondary' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}/{desired}
        </span>
      )
    }
    case 'upToDate':
      return <span className="text-sm text-theme-text-secondary">{status.updatedReplicas || 0}</span>
    case 'available':
      return <span className="text-sm text-theme-text-secondary">{status.availableReplicas || 0}</span>
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">
            {display}
          </span>
        </Tooltip>
      )
    }
    case 'conditions': {
      const { conditions, hasIssues } = getWorkloadConditions(resource)
      if (conditions.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = conditions.join(', ')
      return (
        <Tooltip content={display}>
          <span
            className={clsx(
              'text-sm truncate block',
              hasIssues ? 'text-yellow-400' : 'text-green-400'
            )}
          >
            {display}
          </span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function DaemonSetCell({ resource, column }: { resource: any; column: string }) {
  const status = resource.status || {}

  switch (column) {
    case 'desired':
      return <span className="text-sm text-theme-text-secondary">{status.desiredNumberScheduled || 0}</span>
    case 'ready': {
      const desired = status.desiredNumberScheduled || 0
      const ready = status.numberReady || 0
      const allReady = ready === desired && desired > 0
      return (
        <span className={clsx(
          'text-sm font-medium',
          allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}
        </span>
      )
    }
    case 'upToDate':
      return <span className="text-sm text-theme-text-secondary">{status.updatedNumberScheduled || 0}</span>
    case 'available':
      return <span className="text-sm text-theme-text-secondary">{status.numberAvailable || 0}</span>
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">
            {display}
          </span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ReplicaSetCell({ resource, column }: { resource: any; column: string }) {
  const status = resource.status || {}
  const spec = resource.spec || {}

  switch (column) {
    case 'ready': {
      const desired = spec.replicas ?? 0
      const ready = status.readyReplicas || 0
      const allReady = ready === desired && desired > 0
      return (
        <span className={clsx(
          'text-sm font-medium',
          desired === 0 ? 'text-theme-text-secondary' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}/{desired}
        </span>
      )
    }
    case 'owner': {
      const owner = getReplicaSetOwner(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{owner || '-'}</span>
    }
    case 'status': {
      const isActive = isReplicaSetActive(resource)
      return (
        <span className={clsx(
          'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
          isActive ? 'status-neutral' : 'status-unknown'
        )}>
          {isActive ? 'Active' : 'Old'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'type': {
      const status = getServiceStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'selector': {
      const selector = getServiceSelector(resource)
      return (
        <Tooltip content={selector}>
          <span className="text-sm text-theme-text-secondary truncate">
            {selector}
          </span>
        </Tooltip>
      )
    }
    case 'endpoints': {
      const { status, color } = getServiceEndpointsStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', color)}>
          {status}
        </span>
      )
    }
    case 'clusterIP':
      return <span className="text-sm text-theme-text-secondary font-mono">{resource.spec?.clusterIP || '-'}</span>
    case 'externalIP': {
      const external = getServiceExternalIP(resource)
      if (!external) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={external}>
          <div className="flex items-center gap-1">
            <Globe className="w-3.5 h-3.5 text-violet-400" />
            <span className="text-sm text-violet-400 truncate">{external}</span>
          </div>
        </Tooltip>
      )
    }
    case 'ports': {
      const ports = getServicePorts(resource)
      return <span className="text-sm text-theme-text-secondary">{ports}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function IngressCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'class': {
      const ingressClass = getIngressClass(resource)
      return <span className="text-sm text-theme-text-secondary">{ingressClass || '-'}</span>
    }
    case 'hosts': {
      const hosts = getIngressHosts(resource)
      return (
        <Tooltip content={hosts}>
          <span className="text-sm text-theme-text-secondary truncate">{hosts}</span>
        </Tooltip>
      )
    }
    case 'rules': {
      const rules = getIngressRules(resource)
      return (
        <Tooltip content={rules}>
          <span className="text-sm text-theme-text-secondary truncate">{rules}</span>
        </Tooltip>
      )
    }
    case 'tls': {
      const hasTLS = hasIngressTLS(resource)
      return hasTLS ? (
        <Tooltip content="TLS Enabled">
          <span>
            <Shield className="w-4 h-4 text-green-400" />
          </span>
        </Tooltip>
      ) : (
        <span className="text-sm text-theme-text-tertiary">-</span>
      )
    }
    case 'address': {
      const address = getIngressAddress(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{address || 'Pending'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ConfigMapCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'keys': {
      const { count, preview } = getConfigMapKeys(resource)
      return (
        <div className="flex items-center gap-2">
          <span className="text-sm text-theme-text-secondary">{count}</span>
          {count > 0 && (
            <Tooltip content={preview}>
              <span className="text-xs text-theme-text-tertiary truncate">
                ({preview})
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'size': {
      const size = getConfigMapSize(resource)
      return <span className="text-sm text-theme-text-secondary">{size}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function SecretCell({ resource, column }: { resource: any; column: string }) {
  const { certExpiry, certExpiryError } = useContext(ResourcesViewDataContext)
  switch (column) {
    case 'type': {
      const { type, color } = getSecretType(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', color)}>
          {type}
        </span>
      )
    }
    case 'keys': {
      const count = getSecretKeyCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'expires': {
      if (certExpiryError) {
        return <span className="text-sm text-theme-text-tertiary" title="Failed to load certificate expiry">!</span>
      }
      const meta = resource.metadata || {}
      const key = `${meta.namespace}/${meta.name}`
      const expiry = certExpiry?.[key]
      if (!expiry) {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      const color = expiry.expired || expiry.daysLeft < 7
        ? 'text-red-400'
        : expiry.daysLeft < 30
          ? 'text-yellow-400'
          : 'text-green-400'
      const text = expiry.expired
        ? `Expired ${Math.abs(expiry.daysLeft)}d ago`
        : `${expiry.daysLeft}d`
      return <span className={clsx('text-sm font-medium', color)}>{text}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function JobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getJobStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'completions': {
      const { succeeded, total } = getJobCompletions(resource)
      const allDone = succeeded === total
      return (
        <span className={clsx(
          'text-sm font-medium',
          allDone ? 'text-green-400' : succeeded > 0 ? 'text-yellow-400' : 'text-theme-text-secondary'
        )}>
          {succeeded}/{total}
        </span>
      )
    }
    case 'duration': {
      const duration = getJobDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{duration || '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function CronJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'schedule': {
      const { cron, readable } = getCronJobSchedule(resource)
      return (
        <div className="flex flex-col">
          <span className="text-sm text-theme-text-secondary font-mono">{cron}</span>
          <span className="text-xs text-theme-text-tertiary">{readable}</span>
        </div>
      )
    }
    case 'status': {
      const status = getCronJobStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'lastRun': {
      const lastRun = getCronJobLastRun(resource)
      return <span className="text-sm text-theme-text-secondary">{lastRun || 'Never'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function HPACell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'target': {
      const target = getHPATarget(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{target}</span>
    }
    case 'replicas': {
      const { current, min, max } = getHPAReplicas(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          <span className="text-theme-text-primary font-medium">{current}</span>
          <span className="text-theme-text-tertiary"> ({min}-{max})</span>
        </span>
      )
    }
    case 'metrics': {
      const { cpu, memory, custom } = getHPAMetrics(resource)
      const parts: string[] = []
      if (cpu !== undefined) parts.push(`CPU: ${cpu}%`)
      if (memory !== undefined) parts.push(`Mem: ${memory}%`)
      if (custom > 0) parts.push(`+${custom} custom`)
      return <span className="text-sm text-theme-text-secondary">{parts.join(', ') || '-'}</span>
    }
    case 'status': {
      const status = getHPAStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// Format helpers for resource bars
function formatCPU(nanocores: number): string {
  const m = Math.round(nanocores / 1e6)
  return `${m}m`
}

function formatMemoryShort(bytes: number): string {
  const gib = bytes / (1024 * 1024 * 1024)
  if (gib >= 1) return `${gib.toFixed(1)}Gi`
  const mib = bytes / (1024 * 1024)
  return `${Math.round(mib)}Mi`
}

// Bullet graph color logic for pod resource bars:
// green when usage < request, yellow when above request, red when > 85% of limit
function getBulletBarScheme(_usagePct: number, _markerPct: number | undefined): 'utilization' {
  return 'utilization'
}

function buildResourceTooltip(
  type: 'CPU' | 'Memory',
  usage: number,
  request: number,
  limit: number,
  formatFn: (n: number) => string,
) {
  const isCPU = type === 'CPU'

  let guidance: string
  if (limit > 0 && request > 0) {
    const pctOfLimit = (usage / limit) * 100
    if (pctOfLimit > 90) {
      guidance = isCPU
        ? 'Near the limit — CPU may be throttled'
        : 'Near the limit — at risk of OOM kill'
    } else if (usage > request) {
      guidance = 'Exceeds request — consider raising it if sustained'
    } else {
      guidance = 'Below request — healthy headroom'
    }
  } else if (limit > 0) {
    const pctOfLimit = (usage / limit) * 100
    if (pctOfLimit > 90) {
      guidance = isCPU
        ? 'Near the limit — CPU may be throttled'
        : 'Near the limit — at risk of OOM kill'
    } else {
      guidance = 'No request set — scheduling may be suboptimal'
    }
  } else if (request > 0) {
    guidance = usage > request
      ? `Exceeds request with no limit — unbounded ${isCPU ? 'CPU' : 'memory'} access`
      : 'No limit set — pod can burst beyond request'
  } else {
    guidance = 'No request or limit configured'
  }

  return (
    <div className="whitespace-normal w-52 flex flex-col gap-1.5 py-0.5">
      <div className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs font-mono">
        <span className="text-theme-text-tertiary">Usage</span>
        <span className="text-theme-text-primary">{formatFn(usage)}</span>
        {request > 0 && (
          <>
            <span className="text-theme-text-tertiary">Request</span>
            <span className="text-theme-text-primary">{formatFn(request)}</span>
          </>
        )}
        {limit > 0 && (
          <>
            <span className="text-theme-text-tertiary">Limit</span>
            <span className="text-theme-text-primary">{formatFn(limit)}</span>
          </>
        )}
      </div>
      <div className="text-[11px] text-theme-text-secondary border-t border-theme-border/50 pt-1">
        {guidance}
      </div>
    </div>
  )
}

function NodeCell({ resource, column, majorityNodeMinorVersion }: { resource: any; column: string; majorityNodeMinorVersion?: string }) {
  const metrics = useContext(MetricsContext)

  switch (column) {
    case 'status': {
      const status = getNodeStatus(resource)
      const { problems } = getNodeConditions(resource)
      return (
        <div className="flex items-center gap-2">
          <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
            {status.text}
          </span>
          {problems.length > 0 && (
            <Tooltip content={problems.join(', ')}>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'roles': {
      const roles = getNodeRoles(resource)
      return <span className="text-sm text-theme-text-secondary">{roles}</span>
    }
    case 'conditions': {
      const { problems, healthy } = getNodeConditions(resource)
      if (healthy) {
        return <span className="text-sm text-green-400">Healthy</span>
      }
      return (
        <Tooltip content={problems.join(', ')}>
          <span className="text-sm text-yellow-400 truncate">
            {problems.join(', ')}
          </span>
        </Tooltip>
      )
    }
    case 'taints': {
      const { text, count } = getNodeTaints(resource)
      return (
        <span className={clsx('text-sm', count > 0 ? 'text-yellow-400' : 'text-theme-text-secondary')}>
          {text}
        </span>
      )
    }
    case 'version': {
      const version = getNodeVersion(resource)
      const isSkewed = majorityNodeMinorVersion && version && !version.startsWith(`v${majorityNodeMinorVersion}`)
      return (
        <span className={clsx('text-sm', isSkewed ? 'text-yellow-400 font-medium' : 'text-theme-text-secondary')}>
          {version}
        </span>
      )
    }
    case 'cpu': {
      const m = metrics.nodes.get(resource.metadata?.name)
      if (!m || m.cpu === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const pct = m.cpuAllocatable > 0 ? (m.cpu / m.cpuAllocatable) * 100 : 0
      return <ResourceBar used={formatCPU(m.cpu)} total={formatCPU(m.cpuAllocatable)} percent={pct} />
    }
    case 'memory': {
      const m = metrics.nodes.get(resource.metadata?.name)
      if (!m || m.memory === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const pct = m.memoryAllocatable > 0 ? (m.memory / m.memoryAllocatable) * 100 : 0
      return <ResourceBar used={formatMemoryShort(m.memory)} total={formatMemoryShort(m.memoryAllocatable)} percent={pct} />
    }
    case 'pods': {
      const m = metrics.nodes.get(resource.metadata?.name)
      const allocatable = resource.status?.allocatable?.pods
      const podCount = m?.podCount ?? 0
      if (!allocatable) return <span className="text-sm text-theme-text-tertiary font-mono">{podCount || '-'}</span>
      const max = parseInt(allocatable, 10)
      if (isNaN(max) || max <= 0) return <span className="text-sm text-theme-text-tertiary font-mono">{podCount || '-'}</span>
      const pct = (podCount / max) * 100
      return <ResourceBar used={String(podCount)} total={String(max)} percent={pct} colorScheme="count" />
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PVCCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPVCStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'capacity': {
      const capacity = getPVCCapacity(resource)
      return <span className="text-sm text-theme-text-secondary">{capacity}</span>
    }
    case 'storageClass':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.storageClassName || '-'}</span>
    case 'accessModes': {
      const modes = getPVCAccessModes(resource)
      return <span className="text-sm text-theme-text-secondary">{modes}</span>
    }
    case 'volume':
      return (
        <Tooltip content={resource.spec?.volumeName}>
          <span className="text-sm text-theme-text-secondary truncate block">{resource.spec?.volumeName || '-'}</span>
        </Tooltip>
      )
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RolloutCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRolloutStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'ready': {
      const ready = getRolloutReady(resource)
      const parts = ready.split('/')
      const allReady = parts.length === 2 && parts[0] === parts[1] && parts[0] !== '0'
      return (
        <span className={clsx('text-sm font-medium', allReady ? 'text-green-400' : 'text-yellow-400')}>
          {ready}
        </span>
      )
    }
    case 'strategy': {
      const strategy = getRolloutStrategy(resource)
      return <span className="text-sm text-theme-text-secondary">{strategy}</span>
    }
    case 'step': {
      const step = getRolloutStep(resource)
      return <span className="text-sm text-theme-text-secondary">{step || '-'}</span>
    }
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">{display}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkflowCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getWorkflowStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'duration': {
      const duration = getWorkflowDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{duration || '-'}</span>
    }
    case 'progress': {
      const progress = getWorkflowProgress(resource)
      return <span className="text-sm text-theme-text-secondary">{progress || '-'}</span>
    }
    case 'template': {
      const template = getWorkflowTemplate(resource)
      return (
        <Tooltip content={template}>
          <span className="text-sm text-theme-text-secondary truncate block">{template || '-'}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}


function PersistentVolumeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPVStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'capacity':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.capacity?.storage || '-'}</span>
    case 'accessModes': {
      const modes = getPVAccessModes(resource)
      return <span className="text-sm text-theme-text-secondary">{modes}</span>
    }
    case 'reclaimPolicy': {
      const policy = resource.spec?.persistentVolumeReclaimPolicy || '-'
      return (
        <span className={clsx('text-sm', policy === 'Delete' ? 'text-red-400' : policy === 'Retain' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {policy}
        </span>
      )
    }
    case 'storageClass':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.storageClassName || '-'}</span>
    case 'claim': {
      const claim = getPVClaim(resource)
      return (
        <Tooltip content={claim}>
          <span className="text-sm text-theme-text-secondary truncate block">{claim}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function StorageClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'provisioner':
      return (
        <Tooltip content={getStorageClassProvisioner(resource)}>
          <span className="text-sm text-theme-text-secondary truncate block">{getStorageClassProvisioner(resource)}</span>
        </Tooltip>
      )
    case 'reclaimPolicy': {
      const policy = getStorageClassReclaimPolicy(resource)
      return (
        <span className={clsx('text-sm', policy === 'Delete' ? 'text-red-400' : policy === 'Retain' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {policy}
        </span>
      )
    }
    case 'bindingMode':
      return <span className="text-sm text-theme-text-secondary">{getStorageClassBindingMode(resource)}</span>
    case 'expansion': {
      const expansion = getStorageClassExpansion(resource)
      return (
        <span className={clsx('text-sm', expansion === 'Yes' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {expansion}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}



function GatewayCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getGatewayStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'class':
      return <span className="text-sm text-theme-text-secondary">{getGatewayClass(resource)}</span>
    case 'listeners': {
      const listeners = getGatewayListeners(resource)
      return (
        <Tooltip content={listeners}>
          <span className="text-sm text-theme-text-secondary truncate block">{listeners}</span>
        </Tooltip>
      )
    }
    case 'routes':
      return <span className="text-sm text-theme-text-secondary">{getGatewayAttachedRoutes(resource)}</span>
    case 'addresses': {
      const addrs = getGatewayAddresses(resource)
      return (
        <Tooltip content={addrs}>
          <span className="text-sm text-theme-text-secondary truncate block">{addrs}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function GatewayClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getGatewayClassStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'controller': {
      const controller = getGatewayClassController(resource)
      return (
        <Tooltip content={controller}>
          <span className="text-sm text-theme-text-secondary truncate block">{controller}</span>
        </Tooltip>
      )
    }
    case 'description': {
      const desc = getGatewayClassDescription(resource)
      return (
        <Tooltip content={desc}>
          <span className="text-sm text-theme-text-secondary truncate block">{desc}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// Shared cell renderer for all Gateway API route types (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
function RouteCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRouteStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'hostnames': {
      const hostnames = getRouteHostnames(resource)
      return (
        <Tooltip content={hostnames}>
          <span className="text-sm text-theme-text-secondary truncate block">{hostnames}</span>
        </Tooltip>
      )
    }
    case 'parents':
      return <span className="text-sm text-theme-text-secondary">{getRouteParents(resource)}</span>
    case 'backends': {
      const backends = getRouteBackends(resource)
      return (
        <Tooltip content={backends}>
          <span className="text-sm text-theme-text-secondary truncate block">{backends}</span>
        </Tooltip>
      )
    }
    case 'rules':
      return <span className="text-sm text-theme-text-secondary">{getRouteRulesCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function SealedSecretCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getSealedSecretStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'keys':
      return <span className="text-sm text-theme-text-secondary">{getSealedSecretKeyCount(resource)}</span>
    case 'type':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.template?.type || 'Opaque'}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkflowTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'entrypoint':
      return <span className="text-sm text-theme-text-secondary">{getWorkflowTemplateEntrypoint(resource)}</span>
    case 'templates':
      return <span className="text-sm text-theme-text-secondary">{getWorkflowTemplateCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function NetworkPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'policyTypes':
      return <span className="text-sm text-theme-text-secondary">{getNetworkPolicyTypes(resource)}</span>
    case 'selector': {
      const selector = getNetworkPolicySelector(resource)
      return (
        <Tooltip content={selector}>
          <span className="text-sm text-theme-text-secondary truncate block">{selector}</span>
        </Tooltip>
      )
    }
    case 'rules': {
      const { ingress, egress } = getNetworkPolicyRuleCount(resource)
      return <span className="text-sm text-theme-text-secondary">{ingress}i / {egress}e</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PDBCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPDBStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'budget':
      return <span className="text-sm text-theme-text-secondary">{getPDBBudget(resource)}</span>
    case 'healthy': {
      const healthy = getPDBHealthy(resource)
      return <span className="text-sm text-theme-text-secondary">{healthy}</span>
    }
    case 'allowed': {
      const allowed = getPDBAllowed(resource)
      return (
        <span className={clsx('text-sm font-medium', allowed > 0 ? 'text-green-400' : 'text-red-400')}>
          {allowed}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ServiceAccountCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'automount': {
      const automount = getServiceAccountAutomount(resource)
      return <span className={clsx('text-sm', automount === 'No' ? 'text-green-400' : 'text-yellow-400')}>{automount}</span>
    }
    case 'secrets':
      return <span className="text-sm text-theme-text-secondary">{getServiceAccountSecretCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RoleCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'rules':
      return <span className="text-sm text-theme-text-secondary">{getRoleRuleCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RoleBindingCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'role': {
      const ref = resource.roleRef
      if (!ref?.name) return <span className="text-sm text-theme-text-tertiary">-</span>
      const kindColor = ref.kind === 'ClusterRole' ? 'bg-purple-500/20 text-purple-400' : 'bg-blue-500/20 text-blue-400'
      return (
        <span className="text-sm text-theme-text-secondary inline-flex items-center gap-1.5">
          <span className={clsx('px-1.5 py-0.5 rounded text-xs', kindColor)}>{ref.kind === 'ClusterRole' ? 'CR' : 'R'}</span>
          {ref.name}
        </span>
      )
    }
    case 'subjects': {
      const subjects: any[] = resource.subjects || []
      if (subjects.length === 0) return <span className="text-sm text-theme-text-tertiary">0</span>
      const preview = subjects.slice(0, 2).map((s: any) => {
        const prefix = s.kind === 'ServiceAccount' ? 'sa:' : s.kind === 'Group' ? 'grp:' : ''
        return prefix + s.name
      }).join(', ')
      const more = subjects.length > 2 ? ` +${subjects.length - 2}` : ''
      return <span className="text-sm text-theme-text-secondary" title={subjects.map((s: any) => `${s.kind}:${s.name}`).join(', ')}>{preview}{more}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function IngressClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'controller':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.controller || '-'}</span>
    case 'default': {
      const isDefault = resource.metadata?.annotations?.['ingressclass.kubernetes.io/is-default-class'] === 'true'
      return <span className={clsx('text-sm', isDefault ? 'text-green-400' : 'text-theme-text-tertiary')}>{isDefault ? 'Yes' : 'No'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function LeaseCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'holder':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.holderIdentity || '-'}</span>
    case 'renewTime': {
      const renewTime = resource.spec?.renewTime
      if (!renewTime) return <span className="text-sm text-theme-text-tertiary">-</span>
      const elapsed = (Date.now() - new Date(renewTime).getTime()) / 1000
      const duration = resource.spec?.leaseDurationSeconds || 0
      const isStale = duration > 0 && elapsed > duration
      const diff = Date.now() - new Date(renewTime).getTime()
      const seconds = Math.floor(diff / 1000)
      const label = seconds < 60 ? `${seconds}s ago` : seconds < 3600 ? `${Math.floor(seconds / 60)}m ago` : seconds < 86400 ? `${Math.floor(seconds / 3600)}h ago` : `${Math.floor(seconds / 86400)}d ago`
      return <span className={clsx('text-sm', isStale ? 'text-red-400' : 'text-green-400')} title={new Date(renewTime).toLocaleString()}>{label}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PriorityClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'value':
      return <span className="text-sm text-theme-text-secondary">{resource.value ?? '-'}</span>
    case 'globalDefault':
      return <span className={clsx('text-sm', resource.globalDefault ? 'text-green-400' : 'text-theme-text-tertiary')}>{resource.globalDefault ? 'Yes' : 'No'}</span>
    case 'preemptionPolicy':
      return <span className="text-sm text-theme-text-secondary">{resource.preemptionPolicy || 'PreemptLowerPriority'}</span>
    case 'description':
      return resource.description
        ? <span className="text-sm text-theme-text-secondary truncate" title={resource.description}>{resource.description}</span>
        : <span className="text-sm text-theme-text-tertiary">-</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RuntimeClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'handler':
      return <span className="text-sm text-theme-text-secondary">{resource.handler || '-'}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WebhookConfigCell({ resource, column }: { resource: any; column: string }) {
  const webhooks = resource.webhooks || []
  switch (column) {
    case 'webhooks':
      return <span className="text-sm text-theme-text-secondary">{webhooks.length}</span>
    case 'failurePolicy': {
      const hasFail = webhooks.some((w: any) => w.failurePolicy === 'Fail')
      return hasFail
        ? <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-red-500/20 text-red-400">Fail</span>
        : <span className="text-sm text-theme-text-secondary">Ignore</span>
    }
    case 'target': {
      if (webhooks.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const first = webhooks[0]
      const svc = first.clientConfig?.service
      const target = svc ? `${svc.namespace}/${svc.name}` : first.clientConfig?.url || '-'
      const more = webhooks.length > 1 ? ` +${webhooks.length - 1}` : ''
      return <span className="text-sm text-theme-text-secondary truncate" title={target}>{target}{more && <span className="text-theme-text-tertiary">{more}</span>}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function EventCell({ resource, column }: { resource: any; column: string }) {
  const eventType = resource.type || 'Normal'
  const isWarning = eventType === 'Warning'

  switch (column) {
    case 'type':
      return (
        <span className={clsx(
          'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
          isWarning ? 'bg-amber-500/20 text-amber-400' : 'bg-blue-500/20 text-blue-400'
        )}>
          {eventType}
        </span>
      )
    case 'reason':
      return (
        <span className={clsx(
          'text-sm font-medium',
          isWarning ? 'text-amber-400' : 'text-theme-text-secondary'
        )}>
          {resource.reason || '-'}
        </span>
      )
    case 'message': {
      const message = resource.message || ''
      return (
        <Tooltip content={message}>
          <span className="text-sm text-theme-text-secondary truncate block max-w-64">
            {message || '-'}
          </span>
        </Tooltip>
      )
    }
    case 'object': {
      const obj = resource.involvedObject
      if (!obj) return <span className="text-sm text-theme-text-tertiary">-</span>
      const objRef = `${obj.kind}/${obj.name}`
      return (
        <Tooltip content={`${obj.kind}: ${obj.namespace ? obj.namespace + '/' : ''}${obj.name}`}>
          <span className="text-sm text-theme-text-secondary truncate block">
            {objRef}
          </span>
        </Tooltip>
      )
    }
    case 'count': {
      const count = resource.count || 1
      return (
        <span className={clsx(
          'text-sm',
          count > 1 ? 'text-amber-400 font-medium' : 'text-theme-text-secondary'
        )}>
          {count}
        </span>
      )
    }
    case 'lastSeen': {
      const lastTimestamp = resource.lastTimestamp || resource.metadata?.creationTimestamp
      if (!lastTimestamp) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary">{formatAge(lastTimestamp)}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}



