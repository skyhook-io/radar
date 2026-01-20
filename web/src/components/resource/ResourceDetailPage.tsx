import { useState, useMemo } from 'react'
import { clsx } from 'clsx'
import {
  ArrowLeft,
  Clock,
  AlertCircle,
  CheckCircle,
  RefreshCw,
  Plus,
  Trash2,
  ChevronRight,
  ExternalLink,
  Box,
  Layers,
  Server,
  Settings,
} from 'lucide-react'
import type { TimelineEvent, TimeRange, ResourceRef, Relationships } from '../../types'
import { useChanges, useResourceWithRelationships } from '../../api/client'
import { DiffViewer } from '../events/DiffViewer'

interface ResourceDetailPageProps {
  kind: string
  namespace: string
  name: string
  onBack: () => void
  onNavigateToResource?: (kind: string, namespace: string, name: string) => void
}

export function ResourceDetailPage({
  kind,
  namespace,
  name,
  onBack,
  onNavigateToResource,
}: ResourceDetailPageProps) {
  const [timeRange, setTimeRange] = useState<TimeRange>('1h')
  const [selectedEvent, setSelectedEvent] = useState<TimelineEvent | null>(null)

  // Fetch resource with relationships
  const { data: resourceResponse } = useResourceWithRelationships(kind, namespace, name)
  const relationships = resourceResponse?.relationships

  // Fetch events scoped to this resource
  const { data: allEvents, isLoading: eventsLoading } = useChanges({
    namespace,
    timeRange,
    includeK8sEvents: true,
    includeManaged: true, // Include all events to filter client-side
    limit: 500,
  })

  // Filter events to this resource and its children
  const resourceEvents = useMemo(() => {
    if (!allEvents) return []
    return allEvents.filter(e =>
      (e.kind === kind && e.namespace === namespace && e.name === name) ||
      (e.owner?.kind === kind && e.owner?.name === name)
    )
  }, [allEvents, kind, namespace, name])

  // Separate own events vs child events
  const ownEvents = useMemo(() =>
    resourceEvents.filter(e => e.kind === kind && e.name === name),
    [resourceEvents, kind, name]
  )

  const childEvents = useMemo(() =>
    resourceEvents.filter(e => e.owner?.kind === kind && e.owner?.name === name),
    [resourceEvents, kind, name]
  )

  // Get health from latest event
  const latestEvent = ownEvents[0]
  const healthState = latestEvent?.healthState || 'unknown'

  return (
    <div className="flex flex-col h-full bg-slate-900">
      {/* Header */}
      <div className="flex-shrink-0 border-b border-slate-700 bg-slate-800">
        <div className="px-6 py-4">
          <div className="flex items-center gap-4">
            <button
              onClick={onBack}
              className="p-2 text-slate-400 hover:text-white hover:bg-slate-700 rounded-lg transition-colors"
            >
              <ArrowLeft className="w-5 h-5" />
            </button>
            <div className="flex-1">
              <div className="flex items-center gap-3 mb-1">
                <KindBadge kind={kind} />
                <HealthBadge state={healthState} />
              </div>
              <h1 className="text-xl font-semibold text-white">{name}</h1>
              <p className="text-sm text-slate-400">{namespace}</p>
            </div>
            <TimeRangeSelector value={timeRange} onChange={setTimeRange} />
          </div>
        </div>

        {/* Stats bar */}
        <div className="grid grid-cols-4 gap-px bg-slate-700">
          <StatCard label="Events" value={ownEvents.length} icon={<Clock className="w-4 h-4" />} />
          <StatCard
            label="Changes"
            value={ownEvents.filter(e => e.type === 'change').length}
            icon={<RefreshCw className="w-4 h-4" />}
          />
          <StatCard
            label="Warnings"
            value={ownEvents.filter(e => e.eventType === 'Warning').length}
            icon={<AlertCircle className="w-4 h-4" />}
            warning
          />
          <StatCard
            label="Child Events"
            value={childEvents.length}
            icon={<Layers className="w-4 h-4" />}
          />
        </div>
      </div>

      {/* Main content */}
      <div className="flex-1 flex overflow-hidden">
        {/* Timeline (main area) */}
        <div className="flex-1 flex flex-col overflow-hidden">
          <div className="px-6 py-3 border-b border-slate-700 bg-slate-800/50">
            <h2 className="text-sm font-medium text-slate-300">Activity Timeline</h2>
          </div>
          <div className="flex-1 overflow-auto">
            {eventsLoading ? (
              <div className="flex items-center justify-center py-12 text-slate-500">
                <RefreshCw className="w-5 h-5 animate-spin mr-2" />
                Loading events...
              </div>
            ) : resourceEvents.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-12 text-slate-500">
                <Clock className="w-12 h-12 mb-4 opacity-50" />
                <p className="text-lg">No events in selected time range</p>
                <p className="text-sm mt-1">Try expanding the time range</p>
              </div>
            ) : (
              <EventTimeline
                events={resourceEvents}
                selectedEvent={selectedEvent}
                onSelectEvent={setSelectedEvent}
                resourceKind={kind}
                resourceName={name}
              />
            )}
          </div>
        </div>

        {/* Right sidebar - Related resources */}
        <div className="w-72 border-l border-slate-700 flex flex-col overflow-hidden">
          <div className="px-4 py-3 border-b border-slate-700 bg-slate-800/50">
            <h2 className="text-sm font-medium text-slate-300">Related Resources</h2>
          </div>
          <div className="flex-1 overflow-auto">
            <RelatedResources
              relationships={relationships}
              onNavigate={onNavigateToResource}
            />
          </div>
        </div>
      </div>

      {/* Event detail drawer */}
      {selectedEvent && (
        <EventDetailDrawer
          event={selectedEvent}
          onClose={() => setSelectedEvent(null)}
        />
      )}
    </div>
  )
}

