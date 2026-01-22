import { useState, useMemo, useEffect, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Box,
  Search,
  RefreshCw,
  AlertTriangle,
  Shield,
  Globe,
  HardDrive,
  Database,
  Puzzle,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Eye,
  EyeOff,
  ArrowUpDown,
  Clock,
  Filter,
  X,
  // K8s resource icons
  Rocket,
  Rows3,
  DatabaseZap,
  Copy,
  Play,
  Timer,
  Plug,
  DoorOpen,
  ShieldCheck,
  Radio,
  FileSliders,
  KeyRound,
  Cylinder,
  Cpu,
  FolderOpen,
  UserCog,
  Activity,
  Scaling,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { SelectedResource, APIResource } from '../../types'
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
  formatAge,
  truncate,
} from './resource-utils'
import { Tooltip } from '../ui/Tooltip'

// Filter options for different resource kinds
const POD_PHASES = ['Running', 'Pending', 'Succeeded', 'Failed', 'Unknown'] as const
const POD_PROBLEMS = ['CrashLoopBackOff', 'ImagePullBackOff', 'OOMKilled', 'Unschedulable', 'Not Ready', 'High Restarts'] as const
const WORKLOAD_HEALTH = ['Healthy', 'Degraded', 'Unhealthy', 'Scaled to 0'] as const
const NODE_CONDITIONS = ['DiskPressure', 'MemoryPressure', 'PIDPressure', 'NetworkUnavailable', 'NotReady'] as const

// Fallback resource types when API resources aren't loaded yet
const CORE_RESOURCE_TYPES = [
  { kind: 'pods', label: 'Pods', icon: Box },
  { kind: 'deployments', label: 'Deployments', icon: Rocket },
  { kind: 'daemonsets', label: 'DaemonSets', icon: Rows3 },
  { kind: 'statefulsets', label: 'StatefulSets', icon: DatabaseZap },
  { kind: 'replicasets', label: 'ReplicaSets', icon: Copy },
  { kind: 'services', label: 'Services', icon: Plug },
  { kind: 'ingresses', label: 'Ingresses', icon: DoorOpen },
  { kind: 'configmaps', label: 'ConfigMaps', icon: FileSliders },
  { kind: 'secrets', label: 'Secrets', icon: KeyRound },
  { kind: 'jobs', label: 'Jobs', icon: Play },
  { kind: 'cronjobs', label: 'CronJobs', icon: Timer },
  { kind: 'hpas', label: 'HPAs', icon: Scaling },
] as const

// Map kind names to icons
const KIND_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  // Workloads
  pod: Box,
  deployment: Rocket,
  daemonset: Rows3,
  statefulset: DatabaseZap,
  replicaset: Copy,
  job: Play,
  cronjob: Timer,

  // Networking
  service: Plug,
  ingress: DoorOpen,
  networkpolicy: ShieldCheck,
  endpoints: Radio,
  endpointslice: Radio,

  // Config & Storage
  configmap: FileSliders,
  secret: KeyRound,
  persistentvolumeclaim: HardDrive,
  persistentvolume: Cylinder,
  storageclass: Database,

  // Cluster
  node: Cpu,
  namespace: FolderOpen,
  serviceaccount: UserCog,
  event: Activity,

  // Scaling
  horizontalpodautoscaler: Scaling,

  // Default for CRDs
  default: Puzzle,
}

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

