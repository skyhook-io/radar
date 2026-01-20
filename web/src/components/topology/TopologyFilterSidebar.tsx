import { memo, useMemo } from 'react'
import {
  Globe,
  Server,
  Box,
  Layers,
  Settings,
  Key,
  Activity,
  Clock,
  Timer,
  ChevronLeft,
  ChevronRight,
  Eye,
  EyeOff
} from 'lucide-react'
import { clsx } from 'clsx'
import type { NodeKind, TopologyNode } from '../../types'

// Resource kind configuration
const RESOURCE_KINDS: {
  kind: NodeKind
  label: string
  icon: typeof Globe
  color: string
  category: 'workloads' | 'networking' | 'config' | 'scaling'
}[] = [
  // Networking
  { kind: 'Ingress', label: 'Ingress', icon: Globe, color: 'text-purple-400', category: 'networking' },
  { kind: 'Service', label: 'Service', icon: Server, color: 'text-blue-400', category: 'networking' },

  // Workloads
  { kind: 'Deployment', label: 'Deployment', icon: Layers, color: 'text-emerald-400', category: 'workloads' },
  { kind: 'DaemonSet', label: 'DaemonSet', icon: Layers, color: 'text-teal-400', category: 'workloads' },
  { kind: 'StatefulSet', label: 'StatefulSet', icon: Layers, color: 'text-cyan-400', category: 'workloads' },
  { kind: 'ReplicaSet', label: 'ReplicaSet', icon: Layers, color: 'text-green-400', category: 'workloads' },
  { kind: 'Pod', label: 'Pod', icon: Box, color: 'text-lime-400', category: 'workloads' },
  { kind: 'PodGroup', label: 'Pod Group', icon: Box, color: 'text-lime-400', category: 'workloads' },
  { kind: 'Job', label: 'Job', icon: Activity, color: 'text-orange-400', category: 'workloads' },
  { kind: 'CronJob', label: 'CronJob', icon: Timer, color: 'text-orange-300', category: 'workloads' },

  // Config
  { kind: 'ConfigMap', label: 'ConfigMap', icon: Settings, color: 'text-amber-400', category: 'config' },
  { kind: 'Secret', label: 'Secret', icon: Key, color: 'text-red-400', category: 'config' },

  // Scaling
  { kind: 'HPA', label: 'HPA', icon: Clock, color: 'text-pink-400', category: 'scaling' },
]

const CATEGORIES = [
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
      <div className="flex flex-col items-center py-3 px-1 bg-slate-800/90 backdrop-blur border-r border-slate-700">
        <button
          onClick={onToggleCollapse}
          className="p-2 text-slate-400 hover:text-white hover:bg-slate-700 rounded-lg transition-colors"
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
                    ? 'bg-slate-700 text-white'
                    : 'text-slate-500 hover:text-slate-300'
                )}
                title={kind}
              >
                <Icon className={clsx('w-4 h-4', isVisible && color)} />
              </button>
            )
          })}
          {availableKinds.length > 6 && (
            <span className="text-xs text-slate-500 text-center">
              +{availableKinds.length - 6}
            </span>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="w-56 flex flex-col bg-slate-800/90 backdrop-blur border-r border-slate-700 overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-slate-700">
        <span className="text-sm font-medium text-slate-300">Filters</span>
        <button
          onClick={onToggleCollapse}
          className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded transition-colors"
          title="Collapse sidebar"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
      </div>

      {/* Quick actions */}
      <div className="flex gap-1 px-3 py-2 border-b border-slate-700">
        <button
          onClick={onShowAll}
          disabled={allVisible}
          className={clsx(
            'flex-1 flex items-center justify-center gap-1 px-2 py-1.5 text-xs rounded transition-colors',
            allVisible
              ? 'bg-slate-700/50 text-slate-500 cursor-not-allowed'
              : 'bg-slate-700 text-slate-300 hover:bg-slate-600 hover:text-white'
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
              ? 'bg-slate-700/50 text-slate-500 cursor-not-allowed'
              : 'bg-slate-700 text-slate-300 hover:bg-slate-600 hover:text-white'
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
              <div className="text-xs font-medium text-slate-500 uppercase tracking-wider px-1 mb-1">
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
                          ? 'bg-slate-700/70 text-white'
                          : 'text-slate-400 hover:bg-slate-700/40 hover:text-slate-300'
                      )}
                    >
                      <Icon className={clsx('w-4 h-4 flex-shrink-0', isVisible ? color : 'text-slate-500')} />
                      <span className="flex-1 text-sm truncate">{label}</span>
                      <span className={clsx(
                        'text-xs px-1.5 py-0.5 rounded',
                        isVisible ? 'bg-slate-600 text-slate-300' : 'bg-slate-700/50 text-slate-500'
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
      <div className="px-3 py-2 border-t border-slate-700 bg-slate-800/50">
        <div className="text-xs text-slate-500">
          Showing {availableKinds.filter(k => visibleKinds.has(k.kind)).reduce((sum, k) => sum + (kindCounts.get(k.kind) || 0), 0)} of {nodes.length} resources
        </div>
      </div>
    </div>
  )
})
