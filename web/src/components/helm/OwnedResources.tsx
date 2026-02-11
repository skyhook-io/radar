import { useState, useCallback } from 'react'
import { Link2, ExternalLink, AlertCircle, Terminal, FileText, Plug, X, Loader2 } from 'lucide-react'
import { getResourceIcon } from '../../utils/resource-icons'
import { clsx } from 'clsx'
import type { HelmOwnedResource } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { kindToPlural } from '../../utils/navigation'
import { getResourceStatusColor, SEVERITY_BADGE } from '../../utils/badge-colors'
import { useQueryClient } from '@tanstack/react-query'
import { useOpenTerminal, useOpenLogs } from '../dock'
import { useStartPortForward } from '../portforward/PortForwardManager'
import { useAvailablePorts } from '../../api/client'
import { useCanExec, useCanViewLogs, useCanPortForward } from '../../contexts/CapabilitiesContext'

interface OwnedResourcesProps {
  resources: HelmOwnedResource[]
  onNavigate?: NavigateToResource
}

function getIconForKind(kind: string) {
  return getResourceIcon(kind)
}

// Group resources by kind
function groupByKind(resources: HelmOwnedResource[]): Map<string, HelmOwnedResource[]> {
  const groups = new Map<string, HelmOwnedResource[]>()
  for (const resource of resources) {
    const existing = groups.get(resource.kind) || []
    existing.push(resource)
    groups.set(resource.kind, existing)
  }
  return groups
}

// Health status types
type HealthFilter = 'all' | 'healthy' | 'warning' | 'error'

// Determine health status of a resource
function getResourceHealth(status?: string): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (!status) return 'unknown'
  const s = status.toLowerCase()
  if (['running', 'active', 'succeeded', 'bound', 'available'].includes(s)) return 'healthy'
  if (['pending', 'progressing', 'scaled to 0', 'suspended', 'creating'].includes(s)) return 'warning'
  if (['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted', 'terminating'].includes(s)) return 'error'
  return 'unknown'
}

// Compute health summary
function computeHealthSummary(resources: HelmOwnedResource[]) {
  let healthy = 0
  let warning = 0
  let error = 0
  let unknown = 0

  for (const r of resources) {
    const health = getResourceHealth(r.status)
    if (health === 'healthy') healthy++
    else if (health === 'warning') warning++
    else if (health === 'error') error++
    else unknown++
  }

  return { healthy, warning, error, unknown, total: resources.length }
}

