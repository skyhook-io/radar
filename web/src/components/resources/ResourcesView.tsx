import { useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Box,
  Server,
  Layers,
  Container,
  FileJson,
  Lock,
  Gauge,
  Network,
  Clock,
  CalendarClock,
  Search,
  RefreshCw,
  AlertTriangle,
  Shield,
  Globe,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { SelectedResource } from '../../types'
import {
  getPodStatus,
  getPodReadiness,
  getPodRestarts,
  getPodProblems,
  getWorkloadImages,
  getReplicaSetOwner,
  isReplicaSetActive,
  getServiceStatus,
  getServicePorts,
  getServiceExternalIP,
  getIngressHosts,
  getIngressClass,
  hasIngressTLS,
  getIngressAddress,
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
  formatAge,
  truncate,
} from './resource-utils'

// Resource types we support
const RESOURCE_TYPES = [
  { kind: 'pods', label: 'Pods', icon: Container },
  { kind: 'deployments', label: 'Deployments', icon: Box },
  { kind: 'daemonsets', label: 'DaemonSets', icon: Box },
  { kind: 'statefulsets', label: 'StatefulSets', icon: Box },
  { kind: 'replicasets', label: 'ReplicaSets', icon: Layers },
  { kind: 'services', label: 'Services', icon: Server },
  { kind: 'ingresses', label: 'Ingresses', icon: Network },
  { kind: 'configmaps', label: 'ConfigMaps', icon: FileJson },
  { kind: 'secrets', label: 'Secrets', icon: Lock },
  { kind: 'jobs', label: 'Jobs', icon: Clock },
  { kind: 'cronjobs', label: 'CronJobs', icon: CalendarClock },
  { kind: 'hpas', label: 'HPAs', icon: Gauge },
] as const

type ResourceKind = typeof RESOURCE_TYPES[number]['kind']

// Column definitions per resource kind
interface Column {
  key: string
  label: string
  width?: string
  hideOnMobile?: boolean
}

const COLUMNS: Record<ResourceKind, Column[]> = {
  pods: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'ready', label: 'Ready', width: 'w-20' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'restarts', label: 'Restarts', width: 'w-20' },
    { key: 'node', label: 'Node', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  deployments: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  daemonsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'desired', label: 'Desired', width: 'w-20' },
    { key: 'ready', label: 'Ready', width: 'w-20' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
    { key: 'available', label: 'Available', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  statefulsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-24', hideOnMobile: true },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  replicasets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'owner', label: 'Owner', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  services: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'clusterIP', label: 'Cluster IP', width: 'w-32', hideOnMobile: true },
    { key: 'externalIP', label: 'External', width: 'w-40', hideOnMobile: true },
    { key: 'ports', label: 'Ports', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  ingresses: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'class', label: 'Class', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-48' },
    { key: 'tls', label: 'TLS', width: 'w-16' },
    { key: 'address', label: 'Address', width: 'w-36', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  configmaps: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'keys', label: 'Keys', width: 'w-48' },
    { key: 'size', label: 'Size', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  secrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'keys', label: 'Keys', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  jobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'completions', label: 'Completions', width: 'w-28' },
    { key: 'duration', label: 'Duration', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  cronjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'schedule', label: 'Schedule', width: 'w-40' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
  hpas: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'target', label: 'Target', width: 'w-48' },
    { key: 'replicas', label: 'Replicas', width: 'w-32' },
    { key: 'metrics', label: 'Metrics', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-20' },
  ],
}

interface ResourcesViewProps {
  namespace: string
  selectedResource?: SelectedResource | null
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

export function ResourcesView({ namespace, selectedResource, onResourceClick }: ResourcesViewProps) {
  const [selectedKind, setSelectedKind] = useState<ResourceKind>('pods')
  const [searchTerm, setSearchTerm] = useState('')

  // Fetch resources for selected kind
  const { data: resources, isLoading, refetch } = useQuery({
    queryKey: ['resources', selectedKind, namespace],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespace) params.set('namespace', namespace)
      const res = await fetch(`/api/resources/${selectedKind}?${params}`)
      if (!res.ok) throw new Error('Failed to fetch resources')
      return res.json()
    },
    refetchInterval: 10000,
  })

  // Filter resources by search term
  const filteredResources = useMemo(() => {
    if (!resources) return []
    if (!searchTerm) return resources
    const term = searchTerm.toLowerCase()
    return resources.filter((r: any) =>
      r.metadata?.name?.toLowerCase().includes(term) ||
      r.metadata?.namespace?.toLowerCase().includes(term)
    )
  }, [resources, searchTerm])

  // Get count for each resource type
  const { data: counts } = useQuery({
    queryKey: ['resource-counts', namespace],
    queryFn: async () => {
      const results: Record<string, number> = {}
      await Promise.all(
        RESOURCE_TYPES.map(async (type) => {
          try {
            const params = new URLSearchParams()
            if (namespace) params.set('namespace', namespace)
            const res = await fetch(`/api/resources/${type.kind}?${params}`)
            if (res.ok) {
              const data = await res.json()
              results[type.kind] = Array.isArray(data) ? data.length : 0
            }
          } catch {
            results[type.kind] = 0
          }
        })
      )
      return results
    },
    refetchInterval: 30000,
  })

  const columns = COLUMNS[selectedKind]

  return (
    <div className="flex h-full">
      {/* Sidebar - Resource Types */}
      <div className="w-56 bg-slate-800 border-r border-slate-700 overflow-y-auto shrink-0">
        <div className="p-3 border-b border-slate-700">
          <h2 className="text-sm font-medium text-slate-400 uppercase tracking-wide">
            Resources
          </h2>
        </div>
        <nav className="p-2">
          {RESOURCE_TYPES.map((type) => {
            const Icon = type.icon
            const count = counts?.[type.kind] ?? 0
            const isSelected = selectedKind === type.kind
            return (
              <button
                key={type.kind}
                onClick={() => setSelectedKind(type.kind)}
                className={clsx(
                  'w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
                  isSelected
                    ? 'bg-indigo-500/20 text-indigo-300'
                    : 'text-slate-400 hover:bg-slate-700 hover:text-white'
                )}
              >
                <Icon className="w-4 h-4 flex-shrink-0" />
                <span className="flex-1 text-left">{type.label}</span>
                <span className={clsx(
                  'text-xs px-2 py-0.5 rounded',
                  isSelected ? 'bg-indigo-500/30' : 'bg-slate-700'
                )}>
                  {count}
                </span>
              </button>
            )
          })}
        </nav>
      </div>

      {/* Main Content - Resource Table */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        {/* Toolbar */}
        <div className="flex items-center gap-4 px-4 py-3 border-b border-slate-700 bg-slate-800/50 shrink-0">
          <div className="flex-1 relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
            <input
              type="text"
              placeholder="Search resources..."
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="w-full max-w-md pl-10 pr-4 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
            />
          </div>
          <button
            onClick={() => refetch()}
            className="p-2 text-slate-400 hover:text-white hover:bg-slate-700 rounded-lg"
            title="Refresh"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
        </div>

        {/* Table */}
        <div className="flex-1 overflow-auto">
          {isLoading ? (
            <div className="flex items-center justify-center h-full text-slate-500">
              Loading...
            </div>
          ) : filteredResources.length === 0 ? (
            <div className="flex items-center justify-center h-full text-slate-500">
              No {selectedKind} found
            </div>
          ) : (
            <table className="w-full">
              <thead className="bg-slate-800 sticky top-0 z-10">
                <tr>
                  {columns.map((col) => (
                    <th
                      key={col.key}
                      className={clsx(
                        'text-left px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wide',
                        col.width,
                        col.hideOnMobile && 'hidden xl:table-cell'
                      )}
                    >
                      {col.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-700/50">
                {filteredResources.map((resource: any) => {
                  const isSelected = selectedResource?.kind === selectedKind &&
                    selectedResource?.namespace === resource.metadata?.namespace &&
                    selectedResource?.name === resource.metadata?.name
                  return (
                    <ResourceRow
                      key={resource.metadata?.uid || `${resource.metadata?.namespace}-${resource.metadata?.name}`}
                      resource={resource}
                      kind={selectedKind}
                      columns={columns}
                      isSelected={isSelected}
                      onClick={() => onResourceClick?.(selectedKind, resource.metadata?.namespace, resource.metadata?.name)}
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

interface ResourceRowProps {
  resource: any
  kind: ResourceKind
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
          ? 'bg-indigo-500/20 hover:bg-indigo-500/30'
          : 'hover:bg-slate-800/50'
      )}
    >
      {columns.map((col) => (
        <td
          key={col.key}
          className={clsx(
            'px-4 py-3',
            col.width,
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
  kind: ResourceKind
  column: string
}

function CellContent({ resource, kind, column }: CellContentProps) {
  const meta = resource.metadata || {}

  // Common columns
  if (column === 'name') {
    return <span className="text-sm text-white font-medium">{meta.name}</span>
  }
  if (column === 'namespace') {
    return <span className="text-sm text-slate-400">{meta.namespace || '-'}</span>
  }
  if (column === 'age') {
    return <span className="text-sm text-slate-400">{formatAge(meta.creationTimestamp)}</span>
  }

  // Kind-specific columns
  switch (kind) {
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
      return <HPACell resource={resource} column={column} />
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

// ============================================================================
// KIND-SPECIFIC CELL RENDERERS
// ============================================================================

function PodCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'ready': {
      const { ready, total } = getPodReadiness(resource)
      const allReady = ready === total && total > 0
      return (
        <span className={clsx(
          'text-sm font-medium',
          allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
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
            <span className="text-red-400" title={problems.map(p => p.message).join(', ')}>
              <AlertTriangle className="w-3.5 h-3.5" />
            </span>
          )}
        </div>
      )
    }
    case 'restarts': {
      const restarts = getPodRestarts(resource)
      return (
        <span className={clsx(
          'text-sm',
          restarts > 5 ? 'text-red-400 font-medium' : restarts > 0 ? 'text-yellow-400' : 'text-slate-400'
        )}>
          {restarts}
        </span>
      )
    }
    case 'node':
      return <span className="text-sm text-slate-400 truncate">{resource.spec?.nodeName || '-'}</span>
    default:
      return <span className="text-sm text-slate-500">-</span>
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
          desired === 0 ? 'text-slate-400' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}/{desired}
        </span>
      )
    }
    case 'upToDate':
      return <span className="text-sm text-slate-400">{status.updatedReplicas || 0}</span>
    case 'available':
      return <span className="text-sm text-slate-400">{status.availableReplicas || 0}</span>
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-slate-500">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <span className="text-sm text-slate-400 truncate" title={images.join('\n')}>
          {display}
        </span>
      )
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

function DaemonSetCell({ resource, column }: { resource: any; column: string }) {
  const status = resource.status || {}

  switch (column) {
    case 'desired':
      return <span className="text-sm text-slate-400">{status.desiredNumberScheduled || 0}</span>
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
      return <span className="text-sm text-slate-400">{status.updatedNumberScheduled || 0}</span>
    case 'available':
      return <span className="text-sm text-slate-400">{status.numberAvailable || 0}</span>
    default:
      return <span className="text-sm text-slate-500">-</span>
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
          desired === 0 ? 'text-slate-400' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}/{desired}
        </span>
      )
    }
    case 'owner': {
      const owner = getReplicaSetOwner(resource)
      return <span className="text-sm text-slate-400 truncate">{owner || '-'}</span>
    }
    case 'status': {
      const isActive = isReplicaSetActive(resource)
      return (
        <span className={clsx(
          'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
          isActive ? 'bg-blue-500/20 text-blue-400' : 'bg-slate-500/20 text-slate-400'
        )}>
          {isActive ? 'Active' : 'Old'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
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
    case 'clusterIP':
      return <span className="text-sm text-slate-400 font-mono">{resource.spec?.clusterIP || '-'}</span>
    case 'externalIP': {
      const external = getServiceExternalIP(resource)
      if (!external) return <span className="text-sm text-slate-500">-</span>
      return (
        <div className="flex items-center gap-1">
          <Globe className="w-3.5 h-3.5 text-violet-400" />
          <span className="text-sm text-violet-400 truncate">{external}</span>
        </div>
      )
    }
    case 'ports': {
      const ports = getServicePorts(resource)
      return <span className="text-sm text-slate-400">{ports}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

function IngressCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'class': {
      const ingressClass = getIngressClass(resource)
      return <span className="text-sm text-slate-400">{ingressClass || '-'}</span>
    }
    case 'hosts': {
      const hosts = getIngressHosts(resource)
      return <span className="text-sm text-slate-400 truncate">{hosts}</span>
    }
    case 'tls': {
      const hasTLS = hasIngressTLS(resource)
      return hasTLS ? (
        <span title="TLS Enabled">
          <Shield className="w-4 h-4 text-green-400" />
        </span>
      ) : (
        <span className="text-sm text-slate-500">-</span>
      )
    }
    case 'address': {
      const address = getIngressAddress(resource)
      return <span className="text-sm text-slate-400 truncate">{address || 'Pending'}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

function ConfigMapCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'keys': {
      const { count, preview } = getConfigMapKeys(resource)
      return (
        <div className="flex items-center gap-2">
          <span className="text-sm text-slate-400">{count}</span>
          {count > 0 && (
            <span className="text-xs text-slate-500 truncate" title={preview}>
              ({preview})
            </span>
          )}
        </div>
      )
    }
    case 'size': {
      const size = getConfigMapSize(resource)
      return <span className="text-sm text-slate-400">{size}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
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
      return <span className="text-sm text-slate-400">{count}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
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
          allDone ? 'text-green-400' : succeeded > 0 ? 'text-yellow-400' : 'text-slate-400'
        )}>
          {succeeded}/{total}
        </span>
      )
    }
    case 'duration': {
      const duration = getJobDuration(resource)
      return <span className="text-sm text-slate-400">{duration || '-'}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

function CronJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'schedule': {
      const { cron, readable } = getCronJobSchedule(resource)
      return (
        <div className="flex flex-col">
          <span className="text-sm text-slate-400 font-mono">{cron}</span>
          <span className="text-xs text-slate-500">{readable}</span>
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
      return <span className="text-sm text-slate-400">{lastRun || 'Never'}</span>
    }
    default:
      return <span className="text-sm text-slate-500">-</span>
  }
}

function HPACell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'target': {
      const target = getHPATarget(resource)
      return <span className="text-sm text-slate-400 truncate">{target}</span>
    }
    case 'replicas': {
      const { current, min, max } = getHPAReplicas(resource)
      return (
        <span className="text-sm text-slate-400">
          <span className="text-white font-medium">{current}</span>
          <span className="text-slate-500"> ({min}-{max})</span>
        </span>
      )
    }
    case 'metrics': {
      const { cpu, memory, custom } = getHPAMetrics(resource)
      const parts: string[] = []
      if (cpu !== undefined) parts.push(`CPU: ${cpu}%`)
      if (memory !== undefined) parts.push(`Mem: ${memory}%`)
      if (custom > 0) parts.push(`+${custom} custom`)
      return <span className="text-sm text-slate-400">{parts.join(', ') || '-'}</span>
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
      return <span className="text-sm text-slate-500">-</span>
  }
}
