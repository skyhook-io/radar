import { useState } from 'react'
import { ChevronDown, ChevronRight, Package, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import type { ManagedResource, GitOpsHealthStatus } from '../../types/gitops'
import { groupManagedResourcesByKind } from '../../types/gitops'

interface ManagedResourcesListProps {
  resources: ManagedResource[]
  /** Callback when a resource is clicked (for navigation) */
  onResourceClick?: (resource: ManagedResource) => void
  /** Maximum resources to show before collapsing */
  maxVisible?: number
  /** Title for the section */
  title?: string
  /** Whether to show health status for each resource */
  showHealth?: boolean
}

/**
 * Displays a list of managed resources grouped by kind
 * Used for showing Flux inventory or ArgoCD managed resources
 */
export function ManagedResourcesList({
  resources,
  onResourceClick,
  maxVisible = 20,
  title = 'Managed Resources',
  showHealth = false,
}: ManagedResourcesListProps) {
  const [expanded, setExpanded] = useState(resources.length <= 10)
  const [showAll, setShowAll] = useState(false)

  if (!resources || resources.length === 0) {
    return null
  }

  const grouped = groupManagedResourcesByKind(resources)
  const sortedGroups = Array.from(grouped.entries()).sort((a, b) => a[0].localeCompare(b[0]))
  const hasMore = resources.length > maxVisible && !showAll

  return (
    <div className="border-b-subtle pb-4 last:border-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full text-left mb-2 hover:text-theme-text-primary transition-colors"
      >
        {expanded ? (
          <ChevronDown className="w-4 h-4 text-theme-text-tertiary" />
        ) : (
          <ChevronRight className="w-4 h-4 text-theme-text-tertiary" />
        )}
        <Package className="w-4 h-4 text-theme-text-secondary" />
        <span className="text-sm font-medium text-theme-text-secondary">
          {title} ({resources.length})
        </span>
      </button>

      {expanded && (
        <div className="pl-6 space-y-3">
          {sortedGroups.map(([kind, kindResources]) => (
            <ResourceKindGroup
              key={kind}
              kind={kind}
              resources={showAll ? kindResources : kindResources.slice(0, Math.ceil(maxVisible / sortedGroups.length))}
              onResourceClick={onResourceClick}
              showHealth={showHealth}
            />
          ))}

          {hasMore && (
            <button
              onClick={() => setShowAll(true)}
              className="text-xs text-blue-400 hover:text-blue-300 transition-colors"
            >
              Show all {resources.length} resources...
            </button>
          )}
        </div>
      )}
    </div>
  )
}

interface ResourceKindGroupProps {
  kind: string
  resources: ManagedResource[]
  onResourceClick?: (resource: ManagedResource) => void
  showHealth?: boolean
}

function ResourceKindGroup({ kind, resources, onResourceClick, showHealth }: ResourceKindGroupProps) {
  const [expanded, setExpanded] = useState(resources.length <= 5)

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors mb-1"
      >
        {expanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
        <span className="font-medium">{kind}</span>
        <span className="text-theme-text-tertiary">({resources.length})</span>
      </button>

      {expanded && (
        <div className="ml-4 space-y-0.5">
          {resources.map((resource, idx) => (
            <ResourceItem
              key={`${resource.namespace}-${resource.name}-${idx}`}
              resource={resource}
              onClick={onResourceClick}
              showHealth={showHealth}
            />
          ))}
        </div>
      )}
    </div>
  )
}

interface ResourceItemProps {
  resource: ManagedResource
  onClick?: (resource: ManagedResource) => void
  showHealth?: boolean
}

function ResourceItem({ resource, onClick, showHealth }: ResourceItemProps) {
  const displayName = resource.namespace
    ? `${resource.namespace}/${resource.name}`
    : resource.name

  const content = (
    <div className="flex items-center gap-2 py-0.5">
      {showHealth && resource.health && (
        <HealthDot health={resource.health} />
      )}
      <span className="text-xs text-theme-text-secondary truncate" title={displayName}>
        {displayName}
      </span>
      {onClick && (
        <ExternalLink className="w-3 h-3 text-theme-text-tertiary opacity-0 group-hover:opacity-100 transition-opacity" />
      )}
    </div>
  )

  if (onClick) {
    return (
      <button
        onClick={() => onClick(resource)}
        className="group w-full text-left hover:bg-theme-elevated/30 rounded px-1 -mx-1 transition-colors"
      >
        {content}
      </button>
    )
  }

  return <div className="px-1 -mx-1">{content}</div>
}

function HealthDot({ health }: { health: GitOpsHealthStatus }) {
  const colorClass = getHealthDotColor(health)
  return (
    <span
      className={clsx('w-1.5 h-1.5 rounded-full shrink-0', colorClass)}
      title={health}
    />
  )
}

function getHealthDotColor(health: GitOpsHealthStatus): string {
  switch (health) {
    case 'Healthy':
      return 'bg-green-400'
    case 'Progressing':
      return 'bg-blue-400'
    case 'Degraded':
      return 'bg-red-400'
    case 'Suspended':
      return 'bg-yellow-400'
    case 'Missing':
      return 'bg-orange-400'
    default:
      return 'bg-gray-400'
  }
}

/**
 * Compact inventory count display for table cells
 */
export function InventoryCount({ count, healthy, unhealthy }: { count: number; healthy?: number; unhealthy?: number }) {
  if (count === 0) {
    return <span className="text-theme-text-tertiary">-</span>
  }

  if (healthy !== undefined && unhealthy !== undefined) {
    return (
      <span className="text-xs">
        <span className="text-green-400">{healthy}</span>
        <span className="text-theme-text-tertiary">/</span>
        <span className={unhealthy > 0 ? 'text-red-400' : 'text-theme-text-secondary'}>{count}</span>
      </span>
    )
  }

  return (
    <span className="text-xs text-theme-text-secondary">
      {count} {count === 1 ? 'resource' : 'resources'}
    </span>
  )
}
