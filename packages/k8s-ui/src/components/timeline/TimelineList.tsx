import { useState, useMemo, useEffect, useCallback, useRef } from 'react'
import { PaneLoader } from '../ui/PaneLoader'
import { Tooltip } from '../ui/Tooltip'
import {
  AlertCircle,
  CheckCircle,
  Clock,
  RefreshCw,
  ChevronRight,
  Plus,
  Trash2,
  Shield,
  X,
} from 'lucide-react'
import { clsx } from 'clsx'
import { DiffViewer, DiffBadge } from './DiffViewer'
import { TimelineToolbar } from './TimelineToolbar'
import {
  matchesActivityFilter,
  matchesTimelineSearch,
  mergeKindOptions,
  describeActiveFilters,
  TIMELINE_RESOURCE_KINDS,
  type ActivityTypeFilter,
  type ActivityFilterKey,
} from './timeline-filters'
import type { TimelineEvent, TimeRange } from '../../types'
import { isChangeEvent, isHistoricalEvent } from '../../types'
import { getHealthBadgeColor, SEVERITY_BADGE } from '../../utils/badge-colors'
import { ResourceRefBadge } from '../ui/drawer-components'
import type { NavigateToResource } from '../../utils/navigation'
import { kindToPlural, refToSelectedResource, apiVersionToGroup } from '../../utils/navigation'
import { pluralize } from '../../utils/pluralize'

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

export type { ActivityTypeFilter, ActivityFilterKey }

export interface TimelineListProps {
  events: TimelineEvent[]
  isLoading: boolean
  onRefresh?: () => void
  onQueryChange?: (params: { timeRange: TimeRange; kinds: string[] }) => void
  hasLimitedAccess?: boolean
  namespaces?: string[]
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: NavigateToResource
  initialFilter?: ActivityTypeFilter
  initialTimeRange?: TimeRange
  // Time-range dropdown options. Defaults to the standard set; a retained host
  // can pass a deeper set (e.g. adds 7d/30d) without changing OSS behavior.
  rangeOptions?: { value: TimeRange; label: string }[]
  // Hide the built-in time-range dropdown when an external control (the
  // retained-mode scrubber) owns the range instead.
  hideRangeSelector?: boolean
  // Controlled "show deleted" toggle. When omitted the component manages it
  // internally; the host passes it to share one toggle across list + swimlane
  // and to drive server-side delete filtering.
  showDeleted?: boolean
  onShowDeletedChange?: (showDeleted: boolean) => void
  // Controlled filter state. When omitted each is managed internally; the host
  // passes them to share one set of filters across list + swimlane so they
  // survive the view switch. Existing hosts that pass nothing are unaffected.
  search?: string
  onSearchChange?: (value: string) => void
  activityFilter?: ActivityFilterKey[]
  onActivityFilterChange?: (keys: ActivityFilterKey[]) => void
  kindFilter?: string[]
  onKindFilterChange?: (kinds: string[]) => void
  // Reports the time span of the rows currently visible in the list's
  // scrollport (null when nothing is visible). A host scrubber renders it as
  // the lens, so scrolling the list moves the lens across the strip.
  onVisibleWindowChange?: (window: { fromMs: number; toMs: number } | null) => void
  // On mount/switch, scroll the list so the row nearest this time sits at the
  // top — carries the swimlane's view window over when switching to list view.
  scrollToMs?: number
  // Strip the toolbar (search / filters / range / view toggle) for an embedded
  // list where those controls are overkill — e.g. the workload detail. Default false.
  compact?: boolean
  // Externally-controlled selection for bidirectional sync with a swimlane: the
  // matching card is highlighted and scrolled into view; a card click reports up.
  selectedEventId?: string | null
  onSelectEvent?: (id: string | null) => void
  // The server-side fetch cap the events were limited to (e.g. 2000). When the
  // returned set reaches it, the list surfaces an end-of-list note so the drop of
  // older events isn't silent. Omit when the source isn't capped (e.g. compact).
  truncatedAt?: number
}

