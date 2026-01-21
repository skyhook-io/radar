import { Link2, ExternalLink, Box, Server, FileJson, Lock, Network, Settings, Puzzle, AlertCircle } from 'lucide-react'
import { clsx } from 'clsx'
import type { HelmOwnedResource } from '../../types'
import { kindToPlural } from './helm-utils'

// Status color mapping
function getStatusColor(status?: string): string {
  if (!status) return 'bg-theme-hover/50 text-theme-text-secondary'

  const statusLower = status.toLowerCase()

  // Green - healthy states
  if (['running', 'active', 'succeeded', 'bound'].includes(statusLower)) {
    return 'bg-green-500/20 text-green-400'
  }

  // Yellow - transitional states
  if (['pending', 'progressing', 'scaled to 0', 'suspended'].includes(statusLower)) {
    return 'bg-yellow-500/20 text-yellow-400'
  }

  // Red - error states
  if (['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted'].includes(statusLower)) {
    return 'bg-red-500/20 text-red-400'
  }

  // Blue - completed/terminated
  if (['completed', 'terminated'].includes(statusLower)) {
    return 'bg-blue-500/20 text-blue-400'
  }

  return 'bg-theme-hover/50 text-theme-text-secondary'
}

interface OwnedResourcesProps {
  resources: HelmOwnedResource[]
  onNavigate?: (kind: string, namespace: string, name: string) => void
}

// Map kind names to icons
const KIND_ICONS: Record<string, typeof Box> = {
  deployment: Box,
  statefulset: Box,
  daemonset: Box,
  service: Server,
  configmap: FileJson,
  secret: Lock,
  ingress: Network,
  serviceaccount: Settings,
}

function getIconForKind(kind: string) {
  return KIND_ICONS[kind.toLowerCase()] || Puzzle
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

// Compute health summary
function computeHealthSummary(resources: HelmOwnedResource[]) {
  let healthy = 0
  let warning = 0
  let error = 0
  let unknown = 0

  for (const r of resources) {
    if (!r.status) {
      unknown++
      continue
    }
    const status = r.status.toLowerCase()
    if (['running', 'active', 'succeeded', 'bound'].includes(status)) {
      healthy++
    } else if (['pending', 'progressing', 'scaled to 0', 'suspended'].includes(status)) {
      warning++
    } else if (['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted'].includes(status)) {
      error++
    } else {
      unknown++
    }
  }

  return { healthy, warning, error, unknown, total: resources.length }
}

export function OwnedResources({ resources, onNavigate }: OwnedResourcesProps) {
  if (!resources || resources.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
        <Link2 className="w-8 h-8 text-theme-text-disabled" />
        <span>No owned resources</span>
      </div>
    )
  }

  const grouped = groupByKind(resources)
  const health = computeHealthSummary(resources)

  return (
    <div className="p-4 space-y-4">
      {/* Health summary */}
      <div className="flex items-center justify-between">
        <div className="text-sm text-theme-text-secondary">
          {resources.length} resource{resources.length !== 1 ? 's' : ''} created by this release
        </div>
        <div className="flex items-center gap-2">
          {health.healthy > 0 && (
            <span className="flex items-center gap-1 px-2 py-0.5 text-xs rounded bg-green-500/20 text-green-400">
              {health.healthy} healthy
            </span>
          )}
          {health.warning > 0 && (
            <span className="flex items-center gap-1 px-2 py-0.5 text-xs rounded bg-yellow-500/20 text-yellow-400">
              {health.warning} pending
            </span>
          )}
          {health.error > 0 && (
            <span className="flex items-center gap-1 px-2 py-0.5 text-xs rounded bg-red-500/20 text-red-400">
              {health.error} failed
            </span>
          )}
        </div>
      </div>

      {Array.from(grouped.entries()).map(([kind, items]) => {
        const Icon = getIconForKind(kind)

        return (
          <div key={kind} className="bg-theme-elevated/30 rounded-lg p-3">
            <div className="flex items-center gap-2 mb-2">
              <Icon className="w-4 h-4 text-theme-text-secondary" />
              <span className="text-sm font-medium text-theme-text-secondary">{kind}</span>
              <span className="text-xs text-theme-text-tertiary">({items.length})</span>
            </div>
            <div className="space-y-1">
              {items.map((resource) => (
                <ResourceItem
                  key={`${resource.namespace}-${resource.name}`}
                  resource={resource}
                  onNavigate={onNavigate}
                />
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}

interface ResourceItemProps {
  resource: HelmOwnedResource
  onNavigate?: (kind: string, namespace: string, name: string) => void
}

function ResourceItem({ resource, onNavigate }: ResourceItemProps) {
  const canNavigate = !!onNavigate

  const handleClick = () => {
    if (onNavigate) {
      onNavigate(kindToPlural(resource.kind), resource.namespace, resource.name)
    }
  }

  const isError = resource.status && ['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted'].includes(resource.status.toLowerCase())

  return (
    <div
      onClick={canNavigate ? handleClick : undefined}
      className={clsx(
        'flex items-center justify-between p-2 rounded text-sm',
        canNavigate
          ? 'cursor-pointer hover:bg-theme-elevated/50 group'
          : 'bg-theme-surface/50'
      )}
    >
      <div className="flex items-center gap-2 min-w-0 flex-1">
        <span className="text-theme-text-primary truncate">{resource.name}</span>
        {resource.namespace && (
          <span className="text-xs text-theme-text-tertiary shrink-0">{resource.namespace}</span>
        )}
      </div>

      <div className="flex items-center gap-2 shrink-0">
        {/* Ready count (e.g., 3/3) */}
        {resource.ready && (
          <span className="text-xs text-theme-text-secondary font-mono">{resource.ready}</span>
        )}

        {/* Status badge */}
        {resource.status && (
          <span
            className={clsx('px-1.5 py-0.5 text-xs rounded', getStatusColor(resource.status))}
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
          <ExternalLink className="w-3.5 h-3.5 text-theme-text-tertiary opacity-0 group-hover:opacity-100 transition-opacity" />
        )}
      </div>
    </div>
  )
}
