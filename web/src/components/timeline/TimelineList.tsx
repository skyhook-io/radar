import { useState, useMemo, useRef, useEffect } from 'react'
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
} from 'lucide-react'
import { clsx } from 'clsx'
import { useChanges } from '../../api/client'
import { DiffViewer, DiffBadge } from './DiffViewer'
import type { TimelineEvent, TimeRange } from '../../types'

interface TimelineListProps {
  namespace: string
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

type ActivityTypeFilter = 'all' | 'changes' | 'k8s_events' | 'warnings'

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
  'ReplicaSet',
  'DaemonSet',
  'StatefulSet',
]

export function TimelineList({ namespace, onViewChange, currentView = 'list', onResourceClick }: TimelineListProps) {
  const [searchTerm, setSearchTerm] = useState('')
  const [activityTypeFilter, setActivityTypeFilter] = useState<ActivityTypeFilter>('all')
  const [timeRange, setTimeRange] = useState<TimeRange>('1h')
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

  // Fetch unified timeline
  const { data: activity, isLoading, refetch } = useChanges({
    namespace: namespace || undefined,
    kind: kindFilter || undefined,
    timeRange,
    includeK8sEvents: activityTypeFilter !== 'changes',
    limit: 500,
  })

  // Filter activity
  const filteredActivity = useMemo(() => {
    if (!activity) return []

    return activity.filter((item) => {
      // Filter by activity type
      if (activityTypeFilter === 'changes' && item.type !== 'change') return false
      if (activityTypeFilter === 'k8s_events' && item.type !== 'k8s_event') return false
      if (activityTypeFilter === 'warnings') {
        // Warnings filter includes: K8s Warning events + unhealthy/degraded changes
        const isK8sWarning = item.eventType === 'Warning'
        const isUnhealthyChange = item.type === 'change' && (item.healthState === 'unhealthy' || item.healthState === 'degraded')
        if (!isK8sWarning && !isUnhealthyChange) return false
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

  // Group activity by time period
  const groupedActivity = useMemo(() => {
    const groups: { label: string; items: TimelineEvent[] }[] = []
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

    if (last5min.length > 0) groups.push({ label: 'Last 5 minutes', items: last5min })
    if (last30min.length > 0) groups.push({ label: 'Last 30 minutes', items: last30min })
    if (lastHour.length > 0) groups.push({ label: 'Last hour', items: lastHour })
    if (today.length > 0) groups.push({ label: 'Today', items: today })
    if (older.length > 0) groups.push({ label: 'Older', items: older })

    return groups
  }, [filteredActivity])

  // Count stats
  const stats = useMemo(() => {
    if (!activity) return { total: 0, changes: 0, warnings: 0 }
    return {
      total: activity.length,
      changes: activity.filter((e) => e.type === 'change').length,
      warnings: activity.filter((e) =>
        e.eventType === 'Warning' ||
        (e.type === 'change' && (e.healthState === 'unhealthy' || e.healthState === 'degraded'))
      ).length,
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
          />
          <FilterButton
            active={activityTypeFilter === 'changes'}
            onClick={() => setActivityTypeFilter('changes')}
            icon={<RefreshCw className="w-3 h-3" />}
            label="Changes"
            count={stats.changes}
            color="blue"
          />
          <FilterButton
            active={activityTypeFilter === 'warnings'}
            onClick={() => setActivityTypeFilter('warnings')}
            icon={<AlertCircle className="w-3 h-3" />}
            label="Warnings"
            count={stats.warnings}
            color="amber"
          />
          <FilterButton
            active={activityTypeFilter === 'k8s_events'}
            onClick={() => setActivityTypeFilter('k8s_events')}
            icon={<CheckCircle className="w-3 h-3" />}
            label="K8s Events"
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
          onClick={() => refetch()}
          className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg"
          title="Refresh"
        >
          <RefreshCw className="w-4 h-4" />
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
                  {group.items.map((item) => (
                    <ActivityCard
                      key={item.id}
                      item={item}
                      expanded={expandedItem === item.id}
                      onToggle={() => setExpandedItem(expandedItem === item.id ? null : item.id)}
                      onResourceClick={onResourceClick}
                    />
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
  color?: 'blue' | 'amber' | 'green'
}

function FilterButton({ active, onClick, icon, label, count, color }: FilterButtonProps) {
  const colorClasses = {
    blue: 'bg-blue-500/20 text-blue-300',
    amber: 'bg-amber-500/20 text-amber-300',
    green: 'bg-green-500/20 text-green-300',
  }

  return (
    <button
      onClick={onClick}
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
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

function ActivityCard({ item, expanded, onToggle, onResourceClick }: ActivityCardProps) {
  const isChange = item.type === 'change'
  const isWarning = item.eventType === 'Warning'
  const time = formatTime(item.timestamp)

  // Determine card styling based on type
  const getCardStyle = () => {
    if (isChange) {
      switch (item.operation) {
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
      switch (item.operation) {
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
      className={clsx('rounded-lg border transition-all cursor-pointer', getCardStyle())}
      onClick={onToggle}
    >
      <div className="p-3">
        {/* Header row */}
        <div className="flex items-start gap-3">
          {/* Icon */}
          <div className="flex-shrink-0 mt-0.5">{getIcon()}</div>

          {/* Content */}
          <div className="flex-1 min-w-0">
            {/* Resource info */}
            <div className="flex items-center gap-2 flex-wrap">
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onResourceClick?.(item.kind, item.namespace, item.name)
                }}
                className="flex items-center gap-2 hover:bg-theme-elevated/50 rounded px-1 -ml-1 transition-colors group"
              >
                <span className="text-xs px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-secondary group-hover:bg-theme-hover">
                  {item.kind}
                </span>
                <span className="text-sm font-medium text-theme-text-primary truncate group-hover:text-blue-300">{item.name}</span>
              </button>
              {item.namespace && <span className="text-xs text-theme-text-tertiary">in {item.namespace}</span>}
            </div>

            {/* Activity details */}
            <div className="mt-1 flex items-center gap-2 flex-wrap">
              {isChange ? (
                <>
                  <span
                    className={clsx(
                      'text-sm font-medium',
                      item.operation === 'add' && 'text-green-400',
                      item.operation === 'update' && 'text-blue-400',
                      item.operation === 'delete' && 'text-red-400'
                    )}
                  >
                    {item.operation}
                  </span>
                  {item.diff && <DiffBadge diff={item.diff} />}
                  {item.healthState && item.healthState !== 'unknown' && (
                    <span
                      className={clsx(
                        'text-xs px-1.5 py-0.5 rounded',
                        item.healthState === 'healthy' && 'bg-green-500/20 text-green-400',
                        item.healthState === 'degraded' && 'bg-yellow-500/20 text-yellow-400',
                        item.healthState === 'unhealthy' && 'bg-red-500/20 text-red-400'
                      )}
                    >
                      {item.healthState}
                    </span>
                  )}
                </>
              ) : (
                <>
                  <span className={clsx('text-sm font-medium', isWarning ? 'text-amber-300' : 'text-theme-text-secondary')}>
                    {item.reason}
                  </span>
                  <span className="text-sm text-theme-text-secondary">
                    {item.message && item.message.length > 80 && !expanded
                      ? `${item.message.slice(0, 80)}...`
                      : item.message}
                  </span>
                </>
              )}
            </div>
          </div>

          {/* Time and count */}
          <div className="flex-shrink-0 text-right">
            <div className="text-xs text-theme-text-tertiary">{time}</div>
            {item.count && item.count > 1 && (
              <div className="text-xs text-theme-text-disabled mt-1">x{item.count}</div>
            )}
          </div>

          {/* Expand indicator */}
          <ChevronRight
            className={clsx('w-4 h-4 text-theme-text-disabled transition-transform flex-shrink-0', expanded && 'rotate-90')}
          />
        </div>

        {/* Expanded details */}
        {expanded && (
          <div className="mt-3 pt-3 border-t-subtle space-y-3">
            {/* Diff viewer for changes */}
            {isChange && item.diff && (
              <div>
                <div className="text-xs text-theme-text-tertiary mb-2">Changes:</div>
                <DiffViewer diff={item.diff} />
              </div>
            )}

            {/* Full message for K8s events */}
            {!isChange && item.message && item.message.length > 80 && (
              <div>
                <div className="text-xs text-theme-text-tertiary mb-1">Full message:</div>
                <p className="text-sm text-theme-text-secondary whitespace-pre-wrap">{item.message}</p>
              </div>
            )}

            {/* Metadata */}
            <div className="grid grid-cols-2 gap-4 text-xs">
              <div>
                <span className="text-theme-text-tertiary">Timestamp:</span>
                <span className="ml-2 text-theme-text-secondary">{new Date(item.timestamp).toLocaleString()}</span>
              </div>
              <div>
                <span className="text-theme-text-tertiary">Type:</span>
                <span className="ml-2 text-theme-text-secondary">{isChange ? `Change (${item.operation})` : item.eventType}</span>
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