export function OwnedResources({ resources, onNavigate }: OwnedResourcesProps) {
  const [healthFilter, setHealthFilter] = useState<HealthFilter>('all')

  if (!resources || resources.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
        <Link2 className="w-8 h-8 text-theme-text-disabled" />
        <span>No owned resources</span>
      </div>
    )
  }

  const health = computeHealthSummary(resources)

  // Filter resources by health status
  const filteredResources = healthFilter === 'all'
    ? resources
    : resources.filter(r => getResourceHealth(r.status) === healthFilter)

  const grouped = groupByKind(filteredResources)

  const handleFilterClick = (filter: HealthFilter) => {
    setHealthFilter(prev => prev === filter ? 'all' : filter)
  }

  return (
    <div className="p-4 space-y-4">
      {/* Health summary - clickable badges */}
      <div className="flex items-center justify-between">
        <div className="text-sm text-theme-text-secondary">
          {healthFilter === 'all' ? (
            <>{resources.length} resource{resources.length !== 1 ? 's' : ''} created by this release</>
          ) : (
            <span className="flex items-center gap-2">
              Showing {filteredResources.length} of {resources.length} resources
              <button
                onClick={() => setHealthFilter('all')}
                className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                title="Clear filter"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {health.healthy > 0 && (
            <button
              onClick={() => handleFilterClick('healthy')}
              className={clsx(
                'flex items-center gap-1 px-2 py-0.5 text-xs rounded transition-all',
                SEVERITY_BADGE.success,
                healthFilter === 'healthy' && 'ring-2 ring-green-400/50 ring-offset-1 ring-offset-theme-surface'
              )}
            >
              {health.healthy} healthy
            </button>
          )}
          {health.warning > 0 && (
            <button
              onClick={() => handleFilterClick('warning')}
              className={clsx(
                'flex items-center gap-1 px-2 py-0.5 text-xs rounded transition-all',
                SEVERITY_BADGE.warning,
                healthFilter === 'warning' && 'ring-2 ring-amber-400/50 ring-offset-1 ring-offset-theme-surface'
              )}
            >
              {health.warning} pending
            </button>
          )}
          {health.error > 0 && (
            <button
              onClick={() => handleFilterClick('error')}
              className={clsx(
                'flex items-center gap-1 px-2 py-0.5 text-xs rounded transition-all',
                SEVERITY_BADGE.error,
                healthFilter === 'error' && 'ring-2 ring-red-400/50 ring-offset-1 ring-offset-theme-surface'
              )}
            >
              {health.error} failed
            </button>
          )}
        </div>
      </div>

      {filteredResources.length === 0 ? (
        <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
          <AlertCircle className="w-8 h-8 text-theme-text-disabled" />
          <span>No resources match the selected filter</span>
          <button
            onClick={() => setHealthFilter('all')}
            className="text-xs text-blue-400 hover:text-blue-300"
          >
            Clear filter
          </button>
        </div>
      ) : (
        Array.from(grouped.entries()).map(([kind, items]) => {
          const Icon = getIconForKind(kind)

          return (
            <div key={kind} className="bg-theme-elevated/30 rounded-lg p-3">
              <div className="flex items-center gap-2 mb-2">
                <Icon className="w-4 h-4 text-theme-text-secondary" />
                <span className="text-sm font-medium text-theme-text-secondary">{kind}</span>
                <span className="text-xs text-theme-text-tertiary">({items.length})</span>
              </div>
              <div className="space-y-1">
                {items.map((resource, idx) => (
                  <ResourceItem
                    key={`${resource.namespace}-${resource.name}-${idx}`}
                    resource={resource}
                    onNavigate={onNavigate}
                  />
                ))}
              </div>
            </div>
          )
        })
      )}
    </div>
  )
}

interface ResourceItemProps {
  resource: HelmOwnedResource
  onNavigate?: NavigateToResource
}

function ResourceItem({ resource, onNavigate }: ResourceItemProps) {
  const canNavigate = !!onNavigate
  const isPod = resource.kind.toLowerCase() === 'pod'
  const isService = resource.kind.toLowerCase() === 'service'
  const isRunning = resource.status?.toLowerCase() === 'running'

  const handleClick = () => {
    if (onNavigate) {
      onNavigate({ kind: kindToPlural(resource.kind), namespace: resource.namespace, name: resource.name })
    }
  }

  const isError = resource.status && ['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted'].includes(resource.status.toLowerCase())

  return (
    <div
      className={clsx(
        'flex items-center justify-between p-2 rounded text-sm group',
        canNavigate
          ? 'cursor-pointer hover:bg-theme-elevated/50'
          : 'bg-theme-surface/50'
      )}
    >
      <div
        onClick={canNavigate ? handleClick : undefined}
        className="flex items-center gap-2 min-w-0 flex-1"
      >
        <span className="text-theme-text-primary truncate">{resource.name}</span>
        {resource.namespace && (
          <span className="text-xs text-theme-text-tertiary shrink-0">{resource.namespace}</span>
        )}
      </div>

      <div className="flex items-center gap-2 shrink-0">
        {/* Quick actions for pods */}
        {isPod && (
          <PodQuickActions
            namespace={resource.namespace}
            podName={resource.name}
            isRunning={isRunning}
          />
        )}

        {/* Quick actions for services */}
        {isService && (
          <ServiceQuickActions
            namespace={resource.namespace}
            serviceName={resource.name}
          />
        )}

        {/* Ready count (e.g., 3/3) */}
        {resource.ready && (
          <span className="text-xs text-theme-text-secondary font-mono">{resource.ready}</span>
        )}

        {/* Status badge */}
        {resource.status && (
          <span
            className={clsx('px-1.5 py-0.5 text-xs rounded', getResourceStatusColor(resource.status || ''))}
            title={resource.message || resource.status}
          >
            {resource.status}
          </span>
        )}

        {/* Error icon with message tooltip */}
        {isError && resource.message && (
          <span title={resource.message}>
            <AlertCircle className="w-3.5 h-3.5 text-red-400" />
          </span>
        )}

        {canNavigate && (
          <button
            onClick={handleClick}
            className="p-1 text-theme-text-tertiary opacity-0 group-hover:opacity-100 hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-all"
            title="View details"
          >
            <ExternalLink className="w-3.5 h-3.5" />
          </button>
        )}
      </div>
    </div>
  )
}

// Quick actions for pods
interface PodQuickActionsProps {
  namespace: string
  podName: string
  isRunning: boolean
}

function PodQuickActions({ namespace, podName, isRunning }: PodQuickActionsProps) {
  const queryClient = useQueryClient()
  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()
  const startPortForward = useStartPortForward()
  const { data: portsData, isLoading: portsLoading } = useAvailablePorts('pod', namespace, podName)

  // Check capabilities
  const canExec = useCanExec()
  const canViewLogs = useCanViewLogs()
  const canPortForward = useCanPortForward()

  const [isLoadingAction, setIsLoadingAction] = useState(false)

  // Fetch pod data using React Query cache - shared with resource views
  const fetchPodData = useCallback(async () => {
    return queryClient.fetchQuery({
      queryKey: ['resource', 'pods', namespace, podName],
      queryFn: async () => {
        const response = await fetch(`/api/resources/pods/${namespace}/${podName}`)
        if (!response.ok) throw new Error('Failed to fetch pod')
        return response.json()
      },
      staleTime: 30000,
    })
  }, [queryClient, namespace, podName])

  const handleOpenTerminal = useCallback(async () => {
    if (!isRunning) return
    setIsLoadingAction(true)
    try {
      const data = await fetchPodData()
      const containers = data.resource?.spec?.containers || []
      if (containers.length > 0) {
        openTerminal({
          namespace,
          podName,
          containerName: containers[0].name,
          containers: containers.map((c: { name: string }) => c.name),
        })
      }
    } catch (error) {
      console.error('Failed to open terminal:', error)
    } finally {
      setIsLoadingAction(false)
    }
  }, [namespace, podName, isRunning, openTerminal, fetchPodData])

  const handleOpenLogs = useCallback(async () => {
    setIsLoadingAction(true)
    try {
      const data = await fetchPodData()
      const containers = data.resource?.spec?.containers || []
      openLogs({
        namespace,
        podName,
        containers: containers.map((c: { name: string }) => c.name),
      })
    } catch (error) {
      console.error('Failed to open logs:', error)
    } finally {
      setIsLoadingAction(false)
    }
  }, [namespace, podName, openLogs, fetchPodData])

  const handlePortForward = useCallback((port: number) => {
    startPortForward.mutate({
      namespace,
      podName,
      podPort: port,
    })
  }, [namespace, podName, startPortForward])

  const ports = portsData?.ports || []

  return (
    <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
      {isLoadingAction && <Loader2 className="w-3.5 h-3.5 animate-spin text-theme-text-tertiary" />}

      {/* Terminal */}
      {isRunning && canExec && (
        <button
          onClick={(e) => { e.stopPropagation(); handleOpenTerminal() }}
          disabled={isLoadingAction}
          className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-blue-500/10 rounded transition-colors disabled:opacity-50"
          title="Open terminal"
        >
          <Terminal className="w-3.5 h-3.5" />
        </button>
      )}

      {/* Logs */}
      {canViewLogs && (
        <button
          onClick={(e) => { e.stopPropagation(); handleOpenLogs() }}
          disabled={isLoadingAction}
          className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-blue-500/10 rounded transition-colors disabled:opacity-50"
          title="View logs"
        >
          <FileText className="w-3.5 h-3.5" />
        </button>
      )}

      {/* Port Forward */}
      {canPortForward && !portsLoading && ports.length > 0 && (
        <button
          onClick={(e) => { e.stopPropagation(); handlePortForward(ports[0].port) }}
          disabled={startPortForward.isPending}
          className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-blue-500/10 rounded transition-colors disabled:opacity-50"
          title={`Port forward :${ports[0].port}`}
        >
          {startPortForward.isPending ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
          ) : (
            <Plug className="w-3.5 h-3.5" />
          )}
        </button>
      )}
    </div>
  )
}

// Quick actions for services
interface ServiceQuickActionsProps {
  namespace: string
  serviceName: string
}

function ServiceQuickActions({ namespace, serviceName }: ServiceQuickActionsProps) {
  const startPortForward = useStartPortForward()
  const { data: portsData, isLoading: portsLoading } = useAvailablePorts('service', namespace, serviceName)
  const canPortForward = useCanPortForward()

  const handlePortForward = useCallback((port: number) => {
    startPortForward.mutate({
      namespace,
      serviceName,
      podPort: port,
    })
  }, [namespace, serviceName, startPortForward])

  const ports = portsData?.ports || []

  if (!canPortForward || portsLoading || ports.length === 0) return null

  return (
    <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
      <button
        onClick={(e) => { e.stopPropagation(); handlePortForward(ports[0].port) }}
        disabled={startPortForward.isPending}
        className="p-1 text-theme-text-tertiary hover:text-blue-400 hover:bg-blue-500/10 rounded transition-colors disabled:opacity-50"
        title={`Port forward :${ports[0].port}`}
      >
        {startPortForward.isPending ? (
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
        ) : (
          <Plug className="w-3.5 h-3.5" />
        )}
      </button>
    </div>
  )
}
