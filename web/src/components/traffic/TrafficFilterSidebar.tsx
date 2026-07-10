import { memo, useState } from 'react'
import {
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
import { SEVERITY_BADGE } from '@skyhook-io/k8s-ui/utils/badge-colors'
import type { AddonMode } from './TrafficView'
import { getNamespaceColor } from '../../utils/traffic-colors'
import { Tooltip } from '../ui/Tooltip'
import { Input } from '@skyhook-io/k8s-ui'

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

  // L7 filters (Hubble-only)
  isHubble?: boolean
  l7Protocol: string // 'all' | 'HTTP' | 'DNS' | 'TCP'
  setL7Protocol: (v: string) => void
  l7Methods: Set<string>
  onToggleL7Method: (method: string) => void
  l7StatusRanges: Set<string>
  onToggleL7StatusRange: (range: string) => void
  l7Verdicts: Set<string>
  onToggleL7Verdict: (verdict: string) => void
  dnsPattern: string
  setDnsPattern: (v: string) => void

  // Namespace filtering
  namespaces: Array<{ name: string; nodeCount: number }>
  hiddenNamespaces: Set<string>
  onToggleNamespace: (ns: string) => void

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
      enabled ? 'selection' : 'hover:bg-theme-elevated'
    )}>
      <button
        onClick={onToggle}
        className="flex-1 flex items-center gap-2 text-left"
      >
        <Icon className={clsx(
          'w-3.5 h-3.5 shrink-0',
          enabled ? 'selection-text' : 'text-theme-text-tertiary'
        )} />
        <span className={clsx(
          'flex-1 text-xs',
          enabled ? 'selection-text' : 'text-theme-text-primary'
        )}>
          {label}
        </span>
      </button>
      <Tooltip content={description} position="right">
        <Info className="w-3 h-3 text-theme-text-tertiary hover:text-theme-text-secondary cursor-help" />
      </Tooltip>
      <button
        onClick={onToggle}
        className={clsx(
          'w-7 h-4 rounded-full transition-colors relative shrink-0',
          enabled ? 'bg-skyhook-500' : 'bg-theme-elevated'
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
  isHubble,
  l7Protocol,
  setL7Protocol,
  l7Methods,
  onToggleL7Method,
  l7StatusRanges,
  onToggleL7StatusRange,
  l7Verdicts,
  onToggleL7Verdict,
  dnsPattern,
  setDnsPattern,
  namespaces,
  hiddenNamespaces,
  onToggleNamespace,
}: TrafficFilterSidebarProps) {
  const [namespacesExpanded, setNamespacesExpanded] = useState(false)

  // Sort namespaces by node count (descending)
  const sortedNamespaces = [...namespaces].sort((a, b) => b.nodeCount - a.nodeCount)
  const visibleNamespaces = namespacesExpanded ? sortedNamespaces : sortedNamespaces.slice(0, 8)
  const hasMore = sortedNamespaces.length > 8

  return (
    <div className="w-72 flex flex-col shrink-0 bg-theme-surface/90 backdrop-blur border-r border-theme-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center px-3 py-2 border-b border-theme-border">
        <span className="text-sm font-medium text-theme-text-secondary">Traffic Filters</span>
      </div>

      {/* Scrollable content */}
      <div className="flex-1 overflow-y-auto">
        {/* Time Range & Threshold */}
        <div className="px-3 py-2 border-b border-theme-border space-y-1.5">
          <div className="flex items-center gap-2">
            <Clock className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <Tooltip content="Show traffic from the selected time window" wrapperClassName="!flex flex-1">
            <select
              value={timeRange}
              onChange={(e) => setTimeRange(e.target.value)}
              className="flex-1 bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 border border-theme-border focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {TIME_RANGES.map(({ value, label }) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
            </Tooltip>
          </div>
          <div className="flex items-center gap-2">
            <Filter className="w-3.5 h-3.5 text-theme-text-tertiary" />
            <Tooltip content="Hide low-traffic flows to reduce noise" wrapperClassName="!flex flex-1">
            <select
              value={minConnections}
              onChange={(e) => setMinConnections(Number(e.target.value))}
              className="flex-1 bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 border border-theme-border focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {CONNECTION_THRESHOLDS.map(({ value, label }) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
            </Tooltip>
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
              <Tooltip content="Monitoring, logging, cert-manager, etc. Excludes ingress controllers and service mesh." position="right">
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
                      ? 'bg-skyhook-500 text-white'
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

        {/* L7 Filters (Hubble only) */}
        {isHubble && (
          <div className="space-y-2 px-3 py-2 border-t border-theme-border">
            <div className="flex items-center gap-1.5">
              <Filter className="w-3 h-3 text-theme-text-tertiary" />
              <span className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider">L7 Filters</span>
            </div>

            {/* Protocol selector */}
            <div>
              <div className="text-[10px] text-theme-text-tertiary mb-1">Protocol</div>
              <div className="flex rounded-md overflow-hidden border border-theme-border">
                {['all', 'HTTP', 'DNS', 'TCP'].map(proto => (
                  <button
                    key={proto}
                    onClick={() => setL7Protocol(proto)}
                    className={clsx(
                      'flex-1 px-2 py-1 text-[10px] font-medium transition-colors capitalize',
                      l7Protocol === proto
                        ? 'bg-skyhook-500 text-white'
                        : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
                    )}
                  >
                    {proto === 'all' ? 'All' : proto}
                  </button>
                ))}
              </div>
            </div>

            {/* HTTP sub-filters (visible when protocol is All or HTTP) */}
            {(l7Protocol === 'all' || l7Protocol === 'HTTP') && (
              <>
                <div>
                  <div className="text-[10px] text-theme-text-tertiary mb-1">HTTP Method</div>
                  <div className="flex flex-wrap gap-1">
                    {['GET', 'POST', 'PUT', 'DELETE', 'PATCH'].map(method => (
                      <button
                        key={method}
                        onClick={() => onToggleL7Method(method)}
                        className={clsx(
                          'px-1.5 py-0.5 rounded text-[10px] font-medium transition-colors',
                          l7Methods.has(method)
                            ? SEVERITY_BADGE.info
                            : 'bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary'
                        )}
                      >
                        {method}
                      </button>
                    ))}
                  </div>
                </div>

                <div>
                  <div className="text-[10px] text-theme-text-tertiary mb-1">Status Code</div>
                  <div className="flex flex-wrap gap-1">
                    {([
                      { label: '2xx', active: SEVERITY_BADGE.success },
                      { label: '3xx', active: SEVERITY_BADGE.neutral },
                      { label: '4xx', active: SEVERITY_BADGE.warning },
                      { label: '5xx', active: SEVERITY_BADGE.error },
                    ] as const).map(({ label, active }) => (
                      <button
                        key={label}
                        onClick={() => onToggleL7StatusRange(label)}
                        className={clsx(
                          'px-1.5 py-0.5 rounded text-[10px] font-medium transition-colors',
                          l7StatusRanges.has(label)
                            ? active
                            : 'bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary'
                        )}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>
              </>
            )}

            {/* DNS sub-filter (visible when protocol is All or DNS) */}
            {(l7Protocol === 'all' || l7Protocol === 'DNS') && (
              <div>
                <div className="text-[10px] text-theme-text-tertiary mb-1">DNS Query</div>
                <Input
                  value={dnsPattern}
                  onChange={(e) => setDnsPattern(e.target.value)}
                  placeholder="e.g. example.com"
                  className="w-full px-2 py-1 text-[11px] rounded bg-theme-elevated border border-theme-border text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                />
              </div>
            )}

            {/* Verdict (always visible — applies to all protocols) */}
            <div>
              <div className="text-[10px] text-theme-text-tertiary mb-1">Verdict</div>
              <div className="flex flex-wrap gap-1">
                {([
                  { label: 'forwarded', active: SEVERITY_BADGE.success },
                  { label: 'dropped', active: SEVERITY_BADGE.error },
                  { label: 'error', active: SEVERITY_BADGE.warning },
                ] as const).map(({ label, active }) => (
                  <button
                    key={label}
                    onClick={() => onToggleL7Verdict(label)}
                    className={clsx(
                      'px-1.5 py-0.5 rounded text-[10px] font-medium capitalize transition-colors',
                      l7Verdicts.has(label)
                        ? active
                        : 'bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary'
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>
          </div>
        )}

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
                        className="w-2.5 h-2.5 rounded-sm shrink-0"
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