const TIME_RANGES: { value: TimeRange; label: string }[] = [
  { value: '5m', label: '5 min' },
  { value: '30m', label: '30 min' },
  { value: '1h', label: '1 hour' },
  { value: '6h', label: '6 hours' },
  { value: '24h', label: '24 hours' },
  { value: 'all', label: 'All' },
]

export function TimelineList({ events, isLoading, onRefresh, onQueryChange, hasLimitedAccess, namespaces, onViewChange, currentView = 'list', onResourceClick, initialFilter, initialTimeRange, rangeOptions = TIME_RANGES, hideRangeSelector = false, showDeleted: showDeletedProp, onShowDeletedChange, search: searchProp, onSearchChange, activityFilter: activityFilterProp, onActivityFilterChange, kindFilter: kindFilterProp, onKindFilterChange, onVisibleWindowChange, scrollToMs, compact = false, selectedEventId, onSelectEvent, truncatedAt }: TimelineListProps) {
  const [searchInternal, setSearchInternal] = useState('')
  const searchTerm = searchProp ?? searchInternal
  const setSearchTerm = onSearchChange ?? setSearchInternal
  const [activityFilterInternal, setActivityFilterInternal] = useState<ActivityFilterKey[]>(
    initialFilter && initialFilter !== 'all' ? [initialFilter] : [],
  )
  const activityTypeFilter = activityFilterProp ?? activityFilterInternal
  const setActivityTypeFilter = onActivityFilterChange ?? setActivityFilterInternal
  const [timeRange, setTimeRange] = useState<TimeRange>(initialTimeRange ?? '1h')
  const [kindFilterInternal, setKindFilterInternal] = useState<string[]>([])
  const kindFilter = kindFilterProp ?? kindFilterInternal
  const setKindFilter = onKindFilterChange ?? setKindFilterInternal
  const [showDeletedInternal, setShowDeletedInternal] = useState(true)
  const showDeleted = showDeletedProp ?? showDeletedInternal
  const setShowDeleted = onShowDeletedChange ?? setShowDeletedInternal
  const [expandedItem, setExpandedItem] = useState<string | null>(null)

  // Clear every content filter at once. Each setter already resolves to the
  // controlled callback or the internal state setter, so this works in both
  // host-driven and standalone modes.
  const clearAllFilters = useCallback(() => {
    setSearchTerm('')
    setActivityTypeFilter([])
    setKindFilter([])
    setShowDeleted(true)
  }, [setSearchTerm, setActivityTypeFilter, setKindFilter, setShowDeleted])

  useEffect(() => {
    onQueryChange?.({ timeRange, kinds: kindFilter })
  }, [timeRange, kindFilter, onQueryChange])

  // Kind filter options: seed with common kinds, then accumulate every kind seen
  // in the data so CRDs the cluster actually emits become filterable. The set only
  // grows — selecting a kind narrows the server query to it, so deriving options
  // from the current events alone would collapse the dropdown to that one kind.
  const [seenKinds, setSeenKinds] = useState<Set<string>>(() => new Set(TIMELINE_RESOURCE_KINDS))
  useEffect(() => {
    if (!events?.length) return
    setSeenKinds((prev) => {
      let next: Set<string> | null = null
      for (const e of events) {
        if (e.kind && !prev.has(e.kind)) {
          if (!next) next = new Set(prev)
          next.add(e.kind)
        }
      }
      return next ?? prev
    })
  }, [events])
  // Common kinds keep their curated order (most-used first); kinds discovered in
  // the data that aren't in the seed (CRDs) are appended alphabetically.
  const kindOptions = useMemo(() => mergeKindOptions(seenKinds), [seenKinds])

  // Filter activity through the shared predicates so list + swimlane can't drift.
  const filteredActivity = useMemo(() => {
    if (!events) return []
    return events.filter((item) => {
      if (!matchesActivityFilter(item, activityTypeFilter)) return false
      if (kindFilter.length > 0 && !kindFilter.includes(item.kind)) return false
      if (!showDeleted && item.eventType === 'delete') return false
      if (!matchesTimelineSearch(item, searchTerm)) return false
      return true
    })
  }, [events, activityTypeFilter, kindFilter, searchTerm, showDeleted])

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
    // Compact (single-subject embed): a resource's events are often all old, so
    // the relative buckets below collapse into one meaningless "Older". Group by
    // calendar date instead.
    if (compact) {
      const nowDate = new Date()
      const todayKey = nowDate.toDateString()
      const yKey = new Date(nowDate.getTime() - 86_400_000).toDateString()
      const byDay = new Map<string, TimelineEvent[]>()
      for (const item of filteredActivity) {
        const key = new Date(item.timestamp).toDateString()
        const arr = byDay.get(key)
        if (arr) arr.push(item)
        else byDay.set(key, [item])
      }
      return [...byDay.entries()]
        .sort((a, b) => (new Date(b[0]).getTime() || 0) - (new Date(a[0]).getTime() || 0))
        .map(([key, items]) => {
          const d = new Date(key)
          const label = isNaN(d.getTime()) ? 'Unknown date'
            : key === todayKey ? 'Today'
            : key === yKey ? 'Yesterday'
            : d.getFullYear() === nowDate.getFullYear()
              ? d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
              : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
          return { label, items: aggregateEvents(items) }
        })
    }

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
  }, [filteredActivity, compact])

  // Visible-window reporting: rows carry their time span in data attributes;
  // on scroll (rAF-throttled) and whenever the row set changes, the span of
  // rows intersecting the scrollport is reported so a host scrubber can show
  // it as the lens. DOM-measured, so it runs only in effects/handlers —
  // SSR-safe by construction.
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const visibleWindowRaf = useRef<number | null>(null)
  const reportVisibleWindow = useCallback(() => {
    const container = scrollRef.current
    if (!container || !onVisibleWindowChange) return
    const containerRect = container.getBoundingClientRect()
    let fromMs = Infinity
    let toMs = -Infinity
    for (const row of container.querySelectorAll<HTMLElement>('[data-ts-from]')) {
      const rect = row.getBoundingClientRect()
      if (rect.bottom < containerRect.top || rect.top > containerRect.bottom) continue
      const from = Number(row.dataset.tsFrom)
      const to = Number(row.dataset.tsTo)
      if (Number.isFinite(from) && from < fromMs) fromMs = from
      if (Number.isFinite(to) && to > toMs) toMs = to
    }
    onVisibleWindowChange(fromMs <= toMs ? { fromMs, toMs } : null)
  }, [onVisibleWindowChange])
  const handleListScroll = useCallback(() => {
    if (visibleWindowRaf.current != null) return
    visibleWindowRaf.current = requestAnimationFrame(() => {
      visibleWindowRaf.current = null
      reportVisibleWindow()
    })
  }, [reportVisibleWindow])
  useEffect(() => {
    reportVisibleWindow()
    return () => {
      if (visibleWindowRaf.current != null) cancelAnimationFrame(visibleWindowRaf.current)
    }
  }, [reportVisibleWindow, groupedActivity])

  // Carry the swimlane's view window into the list: once rows exist, scroll so
  // the row nearest `scrollToMs` sits at the top. Applied once per `scrollToMs`
  // (a fresh view switch), so it never fights the user's own scrolling after.
  const scrolledToRef = useRef<number | null>(null)
  useEffect(() => {
    if (scrollToMs == null || scrolledToRef.current === scrollToMs) return
    const container = scrollRef.current
    if (!container) return
    const rows = container.querySelectorAll<HTMLElement>('[data-ts-from]')
    if (rows.length === 0) return
    let best: HTMLElement | null = null
    let bestDelta = Infinity
    for (const row of rows) {
      const delta = Math.abs(Number(row.dataset.tsFrom) - scrollToMs)
      if (delta < bestDelta) { bestDelta = delta; best = row }
    }
    if (best) {
      container.scrollTop += best.getBoundingClientRect().top - container.getBoundingClientRect().top
      scrolledToRef.current = scrollToMs
      // A jump that lands where the list already sits emits no scroll event, so
      // the host's lens would keep showing the pre-jump window without this.
      reportVisibleWindow()
    }
  }, [scrollToMs, groupedActivity, reportVisibleWindow])

  // Bidirectional sync: when the selection changes externally (a swimlane marker
  // click), scroll the matching card into view. Re-runs on groupedActivity so a
  // selection made before the rows exist still lands once they render.
  useEffect(() => {
    if (!selectedEventId) return
    const container = scrollRef.current
    const card = container?.querySelector<HTMLElement>(`[data-event-id="${CSS.escape(selectedEventId)}"]`)
    card?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }, [selectedEventId, groupedActivity])

  return (
    <div className="flex flex-col h-full w-full">
      {!compact && (
        <TimelineToolbar
          search={searchTerm}
          onSearchChange={setSearchTerm}
          searchShortcutId="timeline-list-search"
          activityFilter={activityTypeFilter}
          onActivityFilterChange={setActivityTypeFilter}
          events={events}
          showDeleted={showDeleted}
          onShowDeletedChange={setShowDeleted}
          kindFilter={kindFilter}
          onKindFilterChange={setKindFilter}
          kindOptions={kindOptions}
          rangeOptions={hideRangeSelector ? undefined : rangeOptions}
          timeRange={hideRangeSelector ? undefined : timeRange}
          onTimeRangeChange={hideRangeSelector ? undefined : setTimeRange}
          counts={{ events: filteredActivity.length }}
          view={currentView}
          onViewChange={onViewChange}
          onRefresh={onRefresh}
        />
      )}

      {/* Timeline content */}
      <div className="flex-1 overflow-auto" ref={scrollRef} onScroll={onVisibleWindowChange ? handleListScroll : undefined}>
        {isLoading ? (
          <PaneLoader label="Loading timeline…" className="h-full" />
        ) : filteredActivity.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
            <Clock className="w-12 h-12 mb-4 opacity-50" />
            {(() => {
              const activeFilters = describeActiveFilters({ search: searchTerm, activityFilter: activityTypeFilter, kindFilter, showDeleted })
              return (
                <>
                  <p className="text-lg">No activity found</p>
                  <p className="text-sm mt-2">
                    {activeFilters || 'Activity will appear here when cluster changes occur'}
                  </p>
                  {activeFilters && (
                    <button
                      type="button"
                      onClick={clearAllFilters}
                      className="mt-4 flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-lg text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors"
                    >
                      <X className="w-3.5 h-3.5" />
                      Clear filters
                    </button>
                  )}
                  {hasLimitedAccess && !activeFilters && (
                    <p className="flex items-center gap-1 text-sm mt-2 text-amber-400/80">
                      <Shield className="w-3.5 h-3.5" />
                      Some resource types are not monitored due to RBAC restrictions
                    </p>
                  )}
                  {namespaces && namespaces.length > 0 && (
                    <p className="text-sm mt-2 text-theme-text-secondary">
                      Filtering by namespace: <span className="font-medium text-theme-text-primary">{namespaces.length === 1 ? namespaces[0] : `${namespaces.length} namespaces`}</span>
                    </p>
                  )}
                </>
              )
            })()}
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
                    ({pluralize(group.items.length, 'item')})
                  </span>
                </div>

                {/* Activity list */}
                <div className="space-y-2 ml-6 border-l-2 border-theme-border pl-4">
                  {group.items.map((aggItem) => (
                    aggItem.type === 'aggregated' ? (
                      <div
                        key={`agg-${aggItem.first.id}-${aggItem.last.id}`}
                        data-event-id={aggItem.first.id}
                        data-ts-from={new Date(aggItem.first.timestamp).getTime()}
                        data-ts-to={new Date(aggItem.last.timestamp).getTime()}
                      >
                        <AggregatedActivityCard
                          first={aggItem.first}
                          last={aggItem.last}
                          count={aggItem.count}
                          reason={aggItem.reason}
                          expanded={expandedItem === aggItem.first.id}
                          onToggle={() => setExpandedItem(expandedItem === aggItem.first.id ? null : aggItem.first.id)}
                          onResourceClick={onResourceClick}
                          compact={compact}
                          selected={selectedEventId === aggItem.first.id}
                          onSelect={() => onSelectEvent?.(selectedEventId === aggItem.first.id ? null : aggItem.first.id)}
                        />
                      </div>
                    ) : (
                      <div
                        key={aggItem.item.id}
                        data-event-id={aggItem.item.id}
                        data-ts-from={new Date(aggItem.item.timestamp).getTime()}
                        data-ts-to={new Date(aggItem.item.timestamp).getTime()}
                      >
                        <ActivityCard
                          item={aggItem.item}
                          expanded={expandedItem === aggItem.item.id}
                          onToggle={() => setExpandedItem(expandedItem === aggItem.item.id ? null : aggItem.item.id)}
                          onResourceClick={onResourceClick}
                          compact={compact}
                          selected={selectedEventId === aggItem.item.id}
                          onSelect={() => onSelectEvent?.(selectedEventId === aggItem.item.id ? null : aggItem.item.id)}
                        />
                      </div>
                    )
                  ))}
                </div>
              </div>
            ))}
            {truncatedAt != null && events.length >= truncatedAt && (
              <div className="flex items-center justify-center gap-2 py-4 text-sm text-theme-text-tertiary">
                <Clock className="h-4 w-4 opacity-50" />
                <span>Showing the newest {truncatedAt.toLocaleString()} events in this range — narrow the query to see older ones</span>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

interface ActivityCardProps {
  item: TimelineEvent
  expanded: boolean
  onToggle: () => void
  onResourceClick?: NavigateToResource
  compact?: boolean
  selected?: boolean
  onSelect?: () => void
}

function ActivityCard({ item, expanded, onToggle, onResourceClick, compact, selected, onSelect }: ActivityCardProps) {
  const isChange = isChangeEvent(item)
  const isHistorical = isHistoricalEvent(item)
  const isWarning = item.eventType === 'Warning'
  const time = formatTime(item.timestamp, compact)

  // Only expandable if there's a diff to show
  const hasExpandableContent = isChange && !!item.diff

  // Color budget: a change's operation is carried by the icon SHAPE, not
  // by card/text hue — a routine delete must not read as an alarming red failure;
  // the swimlane paints it a neutral blue ▼ and the list matches. Color is
  // reserved for status: only a Warning tints the card (amber); the per-event
  // health badge below carries any unhealthy state.
  const getCardStyle = () =>
    isWarning
      ? 'bg-amber-500/5 border-amber-500/30 hover:border-amber-500/50'
      : 'bg-theme-surface/50 border-theme-border hover:border-theme-border-light'

  const getIcon = () => {
    if (isChange) {
      // One activity hue (info-blue); Plus/Trash2/RefreshCw distinguish add/delete/update.
      switch (item.eventType) {
        case 'add':
          return <Plus className="w-4 h-4 text-blue-400" />
        case 'delete':
          return <Trash2 className="w-4 h-4 text-blue-400" />
        case 'update':
          return <RefreshCw className="w-4 h-4 text-blue-400" />
        default:
          return <CheckCircle className="w-4 h-4 text-theme-text-secondary" />
      }
    }
    if (isWarning) {
      return <AlertCircle className="w-4 h-4 text-amber-400" />
    }
    return <CheckCircle className="w-4 h-4 text-theme-text-secondary" />
  }

  return (
    <div
      className={clsx(
        'rounded-lg border transition-all',
        (hasExpandableContent || onSelect) && 'cursor-pointer',
        getCardStyle(),
        selected && 'ring-2 ring-skyhook-500/60 ring-offset-1 ring-offset-theme-base',
      )}
      onClick={() => { if (onSelect) { onSelect() } else if (hasExpandableContent) { onToggle() } }}
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
                  onResourceClick?.({ kind: kindToPlural(item.kind), namespace: item.namespace, name: item.name, group: apiVersionToGroup(item.apiVersion) })
                }}
                className="flex items-center gap-2 hover:bg-theme-elevated/50 rounded px-1 -ml-1 transition-colors group"
              >
                <span className="badge-sm bg-theme-elevated text-theme-text-secondary group-hover:bg-theme-hover">
                  {item.kind || 'Event'}
                </span>
                <span className="text-sm font-medium text-theme-text-primary truncate group-hover:text-blue-300">{item.name}</span>
              </button>
              {item.namespace && <span className="text-xs text-theme-text-tertiary">in {item.namespace}</span>}
              {item.owner && (
                <span className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                  <span className="text-xs text-theme-text-quaternary">←</span>
                  {item.owner.kind === 'ReplicaSet' ? (
                    <ResourceRefBadge
                      resourceRef={{ kind: 'Deployment', namespace: item.namespace, name: item.owner.name.replace(/-[a-z0-9]+$/, '') }}
                      onClick={(ref) => onResourceClick?.(refToSelectedResource(ref))}
                    />
                  ) : (
                    <ResourceRefBadge
                      resourceRef={{ kind: item.owner.kind, namespace: item.namespace, name: item.owner.name }}
                      onClick={(ref) => onResourceClick?.(refToSelectedResource(ref))}
                    />
                  )}
                </span>
              )}
              {item.createdAt && (
                <Tooltip content={`Created: ${new Date(item.createdAt).toLocaleString()}`}>
                  <span className="text-xs text-theme-text-quaternary">
                    • {formatResourceAge(item.createdAt)} old
                  </span>
                </Tooltip>
              )}
            </div>

            {/* Activity details */}
            <div className="mt-1 flex items-center gap-2 flex-wrap">
              {isChange ? (
                <>
                  <span className="text-sm font-medium text-theme-text-primary">
                    {isHistorical && item.reason ? item.reason : item.eventType}
                  </span>
                  {item.diff && <DiffBadge diff={item.diff} />}
                  {item.healthState && item.healthState !== 'unknown' && (
                    <span className={clsx('badge-sm', getHealthBadgeColor(item.healthState))}>
                      {item.healthState}
                    </span>
                  )}
                  {isHistorical && item.message && (
                    <span className="min-w-0 flex-1 break-words text-sm text-theme-text-secondary">
                      {item.message}
                    </span>
                  )}
                </>
              ) : (
                <>
                  <span className={clsx('text-sm font-medium', isWarning ? 'text-amber-700 dark:text-amber-300' : 'text-theme-text-secondary')}>
                    {item.reason}
                  </span>
                  <span className="min-w-0 flex-1 break-words text-sm text-theme-text-secondary">
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

          {/* Expand toggle — its own control so a select-click on the card body
              doesn't also flip the diff open/closed. */}
          {hasExpandableContent && (
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onToggle() }}
              aria-label={expanded ? 'Collapse changes' : 'Expand changes'}
              className="shrink-0 rounded p-0.5 text-theme-text-disabled hover:bg-theme-elevated hover:text-theme-text-secondary"
            >
              <ChevronRight className={clsx('w-4 h-4 transition-transform', expanded && 'rotate-90')} />
            </button>
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
  compact?: boolean
  selected?: boolean
  onSelect?: () => void
}

function AggregatedActivityCard({ first, last, count, reason, expanded, onToggle, onResourceClick, compact, selected, onSelect }: AggregatedActivityCardProps) {
  const isWarning = first.eventType === 'Warning'
  const isUnhealthy = !isWarning && first.healthState === 'unhealthy'
  const firstTime = formatTime(first.timestamp, compact)
  const lastTime = formatTime(last.timestamp, compact)

  // Same color budget as a single ActivityCard: a Warning tints amber and
  // a genuinely unhealthy aggregate goes red; everything else stays neutral with
  // the blue activity accent, so a cluster of routine repeats doesn't read as an
  // alarm.
  const cardStyle = isWarning
    ? 'bg-amber-500/5 border-amber-500/30 hover:border-amber-500/50'
    : isUnhealthy
      ? 'bg-red-500/5 border-red-500/30 hover:border-red-500/50'
      : 'bg-theme-surface/50 border-theme-border hover:border-theme-border-light'

  const dotColor = isWarning ? 'bg-amber-500' : isUnhealthy ? 'bg-red-500' : 'bg-blue-500'
  const lineColor = isWarning ? 'bg-amber-500/40' : isUnhealthy ? 'bg-red-500/40' : 'bg-blue-500/40'
  const textColor = isWarning ? 'text-amber-400' : isUnhealthy ? 'text-red-400' : 'text-theme-text-secondary'
  const countBadge = isWarning
    ? SEVERITY_BADGE.warning
    : isUnhealthy
      ? SEVERITY_BADGE.error
      : 'bg-theme-elevated text-theme-text-secondary'

  return (
    <div
      className={clsx(
        'rounded-lg border transition-all cursor-pointer',
        cardStyle,
        selected && 'ring-2 ring-skyhook-500/60 ring-offset-1 ring-offset-theme-base',
      )}
      onClick={() => { if (onSelect) { onSelect() } else { onToggle() } }}
    >
      <div className="p-3">
        {/* Header row */}
        <div className="flex items-start gap-3">
          {/* Aggregation visualization: first dot - line - last dot */}
          <div className="flex flex-col items-center shrink-0 mt-0.5">
            {/* First occurrence dot */}
            <Tooltip content={`First: ${firstTime}`}>
              <div className={clsx('w-2.5 h-2.5 rounded-full', dotColor)} />
            </Tooltip>
            {/* Connecting line */}
            <div className={clsx('w-0.5 h-4 my-0.5', lineColor)} />
            {/* Last occurrence dot */}
            <Tooltip content={`Last: ${lastTime}`}>
              <div className={clsx('w-2.5 h-2.5 rounded-full', dotColor)} />
            </Tooltip>
          </div>

          {/* Content */}
          <div className="flex-1 min-w-0">
            {/* Resource info */}
            <div className="flex items-center gap-2 flex-wrap">
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onResourceClick?.({ kind: kindToPlural(first.kind), namespace: first.namespace, name: first.name, group: apiVersionToGroup(first.apiVersion) })
                }}
                className="flex items-center gap-2 hover:bg-theme-elevated/50 rounded px-1 -ml-1 transition-colors group"
              >
                <span className="badge-sm bg-theme-elevated text-theme-text-secondary group-hover:bg-theme-hover">
                  {first.kind || 'Event'}
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
              <span className={clsx('badge-sm', countBadge)}>
                x{count}
              </span>
              <span className="text-xs text-theme-text-tertiary">
                {firstTime} → {lastTime}
              </span>
            </div>
          </div>

          {/* Expand toggle — its own control, separate from the card select-click. */}
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); onToggle() }}
            aria-label={expanded ? 'Collapse occurrences' : 'Expand occurrences'}
            className="shrink-0 rounded p-0.5 text-theme-text-disabled hover:bg-theme-elevated hover:text-theme-text-secondary"
          >
            <ChevronRight className={clsx('w-4 h-4 transition-transform', expanded && 'rotate-90')} />
          </button>
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

function formatTime(timestamp: string, compact = false): string {
  if (!timestamp) return '-'
  const date = new Date(timestamp)
  if (isNaN(date.getTime())) return '-'
  // Compact (date-grouped) rows show the clock time — the group header carries
  // the date, and "5m ago" / a bare date is useless for a batch of same-day events.
  if (compact) return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)

  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  return date.toLocaleDateString()
}
