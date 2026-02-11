import { useState, useMemo, useEffect, useCallback, useRef, forwardRef } from 'react'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { useLocation } from 'react-router-dom'
import { useQueries } from '@tanstack/react-query'
import { ApiError, isForbiddenError } from '../../api/client'
import {
  Search,
  RefreshCw,
  AlertTriangle,
  Globe,
  Shield,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Eye,
  EyeOff,
  ArrowUpDown,
  Clock,
  Filter,
  X,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { SelectedResource, APIResource } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { useAPIResources, categorizeResources, CORE_RESOURCES } from '../../api/apiResources'
import {
  getPodStatus,
  getPodReadiness,
  getPodRestarts,
  getPodProblems,
  getWorkloadStatus,
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
  getCertificateStatus,
  getCertificateDomains,
  getCertificateIssuer,
  getCertificateExpiry,
  getPVStatus,
  getPVAccessModes,
  getPVClaim,
  getStorageClassProvisioner,
  getStorageClassReclaimPolicy,
  getStorageClassBindingMode,
  getStorageClassExpansion,
  getCertificateRequestStatus,
  getCertificateRequestIssuer,
  getCertificateRequestApproved,
  getClusterIssuerStatus,
  getClusterIssuerType,
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
  getRoleBindingRole,
  getRoleBindingSubjectCount,
  // FluxCD GitOps
  getGitRepositoryUrl,
  getGitRepositoryRef,
  getGitRepositoryRevision,
  getGitRepositoryStatus,
  getOCIRepositoryUrl,
  getOCIRepositoryRef,
  getOCIRepositoryRevision,
  getOCIRepositoryStatus,
  getHelmRepositoryUrl,
  getHelmRepositoryType,
  getHelmRepositoryStatus,
  getKustomizationSource,
  getKustomizationPath,
  getKustomizationInventory,
  getKustomizationStatus,
  getFluxHelmReleaseChart,
  getFluxHelmReleaseVersion,
  getFluxHelmReleaseRevision,
  getFluxHelmReleaseStatus,
  getFluxAlertProvider,
  getFluxAlertEventCount,
  getFluxAlertStatus,
  // ArgoCD GitOps
  getArgoApplicationProject,
  getArgoApplicationSync,
  getArgoApplicationHealth,
  getArgoApplicationRepo,
  getArgoApplicationSetGenerators,
  getArgoApplicationSetTemplate,
  getArgoApplicationSetAppCount,
  getArgoApplicationSetStatus,
  getArgoAppProjectDescription,
  getArgoAppProjectDestinations,
  getArgoAppProjectSources,
  formatAge,
  truncate,
} from './resource-utils'
import { Tooltip } from '../ui/Tooltip'
import { getResourceIcon } from '../../utils/resource-icons'

// Filter options for different resource kinds
const POD_PHASES = ['Running', 'Pending', 'Succeeded', 'Failed', 'Unknown'] as const
const POD_PROBLEMS = ['CrashLoopBackOff', 'ImagePullBackOff', 'OOMKilled', 'Unschedulable', 'Not Ready', 'High Restarts'] as const
const WORKLOAD_HEALTH = ['Healthy', 'Degraded', 'Unhealthy', 'Scaled to 0'] as const
const NODE_CONDITIONS = ['DiskPressure', 'MemoryPressure', 'PIDPressure', 'NetworkUnavailable', 'NotReady'] as const

// Fallback resource types when API resources aren't loaded yet
const CORE_RESOURCE_TYPES = [
  { kind: 'pods', label: 'Pods' },
  { kind: 'deployments', label: 'Deployments' },
  { kind: 'daemonsets', label: 'DaemonSets' },
  { kind: 'statefulsets', label: 'StatefulSets' },
  { kind: 'replicasets', label: 'ReplicaSets' },
  { kind: 'services', label: 'Services' },
  { kind: 'ingresses', label: 'Ingresses' },
  { kind: 'configmaps', label: 'ConfigMaps' },
  { kind: 'secrets', label: 'Secrets' },
  { kind: 'jobs', label: 'Jobs' },
  { kind: 'cronjobs', label: 'CronJobs' },
  { kind: 'hpas', label: 'HPAs' },
] as const

// Core kinds that are always shown even with 0 instances
// These are the most commonly used Kubernetes resources (using Kind names, not plural names)
const ALWAYS_SHOWN_KINDS = new Set([
  'Pod',
  'Deployment',
  'DaemonSet',
  'StatefulSet',
  'ReplicaSet',
  'Service',
  'Ingress',
  'ConfigMap',
  'Secret',
  'Job',
  'CronJob',
  'HorizontalPodAutoscaler',
  'PersistentVolumeClaim',
  'Node',
  'Namespace',
  'ServiceAccount',
  'NetworkPolicy',
  'Event',
])

// Selected resource type info (need both name for API and kind for display)
interface SelectedKindInfo {
  name: string      // Plural name for API calls (e.g., 'pods')
  kind: string      // Kind for display (e.g., 'Pod')
  group: string     // API group for disambiguation (e.g., '', 'metrics.k8s.io')
}

// Column definitions per resource kind
interface Column {
  key: string
  label: string
  width?: string
  hideOnMobile?: boolean
  tooltip?: string // Explanation of what this column means
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
    { key: 'ready', label: 'Ready', width: 'w-16' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'restarts', label: 'Restarts', width: 'w-24' },
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
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  gatewayclasses: [
    { key: 'name', label: 'Name' },
    { key: 'controller', label: 'Controller', width: 'w-64', tooltip: 'Gateway controller implementation (spec.controllerName)' },
    { key: 'description', label: 'Description', width: 'w-64', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  grpcroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  tcproutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  tlsroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48', tooltip: 'SNI hostnames for TLS routing' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
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
}

function getColumnsForKind(kind: string): Column[] {
  return KNOWN_COLUMNS[kind.toLowerCase()] || DEFAULT_COLUMNS
}

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: NavigateToResource
  onKindChange?: () => void // Called when user changes resource type in sidebar
}

// Default selected kind
const DEFAULT_KIND_INFO: SelectedKindInfo = { name: 'pods', kind: 'Pod', group: '' }

// Read initial state from URL
function getInitialKindFromURL(): SelectedKindInfo {
  const params = new URLSearchParams(window.location.search)
  const kind = params.get('kind')
  const group = params.get('apiGroup') || ''
  if (kind) {
    // Find matching resource from CORE_RESOURCES or use as-is
    const coreMatch = CORE_RESOURCES.find(r => r.kind === kind || r.name === kind)
    if (coreMatch) {
      return { name: coreMatch.name, kind: coreMatch.kind, group: coreMatch.group }
    }
    return { name: kind, kind: kind, group }
  }
  return DEFAULT_KIND_INFO
}

// Get initial filters from URL
function getInitialFiltersFromURL() {
  const params = new URLSearchParams(window.location.search)
  return {
    search: params.get('search') || '',
    statusFilter: params.get('status') || '',
    problemFilters: params.get('problems')?.split(',').filter(Boolean) || [],
    showInactive: params.get('showInactive') === 'true',
    labelSelector: params.get('labels') || '', // e.g., "app=caretta,version=v1"
    ownerKind: params.get('ownerKind') || '', // e.g., "DaemonSet"
    ownerName: params.get('ownerName') || '', // e.g., "app-caretta"
  }
}

// Sort state type
type SortDirection = 'asc' | 'desc' | null

export function ResourcesView({ namespaces, selectedResource, onResourceClick, onKindChange }: ResourcesViewProps) {
  const location = useLocation()
  const initialFilters = getInitialFiltersFromURL()
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(getInitialKindFromURL)
  const [searchTerm, setSearchTerm] = useState(initialFilters.search)
  const [expandedCategories, setExpandedCategories] = useState<Set<string>>(new Set(['Workloads', 'Networking', 'Configuration']))
  const [showEmptyKinds, setShowEmptyKinds] = useState(false)
  const [kindFilter, setKindFilter] = useState('')
  const [sortColumn, setSortColumn] = useState<string | null>(null)
  const [sortDirection, setSortDirection] = useState<SortDirection>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  // Filter state
  const [statusFilter, setStatusFilter] = useState<string>(initialFilters.statusFilter)
  const [problemFilters, setProblemFilters] = useState<string[]>(initialFilters.problemFilters)
  const [showFilterDropdown, setShowFilterDropdown] = useState(false)
  // ReplicaSet-specific: hide inactive by default
  const [showInactiveReplicaSets, setShowInactiveReplicaSets] = useState(initialFilters.showInactive)
  // Label/owner filtering for deep-linking from workload details
  const [labelSelector, setLabelSelector] = useState<string>(initialFilters.labelSelector)
  const [ownerKind, setOwnerKind] = useState<string>(initialFilters.ownerKind)
  const [ownerName, setOwnerName] = useState<string>(initialFilters.ownerName)

  // Track if this is the initial mount to avoid re-syncing on first render
  const isInitialMount = useRef(true)
  const isSyncingFromURL = useRef(false)

  // Ref to selected row for scrolling into view on deeplink
  const selectedRowRef = useRef<HTMLTableRowElement>(null)
  // Ref to selected sidebar item for scrolling into view on deeplink
  const selectedSidebarRef = useRef<HTMLButtonElement>(null)
  // Track what resource we last scrolled to, to avoid re-scrolling on group expand
  const lastScrolledResource = useRef<string | null>(null)
  // Ref to search input for keyboard shortcut
  const searchInputRef = useRef<HTMLInputElement>(null)

  // Keyboard shortcut: / or Cmd/Ctrl+K to focus search
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === '/' || ((e.metaKey || e.ctrlKey) && e.key === 'k')) {
        const tag = (e.target as HTMLElement)?.tagName
        if (tag === 'INPUT' || tag === 'TEXTAREA') return
        e.preventDefault()
        searchInputRef.current?.focus()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  // Sync state from URL when navigation occurs (e.g., deep linking from WorkloadRenderer)
  useEffect(() => {
    if (isInitialMount.current) {
      isInitialMount.current = false
      return
    }

    // Mark that we're syncing from URL to prevent URL write-back
    isSyncingFromURL.current = true

    // Re-read URL params and update state
    const newKind = getInitialKindFromURL()
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

    // Reset the flag after a tick to allow normal URL updates
    requestAnimationFrame(() => {
      isSyncingFromURL.current = false
    })
  }, [location.search]) // Re-run when URL search params change

  // Update URL with all state
  const updateURL = useCallback((
    kindInfo: SelectedKindInfo,
    search: string,
    status: string,
    problems: string[],
    showInactive: boolean,
    resourceNs?: string,
    resourceName?: string
  ) => {
    // Preserve existing params (like namespace from App)
    const params = new URLSearchParams(window.location.search)

    // Set/update resources-specific params
    params.set('kind', kindInfo.kind)
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
    if (status) {
      params.set('status', status)
    } else {
      params.delete('status')
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
    if (resourceNs && resourceName) {
      params.set('resource', `${resourceNs}/${resourceName}`)
    } else {
      params.delete('resource')
    }

    const newURL = `${window.location.pathname}?${params.toString()}`
    window.history.replaceState({}, '', newURL)
  }, [])

  // Update URL when any filter changes
  useEffect(() => {
    // Skip URL update if we're syncing FROM the URL (e.g., browser back button)
    if (isSyncingFromURL.current) {
      return
    }
    // Skip URL update if selectedResource's kind doesn't match selectedKind (still syncing)
    if (selectedResource) {
      const resourceKindLower = selectedResource.kind.toLowerCase()
      if (selectedKind.name.toLowerCase() !== resourceKindLower) {
        return // Wait for kind sync effect to run first
      }
    }
    updateURL(selectedKind, searchTerm, statusFilter, problemFilters, showInactiveReplicaSets, selectedResource?.namespace, selectedResource?.name)
  }, [selectedKind, searchTerm, statusFilter, problemFilters, showInactiveReplicaSets, selectedResource, updateURL])

  // Handle resource click from URL on mount
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const resourceParam = params.get('resource')
    if (resourceParam && onResourceClick) {
      const [ns, name] = resourceParam.split('/')
      if (ns && name) {
        onResourceClick({ kind: selectedKind.name, namespace: ns, name, group: selectedKind.group })
      }
    }
  }, []) // Only on mount

  // Fetch API resources for dynamic sidebar (must be above effects that reference apiResources)
  const { data: apiResources } = useAPIResources()

  // Sync selectedKind when selectedResource changes from external navigation (e.g., from Helm view)
  // Also re-runs when apiResources loads, to correct CRD kinds that were initially resolved via fallback
  useEffect(() => {
    if (!selectedResource) return

    const resourceKindLower = selectedResource.kind.toLowerCase()

    // Find the best match: prefer dynamic API discovery (has correct kind for CRDs) over hardcoded
    const apiMatch = apiResources?.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const coreMatch = CORE_RESOURCES.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const match = apiMatch || coreMatch

    if (match) {
      // Skip if already correctly resolved (check kind too — fallback may have set wrong casing)
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

  // Auto-expand the sidebar category containing the selected kind (e.g., when deep-linking to a CRD)
  useEffect(() => {
    if (!categories) return
    for (const cat of categories) {
      const match = cat.resources.some(r => r.kind === selectedKind.kind || r.name === selectedKind.name)
      if (match && !expandedCategories.has(cat.name)) {
        setExpandedCategories(prev => new Set([...prev, cat.name]))
        break
      }
    }
  }, [categories, selectedKind.kind, selectedKind.name])

  // Get resources to count - use kind as unique key since name can conflict (e.g., pods vs PodMetrics)
  const resourcesToCount = useMemo(() => {
    if (categories) {
      return categories.flatMap(c => c.resources).map(r => ({
        kind: r.kind,
        name: r.name,
        group: r.group,
      }))
    }
    return CORE_RESOURCE_TYPES.map(t => ({
      kind: t.label,
      name: t.kind,
      group: '',
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

  // Fetch ALL resources using useQueries - single source of truth for both counts and display
  const resourceQueries = useQueries({
    queries: resourcesToCount.map((resource) => ({
      queryKey: ['resources', resource.name, resource.group, namespaces],
      queryFn: async () => {
        const params = new URLSearchParams()
        if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
        if (resource.group) params.set('group', resource.group)
        const res = await fetch(`/api/resources/${resource.name}?${params}`)
        if (!res.ok) {
          if (res.status === 403) {
            throw new ApiError('Insufficient permissions', 403)
          }
          return []
        }
        return res.json()
      },
      staleTime: 30000,
      refetchInterval: 30000,
      retry: (failureCount: number, error: Error) => {
        // Don't retry on 403 - permissions won't change
        if (isForbiddenError(error)) return false
        return failureCount < 3
      },
    })),
  })

  // Find the selected kind's query and derive resources/isLoading/refetch from it
  const selectedQueryIndex = useMemo(() => {
    return resourcesToCount.findIndex(r =>
      r.name === selectedKind.name && r.group === selectedKind.group
    )
  }, [resourcesToCount, selectedKind.name, selectedKind.group])

  const selectedQuery = resourceQueries[selectedQueryIndex]
  const resources = selectedQuery?.data
  const isLoading = selectedQuery?.isLoading ?? true
  const selectedQueryError = selectedQuery?.error
  const isSelectedForbidden = isForbiddenError(selectedQueryError)
  const refetchFn = selectedQuery?.refetch
  const dataUpdatedAt = selectedQuery?.dataUpdatedAt

  const [refetch, isRefreshAnimating] = useRefreshAnimation(() => refetchFn?.())

  // Track last updated time
  useEffect(() => {
    if (dataUpdatedAt) {
      setLastUpdated(new Date(dataUpdatedAt))
    }
  }, [dataUpdatedAt])

  // Derive counts from all query results
  const counts = useMemo(() => {
    const results: Record<string, number> = {}
    resourcesToCount.forEach((resource, index) => {
      const data = resourceQueries[index]?.data
      results[resource.kind] = Array.isArray(data) ? data.length : 0
    })
    return results
  }, [resourcesToCount, resourceQueries])

  // Track which resource kinds returned 403 Forbidden
  const forbiddenKinds = useMemo(() => {
    const result = new Set<string>()
    resourcesToCount.forEach((resource, index) => {
      if (isForbiddenError(resourceQueries[index]?.error)) {
        result.add(resource.kind)
      }
    })
    return result
  }, [resourcesToCount, resourceQueries])

  // Reset sort and filters when kind changes
  useEffect(() => {
    setSortColumn(null)
    setSortDirection(null)
    setStatusFilter('')
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
      case 'ready':
        // For pods, use ready/total ratio
        if (status.containerStatuses) {
          const ready = status.containerStatuses.filter((c: any) => c.ready).length
          const total = status.containerStatuses.length
          return total > 0 ? ready / total : 0
        }
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
      case 'type':
        return resource.spec?.type || ''
      case 'version':
        return status.nodeInfo?.kubeletVersion || ''
      default:
        return ''
    }
  }, [])

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

  // Helper to get workload health level
  const getWorkloadHealthLevel = useCallback((resource: any, kind: string): string => {
    const status = getWorkloadStatus(resource, kind)
    if (status.text === 'Scaled to 0') return 'Scaled to 0'
    if (status.level === 'healthy') return 'Healthy'
    if (status.level === 'degraded') return 'Degraded'
    if (status.level === 'unhealthy') return 'Unhealthy'
    return 'Unknown'
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

    // Apply status filter
    if (statusFilter) {
      const kindLower = selectedKind.name.toLowerCase()
      result = result.filter((r: any) => {
        if (kindLower === 'pods') {
          // Pod phase filter
          return r.status?.phase === statusFilter
        } else if (['deployments', 'statefulsets', 'daemonsets', 'replicasets'].includes(kindLower)) {
          // Workload health filter
          const health = getWorkloadHealthLevel(r, kindLower)
          return health === statusFilter
        } else if (kindLower === 'nodes') {
          // Node condition filter
          const nodeStatus = getNodeStatus(r)
          const { problems } = getNodeConditions(r)
          if (statusFilter === 'NotReady') {
            return nodeStatus.text.includes('NotReady')
          }
          return problems.some(p => p.replace(' ', '') === statusFilter.replace(' ', ''))
        }
        return true
      })
    }

    // Apply problem filters (pods only)
    if (problemFilters.length > 0 && selectedKind.name.toLowerCase() === 'pods') {
      result = result.filter((r: any) => podMatchesProblemFilter(r, problemFilters))
    }

    // Apply inactive ReplicaSet filter (default: hide inactive)
    if (selectedKind.name.toLowerCase() === 'replicasets' && !showInactiveReplicaSets) {
      result = result.filter((r: any) => isReplicaSetActive(r))
    }

    // Apply label selector filter (e.g., "app=caretta,version=v1")
    if (labelSelector) {
      const labelPairs = labelSelector.split(',').map(pair => {
        const [key, value] = pair.split('=')
        return { key: key?.trim(), value: value?.trim() }
      }).filter(p => p.key && p.value)

      result = result.filter((r: any) => {
        const labels = r.metadata?.labels || {}
        return labelPairs.every(({ key, value }) => labels[key] === value)
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
      const kindLower = selectedKind.name.toLowerCase()

      if (kindLower === 'pods') {
        // Completed pods at bottom
        result = [...result].sort((a: any, b: any) => {
          const aCompleted = a.status?.phase === 'Succeeded'
          const bCompleted = b.status?.phase === 'Succeeded'
          if (aCompleted && !bCompleted) return 1
          if (!aCompleted && bCompleted) return -1
          return 0
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
      }
    }

    return result
  }, [resources, searchTerm, statusFilter, problemFilters, showInactiveReplicaSets, labelSelector, ownerKind, ownerName, selectedKind.name, sortColumn, sortDirection, getSortValue, podMatchesProblemFilter, getWorkloadHealthLevel])

  // Scroll to selected row when selection changes (but not on group expand/filteredResources change)
  useEffect(() => {
    if (!selectedResource) {
      lastScrolledResource.current = null
      return
    }

    // Create a key for the current selection
    const resourceKey = `${selectedResource.kind}/${selectedResource.namespace}/${selectedResource.name}`

    // Only scroll if this is a new selection
    if (lastScrolledResource.current === resourceKey) return
    lastScrolledResource.current = resourceKey

    // Small delay to allow DOM to update, then scroll
    const timer = setTimeout(() => {
      selectedRowRef.current?.scrollIntoView({
        behavior: 'smooth',
        block: 'center',
      })
    }, 100)

    return () => clearTimeout(timer)
  }, [selectedResource]) // Only re-run when selection changes, NOT on filteredResources change

  // Scroll sidebar to show selected kind when deep linking (but not on manual category expand)
  const lastScrolledKind = useRef<string | null>(null)
  useEffect(() => {
    const kindKey = `${selectedKind.group}/${selectedKind.name}`

    // Only scroll if the selected kind actually changed, not just category expansion
    if (lastScrolledKind.current === kindKey) return
    lastScrolledKind.current = kindKey

    // Use requestAnimationFrame to ensure DOM is updated after category expansion
    requestAnimationFrame(() => {
      if (selectedSidebarRef.current) {
        selectedSidebarRef.current.scrollIntoView({
          behavior: 'smooth',
          block: 'center',
        })
      }
    })
  }, [selectedKind.name, selectedKind.group, expandedCategories]) // Re-run after category expansion

  // Calculate category totals, filter empty kinds/groups, and sort (empty categories at bottom)
  const { sortedCategories, hiddenKindsCount, hiddenGroupsCount } = useMemo(() => {
    if (!categories) return { sortedCategories: null, hiddenKindsCount: 0, hiddenGroupsCount: 0 }

    let totalHiddenKinds = 0
    let totalHiddenGroups = 0

    const withTotals = categories.map(category => {
      const total = category.resources.reduce(
        (sum, resource) => sum + (counts?.[resource.kind] ?? 0),
        0
      )

      // Filter resources: show if has instances, is core kind, or showEmptyKinds is true
      const visibleResources = category.resources.filter(resource => {
        const count = counts?.[resource.kind] ?? 0
        const isCore = ALWAYS_SHOWN_KINDS.has(resource.kind)
        const shouldShow = count > 0 || isCore || showEmptyKinds
        if (!shouldShow) totalHiddenKinds++
        return shouldShow
      })

      return { ...category, total, visibleResources }
    })

    // Sort: categories with resources first, empty ones at bottom
    const sorted = withTotals.sort((a, b) => {
      if (a.total === 0 && b.total > 0) return 1
      if (a.total > 0 && b.total === 0) return -1
      return 0
    })

    // Filter out empty groups unless they have visible resources (core kinds) or showEmptyKinds is true
    const visibleCategories = sorted.filter(category => {
      // Show if: has resources with instances, OR has visible resources (core kinds), OR showEmptyKinds
      const shouldShow = category.total > 0 || category.visibleResources.length > 0 || showEmptyKinds
      if (!shouldShow) totalHiddenGroups++
      return shouldShow
    })

    return { sortedCategories: visibleCategories, hiddenKindsCount: totalHiddenKinds, hiddenGroupsCount: totalHiddenGroups }
  }, [categories, counts, showEmptyKinds])

  // Filter sidebar categories/kinds by the kind search term
  const filteredCategories = useMemo(() => {
    if (!sortedCategories || !kindFilter.trim()) return sortedCategories
    const term = kindFilter.toLowerCase()
    return sortedCategories
      .map(category => {
        const matchingResources = category.visibleResources.filter((resource: any) =>
          resource.kind.toLowerCase().includes(term) ||
          resource.name.toLowerCase().includes(term)
        )
        if (matchingResources.length === 0 && !category.name.toLowerCase().includes(term)) return null
        return {
          ...category,
          visibleResources: matchingResources.length > 0 ? matchingResources : category.visibleResources,
        }
      })
      .filter(Boolean) as typeof sortedCategories
  }, [sortedCategories, kindFilter])

  // Auto-expand all categories when filtering
  const isKindFiltering = kindFilter.trim().length > 0
  const effectiveExpandedCategories = useMemo(() => {
    if (!isKindFiltering || !filteredCategories) return expandedCategories
    return new Set(filteredCategories.map(c => c.name))
  }, [isKindFiltering, filteredCategories, expandedCategories])

  const toggleCategory = (categoryName: string) => {
    setExpandedCategories(prev => {
      const next = new Set(prev)
      if (next.has(categoryName)) {
        next.delete(categoryName)
      } else {
        next.add(categoryName)
      }
      return next
    })
  }

  const columns = getColumnsForKind(selectedKind.name)

  // Calculate filter options with counts based on current resources (before filtering)
  const filterOptions = useMemo(() => {
    if (!resources || resources.length === 0) return null

    const kindLower = selectedKind.name.toLowerCase()

    if (kindLower === 'pods') {
      // Pod phase counts
      const phaseCounts: Record<string, number> = {}
      POD_PHASES.forEach(p => phaseCounts[p] = 0)
      // Problem counts
      const problemCounts: Record<string, number> = {}
      POD_PROBLEMS.forEach(p => problemCounts[p] = 0)

      for (const pod of resources) {
        const phase = pod.status?.phase || 'Unknown'
        if (phaseCounts[phase] !== undefined) phaseCounts[phase]++

        // Count problems
        const problems = getPodProblems(pod)
        const msgs = problems.map(p => p.message)
        const restarts = getPodRestarts(pod)

        if (msgs.includes('CrashLoopBackOff')) problemCounts['CrashLoopBackOff']++
        if (msgs.some(m => m.includes('ImagePull'))) problemCounts['ImagePullBackOff']++
        if (msgs.includes('OOMKilled')) problemCounts['OOMKilled']++
        if (msgs.includes('Unschedulable')) problemCounts['Unschedulable']++
        if (msgs.includes('Not Ready') || msgs.some(m => m.includes('Probe'))) problemCounts['Not Ready']++
        if (restarts > 5) problemCounts['High Restarts']++
      }

      return {
        type: 'pods' as const,
        phases: POD_PHASES.map(p => ({ value: p, count: phaseCounts[p] })).filter(p => p.count > 0),
        problems: POD_PROBLEMS.map(p => ({ value: p, count: problemCounts[p] })).filter(p => p.count > 0),
      }
    }

    if (['deployments', 'statefulsets', 'daemonsets', 'replicasets'].includes(kindLower)) {
      const healthCounts: Record<string, number> = {}
      WORKLOAD_HEALTH.forEach(h => healthCounts[h] = 0)

      for (const resource of resources) {
        const health = getWorkloadHealthLevel(resource, kindLower)
        if (healthCounts[health] !== undefined) healthCounts[health]++
      }

      return {
        type: 'workload' as const,
        health: WORKLOAD_HEALTH.map(h => ({ value: h, count: healthCounts[h] })).filter(h => h.count > 0),
      }
    }

    if (kindLower === 'nodes') {
      const conditionCounts: Record<string, number> = {}
      NODE_CONDITIONS.forEach(c => conditionCounts[c] = 0)

      for (const node of resources) {
        const nodeStatus = getNodeStatus(node)
        const { problems } = getNodeConditions(node)
        if (nodeStatus.text.includes('NotReady')) conditionCounts['NotReady']++
        problems.forEach(p => {
          const key = p.replace(' ', '')
          if (conditionCounts[key] !== undefined) conditionCounts[key]++
        })
      }

      return {
        type: 'nodes' as const,
        conditions: NODE_CONDITIONS.map(c => ({ value: c, count: conditionCounts[c] })).filter(c => c.count > 0),
      }
    }

    return null
  }, [resources, selectedKind.name, getWorkloadHealthLevel])

  // Compute inactive ReplicaSet count for toggle display
  const inactiveReplicaSetCount = useMemo(() => {
    if (selectedKind.name.toLowerCase() !== 'replicasets' || !resources) return 0
    return resources.filter((r: any) => !isReplicaSetActive(r)).length
  }, [resources, selectedKind.name])

  // Check if any filters are active
  const hasActiveFilters = statusFilter !== '' || problemFilters.length > 0 || labelSelector !== '' || (ownerKind !== '' && ownerName !== '')
  const hasOwnerFilter = ownerKind !== '' && ownerName !== ''

  // Clear all filters
  const clearFilters = useCallback(() => {
    setStatusFilter('')
    setProblemFilters([])
    setLabelSelector('')
    setOwnerKind('')
    setOwnerName('')
    // Also clear URL params
    const params = new URLSearchParams(window.location.search)
    params.delete('labels')
    params.delete('ownerKind')
    params.delete('ownerName')
    window.history.replaceState({}, '', `${window.location.pathname}?${params.toString()}`)
    setShowFilterDropdown(false)
  }, [])

  // Toggle problem filter
  const toggleProblemFilter = useCallback((problem: string) => {
    setProblemFilters(prev =>
      prev.includes(problem)
        ? prev.filter(p => p !== problem)
        : [...prev, problem]
    )
  }, [])

  return (
    <div className="flex h-full w-full">
      {/* Sidebar - Resource Types */}
      <div className="w-72 bg-theme-surface border-r border-theme-border overflow-y-auto shrink-0">
        <div className="flex items-center gap-2 px-3 py-3 border-b border-theme-border">
          <h2 className="text-sm font-medium text-theme-text-secondary uppercase tracking-wide shrink-0">
            Resources
          </h2>
          <div className="flex-1 relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-theme-text-tertiary" />
            <input
              type="text"
              placeholder="Filter kinds..."
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value)}
              className="w-full pl-7 pr-7 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            {kindFilter && (
              <button
                onClick={() => setKindFilter('')}
                className="absolute right-1.5 top-1/2 -translate-y-1/2 p-0.5 rounded hover:bg-theme-surface text-theme-text-tertiary hover:text-theme-text-secondary"
              >
                <X className="w-3 h-3" />
              </button>
            )}
          </div>
        </div>
        <nav className="p-2">
          {filteredCategories ? (
            // Dynamic categories from API
            filteredCategories.map((category) => {
              const isExpanded = effectiveExpandedCategories.has(category.name)
              return (
                <div key={category.name} className="mb-2">
                  <button
                    onClick={() => toggleCategory(category.name)}
                    className="w-full flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-theme-text-tertiary hover:text-theme-text-secondary uppercase tracking-wide"
                  >
                    {isExpanded ? (
                      <ChevronDown className="w-3 h-3" />
                    ) : (
                      <ChevronRight className="w-3 h-3" />
                    )}
                    <span className="flex-1 text-left">{category.name}</span>
                    {!isExpanded && (
                      <span className="text-xs px-1.5 py-0.5 rounded bg-theme-elevated text-theme-text-secondary font-normal normal-case">
                        {category.total}
                      </span>
                    )}
                  </button>
                  {isExpanded && (
                    <div className="space-y-0.5">
                      {category.visibleResources.map((resource) => {
                        const isResourceSelected =
                          (selectedKind.name === resource.name && selectedKind.group === resource.group) ||
                          (selectedKind.kind.toLowerCase() === resource.kind.toLowerCase() && selectedKind.group === resource.group)
                        return (
                        <ResourceTypeButton
                          key={resource.name}
                          ref={isResourceSelected ? selectedSidebarRef : null}
                          resource={resource}
                          count={counts?.[resource.kind] ?? 0}
                          isSelected={isResourceSelected}
                          isForbidden={forbiddenKinds.has(resource.kind)}
                          onClick={() => {
                            setSelectedKind({ name: resource.name, kind: resource.kind, group: resource.group })
                            onKindChange?.()
                          }}
                        />
                        )
                      })}
                    </div>
                  )}
                </div>
              )
            })
          ) : (
            // Fallback to core resources while loading
            CORE_RESOURCE_TYPES.map((type) => {
              // Fallback: type.label is display name like 'Pods', counts are keyed by Kind like 'Pod'
              // Remove trailing 's' for singular kind lookup (hacky but works for fallback)
              const kindKey = type.label.endsWith('s') && !type.label.endsWith('ss')
                ? type.label.slice(0, -1)
                : type.label
              const Icon = getResourceIcon(kindKey)
              const count = counts?.[kindKey] ?? 0
              const isSelected = selectedKind.name === type.kind
              return (
                <button
                  key={type.kind}
                  onClick={() => {
                    setSelectedKind({ name: type.kind, kind: type.label, group: '' })
                    onKindChange?.()
                  }}
                  className={clsx(
                    'w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
                    isSelected
                      ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300'
                      : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                  )}
                >
                  <Icon className="w-4 h-4 shrink-0" />
                  <span className="flex-1 text-left">{type.label}</span>
                  <span className={clsx(
                    'text-xs px-2 py-0.5 rounded',
                    isSelected ? 'bg-blue-500/30 text-blue-700 dark:text-blue-300' : 'bg-theme-elevated'
                  )}>
                    {count}
                  </span>
                </button>
              )
            })
          )}

          {/* Toggle for showing/hiding empty kinds and groups */}
          {hiddenKindsCount > 0 || hiddenGroupsCount > 0 || showEmptyKinds ? (
            <button
              onClick={() => setShowEmptyKinds(!showEmptyKinds)}
              className="w-full flex items-center gap-2 px-3 py-2 mt-2 text-xs text-theme-text-tertiary hover:text-theme-text-secondary border-t border-theme-border"
            >
              {showEmptyKinds ? (
                <>
                  <EyeOff className="w-3.5 h-3.5" />
                  <span>Hide empty</span>
                </>
              ) : (
                <>
                  <Eye className="w-3.5 h-3.5" />
                  <span>
                    Show {hiddenKindsCount + hiddenGroupsCount} empty
                    {hiddenGroupsCount > 0 && ` (${hiddenGroupsCount} groups)`}
                  </span>
                </>
              )}
            </button>
          ) : null}
        </nav>
      </div>

      {/* Main Content - Resource Table */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        {/* Toolbar */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border bg-theme-surface/50 shrink-0">
          <div className="flex-1 relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
            <input
              ref={searchInputRef}
              type="text"
              placeholder="Search... (/ or ⌘K)"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="w-full max-w-md pl-10 pr-4 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>

          {/* Filter dropdown */}
          {filterOptions && (
            <div className="relative">
              <button
                onClick={() => setShowFilterDropdown(!showFilterDropdown)}
                className={clsx(
                  'flex items-center gap-2 px-3 py-2 rounded-lg text-sm transition-colors',
                  hasActiveFilters
                    ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300 hover:bg-blue-500/30'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <Filter className="w-4 h-4" />
                <span>Filter</span>
                {hasActiveFilters && (
                  <span className="px-1.5 py-0.5 text-xs bg-blue-500/30 text-blue-700 dark:text-blue-300 rounded">
                    {(statusFilter ? 1 : 0) + problemFilters.length}
                  </span>
                )}
              </button>

              {showFilterDropdown && (
                <div className="absolute right-0 top-full mt-2 w-64 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50">
                  <div className="p-3 border-b border-theme-border flex items-center justify-between">
                    <span className="text-sm font-medium text-theme-text-primary">Filters</span>
                    {hasActiveFilters && (
                      <button
                        onClick={clearFilters}
                        className="text-xs text-theme-text-secondary hover:text-theme-text-primary"
                      >
                        Clear all
                      </button>
                    )}
                  </div>

                  <div className="p-3 space-y-4 max-h-80 overflow-y-auto">
                    {/* Status/Phase filter */}
                    {filterOptions.type === 'pods' && filterOptions.phases.length > 0 && (
                      <div>
                        <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide mb-2 block">
                          Phase
                        </label>
                        <div className="flex flex-wrap gap-1.5">
                          {filterOptions.phases.map(({ value, count }) => (
                            <button
                              key={value}
                              onClick={() => setStatusFilter(statusFilter === value ? '' : value)}
                              className={clsx(
                                'px-2 py-1 text-xs rounded transition-colors',
                                statusFilter === value
                                  ? 'bg-blue-500/30 text-blue-700 dark:text-blue-300'
                                  : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary'
                              )}
                            >
                              {value} ({count})
                            </button>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Problem filter (pods only) */}
                    {filterOptions.type === 'pods' && filterOptions.problems.length > 0 && (
                      <div>
                        <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide mb-2 block">
                          Problems
                        </label>
                        <div className="flex flex-wrap gap-1.5">
                          {filterOptions.problems.map(({ value, count }) => (
                            <button
                              key={value}
                              onClick={() => toggleProblemFilter(value)}
                              className={clsx(
                                'px-2 py-1 text-xs rounded transition-colors',
                                problemFilters.includes(value)
                                  ? 'bg-red-500/30 text-red-300'
                                  : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary'
                              )}
                            >
                              {value} ({count})
                            </button>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Workload health filter */}
                    {filterOptions.type === 'workload' && filterOptions.health.length > 0 && (
                      <div>
                        <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide mb-2 block">
                          Health
                        </label>
                        <div className="flex flex-wrap gap-1.5">
                          {filterOptions.health.map(({ value, count }) => (
                            <button
                              key={value}
                              onClick={() => setStatusFilter(statusFilter === value ? '' : value)}
                              className={clsx(
                                'px-2 py-1 text-xs rounded transition-colors',
                                statusFilter === value
                                  ? value === 'Healthy' ? 'bg-green-500/30 text-green-700 dark:text-green-300'
                                    : value === 'Degraded' ? 'bg-yellow-500/30 text-yellow-700 dark:text-yellow-300'
                                    : value === 'Unhealthy' ? 'bg-red-500/30 text-red-700 dark:text-red-300'
                                    : 'bg-blue-500/30 text-blue-700 dark:text-blue-300'
                                  : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary'
                              )}
                            >
                              {value} ({count})
                            </button>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Node conditions filter */}
                    {filterOptions.type === 'nodes' && filterOptions.conditions.length > 0 && (
                      <div>
                        <label className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide mb-2 block">
                          Conditions
                        </label>
                        <div className="flex flex-wrap gap-1.5">
                          {filterOptions.conditions.map(({ value, count }) => (
                            <button
                              key={value}
                              onClick={() => setStatusFilter(statusFilter === value ? '' : value)}
                              className={clsx(
                                'px-2 py-1 text-xs rounded transition-colors',
                                statusFilter === value
                                  ? 'bg-red-500/30 text-red-700 dark:text-red-300'
                                  : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary'
                              )}
                            >
                              {value.replace(/([A-Z])/g, ' $1').trim()} ({count})
                            </button>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* ReplicaSet inactive toggle */}
          {selectedKind.name.toLowerCase() === 'replicasets' && inactiveReplicaSetCount > 0 && (
            <label className="flex items-center gap-1.5 text-xs text-theme-text-tertiary cursor-pointer hover:text-theme-text-secondary">
              <input
                type="checkbox"
                checked={showInactiveReplicaSets}
                onChange={(e) => setShowInactiveReplicaSets(e.target.checked)}
                className="w-3 h-3 rounded border-theme-border-light accent-blue-500"
              />
              Show inactive ({inactiveReplicaSetCount})
            </label>
          )}

          {/* Active filter badges */}
          {hasActiveFilters && (
            <div className="flex items-center gap-2">
              {statusFilter && (
                <span className="flex items-center gap-1 px-2 py-1 text-xs bg-blue-500/20 text-blue-700 dark:text-blue-300 rounded">
                  {statusFilter}
                  <button onClick={() => setStatusFilter('')} className="hover:text-theme-text-primary">
                    <X className="w-3 h-3" />
                  </button>
                </span>
              )}
              {problemFilters.map(p => (
                <span key={p} className="flex items-center gap-1 px-2 py-1 text-xs bg-red-500/20 text-red-700 dark:text-red-300 rounded">
                  {p}
                  <button onClick={() => toggleProblemFilter(p)} className="hover:text-theme-text-primary">
                    <X className="w-3 h-3" />
                  </button>
                </span>
              ))}
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
            </div>
          )}

          {lastUpdated && (
            <div className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
              <Clock className="w-3.5 h-3.5" />
              <span>Updated {formatAge(lastUpdated.toISOString())}</span>
            </div>
          )}
          <button
            onClick={refetch}
            disabled={isRefreshAnimating}
            className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg disabled:opacity-50"
            title="Refresh"
          >
            <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
          </button>
        </div>

        {/* Table */}
        <div className="flex-1 overflow-auto relative">
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
              {searchTerm && <p className="text-sm mt-1">No results for "{searchTerm}"</p>}
              {namespaces.length > 0 && <p className="text-sm mt-1 text-theme-text-disabled">Searching in {namespaces.length === 1 ? `namespace: ${namespaces[0]}` : `${namespaces.length} namespaces`}</p>}
            </div>
          ) : (
            <table className="w-full">
              <thead className="bg-theme-surface sticky top-0 z-10">
                <tr>
                  {columns.map((col) => {
                    const isSortable = ['name', 'namespace', 'age', 'status', 'ready', 'restarts', 'type', 'version', 'desired', 'available', 'upToDate'].includes(col.key)
                    const isSorted = sortColumn === col.key
                    return (
                      <th
                        key={col.key}
                        className={clsx(
                          'text-left px-4 py-3 text-xs font-medium uppercase tracking-wide',
                          col.key !== 'name' && col.width,
                          col.hideOnMobile && 'hidden xl:table-cell',
                          isSortable ? 'text-theme-text-secondary hover:text-theme-text-primary cursor-pointer select-none' : 'text-theme-text-secondary'
                        )}
                        onClick={isSortable ? () => handleSort(col.key) : undefined}
                      >
                        <div className="flex items-center gap-1">
                          {col.tooltip ? (
                            <Tooltip content={col.tooltip}>
                              <span className="border-b border-dotted border-theme-text-tertiary">{col.label}</span>
                            </Tooltip>
                          ) : (
                            <span>{col.label}</span>
                          )}
                          {isSortable && (
                            <span className="text-theme-text-tertiary">
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
                        </div>
                      </th>
                    )
                  })}
                </tr>
              </thead>
              <tbody className="table-divide-subtle">
                {filteredResources.map((resource: any) => {
                  const isSelected = selectedResource?.kind === selectedKind.name &&
                    selectedResource?.namespace === resource.metadata?.namespace &&
                    selectedResource?.name === resource.metadata?.name
                  return (
                    <ResourceRow
                      key={resource.metadata?.uid || `${resource.metadata?.namespace}-${resource.metadata?.name}`}
                      ref={isSelected ? selectedRowRef : null}
                      resource={resource}
                      kind={selectedKind.name}
                      columns={columns}
                      isSelected={isSelected}
                      onClick={() => onResourceClick?.({ kind: selectedKind.name, namespace: resource.metadata?.namespace || '', name: resource.metadata?.name, group: selectedKind.group })}
                    />
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  )
}

// Resource type button in sidebar
interface ResourceTypeButtonProps {
  resource: APIResource
  count: number
  isSelected: boolean
  isForbidden?: boolean
  onClick: () => void
}

const ResourceTypeButton = forwardRef<HTMLButtonElement, ResourceTypeButtonProps>(
  function ResourceTypeButton({ resource, count, isSelected, isForbidden: forbidden, onClick }, ref) {
    const Icon = getResourceIcon(resource.kind)
    return (
      <button
        ref={ref}
        onClick={onClick}
        className={clsx(
          'w-full flex items-center gap-3 px-3 py-1.5 rounded-lg text-sm transition-colors',
          isSelected
            ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300'
            : forbidden
              ? 'text-theme-text-disabled hover:bg-theme-elevated hover:text-theme-text-secondary'
              : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
        )}
      >
        <Icon className="w-4 h-4 shrink-0" />
        <Tooltip content={forbidden ? `${resource.kind} (no access)` : resource.kind} position="right">
          <span className="flex-1 text-left truncate">
            {resource.kind}
          </span>
        </Tooltip>
        {forbidden ? (
          <Tooltip content="Insufficient permissions" position="left">
            <Shield className="w-3.5 h-3.5 text-amber-400/60" />
          </Tooltip>
        ) : (
          <span className={clsx(
            'text-xs px-1.5 py-0.5 rounded min-w-[1.5rem] text-center',
            isSelected ? 'bg-blue-500/30 text-blue-700 dark:text-blue-300' : 'bg-theme-elevated'
          )}>
            {count}
          </span>
        )}
      </button>
    )
  }
)

interface ResourceRowProps {
  resource: any
  kind: string
  columns: Column[]
  isSelected?: boolean
  onClick?: () => void
}

const ResourceRow = forwardRef<HTMLTableRowElement, ResourceRowProps>(
  function ResourceRow({ resource, kind, columns, isSelected, onClick }, ref) {
    return (
      <tr
        ref={ref}
        onClick={onClick}
        className={clsx(
          'cursor-pointer transition-colors',
          isSelected
            ? 'bg-blue-500/20 hover:bg-blue-500/30'
            : 'hover:bg-theme-surface/50'
        )}
      >
      {columns.map((col) => (
        <td
          key={col.key}
          className={clsx(
            'px-4 py-3 overflow-hidden',
            col.key !== 'name' && col.width,
            col.hideOnMobile && 'hidden xl:table-cell'
          )}
        >
          <CellContent resource={resource} kind={kind} column={col.key} />
        </td>
      ))}
      </tr>
    )
  }
)

interface CellContentProps {
  resource: any
  kind: string
  column: string
}

function CellContent({ resource, kind, column }: CellContentProps) {
  const meta = resource.metadata || {}

  // Common columns
  if (column === 'name') {
    return (
      <Tooltip content={meta.name}>
        <span className="text-sm text-theme-text-primary font-medium truncate block">
          {meta.name}
        </span>
      </Tooltip>
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

  // Kind-specific columns
  const kindLower = kind.toLowerCase()
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
      return <NodeCell resource={resource} column={column} />
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
    case 'gateways':
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
    // ArgoCD GitOps resources
    case 'applications':
      return <ArgoApplicationCell resource={resource} column={column} />
    case 'applicationsets':
      return <ArgoApplicationSetCell resource={resource} column={column} />
    case 'appprojects':
      return <ArgoAppProjectCell resource={resource} column={column} />
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
  const phase = resource.status?.phase
  const isCompleted = phase === 'Succeeded'

  switch (column) {
    case 'ready': {
      const { ready, total } = getPodReadiness(resource)
      const allReady = ready === total && total > 0
      // Completed pods (Succeeded) show neutral color, not red
      const color = isCompleted
        ? 'text-theme-text-secondary'
        : allReady
          ? 'text-green-400'
          : ready > 0
            ? 'text-yellow-400'
            : 'text-red-400'
      return (
        <span className={clsx('text-sm font-medium', color)}>
          {ready}/{total}
        </span>
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
      const nodeName = resource.spec?.nodeName || '-'
      return (
        <Tooltip content={nodeName}>
          <span className="text-sm text-theme-text-secondary truncate block">{nodeName}</span>
        </Tooltip>
      )
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

function NodeCell({ resource, column }: { resource: any; column: string }) {
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
      return <span className="text-sm text-theme-text-secondary">{version}</span>
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

function CertificateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCertificateStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'domains': {
      const domains = getCertificateDomains(resource)
      return (
        <Tooltip content={domains}>
          <span className="text-sm text-theme-text-secondary truncate block">{domains || '-'}</span>
        </Tooltip>
      )
    }
    case 'issuer': {
      const issuer = getCertificateIssuer(resource)
      return <span className="text-sm text-theme-text-secondary">{issuer}</span>
    }
    case 'expires': {
      const expiry = getCertificateExpiry(resource)
      return (
        <span className={clsx(
          'text-sm',
          expiry.level === 'unhealthy' ? 'text-red-400' :
          expiry.level === 'degraded' ? 'text-yellow-400' :
          'text-theme-text-secondary'
        )}>
          {expiry.text}
        </span>
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

function CertificateRequestCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCertificateRequestStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'issuer':
      return <span className="text-sm text-theme-text-secondary">{getCertificateRequestIssuer(resource)}</span>
    case 'approved':
      return <span className="text-sm text-theme-text-secondary">{getCertificateRequestApproved(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ClusterIssuerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getClusterIssuerStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'issuerType':
      return <span className="text-sm text-theme-text-secondary">{getClusterIssuerType(resource)}</span>
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
    case 'role':
      return <span className="text-sm text-theme-text-secondary">{getRoleBindingRole(resource)}</span>
    case 'subjects':
      return <span className="text-sm text-theme-text-secondary">{getRoleBindingSubjectCount(resource)}</span>
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

// ============================================================================
// FLUXCD GITOPS CELL RENDERERS
// ============================================================================

function GitRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getGitRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'ref': {
      const ref = getGitRepositoryRef(resource)
      return <span className="text-sm text-theme-text-secondary">{ref}</span>
    }
    case 'status': {
      const status = getGitRepositoryStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getGitRepositoryRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary font-mono">{revision}</span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function OCIRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getOCIRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'ref': {
      const ref = getOCIRepositoryRef(resource)
      return <span className="text-sm text-theme-text-secondary">{ref}</span>
    }
    case 'status': {
      const status = getOCIRepositoryStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getOCIRepositoryRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary font-mono">{revision}</span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function HelmRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getHelmRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'type': {
      const type = getHelmRepositoryType(resource)
      return <span className="text-sm text-theme-text-secondary">{type}</span>
    }
    case 'status': {
      const status = getHelmRepositoryStatus(resource)
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

function KustomizationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'source': {
      const source = getKustomizationSource(resource)
      return (
        <Tooltip content={source}>
          <span className="text-sm text-theme-text-secondary truncate block">{source}</span>
        </Tooltip>
      )
    }
    case 'path': {
      const path = getKustomizationPath(resource)
      return <span className="text-sm text-theme-text-tertiary font-mono">{path}</span>
    }
    case 'status': {
      const status = getKustomizationStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = resource.status?.lastAppliedRevision || resource.status?.lastAttemptedRevision || ''
      if (!revision) return <span className="text-sm text-theme-text-tertiary">-</span>
      // Format: "refs/heads/main@sha1:abc123..." -> "main@abc123"
      const formatted = revision
        .replace(/^refs\/heads\//, '')
        .replace(/^refs\/tags\//, '')
        .replace(/@sha1:([a-f0-9]{8})[a-f0-9]*$/, '@$1')
      return (
        <Tooltip content={revision}>
          <span className="text-sm text-theme-text-secondary font-mono truncate block">{formatted}</span>
        </Tooltip>
      )
    }
    case 'inventory': {
      const count = getKustomizationInventory(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'lastUpdated': {
      // Kustomization uses history[0].lastReconciled or conditions[Ready].lastTransitionTime
      const lastReconcile = resource.status?.history?.[0]?.lastReconciled ||
        resource.status?.conditions?.find((c: any) => c.type === 'Ready')?.lastTransitionTime
      return (
        <span className="text-sm text-theme-text-secondary">
          {lastReconcile ? formatAge(lastReconcile) : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function FluxHelmReleaseCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'chart': {
      const chart = getFluxHelmReleaseChart(resource)
      return (
        <Tooltip content={chart}>
          <span className="text-sm text-theme-text-secondary truncate block">{chart}</span>
        </Tooltip>
      )
    }
    case 'version': {
      const version = getFluxHelmReleaseVersion(resource)
      return <span className="text-sm text-theme-text-secondary">{version}</span>
    }
    case 'status': {
      const status = getFluxHelmReleaseStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getFluxHelmReleaseRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary">
          {revision > 0 ? `#${revision}` : '-'}
        </span>
      )
    }
    case 'lastUpdated': {
      // HelmRelease uses history[0].lastDeployed or conditions[Ready].lastTransitionTime
      const lastReconcile = resource.status?.history?.[0]?.lastDeployed ||
        resource.status?.conditions?.find((c: any) => c.type === 'Ready')?.lastTransitionTime
      return (
        <span className="text-sm text-theme-text-secondary">
          {lastReconcile ? formatAge(lastReconcile) : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function FluxAlertCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'provider': {
      const provider = getFluxAlertProvider(resource)
      return <span className="text-sm text-theme-text-secondary">{provider}</span>
    }
    case 'events': {
      const count = getFluxAlertEventCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'status': {
      const status = getFluxAlertStatus(resource)
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

// ============================================================================
// ARGOCD GITOPS CELL RENDERERS
// ============================================================================

function ArgoApplicationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'project': {
      const project = getArgoApplicationProject(resource)
      return <span className="text-sm text-theme-text-secondary">{project}</span>
    }
    case 'sync': {
      const { status, color } = getArgoApplicationSync(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', color)}>
          {status}
        </span>
      )
    }
    case 'health': {
      const { status, color } = getArgoApplicationHealth(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', color)}>
          {status}
        </span>
      )
    }
    case 'repo': {
      const repo = getArgoApplicationRepo(resource)
      return (
        <Tooltip content={resource.spec?.source?.repoURL || resource.spec?.sources?.[0]?.repoURL || repo}>
          <span className="text-sm text-theme-text-secondary truncate block">{repo}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ArgoApplicationSetCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'generators': {
      const generators = getArgoApplicationSetGenerators(resource)
      return (
        <Tooltip content={generators}>
          <span className="text-sm text-theme-text-secondary truncate block">{generators}</span>
        </Tooltip>
      )
    }
    case 'template': {
      const template = getArgoApplicationSetTemplate(resource)
      return <span className="text-sm text-theme-text-secondary">{template}</span>
    }
    case 'applications': {
      const count = getArgoApplicationSetAppCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'status': {
      const status = getArgoApplicationSetStatus(resource)
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

function ArgoAppProjectCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'description': {
      const desc = getArgoAppProjectDescription(resource)
      return (
        <Tooltip content={desc}>
          <span className="text-sm text-theme-text-secondary truncate block">{desc}</span>
        </Tooltip>
      )
    }
    case 'destinations': {
      const count = getArgoAppProjectDestinations(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count === Infinity ? '*' : count}
        </span>
      )
    }
    case 'sources': {
      const count = getArgoAppProjectSources(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count === Infinity ? '*' : count}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