// Sub-components

function KindBadge({ kind }: { kind: string }) {
  const colors: Record<string, string> = {
    Deployment: 'bg-blue-900/50 text-blue-400',
    StatefulSet: 'bg-purple-900/50 text-purple-400',
    DaemonSet: 'bg-indigo-900/50 text-indigo-400',
    Service: 'bg-cyan-900/50 text-cyan-400',
    Pod: 'bg-green-900/50 text-green-400',
    ReplicaSet: 'bg-violet-900/50 text-violet-400',
    Ingress: 'bg-orange-900/50 text-orange-400',
    ConfigMap: 'bg-yellow-900/50 text-yellow-400',
    Secret: 'bg-red-900/50 text-red-400',
  }
  return (
    <span className={clsx('text-xs px-2 py-1 rounded font-medium', colors[kind] || 'bg-slate-700 text-slate-300')}>
      {kind}
    </span>
  )
}

function HealthBadge({ state }: { state: string }) {
  const config: Record<string, { bg: string; text: string; icon: typeof CheckCircle }> = {
    healthy: { bg: 'bg-green-500/20', text: 'text-green-400', icon: CheckCircle },
    degraded: { bg: 'bg-yellow-500/20', text: 'text-yellow-400', icon: AlertCircle },
    unhealthy: { bg: 'bg-red-500/20', text: 'text-red-400', icon: AlertCircle },
    unknown: { bg: 'bg-slate-500/20', text: 'text-slate-400', icon: Clock },
  }
  const { bg, text, icon: Icon } = config[state] || config.unknown
  return (
    <span className={clsx('inline-flex items-center gap-1 text-xs px-2 py-1 rounded', bg, text)}>
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
      <div className={clsx('text-xl font-semibold', warning && value > 0 ? 'text-amber-400' : 'text-white')}>
        {value}
      </div>
    </div>
  )
}

