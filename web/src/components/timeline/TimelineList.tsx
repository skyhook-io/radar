import { useState, useMemo, useRef, useEffect } from 'react'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import {
  AlertCircle,
  CheckCircle,
  Clock,
  Search,
  RefreshCw,
  ChevronRight,
  Filter,
  Plus,
  Trash2,
  List,
  GanttChart,
  Shield,
} from 'lucide-react'
import { clsx } from 'clsx'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import { useChanges } from '../../api/client'
import { DiffViewer, DiffBadge } from './DiffViewer'
import type { TimelineEvent, TimeRange } from '../../types'
import { isChangeEvent, isK8sEvent, isHistoricalEvent, isOperation } from '../../types'
import { getOperationColor, getHealthBadgeColor } from '../../utils/badge-colors'
import { ResourceRefBadge } from '../resources/drawer-components'
import type { NavigateToResource } from '../../utils/navigation'
import { kindToPlural, refToSelectedResource } from '../../utils/navigation'

/** Format resource age (e.g., "3d", "5h", "10m") */
function formatResourceAge(createdAt: string): string {
  const diff = Date.now() - new Date(createdAt).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return '<1m'
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d`
  const months = Math.floor(days / 30)
  return `${months}mo`
}

export type ActivityTypeFilter = 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy'

interface TimelineListProps {
  namespaces: string[]
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: NavigateToResource
  initialFilter?: ActivityTypeFilter
  initialTimeRange?: TimeRange
}

const TIME_RANGES: { value: TimeRange; label: string }[] = [
  { value: '5m', label: '5 min' },
  { value: '30m', label: '30 min' },
  { value: '1h', label: '1 hour' },
  { value: '6h', label: '6 hours' },
  { value: '24h', label: '24 hours' },
  { value: 'all', label: 'All' },
]

const RESOURCE_KINDS = [
  'Deployment',
  'Pod',
  'Service',
  'ConfigMap',
  'Ingress',
  'Gateway',
  'HTTPRoute',
  'GRPCRoute',
  'TCPRoute',
  'TLSRoute',
  'ReplicaSet',
  'DaemonSet',
  'StatefulSet',
]

export function TimelineList({ namespaces, onViewChange, currentView = 'list', onResourceClick, initialFilter, initialTimeRange }: TimelineListProps) {
  const hasLimitedAccess = useHasLimitedAccess()
  const [searchTerm, setSearchTerm] = useState('')
  const [activityTypeFilter, setActivityTypeFilter] = useState<ActivityTypeFilter>(initialFilter ?? 'all')
  const [timeRange, setTimeRange] = useState<TimeRange>(initialTimeRange ?? '1h')
  const [kindFilter, setKindFilter] = useState<string>('')
  const [expandedItem, setExpandedItem] = useState<string | null>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)

  // Keyboard shortcut: / or Cmd/Ctrl+K to focus search
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Ignore if typing in an input
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        // Allow Escape to blur
        if (e.key === 'Escape') {
          (e.target as HTMLElement).blur()
        }
        return
      }

      if (e.key === '/' || ((e.metaKey || e.ctrlKey) && e.key === 'k')) {
        e.preventDefault()
        searchInputRef.current?.focus()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [])

  // Fetch unified timeline - always include all events, filter client-side
  const { data: activity, isLoading, refetch: refetchChanges } = useChanges({
    namespaces,
    kind: kindFilter || undefined,
    timeRange,
    includeK8sEvents: true, // Always fetch all, filter client-side for stable counts
    limit: 500,
  })
  const [handleRefresh, isRefreshAnimating] = useRefreshAnimation(refetchChanges)

  // Filter activity
  const filteredActivity = useMemo(() => {
    if (!activity) return []

    return activity.filter((item) => {
      // Filter by activity type
      if (activityTypeFilter === 'changes' && !isChangeEvent(item)) return false
      if (activityTypeFilter === 'k8s_events' && !isK8sEvent(item)) return false
      if (activityTypeFilter === 'warnings') {
        // Warnings filter: only K8s Warning events (matches home page count)
        if (item.eventType !== 'Warning') return false
      }
      if (activityTypeFilter === 'unhealthy') {
        // Unhealthy filter: only changes with unhealthy/degraded health state (no K8s events)
        const isUnhealthyChange = isChangeEvent(item) && (item.healthState === 'unhealthy' || item.healthState === 'degraded')
        if (!isUnhealthyChange) return false
      }

      // Filter by search term
      if (searchTerm) {
        const term = searchTerm.toLowerCase()
        const matchesName = item.name.toLowerCase().includes(term)
        const matchesKind = item.kind.toLowerCase().includes(term)
        const matchesNamespace = item.namespace?.toLowerCase().includes(term)
        const matchesReason = item.reason?.toLowerCase().includes(term)
        const matchesMessage = item.message?.toLowerCase().includes(term)
        const matchesSummary = item.diff?.summary?.toLowerCase().includes(term)

        if (!matchesName && !matchesKind && !matchesNamespace && !matchesReason && !matchesMessage && !matchesSummary) {
          return false
        }
      }

      return true
    })
  }, [activity, activityTypeFilter, searchTerm])

  // Aggregated event group type
  type AggregatedItem = {
    type: 'single'
    item: TimelineEvent
  } | {
    type: 'aggregated'
    first: TimelineEvent
    last: TimelineEvent
    count: number
    reason: string
  }

  // Aggregate repeated events for the same resource with the same reason
  const aggregateEvents = (items: TimelineEvent[]): AggregatedItem[] => {
    if (items.length === 0) return []

    // Group events by resource+reason
    const groups = new Map<string, TimelineEvent[]>()
    const singleEvents: TimelineEvent[] = []

    for (const item of items) {
      // Only aggregate K8s Warning events or changes with a specific reason
      const reason = item.reason || ''
      const shouldAggregate = (
        item.eventType === 'Warning' ||
        (isChangeEvent(item) && reason && ['OOMKilled', 'CrashLoopBackOff', 'BackOff', 'FailedScheduling', 'Unhealthy'].includes(reason))
      )

      if (shouldAggregate && reason) {
        const key = `${item.kind}:${item.namespace}:${item.name}:${reason}`
        const existing = groups.get(key) || []
        existing.push(item)
        groups.set(key, existing)
      } else {
        singleEvents.push(item)
      }
    }

    // Convert to aggregated items
    const result: AggregatedItem[] = []

    // Process aggregated groups
    for (const events of groups.values()) {
      if (events.length >= 2) {
        // Sort by time (oldest first)
        events.sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
        result.push({
          type: 'aggregated',
          first: events[0],
          last: events[events.length - 1],
          count: events.length,
          reason: events[0].reason || '',
        })
      } else {
        result.push({ type: 'single', item: events[0] })
      }
    }

    // Add single events
    for (const item of singleEvents) {
      result.push({ type: 'single', item })
    }

    // Sort all by most recent (last event time)
    result.sort((a, b) => {
      const timeA = a.type === 'aggregated' ? new Date(a.last.timestamp).getTime() : new Date(a.item.timestamp).getTime()
      const timeB = b.type === 'aggregated' ? new Date(b.last.timestamp).getTime() : new Date(b.item.timestamp).getTime()
      return timeB - timeA
    })

    return result
  }

  // Group activity by time period
  const groupedActivity = useMemo(() => {
    const groups: { label: string; items: AggregatedItem[] }[] = []
    const now = Date.now()

    const last5min: TimelineEvent[] = []
    const last30min: TimelineEvent[] = []
    const lastHour: TimelineEvent[] = []
    const today: TimelineEvent[] = []
    const older: TimelineEvent[] = []

    for (const item of filteredActivity) {
      const itemTime = new Date(item.timestamp).getTime()
      const diffMs = now - itemTime
      const diffMins = diffMs / 60000
      const diffHours = diffMins / 60

      if (diffMins < 5) {
        last5min.push(item)
      } else if (diffMins < 30) {
        last30min.push(item)
      } else if (diffHours < 1) {
        lastHour.push(item)
      } else if (diffHours < 24) {
        today.push(item)
      } else {
        older.push(item)
      }
    }

    if (last5min.length > 0) groups.push({ label: 'Last 5 minutes', items: aggregateEvents(last5min) })
    if (last30min.length > 0) groups.push({ label: 'Last 30 minutes', items: aggregateEvents(last30min) })
    if (lastHour.length > 0) groups.push({ label: 'Last hour', items: aggregateEvents(lastHour) })
    if (today.length > 0) groups.push({ label: 'Today', items: aggregateEvents(today) })
    if (older.length > 0) groups.push({ label: 'Older', items: aggregateEvents(older) })

    return groups
  }, [filteredActivity])

  // Count stats
  const stats = useMemo(() => {
    if (!activity) return { total: 0, changes: 0, warnings: 0, unhealthy: 0 }
    return {
      total: activity.length,
      changes: activity.filter((e) => isChangeEvent(e)).length,
      warnings: activity.filter((e) => e.eventType === 'Warning').length,
      unhealthy: activity.filter((e) => isChangeEvent(e) && (e.healthState === 'unhealthy' || e.healthState === 'degraded')).length,
    }
  }, [activity])

  return (
    <div className="flex flex-col h-full w-full">
      {/* Toolbar */}
      <div className="flex items-center gap-4 px-4 py-3 border-b border-theme-border bg-theme-surface/50 flex-wrap">
        {/* Search */}
        <div className="flex-1 relative min-w-[200px]">
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

        {/* Activity type filter */}
        <div className="flex items-center gap-1 bg-theme-elevated rounded-lg p-1">
          <FilterButton
            active={activityTypeFilter === 'all'}
            onClick={() => setActivityTypeFilter('all')}
            icon={<Filter className="w-3 h-3" />}
            label="All"
            tooltip="Show all activity: resource changes and K8s events"
          />
          <FilterButton
            active={activityTypeFilter === 'changes'}
            onClick={() => setActivityTypeFilter('changes')}
            icon={<RefreshCw className="w-3 h-3" />}
            label="Changes"
            count={stats.changes}
            color="blue"
            tooltip="Resource mutations: creates, updates, deletes detected by watching K8s API"
          />
          <FilterButton
            active={activityTypeFilter === 'warnings'}
            onClick={() => setActivityTypeFilter('warnings')}
            icon={<AlertCircle className="w-3 h-3" />}
            label="Warning Events"
            count={stats.warnings}
            color="amber"
            tooltip="Native Kubernetes Warning events (e.g., ImagePullBackOff, FailedScheduling)"
          />
          <FilterButton
            active={activityTypeFilter === 'unhealthy'}
            onClick={() => setActivityTypeFilter('unhealthy')}
            icon={<AlertCircle className="w-3 h-3" />}
            label="Unhealthy"
            count={stats.unhealthy}
            color="red"
            tooltip="Resource changes with unhealthy or degraded health state"
          />
          <FilterButton
            active={activityTypeFilter === 'k8s_events'}
            onClick={() => setActivityTypeFilter('k8s_events')}
            icon={<CheckCircle className="w-3 h-3" />}
            label="K8s Events"
            tooltip="All native Kubernetes events (Normal + Warning types)"
          />
        </div>

        {/* Kind filter */}
        <select
          value={kindFilter}
          onChange={(e) => setKindFilter(e.target.value)}
          className="appearance-none bg-theme-elevated text-theme-text-primary text-sm rounded-lg px-3 py-2 border border-theme-border-light focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="">All Kinds</option>
          {RESOURCE_KINDS.map((kind) => (
            <option key={kind} value={kind}>
              {kind}
            </option>
          ))}
        </select>

        {/* Time range */}
        <select
          value={timeRange}
          onChange={(e) => setTimeRange(e.target.value as TimeRange)}
          className="appearance-none bg-theme-elevated text-theme-text-primary text-sm rounded-lg px-3 py-2 border border-theme-border-light focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          {TIME_RANGES.map((range) => (
            <option key={range.value} value={range.value}>
              {range.label}
            </option>
          ))}
        </select>

        {/* View toggle */}
        {onViewChange && (
          <div className="flex items-center gap-1 bg-theme-elevated rounded-lg p-1">
            <button
              onClick={() => onViewChange('list')}
              className={clsx(
                'p-2 rounded-md transition-colors',
                currentView === 'list' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
              )}
              title="List view"
            >
              <List className="w-4 h-4" />
            </button>
            <button
              onClick={() => onViewChange('swimlane')}
              className={clsx(
                'p-2 rounded-md transition-colors',
                currentView === 'swimlane' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
              )}
              title="Swimlane view"
            >
              <GanttChart className="w-4 h-4" />
            </button>
          </div>
        )}

        {/* Refresh */}
        <button
          onClick={handleRefresh}
          disabled={isRefreshAnimating}
          className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg disabled:opacity-50"
          title="Refresh"
        >
          <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
        </button>
      </div>

      {/* Timeline content */}
      <div className="flex-1 overflow-auto">
        {isLoading ? (
          <div className="flex items-center justify-center h-full text-theme-text-tertiary">
            <RefreshCw className="w-5 h-5 animate-spin mr-2" />
            Loading timeline...
          </div>
        ) : filteredActivity.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
            <Clock className="w-12 h-12 mb-4 opacity-50" />
            <p className="text-lg">No activity found</p>
            <p className="text-sm mt-2">
              {searchTerm || activityTypeFilter !== 'all' || kindFilter
                ? 'Try adjusting your filters'
                : 'Activity will appear here when cluster changes occur'}
            </p>
            {hasLimitedAccess && !searchTerm && activityTypeFilter === 'all' && !kindFilter && (
              <p className="flex items-center gap-1 text-sm mt-2 text-amber-400/80">
                <Shield className="w-3.5 h-3.5" />
                Some resource types are not monitored due to RBAC restrictions
              </p>
            )}
            {namespaces && namespaces.length > 0 && <p className="text-sm mt-1 text-theme-text-disabled">Searching in: {namespaces.length === 1 ? namespaces[0] : `${namespaces.length} namespaces`}</p>}
          </div>
        ) : (
          <div className="p-4 space-y-6">
            {groupedActivity.map((group) => (
              <div key={group.label}>
                {/* Time period header */}
                <div className="flex items-center gap-2 mb-3">
                  <Clock className="w-4 h-4 text-theme-text-tertiary" />
                  <span className="text-sm font-medium text-theme-text-secondary">{group.label}</span>
                  <span className="text-xs text-theme-text-disabled">
                    ({group.items.length} item{group.items.length !== 1 ? 's' : ''})
                  </span>
                </div>

                {/* Activity list */}
                <div className="space-y-2 ml-6 border-l-2 border-theme-border pl-4">
                  {group.items.map((aggItem) => (
                    aggItem.type === 'aggregated' ? (
                      <AggregatedActivityCard
                        key={`agg-${aggItem.first.id}-${aggItem.last.id}`}
                        first={aggItem.first}
                        last={aggItem.last}
                        count={aggItem.count}
                        reason={aggItem.reason}
                        expanded={expandedItem === aggItem.first.id}
                        onToggle={() => setExpandedItem(expandedItem === aggItem.first.id ? null : aggItem.first.id)}
                        onResourceClick={onResourceClick}
                      />
                    ) : (
                      <ActivityCard
                        key={aggItem.item.id}
                        item={aggItem.item}
                        expanded={expandedItem === aggItem.item.id}
                        onToggle={() => setExpandedItem(expandedItem === aggItem.item.id ? null : aggItem.item.id)}
                        onResourceClick={onResourceClick}
                      />
                    )
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

interface FilterButtonProps {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  count?: number
  color?: 'blue' | 'amber' | 'green' | 'red'
  tooltip?: string
}

function FilterButton({ active, onClick, icon, label, count, color, tooltip }: FilterButtonProps) {
  const colorClasses = {
    blue: 'bg-blue-500/20 text-blue-700 dark:text-blue-300',
    amber: 'bg-amber-500/20 text-amber-700 dark:text-amber-300',
    green: 'bg-green-500/20 text-green-700 dark:text-green-300',
    red: 'bg-red-500/20 text-red-700 dark:text-red-300',
  }

  return (
    <button
      onClick={onClick}
      title={tooltip}
      className={clsx(
        'px-3 py-1.5 text-sm rounded-md transition-colors flex items-center gap-2',
        active ? (color ? colorClasses[color] : 'bg-theme-hover text-theme-text-primary') : 'text-theme-text-secondary hover:text-theme-text-primary'
      )}
    >
      {icon}
      {label}
      {count !== undefined && count > 0 && (
        <span
          className={clsx(
            'text-xs px-1.5 rounded',
            color ? `bg-${color}-500/30` : 'bg-theme-hover/50'
          )}
        >
          {count}
        </span>
      )}
    </button>
  )
}

interface ActivityCardProps {
  item: TimelineEvent
  expanded: boolean
  onToggle: () => void
  onResourceClick?: NavigateToResource
}

function ActivityCard({ item, expanded, onToggle, onResourceClick }: ActivityCardProps) {
  const isChange = isChangeEvent(item)
  const isHistorical = isHistoricalEvent(item)
  const isWarning = item.eventType === 'Warning'
  const time = formatTime(item.timestamp)

  // Only expandable if there's a diff to show
  const hasExpandableContent = isChange && !!item.diff

  // Determine card styling based on type
  const getCardStyle = () => {
    if (isChange) {
      switch (item.eventType) {
        case 'add':
          return 'bg-green-500/5 border-green-500/30 hover:border-green-500/50'
        case 'delete':
          return 'bg-red-500/5 border-red-500/30 hover:border-red-500/50'
        case 'update':
          return 'bg-blue-500/5 border-blue-500/30 hover:border-blue-500/50'
        default:
          return 'bg-theme-surface/50 border-theme-border hover:border-theme-border-light'
      }
    }
    if (isWarning) {
      return 'bg-amber-500/5 border-amber-500/30 hover:border-amber-500/50'
    }
    return 'bg-theme-surface/50 border-theme-border hover:border-theme-border-light'
  }

  const getIcon = () => {
    if (isChange) {
      switch (item.eventType) {
        case 'add':
          return <Plus className="w-4 h-4 text-green-400" />
        case 'delete':
          return <Trash2 className="w-4 h-4 text-red-400" />
        case 'update':
          return <RefreshCw className="w-4 h-4 text-blue-400" />
        default:
          return <CheckCircle className="w-4 h-4 text-theme-text-secondary" />
      }
    }
    if (isWarning) {
      return <AlertCircle className="w-4 h-4 text-amber-400" />
    }
    return <CheckCircle className="w-4 h-4 text-green-400" />
  }

  return (
    <div
      className={clsx('rounded-lg border transition-all', getCardStyle(), hasExpandableContent && 'cursor-pointer')}
      onClick={hasExpandableContent ? onToggle : undefined}
    >
      <div className="p-3">
        {/* Header row */}
        <div className="flex items-start gap-3">
          {/* Icon */}
          <div className="shrink-0 mt-0.5">{getIcon()}</div>

          {/* Content */}
          <div className="flex-1 min-w-0">
            {/* Resource info */}
            <div className="flex items-center gap-2 flex-wrap">
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onResourceClick?.({ kind: kindToPlural(item.kind), namespace: item.namespace, name: item.name })
                }}
                className="flex items-center gap-2 hover:bg-theme-elevated/50 rounded px-1 -ml-1 transition-colors group"
              >
                <span className="text-xs px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-secondary group-hover:bg-theme-hover">
                  {item.kind}
                </span>
                <span className="text-sm font-medium text-theme-text-primary truncate group-hover:text-blue-300">{item.name}</span>
              </button>
              {item.namespace && <span className="text-xs text-theme-text-tertiary">in {item.namespace}</span>}
              {item.owner && (
                <span className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                  <span className="text-xs text-theme-text-quaternary">←</span>
                  <ResourceRefBadge
                    resourceRef={{ kind: item.owner.kind, namespace: item.namespace, name: item.owner.name }}
                    onClick={(ref) => onResourceClick?.(refToSelectedResource(ref))}
                  />
                </span>
              )}
              {item.createdAt && (
                <span className="text-xs text-theme-text-quaternary" title={`Created: ${new Date(item.createdAt).toLocaleString()}`}>
                  • {formatResourceAge(item.createdAt)} old
                </span>
              )}
            </div>

            {/* Activity details */}
            <div className="mt-1 flex items-center gap-2 flex-wrap">
              {isChange ? (
                <>
                  <span className={clsx('text-sm font-medium', isOperation(item.eventType) && getOperationColor(item.eventType))}>
                    {isHistorical && item.reason ? item.reason : item.eventType}
                  </span>
                  {item.diff && <DiffBadge diff={item.diff} />}
                  {item.healthState && item.healthState !== 'unknown' && (
                    <span className={clsx('text-xs px-1.5 py-0.5 rounded', getHealthBadgeColor(item.healthState))}>
                      {item.healthState}
                    </span>
                  )}
                  {isHistorical && item.message && (
                    <span className="text-sm text-theme-text-secondary">
                      {item.message}
                    </span>
                  )}
                </>
              ) : (
                <>
                  <span className={clsx('text-sm font-medium', isWarning ? 'text-amber-700 dark:text-amber-300' : 'text-theme-text-secondary')}>
                    {item.reason}
                  </span>
                  <span className="text-sm text-theme-text-secondary">
                    {item.message}
                  </span>
                </>
              )}
            </div>
          </div>

          {/* Time and count */}
          <div className="shrink-0 text-right">
            <div className="text-xs text-theme-text-tertiary">{time}</div>
            {item.count && item.count > 1 && (
              <div className="text-xs text-theme-text-disabled mt-1">x{item.count}</div>
            )}
          </div>

          {/* Expand indicator - only show if there's content to expand */}
          {hasExpandableContent && (
            <ChevronRight
              className={clsx('w-4 h-4 text-theme-text-disabled transition-transform shrink-0', expanded && 'rotate-90')}
            />
          )}
        </div>

        {/* Expanded details - only for items with diffs */}
        {expanded && hasExpandableContent && item.diff && (
          <div className="mt-3 pt-3 border-t-subtle">
            <div className="text-xs text-theme-text-tertiary mb-2">Changes:</div>
            <DiffViewer diff={item.diff} />
          </div>
        )}
      </div>
    </div>
  )
}

// Component for aggregated repeated events (e.g., multiple OOMKilled)
interface AggregatedActivityCardProps {
  first: TimelineEvent
  last: TimelineEvent
  count: number
  reason: string
  expanded: boolean
  onToggle: () => void
  onResourceClick?: NavigateToResource
}

function AggregatedActivityCard({ first, last, count, reason, expanded, onToggle, onResourceClick }: AggregatedActivityCardProps) {
  const isWarning = first.eventType === 'Warning'
  const firstTime = formatTime(first.timestamp)
  const lastTime = formatTime(last.timestamp)

  // Card styling - warning/unhealthy style for aggregated events
  const cardStyle = isWarning
    ? 'bg-amber-500/5 border-amber-500/30 hover:border-amber-500/50'
    : 'bg-red-500/5 border-red-500/30 hover:border-red-500/50'

  // Dot color based on severity
  const dotColor = isWarning ? 'bg-amber-500' : 'bg-red-500'
  const textColor = isWarning ? 'text-amber-400' : 'text-red-400'

  return (
    <div
      className={clsx('rounded-lg border transition-all cursor-pointer', cardStyle)}
      onClick={onToggle}
    >
      <div className="p-3">
        {/* Header row */}
        <div className="flex items-start gap-3">
          {/* Aggregation visualization: first dot - line - last dot */}
          <div className="flex flex-col items-center shrink-0 mt-0.5">
            {/* First occurrence dot */}
            <div className={clsx('w-2.5 h-2.5 rounded-full', dotColor)} title={`First: ${firstTime}`} />
            {/* Connecting line */}
            <div className={clsx('w-0.5 h-4 my-0.5', isWarning ? 'bg-amber-500/40' : 'bg-red-500/40')} />
            {/* Last occurrence dot */}
            <div className={clsx('w-2.5 h-2.5 rounded-full', dotColor)} title={`Last: ${lastTime}`} />
          </div>

          {/* Content */}
          <div className="flex-1 min-w-0">
            {/* Resource info */}
            <div className="flex items-center gap-2 flex-wrap">
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onResourceClick?.({ kind: kindToPlural(first.kind), namespace: first.namespace, name: first.name })
                }}
                className="flex items-center gap-2 hover:bg-theme-elevated/50 rounded px-1 -ml-1 transition-colors group"
              >
                <span className="text-xs px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-secondary group-hover:bg-theme-hover">
                  {first.kind}
                </span>
                <span className="text-sm font-medium text-theme-text-primary truncate group-hover:text-blue-300">{first.name}</span>
              </button>
              {first.namespace && <span className="text-xs text-theme-text-tertiary">in {first.namespace}</span>}
            </div>

            {/* Aggregated event details */}
            <div className="mt-1 flex items-center gap-2 flex-wrap">
              <span className={clsx('text-sm font-medium', textColor)}>
                {reason}
              </span>
              <span className={clsx(
                'text-xs px-1.5 py-0.5 rounded font-medium',
                isWarning ? 'bg-amber-500/20 text-amber-400' : 'bg-red-500/20 text-red-400'
              )}>
                x{count}
              </span>
              <span className="text-xs text-theme-text-tertiary">
                {firstTime} → {lastTime}
              </span>
            </div>
          </div>

          {/* Expand indicator */}
          <ChevronRight
            className={clsx('w-4 h-4 text-theme-text-disabled transition-transform shrink-0', expanded && 'rotate-90')}
          />
        </div>

        {/* Expanded details */}
        {expanded && (
          <div className="mt-3 pt-3 border-t-subtle space-y-3">
            {/* First occurrence */}
            <div className="flex items-start gap-2">
              <div className={clsx('w-2 h-2 rounded-full mt-1.5 shrink-0', dotColor)} />
              <div>
                <div className="text-xs text-theme-text-tertiary">First occurrence</div>
                <div className="text-sm text-theme-text-secondary">
                  {new Date(first.timestamp).toLocaleString()}
                </div>
                {first.message && (
                  <p className="text-xs text-theme-text-tertiary mt-1 whitespace-pre-wrap">
                    {first.message}
                  </p>
                )}
              </div>
            </div>

            {/* Last occurrence */}
            <div className="flex items-start gap-2">
              <div className={clsx('w-2 h-2 rounded-full mt-1.5 shrink-0', dotColor)} />
              <div>
                <div className="text-xs text-theme-text-tertiary">Last occurrence ({count}x total)</div>
                <div className="text-sm text-theme-text-secondary">
                  {new Date(last.timestamp).toLocaleString()}
                </div>
                {last.message && (
                  <p className="text-xs text-theme-text-tertiary mt-1 whitespace-pre-wrap">
                    {last.message}
                  </p>
                )}
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function formatTime(timestamp: string): string {
  if (!timestamp) return '-'
  const date = new Date(timestamp)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)

  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  return date.toLocaleDateString()
}