function getIconForKind(kind: string): React.ComponentType<{ className?: string }> {
  return KIND_ICONS[kind.toLowerCase()] || KIND_ICONS.default
}

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
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true },
    { key: 'conditions', label: 'Conditions', width: 'w-44', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  daemonsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'desired', label: 'Desired', width: 'w-20' },
    { key: 'ready', label: 'Ready', width: 'w-20' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  statefulsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
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
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'class', label: 'Class', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-40' },
    { key: 'rules', label: 'Rules', width: 'w-56', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-16' },
    { key: 'address', label: 'Address', width: 'w-32', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
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
}

function getColumnsForKind(kind: string): Column[] {
  return KNOWN_COLUMNS[kind.toLowerCase()] || DEFAULT_COLUMNS
}

interface ResourcesViewProps {
  namespace: string
  selectedResource?: SelectedResource | null
  onResourceClick?: (kind: string, namespace: string, name: string) => void
  onKindChange?: () => void // Called when user changes resource type in sidebar
}

// Default selected kind
const DEFAULT_KIND_INFO: SelectedKindInfo = { name: 'pods', kind: 'Pod', group: '' }

// Read initial state from URL
function getInitialKindFromURL(): SelectedKindInfo {
  const params = new URLSearchParams(window.location.search)
  const kind = params.get('kind')
  const group = params.get('group') || ''
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
  }
}

// Sort state type
type SortDirection = 'asc' | 'desc' | null

export function ResourcesView({ namespace, selectedResource, onResourceClick, onKindChange }: ResourcesViewProps) {
  const initialFilters = getInitialFiltersFromURL()
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(getInitialKindFromURL)
  const [searchTerm, setSearchTerm] = useState(initialFilters.search)
  const [expandedCategories, setExpandedCategories] = useState<Set<string>>(new Set(['Workloads', 'Networking', 'Configuration']))
  const [showEmptyKinds, setShowEmptyKinds] = useState(false)
  const [sortColumn, setSortColumn] = useState<string | null>(null)
  const [sortDirection, setSortDirection] = useState<SortDirection>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  // Filter state
  const [statusFilter, setStatusFilter] = useState<string>(initialFilters.statusFilter)
  const [problemFilters, setProblemFilters] = useState<string[]>(initialFilters.problemFilters)
  const [showFilterDropdown, setShowFilterDropdown] = useState(false)

  // Update URL with all state
  const updateURL = useCallback((
    kindInfo: SelectedKindInfo,
    search: string,
    status: string,
    problems: string[],
    resourceNs?: string,
    resourceName?: string
  ) => {
    // Preserve existing params (like namespace from App)
    const params = new URLSearchParams(window.location.search)

    // Set/update resources-specific params
    params.set('kind', kindInfo.kind)
    if (kindInfo.group) {
      params.set('group', kindInfo.group)
    } else {
      params.delete('group')
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
    // Skip URL update if selectedResource's kind doesn't match selectedKind (still syncing)
    if (selectedResource) {
      const resourceKindLower = selectedResource.kind.toLowerCase()
      if (selectedKind.name.toLowerCase() !== resourceKindLower) {
        return // Wait for kind sync effect to run first
      }
    }
    updateURL(selectedKind, searchTerm, statusFilter, problemFilters, selectedResource?.namespace, selectedResource?.name)
  }, [selectedKind, searchTerm, statusFilter, problemFilters, selectedResource, updateURL])

  // Handle resource click from URL on mount
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const resourceParam = params.get('resource')
    if (resourceParam && onResourceClick) {
      const [ns, name] = resourceParam.split('/')
      if (ns && name) {
        onResourceClick(selectedKind.name, ns, name)
      }
    }
  }, []) // Only on mount

  // Sync selectedKind when selectedResource changes from external navigation (e.g., from Helm view)
  useEffect(() => {
    if (!selectedResource) return

    // Check if the selected resource's kind matches current selectedKind
    const resourceKindLower = selectedResource.kind.toLowerCase()
    if (selectedKind.name.toLowerCase() === resourceKindLower) return

    // Find matching resource from CORE_RESOURCES or use plural name directly
    const coreMatch = CORE_RESOURCES.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )

    if (coreMatch) {
      setSelectedKind({ name: coreMatch.name, kind: coreMatch.kind, group: coreMatch.group })
    } else {
      // Fallback: use the kind directly, derive singular from plural
      const singular = resourceKindLower.endsWith('s')
        ? resourceKindLower.slice(0, -1).charAt(0).toUpperCase() + resourceKindLower.slice(1, -1)
        : resourceKindLower.charAt(0).toUpperCase() + resourceKindLower.slice(1)
      setSelectedKind({ name: resourceKindLower, kind: singular, group: '' })
    }
  }, [selectedResource, selectedKind.name])

  // Fetch API resources for dynamic sidebar
  const { data: apiResources } = useAPIResources()

  // Categorize resources for sidebar
  const categories = useMemo(() => {
    if (!apiResources) return null
    return categorizeResources(apiResources)
  }, [apiResources])

  // Fetch resources for selected kind
  const { data: resources, isLoading, refetch, dataUpdatedAt } = useQuery({
    queryKey: ['resources', selectedKind.name, selectedKind.group, namespace],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespace) params.set('namespace', namespace)
      if (selectedKind.group) params.set('group', selectedKind.group)
      const res = await fetch(`/api/resources/${selectedKind.name}?${params}`)
      if (!res.ok) throw new Error('Failed to fetch resources')
      return res.json()
    },
    refetchInterval: 10000,
  })

  // Track last updated time
  useEffect(() => {
    if (dataUpdatedAt) {
      setLastUpdated(new Date(dataUpdatedAt))
    }
  }, [dataUpdatedAt])

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
  const getSortValue = useCallback((resource: any, column: string): string | number => {
    const meta = resource.metadata || {}
    switch (column) {
      case 'name':
        return meta.name || ''
      case 'namespace':
        return meta.namespace || ''
      case 'age':
        return meta.creationTimestamp ? new Date(meta.creationTimestamp).getTime() : 0
      case 'status':
        return resource.status?.phase || ''
      case 'ready':
        // For pods, use ready/total ratio
        if (resource.status?.containerStatuses) {
          const ready = resource.status.containerStatuses.filter((c: any) => c.ready).length
          const total = resource.status.containerStatuses.length
          return total > 0 ? ready / total : 0
        }
        // For workloads, use readyReplicas/replicas ratio
        const desired = resource.spec?.replicas ?? 0
        const readyReplicas = resource.status?.readyReplicas ?? 0
        return desired > 0 ? readyReplicas / desired : 0
      case 'restarts':
        return getPodRestarts(resource)
      case 'type':
        return resource.spec?.type || ''
      case 'version':
        return resource.status?.nodeInfo?.kubeletVersion || ''
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

    // Apply custom sorting if set
    if (sortColumn && sortDirection) {
      result = [...result].sort((a: any, b: any) => {
        const aVal = getSortValue(a, sortColumn)
        const bVal = getSortValue(b, sortColumn)
        let comparison = 0
        if (typeof aVal === 'number' && typeof bVal === 'number') {
          comparison = aVal - bVal
        } else {
          comparison = String(aVal).localeCompare(String(bVal))
        }
        return sortDirection === 'desc' ? -comparison : comparison
      })
    } else {
      // Default sort: completed pods at bottom for pods, otherwise by name
      if (selectedKind.name === 'pods') {
        result = [...result].sort((a: any, b: any) => {
          const aCompleted = a.status?.phase === 'Succeeded'
          const bCompleted = b.status?.phase === 'Succeeded'
          if (aCompleted && !bCompleted) return 1
          if (!aCompleted && bCompleted) return -1
          return 0
        })
      }
    }

    return result
  }, [resources, searchTerm, statusFilter, problemFilters, selectedKind.name, sortColumn, sortDirection, getSortValue, podMatchesProblemFilter, getWorkloadHealthLevel])

  // Get count for each resource type - now uses dynamic resources
  // Deduplicate to avoid redundant fetches (e.g., multiple API versions of same resource)
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

  const { data: counts } = useQuery({
    queryKey: ['resource-counts', namespace, resourcesToCount.map(r => r.kind).join(',')],
    queryFn: async () => {
      const results: Record<string, number> = {}
      await Promise.all(
        resourcesToCount.map(async (resource) => {
          try {
            const params = new URLSearchParams()
            if (namespace) params.set('namespace', namespace)
            if (resource.group) params.set('group', resource.group)
            const res = await fetch(`/api/resources/${resource.name}?${params}`)
            if (res.ok) {
              const data = await res.json()
              // Key by kind (unique) not name (can conflict)
              results[resource.kind] = Array.isArray(data) ? data.length : 0
            } else {
              results[resource.kind] = 0
            }
          } catch {
            results[resource.kind] = 0
          }
        })
      )
      return results
    },
    refetchInterval: 30000,
    enabled: resourcesToCount.length > 0,
  })

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

  // Check if any filters are active
  const hasActiveFilters = statusFilter !== '' || problemFilters.length > 0

  // Clear all filters
  const clearFilters = useCallback(() => {
    setStatusFilter('')
    setProblemFilters([])
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
    <div className="flex h-full">
      {/* Sidebar - Resource Types */}
      <div className="w-72 bg-theme-surface border-r border-theme-border overflow-y-auto shrink-0">
        <div className="p-3 border-b border-theme-border">
          <h2 className="text-sm font-medium text-theme-text-secondary uppercase tracking-wide">
            Resources
          </h2>
        </div>
        <nav className="p-2">
          {sortedCategories ? (
            // Dynamic categories from API
            sortedCategories.map((category) => {
              const isExpanded = expandedCategories.has(category.name)
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
                      {category.visibleResources.map((resource) => (
                        <ResourceTypeButton
                          key={resource.name}
                          resource={resource}
                          count={counts?.[resource.kind] ?? 0}
                          isSelected={selectedKind.kind === resource.kind}
                          onClick={() => {
                            setSelectedKind({ name: resource.name, kind: resource.kind, group: resource.group })
                            onKindChange?.()
                          }}
                        />
                      ))}
                    </div>
                  )}
                </div>
              )
            })
          ) : (
            // Fallback to core resources while loading
            CORE_RESOURCE_TYPES.map((type) => {
              const Icon = type.icon
              // Fallback: type.label is display name like 'Pods', counts are keyed by Kind like 'Pod'
              // Remove trailing 's' for singular kind lookup (hacky but works for fallback)
              const kindKey = type.label.endsWith('s') && !type.label.endsWith('ss')
                ? type.label.slice(0, -1)
                : type.label
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
                      ? 'bg-blue-500/20 text-blue-300'
                      : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                  )}
                >
                  <Icon className="w-4 h-4 flex-shrink-0" />
                  <span className="flex-1 text-left">{type.label}</span>
                  <span className={clsx(
                    'text-xs px-2 py-0.5 rounded',
                    isSelected ? 'bg-blue-500/30' : 'bg-theme-elevated'
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
              type="text"
              placeholder="Search resources..."
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
                    ? 'bg-blue-500/20 text-blue-300 hover:bg-blue-500/30'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <Filter className="w-4 h-4" />
                <span>Filter</span>
                {hasActiveFilters && (
                  <span className="px-1.5 py-0.5 text-xs bg-blue-500/30 rounded">
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
                                  ? 'bg-blue-500/30 text-blue-300'
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
                                  ? value === 'Healthy' ? 'bg-green-500/30 text-green-300'
                                    : value === 'Degraded' ? 'bg-yellow-500/30 text-yellow-300'
                                    : value === 'Unhealthy' ? 'bg-red-500/30 text-red-300'
                                    : 'bg-blue-500/30 text-blue-300'
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
                                  ? 'bg-red-500/30 text-red-300'
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

          {/* Active filter badges */}
          {hasActiveFilters && (
            <div className="flex items-center gap-2">
              {statusFilter && (
                <span className="flex items-center gap-1 px-2 py-1 text-xs bg-blue-500/20 text-blue-300 rounded">
                  {statusFilter}
                  <button onClick={() => setStatusFilter('')} className="hover:text-theme-text-primary">
                    <X className="w-3 h-3" />
                  </button>
                </span>
              )}
              {problemFilters.map(p => (
                <span key={p} className="flex items-center gap-1 px-2 py-1 text-xs bg-red-500/20 text-red-300 rounded">
                  {p}
                  <button onClick={() => toggleProblemFilter(p)} className="hover:text-theme-text-primary">
                    <X className="w-3 h-3" />
                  </button>
                </span>
              ))}
            </div>
          )}

          {lastUpdated && (
            <div className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
              <Clock className="w-3.5 h-3.5" />
              <span>Updated {formatAge(lastUpdated.toISOString())}</span>
            </div>
          )}
          <button
            onClick={() => refetch()}
            className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg"
            title="Refresh"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
        </div>

        {/* Table */}
        <div className="flex-1 overflow-auto">
          {isLoading ? (
            <div className="flex items-center justify-center h-full text-theme-text-tertiary">
              Loading...
            </div>
          ) : filteredResources.length === 0 ? (
            <div className="flex items-center justify-center h-full text-theme-text-tertiary">
              No {selectedKind.kind} found
            </div>
          ) : (
            <table className="w-full table-fixed">
              <thead className="bg-theme-surface sticky top-0 z-10">
                <tr>
                  {columns.map((col) => {
                    const isSortable = ['name', 'namespace', 'age', 'status', 'ready', 'restarts', 'type', 'version'].includes(col.key)
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
                          <span>{col.label}</span>
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
                      resource={resource}
                      kind={selectedKind.name}
                      columns={columns}
                      isSelected={isSelected}
                      onClick={() => onResourceClick?.(selectedKind.name, resource.metadata?.namespace, resource.metadata?.name)}
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
  onClick: () => void
}

function ResourceTypeButton({ resource, count, isSelected, onClick }: ResourceTypeButtonProps) {
  const Icon = getIconForKind(resource.kind)
  return (
    <button
      onClick={onClick}
      className={clsx(
        'w-full flex items-center gap-3 px-3 py-1.5 rounded-lg text-sm transition-colors',
        isSelected
          ? 'bg-blue-500/20 text-blue-300'
          : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
      )}
    >
      <Icon className="w-4 h-4 flex-shrink-0" />
      <Tooltip content={resource.kind} position="right">
        <span className="flex-1 text-left truncate">
          {resource.kind}
        </span>
      </Tooltip>
      <span className={clsx(
        'text-xs px-1.5 py-0.5 rounded min-w-[1.5rem] text-center',
        isSelected ? 'bg-blue-500/30' : 'bg-theme-elevated'
      )}>
        {count}
      </span>
    </button>
  )
}

interface ResourceRowProps {
  resource: any
  kind: string
  columns: Column[]
  isSelected?: boolean
  onClick?: () => void
}

function ResourceRow({ resource, kind, columns, isSelected, onClick }: ResourceRowProps) {
  return (
    <tr
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