function TimeRangeSelector({ value, onChange }: { value: TimeRange; onChange: (v: TimeRange) => void }) {
  const options: { value: TimeRange; label: string }[] = [
    { value: '5m', label: '5m' },
    { value: '30m', label: '30m' },
    { value: '1h', label: '1h' },
    { value: '6h', label: '6h' },
    { value: '24h', label: '24h' },
    { value: 'all', label: 'All' },
  ]
  return (
    <div className="flex items-center gap-1 bg-slate-700 rounded-lg p-1">
      {options.map(opt => (
        <button
          key={opt.value}
          onClick={() => onChange(opt.value)}
          className={clsx(
            'px-3 py-1.5 text-sm rounded-md transition-colors',
            value === opt.value ? 'bg-slate-600 text-white' : 'text-slate-400 hover:text-white'
          )}
        >
          {opt.label}
        </button>
      ))}
    </div>
  )
}

interface EventTimelineProps {
  events: TimelineEvent[]
  selectedEvent: TimelineEvent | null
  onSelectEvent: (event: TimelineEvent | null) => void
  resourceKind: string
  resourceName: string
}

function EventTimeline({ events, selectedEvent, onSelectEvent, resourceKind, resourceName }: EventTimelineProps) {
  // Group events by day
  const groupedEvents = useMemo(() => {
    const groups: { date: string; events: TimelineEvent[] }[] = []
    let currentDate = ''

    for (const event of events) {
      const date = new Date(event.timestamp).toLocaleDateString()
      if (date !== currentDate) {
        currentDate = date
        groups.push({ date, events: [] })
      }
      groups[groups.length - 1].events.push(event)
    }
    return groups
  }, [events])

  return (
    <div className="divide-y divide-slate-700/30">
      {groupedEvents.map(group => (
        <div key={group.date}>
          <div className="sticky top-0 bg-slate-900/95 backdrop-blur px-6 py-2 text-xs font-medium text-slate-500 border-b border-slate-700/30">
            {group.date}
          </div>
          <div>
            {group.events.map(event => {
              const isOwn = event.kind === resourceKind && event.name === resourceName
              const isSelected = selectedEvent?.id === event.id

              return (
                <button
                  key={event.id}
                  onClick={() => onSelectEvent(isSelected ? null : event)}
                  className={clsx(
                    'w-full px-6 py-3 flex items-start gap-4 text-left transition-colors',
                    isSelected ? 'bg-blue-900/30' : 'hover:bg-slate-800/50',
                    !isOwn && 'pl-10' // Indent child events
                  )}
                >
                  {/* Timeline indicator */}
                  <div className="relative flex flex-col items-center">
                    <EventIcon event={event} />
                    {/* Vertical line connector */}
                    <div className="w-px h-full bg-slate-700 absolute top-8" />
                  </div>

                  {/* Content */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                      {!isOwn && (
                        <span className={clsx(
                          'text-xs px-1.5 py-0.5 rounded',
                          event.kind === 'ReplicaSet' ? 'bg-violet-900/50 text-violet-400' :
                          event.kind === 'Pod' ? 'bg-green-900/50 text-green-400' :
                          'bg-slate-700 text-slate-400'
                        )}>
                          {event.kind}
                        </span>
                      )}
                      <span className="text-sm font-medium text-white">
                        {event.type === 'change' ? (
                          <span className="capitalize">{event.operation}</span>
                        ) : (
                          event.reason
                        )}
                      </span>
                      {event.type === 'change' && event.healthState && event.healthState !== 'unknown' && (
                        <HealthBadge state={event.healthState} />
                      )}
                      {event.type === 'k8s_event' && (
                        <span className={clsx(
                          'text-xs px-1.5 py-0.5 rounded',
                          event.eventType === 'Warning' ? 'bg-amber-500/20 text-amber-400' : 'bg-green-500/20 text-green-400'
                        )}>
                          {event.eventType}
                        </span>
                      )}
                    </div>

                    {!isOwn && (
                      <p className="text-xs text-slate-500 mb-1">{event.name}</p>
                    )}

                    {event.type === 'change' && event.diff?.summary && (
                      <p className="text-sm text-slate-400">{event.diff.summary}</p>
                    )}
                    {event.type === 'k8s_event' && event.message && (
                      <p className="text-sm text-slate-400 line-clamp-2">{event.message}</p>
                    )}

                    <p className="text-xs text-slate-500 mt-1">
                      {new Date(event.timestamp).toLocaleTimeString()}
                      {event.type === 'k8s_event' && event.count && event.count > 1 && (
                        <span className="ml-2">({event.count} times)</span>
                      )}
                    </p>
                  </div>

                  {/* Expand indicator for events with diffs */}
                  {event.type === 'change' && event.diff && (
                    <ChevronRight className={clsx(
                      'w-4 h-4 text-slate-500 transition-transform',
                      isSelected && 'rotate-90'
                    )} />
                  )}
                </button>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}

function EventIcon({ event }: { event: TimelineEvent }) {
  const isChange = event.type === 'change'
  const isWarning = event.eventType === 'Warning'

  const config = isChange ? {
    add: { bg: 'bg-green-500', icon: Plus },
    delete: { bg: 'bg-red-500', icon: Trash2 },
    update: { bg: 'bg-blue-500', icon: RefreshCw },
  }[event.operation || 'update'] : isWarning ? {
    bg: 'bg-amber-500', icon: AlertCircle
  } : {
    bg: 'bg-slate-500', icon: CheckCircle
  }

  const Icon = config?.icon || Clock

  return (
    <div className={clsx(
      'w-8 h-8 rounded-full flex items-center justify-center text-white',
      config?.bg || 'bg-slate-500'
    )}>
      <Icon className="w-4 h-4" />
    </div>
  )
}

function RelatedResources({
  relationships,
  onNavigate
}: {
  relationships?: Relationships
  onNavigate?: (kind: string, namespace: string, name: string) => void
}) {
  if (!relationships) {
    return (
      <div className="px-4 py-8 text-center text-slate-500 text-sm">
        <Settings className="w-8 h-8 mx-auto mb-2 opacity-50" />
        Loading relationships...
      </div>
    )
  }

  const hasRelationships = relationships.owner ||
    (relationships.children?.length ?? 0) > 0 ||
    (relationships.services?.length ?? 0) > 0 ||
    (relationships.pods?.length ?? 0) > 0 ||
    (relationships.configRefs?.length ?? 0) > 0

  if (!hasRelationships) {
    return (
      <div className="px-4 py-8 text-center text-slate-500 text-sm">
        <Box className="w-8 h-8 mx-auto mb-2 opacity-50" />
        No related resources
      </div>
    )
  }

  return (
    <div className="divide-y divide-slate-700/50">
      {relationships.owner && (
        <RelationshipSection
          title="Owner"
          icon={<Layers className="w-4 h-4" />}
          items={[relationships.owner]}
          onNavigate={onNavigate}
        />
      )}
      {relationships.services && relationships.services.length > 0 && (
        <RelationshipSection
          title="Services"
          icon={<Server className="w-4 h-4" />}
          items={relationships.services}
          onNavigate={onNavigate}
        />
      )}
      {relationships.pods && relationships.pods.length > 0 && (
        <RelationshipSection
          title="Pods"
          icon={<Box className="w-4 h-4" />}
          items={relationships.pods}
          onNavigate={onNavigate}
        />
      )}
      {relationships.children && relationships.children.length > 0 && (
        <RelationshipSection
          title="Children"
          icon={<Layers className="w-4 h-4" />}
          items={relationships.children}
          onNavigate={onNavigate}
        />
      )}
      {relationships.configRefs && relationships.configRefs.length > 0 && (
        <RelationshipSection
          title="Config"
          icon={<Settings className="w-4 h-4" />}
          items={relationships.configRefs}
          onNavigate={onNavigate}
        />
      )}
    </div>
  )
}

function RelationshipSection({
  title,
  icon,
  items,
  onNavigate
}: {
  title: string
  icon: React.ReactNode
  items: ResourceRef[]
  onNavigate?: (kind: string, namespace: string, name: string) => void
}) {
  return (
    <div className="py-3">
      <div className="px-4 flex items-center gap-2 text-xs font-medium text-slate-400 mb-2">
        {icon}
        {title}
        <span className="text-slate-500">({items.length})</span>
      </div>
      <div className="space-y-1">
        {items.map(item => (
          <button
            key={`${item.kind}/${item.namespace}/${item.name}`}
            onClick={() => onNavigate?.(item.kind, item.namespace, item.name)}
            className="w-full px-4 py-2 flex items-center gap-2 hover:bg-slate-800/50 text-left group"
          >
            <KindBadge kind={item.kind} />
            <span className="flex-1 text-sm text-slate-300 truncate">{item.name}</span>
            <ExternalLink className="w-3 h-3 text-slate-500 opacity-0 group-hover:opacity-100" />
          </button>
        ))}
      </div>
    </div>
  )
}

function EventDetailDrawer({ event, onClose }: { event: TimelineEvent; onClose: () => void }) {
  const isChange = event.type === 'change'

  return (
    <div className="fixed inset-y-0 right-0 w-[400px] bg-slate-900 border-l border-slate-700 shadow-2xl z-50 flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-slate-700">
        <h3 className="text-sm font-medium text-white">Event Details</h3>
        <button
          onClick={onClose}
          className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
        >
          ×
        </button>
      </div>
      <div className="flex-1 overflow-auto p-4">
        <div className="space-y-4">
          {/* Header info */}
          <div>
            <div className="flex items-center gap-2 mb-2">
              <KindBadge kind={event.kind} />
              <span className="text-white font-medium">{event.name}</span>
            </div>
            <p className="text-sm text-slate-400">{event.namespace}</p>
            <p className="text-xs text-slate-500 mt-1">
              {new Date(event.timestamp).toLocaleString()}
            </p>
          </div>

          {/* Change details */}
          {isChange && (
            <div>
              <div className="flex items-center gap-2 mb-2">
                <span className={clsx(
                  'text-sm font-medium capitalize',
                  event.operation === 'add' && 'text-green-400',
                  event.operation === 'update' && 'text-blue-400',
                  event.operation === 'delete' && 'text-red-400'
                )}>
                  {event.operation}
                </span>
                {event.healthState && event.healthState !== 'unknown' && (
                  <HealthBadge state={event.healthState} />
                )}
              </div>
              {event.diff && (
                <div className="mt-3">
                  <p className="text-xs font-medium text-slate-400 mb-2">Changes</p>
                  <DiffViewer diff={event.diff} />
                </div>
              )}
            </div>
          )}

          {/* K8s event details */}
          {!isChange && (
            <div>
              <div className="flex items-center gap-2 mb-2">
                <span className={clsx(
                  'text-sm font-medium',
                  event.eventType === 'Warning' ? 'text-amber-400' : 'text-green-400'
                )}>
                  {event.reason}
                </span>
                <span className={clsx(
                  'text-xs px-1.5 py-0.5 rounded',
                  event.eventType === 'Warning' ? 'bg-amber-500/20 text-amber-400' : 'bg-green-500/20 text-green-400'
                )}>
                  {event.eventType}
                </span>
              </div>
              {event.message && (
                <p className="text-sm text-slate-300 bg-slate-800 rounded p-3 mt-2">
                  {event.message}
                </p>
              )}
              {event.count && event.count > 1 && (
                <p className="text-xs text-slate-500 mt-2">
                  Occurred {event.count} times
                </p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
