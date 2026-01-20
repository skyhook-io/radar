import { useState } from 'react'
import { clsx } from 'clsx'
import {
  X,
  Clock,
  AlertCircle,
  CheckCircle,
  RefreshCw,
  Plus,
  Trash2,
  ChevronDown,
  ChevronRight,
  
} from 'lucide-react'
import type { TimelineEvent, TimeRange } from '../../types'
import { useResourceChildren } from '../../api/client'
import { DiffViewer } from './DiffViewer'

interface ResourceDetailDrawerProps {
  kind: string
  namespace: string
  name: string
  events: TimelineEvent[]
  timeRange: TimeRange
  onClose: () => void
}

export function ResourceDetailDrawer({
  kind,
  namespace,
  name,
  events,
  timeRange,
  onClose,
}: ResourceDetailDrawerProps) {
  const [activeTab, setActiveTab] = useState<'events' | 'children'>('events')

  // Fetch children for workloads
  const isWorkload = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'].includes(kind)
  const { data: children, isLoading: childrenLoading } = useResourceChildren(
    kind,
    namespace,
    name,
    timeRange
  )

  // Get health status from most recent event
  const latestEvent = events[0]
  const healthState = latestEvent?.healthState || 'unknown'

  return (
    <div className="fixed inset-y-0 right-0 w-[480px] bg-slate-900 border-l border-slate-700 shadow-2xl z-50 flex flex-col">
      {/* Header */}
      <div className="flex items-start justify-between p-4 border-b border-slate-700">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <span className={clsx(
              'text-xs px-2 py-0.5 rounded font-medium',
              kind === 'Deployment' && 'bg-blue-900/50 text-blue-400',
              kind === 'StatefulSet' && 'bg-purple-900/50 text-purple-400',
              kind === 'DaemonSet' && 'bg-indigo-900/50 text-indigo-400',
              kind === 'Service' && 'bg-cyan-900/50 text-cyan-400',
              kind === 'Pod' && 'bg-green-900/50 text-green-400',
              kind === 'ReplicaSet' && 'bg-violet-900/50 text-violet-400',
              !['Deployment', 'StatefulSet', 'DaemonSet', 'Service', 'Pod', 'ReplicaSet'].includes(kind) && 'bg-slate-700 text-slate-300'
            )}>
              {kind}
            </span>
            <HealthBadge state={healthState} />
          </div>
          <h2 className="text-lg font-semibold text-white truncate" title={name}>
            {name}
          </h2>
          <p className="text-sm text-slate-400">
            {namespace}
          </p>
        </div>
        <button
          onClick={onClose}
          className="p-2 text-slate-400 hover:text-white hover:bg-slate-700 rounded-lg transition-colors"
        >
          <X className="w-5 h-5" />
        </button>
      </div>

      {/* Stats summary */}
      <div className="grid grid-cols-3 gap-px bg-slate-700 border-b border-slate-700">
        <StatCard
          label="Events"
          value={events.length}
          icon={<Clock className="w-4 h-4" />}
        />
        <StatCard
          label="Changes"
          value={events.filter(e => e.type === 'change').length}
          icon={<RefreshCw className="w-4 h-4" />}
        />
        <StatCard
          label="Warnings"
          value={events.filter(e => e.eventType === 'Warning').length}
          icon={<AlertCircle className="w-4 h-4" />}
          warning
        />
      </div>

      {/* Tabs (for workloads) */}
      {isWorkload && (
        <div className="flex border-b border-slate-700">
          <button
            onClick={() => setActiveTab('events')}
            className={clsx(
              'flex-1 px-4 py-2.5 text-sm font-medium transition-colors',
              activeTab === 'events'
                ? 'text-white border-b-2 border-blue-500 bg-slate-800/50'
                : 'text-slate-400 hover:text-white'
            )}
          >
            Events & Changes
          </button>
          <button
            onClick={() => setActiveTab('children')}
            className={clsx(
              'flex-1 px-4 py-2.5 text-sm font-medium transition-colors',
              activeTab === 'children'
                ? 'text-white border-b-2 border-blue-500 bg-slate-800/50'
                : 'text-slate-400 hover:text-white'
            )}
          >
            Child Resources
            {children && children.length > 0 && (
              <span className="ml-1.5 px-1.5 py-0.5 text-xs bg-slate-700 rounded">
                {children.length}
              </span>
            )}
          </button>
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-auto">
        {activeTab === 'events' ? (
          <EventsList events={events} />
        ) : (
          <ChildrenList children={children || []} isLoading={childrenLoading} />
        )}
      </div>
    </div>
  )
}

function HealthBadge({ state }: { state: string }) {
  const config = {
    healthy: { bg: 'bg-green-500/20', text: 'text-green-400', icon: CheckCircle },
    degraded: { bg: 'bg-yellow-500/20', text: 'text-yellow-400', icon: AlertCircle },
    unhealthy: { bg: 'bg-red-500/20', text: 'text-red-400', icon: AlertCircle },
    unknown: { bg: 'bg-slate-500/20', text: 'text-slate-400', icon: Clock },
  }[state] || { bg: 'bg-slate-500/20', text: 'text-slate-400', icon: Clock }

  const Icon = config.icon

  return (
    <span className={clsx('inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded', config.bg, config.text)}>
      <Icon className="w-3 h-3" />
      {state}
    </span>
  )
}

function StatCard({ label, value, icon, warning }: { label: string; value: number; icon: React.ReactNode; warning?: boolean }) {
  return (
    <div className="bg-slate-800 px-4 py-3">
      <div className="flex items-center gap-2 text-slate-400 mb-1">
        {icon}
        <span className="text-xs">{label}</span>
      </div>
      <div className={clsx(
        'text-xl font-semibold',
        warning && value > 0 ? 'text-amber-400' : 'text-white'
      )}>
        {value}
      </div>
    </div>
  )
}

function EventsList({ events }: { events: TimelineEvent[] }) {
  const [expandedEvents, setExpandedEvents] = useState<Set<string>>(new Set())

  const toggleEvent = (id: string) => {
    setExpandedEvents(prev => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  if (events.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-slate-500">
        <Clock className="w-10 h-10 mb-3 opacity-50" />
        <p>No events in selected time range</p>
      </div>
    )
  }

  return (
    <div className="divide-y divide-slate-700/50">
      {events.map(event => {
        const isExpanded = expandedEvents.has(event.id)
        const isChange = event.type === 'change'
        const isWarning = event.eventType === 'Warning'

        return (
          <div key={event.id} className="hover:bg-slate-800/30">
            <button
              onClick={() => toggleEvent(event.id)}
              className="w-full px-4 py-3 flex items-start gap-3 text-left"
            >
              {/* Icon */}
              <div className={clsx(
                'w-8 h-8 rounded-full flex items-center justify-center flex-shrink-0 mt-0.5',
                isChange ? (
                  event.operation === 'add' ? 'bg-green-500/20 text-green-400' :
                  event.operation === 'delete' ? 'bg-red-500/20 text-red-400' :
                  'bg-blue-500/20 text-blue-400'
                ) : isWarning ? 'bg-amber-500/20 text-amber-400' : 'bg-slate-500/20 text-slate-400'
              )}>
                {isChange ? (
                  event.operation === 'add' ? <Plus className="w-4 h-4" /> :
                  event.operation === 'delete' ? <Trash2 className="w-4 h-4" /> :
                  <RefreshCw className="w-4 h-4" />
                ) : isWarning ? (
                  <AlertCircle className="w-4 h-4" />
                ) : (
                  <CheckCircle className="w-4 h-4" />
                )}
              </div>

              {/* Content */}
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 mb-1">
                  <span className="text-sm font-medium text-white">
                    {isChange ? (
                      <span className="capitalize">{event.operation}</span>
                    ) : (
                      event.reason
                    )}
                  </span>
                  {isChange && event.healthState && event.healthState !== 'unknown' && (
                    <HealthBadge state={event.healthState} />
                  )}
                  {!isChange && (
                    <span className={clsx(
                      'text-xs px-1.5 py-0.5 rounded',
                      isWarning ? 'bg-amber-500/20 text-amber-400' : 'bg-green-500/20 text-green-400'
                    )}>
                      {event.eventType}
                    </span>
                  )}
                </div>

                {/* Summary */}
                {isChange && event.diff?.summary && (
                  <p className="text-sm text-slate-400 truncate">{event.diff.summary}</p>
                )}
                {!isChange && event.message && (
                  <p className="text-sm text-slate-400 line-clamp-2">{event.message}</p>
                )}

                {/* Timestamp */}
                <p className="text-xs text-slate-500 mt-1">
                  {new Date(event.timestamp).toLocaleString()}
                  {!isChange && event.count && event.count > 1 && (
                    <span className="ml-2 text-slate-500">({event.count} times)</span>
                  )}
                </p>
              </div>

              {/* Expand indicator */}
              {(isChange && event.diff) && (
                <div className="text-slate-500">
                  {isExpanded ? (
                    <ChevronDown className="w-4 h-4" />
                  ) : (
                    <ChevronRight className="w-4 h-4" />
                  )}
                </div>
              )}
            </button>

            {/* Expanded diff */}
            {isExpanded && isChange && event.diff && (
              <div className="px-4 pb-4 pl-14">
                <DiffViewer diff={event.diff} />
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function ChildrenList({ children, isLoading }: { children: TimelineEvent[]; isLoading: boolean }) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12 text-slate-500">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading child resources...
      </div>
    )
  }

  // Group by resource
  const groupedChildren = children.reduce((acc, event) => {
    const key = `${event.kind}/${event.name}`
    if (!acc[key]) {
      acc[key] = {
        kind: event.kind,
        name: event.name,
        events: [],
      }
    }
    acc[key].events.push(event)
    return acc
  }, {} as Record<string, { kind: string; name: string; events: TimelineEvent[] }>)

  const groups = Object.values(groupedChildren).sort((a, b) => {
    // Sort by kind then name
    if (a.kind !== b.kind) {
      if (a.kind === 'ReplicaSet') return -1
      if (b.kind === 'ReplicaSet') return 1
    }
    return a.name.localeCompare(b.name)
  })

  if (groups.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-slate-500">
        <CheckCircle className="w-10 h-10 mb-3 opacity-50" />
        <p>No child resource changes</p>
        <p className="text-xs mt-1">ReplicaSets, Pods, etc. will appear here</p>
      </div>
    )
  }

  return (
    <div className="divide-y divide-slate-700/50">
      {groups.map(group => (
        <ChildResourceGroup key={`${group.kind}/${group.name}`} {...group} />
      ))}
    </div>
  )
}

function ChildResourceGroup({ kind, name, events }: { kind: string; name: string; events: TimelineEvent[] }) {
  const [expanded, setExpanded] = useState(false)

  // Get latest health state
  const latestEvent = events[0]
  const healthState = latestEvent?.healthState || 'unknown'

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full px-4 py-3 flex items-center gap-3 hover:bg-slate-800/30 text-left"
      >
        <div className="text-slate-500">
          {expanded ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className={clsx(
              'text-xs px-1.5 py-0.5 rounded',
              kind === 'ReplicaSet' ? 'bg-violet-900/50 text-violet-400' :
              kind === 'Pod' ? 'bg-green-900/50 text-green-400' :
              'bg-slate-700 text-slate-400'
            )}>
              {kind}
            </span>
            <HealthBadge state={healthState} />
          </div>
          <p className="text-sm text-white truncate mt-1" title={name}>{name}</p>
        </div>
        <span className="text-xs text-slate-500 bg-slate-700 px-2 py-1 rounded">
          {events.length} event{events.length !== 1 ? 's' : ''}
        </span>
      </button>

      {expanded && (
        <div className="bg-slate-800/30 border-t border-slate-700/50">
          <EventsList events={events} />
        </div>
      )}
    </div>
  )
}
