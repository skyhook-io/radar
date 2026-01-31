import { memo, useMemo } from 'react'
import {
  ChevronLeft,
  ChevronRight,
  Eye,
  EyeOff
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { clsx } from 'clsx'
import type { NodeKind, TopologyNode } from '../../types'
import { getTopologyIcon } from '../../utils/resource-icons'

// Resource kind configuration
const RESOURCE_KINDS: {
  kind: NodeKind
  label: string
  icon: LucideIcon
  color: string
  category: 'gitops' | 'workloads' | 'networking' | 'config' | 'scaling'
}[] = [
  // GitOps (ArgoCD + FluxCD)
  { kind: 'Application', label: 'Application', icon: getTopologyIcon('Application'), color: 'text-orange-400', category: 'gitops' },
  { kind: 'Kustomization', label: 'Kustomization', icon: getTopologyIcon('Kustomization'), color: 'text-sky-400', category: 'gitops' },
  { kind: 'HelmRelease', label: 'HelmRelease', icon: getTopologyIcon('HelmRelease'), color: 'text-sky-400', category: 'gitops' },
  { kind: 'GitRepository', label: 'GitRepository', icon: getTopologyIcon('GitRepository'), color: 'text-teal-400', category: 'gitops' },

  // Networking
  { kind: 'Ingress', label: 'Ingress', icon: getTopologyIcon('Ingress'), color: 'text-purple-400', category: 'networking' },
  { kind: 'Service', label: 'Service', icon: getTopologyIcon('Service'), color: 'text-blue-400', category: 'networking' },

  // Workloads
  { kind: 'Deployment', label: 'Deployment', icon: getTopologyIcon('Deployment'), color: 'text-emerald-400', category: 'workloads' },
  { kind: 'Rollout', label: 'Rollout', icon: getTopologyIcon('Rollout'), color: 'text-emerald-400', category: 'workloads' },
  { kind: 'DaemonSet', label: 'DaemonSet', icon: getTopologyIcon('DaemonSet'), color: 'text-teal-400', category: 'workloads' },
  { kind: 'StatefulSet', label: 'StatefulSet', icon: getTopologyIcon('StatefulSet'), color: 'text-cyan-400', category: 'workloads' },
  { kind: 'ReplicaSet', label: 'ReplicaSet', icon: getTopologyIcon('ReplicaSet'), color: 'text-green-400', category: 'workloads' },
  { kind: 'Pod', label: 'Pod', icon: getTopologyIcon('Pod'), color: 'text-lime-400', category: 'workloads' },
  { kind: 'PodGroup', label: 'Pod Group', icon: getTopologyIcon('PodGroup'), color: 'text-lime-400', category: 'workloads' },
  { kind: 'Job', label: 'Job', icon: getTopologyIcon('Job'), color: 'text-orange-400', category: 'workloads' },
  { kind: 'CronJob', label: 'CronJob', icon: getTopologyIcon('CronJob'), color: 'text-orange-300', category: 'workloads' },

  // Config
  { kind: 'ConfigMap', label: 'ConfigMap', icon: getTopologyIcon('ConfigMap'), color: 'text-amber-400', category: 'config' },
  { kind: 'Secret', label: 'Secret', icon: getTopologyIcon('Secret'), color: 'text-red-400', category: 'config' },

  // Scaling
  { kind: 'HPA', label: 'HPA', icon: getTopologyIcon('HPA'), color: 'text-pink-400', category: 'scaling' },
]

const CATEGORIES = [
  { id: 'gitops', label: 'GitOps' },
  { id: 'networking', label: 'Networking' },
  { id: 'workloads', label: 'Workloads' },
  { id: 'config', label: 'Configuration' },
  { id: 'scaling', label: 'Scaling' },
] as const

interface TopologyFilterSidebarProps {
  nodes: TopologyNode[]
  visibleKinds: Set<NodeKind>
  onToggleKind: (kind: NodeKind) => void
  onShowAll: () => void
  onHideAll: () => void
  collapsed?: boolean
  onToggleCollapse?: () => void
}

export const TopologyFilterSidebar = memo(function TopologyFilterSidebar({
  nodes,
  visibleKinds,
  onToggleKind,
  onShowAll,
  onHideAll,
  collapsed = false,
  onToggleCollapse,
}: TopologyFilterSidebarProps) {
  // Count nodes by kind
  const kindCounts = useMemo(() => {
    const counts = new Map<NodeKind, number>()
    for (const node of nodes) {
      counts.set(node.kind, (counts.get(node.kind) || 0) + 1)
    }
    return counts
  }, [nodes])

  // Filter to only show kinds that exist in the topology
  const availableKinds = useMemo(() => {
    return RESOURCE_KINDS.filter(k => kindCounts.has(k.kind))
  }, [kindCounts])

  // Group by category
  const kindsByCategory = useMemo(() => {
    const grouped = new Map<string, typeof availableKinds>()
    for (const category of CATEGORIES) {
      const kinds = availableKinds.filter(k => k.category === category.id)
      if (kinds.length > 0) {
        grouped.set(category.id, kinds)
      }
    }
    return grouped
  }, [availableKinds])

  const allVisible = availableKinds.every(k => visibleKinds.has(k.kind))
  const noneVisible = availableKinds.every(k => !visibleKinds.has(k.kind))

  if (collapsed) {
    return (
      <div className="flex flex-col items-center py-3 px-1 bg-theme-surface/90 backdrop-blur border-r border-theme-border">
        <button
          onClick={onToggleCollapse}
          className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
          title="Expand filters"
        >
          <ChevronRight className="w-4 h-4" />
        </button>
        <div className="mt-3 flex flex-col gap-2">
          {availableKinds.slice(0, 6).map(({ kind, icon: Icon, color }) => {
            const isVisible = visibleKinds.has(kind)
            return (
              <button
                key={kind}
                onClick={() => onToggleKind(kind)}
                className={clsx(
                  'p-1.5 rounded transition-colors',
                  isVisible
                    ? 'bg-theme-elevated text-theme-text-primary'
                    : 'text-theme-text-tertiary hover:text-theme-text-secondary'
                )}
                title={kind}
              >
                <Icon className={clsx('w-4 h-4', isVisible && color)} />
              </button>
            )
          })}
          {availableKinds.length > 6 && (
            <span className="text-xs text-theme-text-tertiary text-center">
              +{availableKinds.length - 6}
            </span>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="w-56 flex flex-col bg-theme-surface/90 backdrop-blur border-r border-theme-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-theme-border">
        <span className="text-sm font-medium text-theme-text-secondary">Filters</span>
        <button
          onClick={onToggleCollapse}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
          title="Collapse sidebar"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
      </div>

      {/* Quick actions */}
      <div className="flex gap-1 px-3 py-2 border-b border-theme-border">
        <button
          onClick={onShowAll}
          disabled={allVisible}
          className={clsx(
            'flex-1 flex items-center justify-center gap-1 px-2 py-1.5 text-xs rounded transition-colors',
            allVisible
              ? 'bg-theme-elevated/50 text-theme-text-tertiary cursor-not-allowed'
              : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
          )}
        >
          <Eye className="w-3 h-3" />
          Show All
        </button>
        <button
          onClick={onHideAll}
          disabled={noneVisible}
          className={clsx(
            'flex-1 flex items-center justify-center gap-1 px-2 py-1.5 text-xs rounded transition-colors',
            noneVisible
              ? 'bg-theme-elevated/50 text-theme-text-tertiary cursor-not-allowed'
              : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
          )}
        >
          <EyeOff className="w-3 h-3" />
          Hide All
        </button>
      </div>

      {/* Kind toggles by category */}
      <div className="flex-1 overflow-y-auto">
        {CATEGORIES.map(category => {
          const kinds = kindsByCategory.get(category.id)
          if (!kinds || kinds.length === 0) return null

          return (
            <div key={category.id} className="px-2 py-2">
              <div className="text-xs font-medium text-theme-text-tertiary uppercase tracking-wider px-1 mb-1">
                {category.label}
              </div>
              <div className="space-y-0.5">
                {kinds.map(({ kind, label, icon: Icon, color }) => {
                  const count = kindCounts.get(kind) || 0
                  const isVisible = visibleKinds.has(kind)

                  return (
                    <button
                      key={kind}
                      onClick={() => onToggleKind(kind)}
                      className={clsx(
                        'w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors',
                        isVisible
                          ? 'bg-theme-elevated/70 text-theme-text-primary'
                          : 'text-theme-text-secondary hover:bg-theme-elevated/40 hover:text-theme-text-secondary'
                      )}
                    >
                      <Icon className={clsx('w-4 h-4 shrink-0', isVisible ? color : 'text-theme-text-tertiary')} />
                      <span className="flex-1 text-sm truncate">{label}</span>
                      <span className={clsx(
                        'text-xs px-1.5 py-0.5 rounded',
                        isVisible ? 'bg-theme-hover text-theme-text-secondary' : 'bg-theme-elevated/50 text-theme-text-tertiary'
                      )}>
                        {count}
                      </span>
                    </button>
                  )
                })}
              </div>
            </div>
          )
        })}
      </div>

      {/* Footer stats */}
      <div className="px-3 py-2 border-t border-theme-border bg-theme-surface/50">
        <div className="text-xs text-theme-text-tertiary">
          Showing {availableKinds.filter(k => visibleKinds.has(k.kind)).reduce((sum, k) => sum + (kindCounts.get(k.kind) || 0), 0)} of {nodes.length} resources
        </div>
      </div>
    </div>
  )
})
