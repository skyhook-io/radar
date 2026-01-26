import { memo, useState, useRef } from 'react'
import { createPortal } from 'react-dom'
import {
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  Eye,
  Layers,
  Globe,
  Cpu,
  Network,
  Clock,
  Filter,
  Info,
  Puzzle,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { AddonMode } from './TrafficView'

// Fast tooltip component using portal to escape overflow
function Tooltip({ children, content }: { children: React.ReactNode; content: string }) {
  const [show, setShow] = useState(false)
  const [pos, setPos] = useState({ x: 0, y: 0 })
  const ref = useRef<HTMLDivElement>(null)

  const handleMouseEnter = () => {
    if (ref.current) {
      const rect = ref.current.getBoundingClientRect()
      setPos({ x: rect.right + 8, y: rect.top + rect.height / 2 })
    }
    setShow(true)
  }

  return (
    <div
      ref={ref}
      className="inline-flex"
      onMouseEnter={handleMouseEnter}
      onMouseLeave={() => setShow(false)}
    >
      {children}
      {show && createPortal(
        <div
          className="fixed z-[9999] pointer-events-none"
          style={{ left: pos.x, top: pos.y, transform: 'translateY(-50%)' }}
        >
          <div className="bg-gray-900 text-white text-[10px] px-2 py-1.5 rounded shadow-lg max-w-[180px] leading-tight whitespace-normal">
            {content}
          </div>
        </div>,
        document.body
      )}
    </div>
  )
}

// Namespace color palette (must match TrafficGraph.tsx)
// Using maximally distinct colors spread across the hue spectrum
const NAMESPACE_PALETTE = [
  '#dc2626', // red-600
  '#2563eb', // blue-600
  '#16a34a', // green-600
  '#9333ea', // purple-600
  '#ea580c', // orange-600
  '#0891b2', // cyan-600
  '#c026d3', // fuchsia-600
  '#65a30d', // lime-600
  '#0d9488', // teal-600
  '#e11d48', // rose-600
  '#7c3aed', // violet-600
  '#ca8a04', // yellow-600
  '#4f46e5', // indigo-600
  '#db2777', // pink-600
  '#059669', // emerald-600
  '#d97706', // amber-600
]

const NAMESPACE_NAMED_COLORS: Record<string, string> = {
  production: '#991b1b', prod: '#991b1b',
  staging: '#854d0e', stg: '#854d0e',
  dev: '#1e40af', development: '#1e40af',
  default: '#374151',
}

// Track assigned colors to avoid repetition
const assignedColors = new Map<string, string>()
let colorIndex = 0

function getNamespaceColor(namespace: string): string {
  const lower = namespace.toLowerCase()
  if (NAMESPACE_NAMED_COLORS[lower]) return NAMESPACE_NAMED_COLORS[lower]

  // Check if already assigned
  if (assignedColors.has(namespace)) {
    return assignedColors.get(namespace)!
  }

  // Assign next color in sequence (avoids hash collisions)
  const color = NAMESPACE_PALETTE[colorIndex % NAMESPACE_PALETTE.length]
  assignedColors.set(namespace, color)
  colorIndex++
  return color
}

// Connection threshold options
const CONNECTION_THRESHOLDS = [
  { value: 0, label: 'All traffic' },
  { value: 100, label: '100+ connections' },
  { value: 1000, label: '1K+ connections' },
  { value: 10000, label: '10K+ connections' },
  { value: 100000, label: '100K+ connections' },
]

// Time range options
const TIME_RANGES = [
  { value: '1m', label: '1 minute' },
  { value: '5m', label: '5 minutes' },
  { value: '15m', label: '15 minutes' },
  { value: '1h', label: '1 hour' },
]

interface TrafficFilterSidebarProps {
  // Filter state
  hideSystem: boolean
  setHideSystem: (v: boolean) => void
  hideExternal: boolean
  setHideExternal: (v: boolean) => void
  minConnections: number
  setMinConnections: (v: number) => void

  // Display options
  showNamespaceGroups: boolean
  setShowNamespaceGroups: (v: boolean) => void
  collapseInternet: boolean
  setCollapseInternet: (v: boolean) => void
  addonMode: AddonMode
  setAddonMode: (v: AddonMode) => void

  // Detection options
  aggregateExternal: boolean
  setAggregateExternal: (v: boolean) => void
  detectServices: boolean
  setDetectServices: (v: boolean) => void

  // Time
  timeRange: string
  setTimeRange: (v: string) => void

  // Namespace filtering
  namespaces: Array<{ name: string; nodeCount: number }>
  hiddenNamespaces: Set<string>
  onToggleNamespace: (ns: string) => void

  // Collapse state
  collapsed?: boolean
  onToggleCollapse?: () => void
}

// Compact toggle component with tooltip
function ToggleOption({
  label,
  description,
  enabled,
  onToggle,
  icon: Icon,
}: {
  label: string
  description: string
  enabled: boolean
  onToggle: () => void
  icon: typeof Eye
}) {
  return (
    <div className={clsx(
      'flex items-center gap-2 px-2 py-1.5 rounded transition-colors',
      enabled ? 'bg-blue-500/10' : 'hover:bg-theme-elevated'
    )}>
      <button
        onClick={onToggle}
        className="flex-1 flex items-center gap-2 text-left"
      >
        <Icon className={clsx(
          'w-3.5 h-3.5 flex-shrink-0',
          enabled ? 'text-blue-400' : 'text-theme-text-tertiary'
        )} />
        <span className={clsx(
          'flex-1 text-xs',
          enabled ? 'text-blue-400' : 'text-theme-text-primary'
        )}>
          {label}
        </span>
      </button>
      <Tooltip content={description}>
        <Info className="w-3 h-3 text-theme-text-tertiary hover:text-theme-text-secondary cursor-help" />
      </Tooltip>
      <button
        onClick={onToggle}
        className={clsx(
          'w-7 h-4 rounded-full transition-colors relative flex-shrink-0',
          enabled ? 'bg-blue-500' : 'bg-theme-elevated'
        )}
      >
        <div className={clsx(
          'absolute top-0.5 w-3 h-3 rounded-full bg-white transition-transform',
          enabled ? 'translate-x-3.5' : 'translate-x-0.5'
        )} />
      </button>
    </div>
  )
}

export const TrafficFilterSidebar = memo(function TrafficFilterSidebar({
  hideSystem,
  setHideSystem,
  hideExternal,
  setHideExternal,
  minConnections,
  setMinConnections,
  showNamespaceGroups,
  setShowNamespaceGroups,
  collapseInternet,
  setCollapseInternet,
  addonMode,
  setAddonMode,
  aggregateExternal,
  setAggregateExternal,
  detectServices,
  setDetectServices,
  timeRange,
  setTimeRange,
  namespaces,
  hiddenNamespaces,
  onToggleNamespace,
  collapsed = false,
  onToggleCollapse,
}: TrafficFilterSidebarProps) {
  const [namespacesExpanded, setNamespacesExpanded] = useState(false)

  // Sort namespaces by node count (descending)
  const sortedNamespaces = [...namespaces].sort((a, b) => b.nodeCount - a.nodeCount)
  const visibleNamespaces = namespacesExpanded ? sortedNamespaces : sortedNamespaces.slice(0, 8)
  const hasMore = sortedNamespaces.length > 8

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
          <button
            onClick={() => setHideSystem(!hideSystem)}
            className={clsx(
              'p-1.5 rounded transition-colors',
              hideSystem ? 'bg-blue-500/20 text-blue-400' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
            )}
            title="Hide system traffic"
          >
            <Cpu className="w-4 h-4" />
          </button>
          <button
            onClick={() => setHideExternal(!hideExternal)}
            className={clsx(
              'p-1.5 rounded transition-colors',
              hideExternal ? 'bg-blue-500/20 text-blue-400' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
            )}
            title="Hide external traffic"
          >
            <Globe className="w-4 h-4" />
          </button>
          <button
            onClick={() => setShowNamespaceGroups(!showNamespaceGroups)}
            className={clsx(
              'p-1.5 rounded transition-colors',
              showNamespaceGroups ? 'bg-blue-500/20 text-blue-400' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
            )}
            title="Show namespace groups"
          >
            <Layers className="w-4 h-4" />
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="w-72 flex flex-col bg-theme-surface/90 backdrop-blur border-r border-theme-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-theme-border">
        <span className="text-sm font-medium text-theme-text-secondary">Traffic Filters</span>
        <button
          onClick={onToggleCollapse}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
          title="Collapse sidebar"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
      </div>

      {/* Scrollable content */}
      <div className="flex-1 overflow-y-auto">
        {/* Time Range & Threshold */}
        <div className="px-3 py-2 border-b border-theme-border space-y-1.5">
          <div className="flex items-center gap-2">
            <Clock className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <select
              value={timeRange}
              onChange={(e) => setTimeRange(e.target.value)}
              title="Show traffic from the selected time window"
              className="flex-1 bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 border border-theme-border focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {TIME_RANGES.map(({ value, label }) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
          </div>
          <div className="flex items-center gap-2">
            <Filter className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <select
              value={minConnections}
              onChange={(e) => setMinConnections(Number(e.target.value))}
              title="Hide low-traffic flows to reduce noise"
              className="flex-1 bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 border border-theme-border focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {CONNECTION_THRESHOLDS.map(({ value, label }) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
          </div>
        </div>

        {/* Filtering */}
        <div className="px-3 py-2 border-b border-theme-border">
          <div className="flex items-center gap-2 mb-1.5">
            <Filter className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <span className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider">Filtering</span>
          </div>
          <div className="space-y-0.5">
            <ToggleOption
              label="Hide System"
              description="Filter out infrastructure traffic (kube-system, monitoring, etc.)"
              enabled={hideSystem}
              onToggle={() => setHideSystem(!hideSystem)}
              icon={Cpu}
            />
            <ToggleOption
              label="Hide External"
              description="Hide traffic to/from external services"
              enabled={hideExternal}
              onToggle={() => setHideExternal(!hideExternal)}
              icon={Globe}
            />
          </div>

          {/* Cluster Addons 3-way toggle */}
          <div className="mt-2 pt-2 border-t border-theme-border/50">
            <div className="flex items-center gap-2 mb-1.5">
              <Puzzle className="w-3.5 h-3.5 text-theme-text-tertiary" />
              <span className="text-xs text-theme-text-primary">Cluster Addons</span>
              <Tooltip content="Monitoring, logging, cert-manager, etc. Excludes ingress controllers and service mesh.">
                <Info className="w-3 h-3 text-theme-text-tertiary hover:text-theme-text-secondary cursor-help" />
              </Tooltip>
            </div>
            <div className="flex rounded-md overflow-hidden border border-theme-border">
              {(['show', 'group', 'hide'] as const).map((mode) => (
                <button
                  key={mode}
                  onClick={() => setAddonMode(mode)}
                  className={clsx(
                    'flex-1 px-2 py-1.5 text-[10px] font-medium transition-colors capitalize',
                    addonMode === mode
                      ? 'bg-blue-500 text-white'
                      : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
                  )}
                >
                  {mode}
                </button>
              ))}
            </div>
          </div>
        </div>

        {/* Display */}
        <div className="px-3 py-2 border-b border-theme-border">
          <div className="flex items-center gap-2 mb-1.5">
            <Eye className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <span className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider">Display</span>
          </div>
          <div className="space-y-0.5">
            <ToggleOption
              label="Namespace Colors"
              description="Color nodes by their namespace"
              enabled={showNamespaceGroups}
              onToggle={() => setShowNamespaceGroups(!showNamespaceGroups)}
              icon={Layers}
            />
            <ToggleOption
              label="Collapse Internet"
              description="Group inbound external IPs into single 'Internet' node"
              enabled={collapseInternet}
              onToggle={() => setCollapseInternet(!collapseInternet)}
              icon={Globe}
            />
          </div>
        </div>

        {/* Service Detection */}
        <div className="px-3 py-2 border-b border-theme-border">
          <div className="flex items-center gap-2 mb-1.5">
            <Network className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <span className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider">Detection</span>
          </div>
          <div className="space-y-0.5">
            <ToggleOption
              label="Aggregate External"
              description="Group traffic to same external service (e.g., multiple MongoDB hosts)"
              enabled={aggregateExternal}
              onToggle={() => setAggregateExternal(!aggregateExternal)}
              icon={Layers}
            />
            <ToggleOption
              label="Identify by Port"
              description="Label well-known ports (27017→MongoDB, 6379→Redis). Heuristic-based."
              enabled={detectServices}
              onToggle={() => setDetectServices(!detectServices)}
              icon={Cpu}
            />
          </div>
        </div>

        {/* Namespaces */}
        {sortedNamespaces.length > 0 && (
          <div className="px-3 py-2">
            <div className="flex items-center justify-between mb-1.5">
              <div className="flex items-center gap-2">
                <Layers className="w-3.5 h-3.5 text-theme-text-tertiary" />
                <span className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider">Namespaces</span>
              </div>
              <div className="flex items-center gap-2 text-[10px]">
                <button
                  onClick={() => {
                    hiddenNamespaces.forEach(ns => onToggleNamespace(ns))
                  }}
                  disabled={hiddenNamespaces.size === 0}
                  className={clsx(
                    hiddenNamespaces.size > 0
                      ? 'text-blue-400 hover:text-blue-300'
                      : 'text-theme-text-tertiary/50 cursor-default'
                  )}
                >
                  All
                </button>
                <span className="text-theme-text-tertiary/30">|</span>
                <button
                  onClick={() => {
                    sortedNamespaces.forEach(({ name }) => {
                      if (!hiddenNamespaces.has(name)) {
                        onToggleNamespace(name)
                      }
                    })
                  }}
                  disabled={hiddenNamespaces.size === sortedNamespaces.length}
                  className={clsx(
                    hiddenNamespaces.size < sortedNamespaces.length
                      ? 'text-blue-400 hover:text-blue-300'
                      : 'text-theme-text-tertiary/50 cursor-default'
                  )}
                >
                  None
                </button>
              </div>
            </div>
            <div className="space-y-0.5">
              {visibleNamespaces.map(({ name, nodeCount }) => {
                const isHidden = hiddenNamespaces.has(name)
                return (
                  <button
                    key={name}
                    onClick={() => onToggleNamespace(name)}
                    className={clsx(
                      'w-full flex items-center gap-2 px-2 py-1 rounded text-left transition-all',
                      isHidden
                        ? 'opacity-50 hover:opacity-70'
                        : 'hover:ring-1 hover:ring-white/20'
                    )}
                    style={{
                      backgroundColor: isHidden ? 'transparent' : getNamespaceColor(name),
                    }}
                  >
                    {isHidden && (
                      <div
                        className="w-2.5 h-2.5 rounded-sm flex-shrink-0"
                        style={{ backgroundColor: getNamespaceColor(name) }}
                      />
                    )}
                    <span className={clsx(
                      'text-[11px] font-medium truncate flex-1',
                      isHidden ? 'text-theme-text-tertiary line-through' : 'text-white'
                    )}>
                      {name}
                    </span>
                    <span className={clsx(
                      'text-[10px] tabular-nums',
                      isHidden ? 'text-theme-text-tertiary' : 'text-white/70'
                    )}>
                      {nodeCount}
                    </span>
                  </button>
                )
              })}
            </div>
            {hasMore && (
              <button
                onClick={() => setNamespacesExpanded(!namespacesExpanded)}
                className="w-full flex items-center justify-center gap-1 mt-2 py-1 text-[10px] text-theme-text-tertiary hover:text-theme-text-secondary"
              >
                <ChevronDown className={clsx(
                  'w-3 h-3 transition-transform',
                  namespacesExpanded && 'rotate-180'
                )} />
                {namespacesExpanded ? 'Show less' : `+${sortedNamespaces.length - 8} more`}
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
})
