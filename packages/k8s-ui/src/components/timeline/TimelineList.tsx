import { memo, useState, useMemo, useEffect, useLayoutEffect, useCallback, useRef, type ReactNode } from 'react'
import { useDebouncedValue } from '../../hooks/useDebouncedValue'
import { PaneLoader } from '../ui/PaneLoader'
import { Tooltip } from '../ui/Tooltip'
import {
  AlertCircle,
  CheckCircle,
  Clock,
  Eye,
  Info,
  RefreshCw,
  Plus,
  Trash2,
  Shield,
  X,
} from 'lucide-react'
import { clsx } from 'clsx'
import { Virtuoso, type VirtuosoHandle, type ListItem } from 'react-virtuoso'
import { Collapse, CollapseChevron } from '../ui/Collapse'
import { DURATION_DISCLOSURE, prefersReducedMotion } from '../../utils/animation'
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
import { builtinGroupForKind } from '../../utils/api-resources'
import { kindToPluralWithGroup, refToSelectedResource, apiVersionToGroup } from '../../utils/navigation'
import { pluralize } from '../../utils/pluralize'
import { formatCompactAge } from '../../utils/format'

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
  // Hosts that filter a capped source after fetching must report truncation
  // explicitly because the visible row count no longer reflects the source cap.
  isTruncated?: boolean
  truncationMessage?: string
  // Routine activity toggle, shown when `onShowRoutineChange` is set.
  // `routineEvents` are the routine rows the host holds: left out of `events`
  // while hidden, included while shown. Counted here through the active filters.
  showRoutine?: boolean
  onShowRoutineChange?: (show: boolean) => void
  routineEvents?: TimelineEvent[]
  // Rows for which this returns true fold into one card per resource per time
  // group, whatever their reason. The host uses it for problems on pods and
  // other child resources, so a crash-looping pod is one card, not dozens.
  foldPerResource?: (event: TimelineEvent) => boolean
}

// Every row stays mounted in compact mode and at or below this many rows, so
// browser find, select and copy, screen readers and focus see the whole list.
// The compact embeds (workload, CNPG) have no search box of their own, so
// browser find is their only text search; the full timeline has one.
const FULL_MOUNT_MAX_ROWS = 300
const FULL_MOUNT_VIEWPORT_PX = 10_000_000
const WINDOWED_VIEWPORT_PX = 200
// Only the first rows of an opening or closing fold are listed while it
// animates; the rest join once it has opened and leave as it starts to close.
// Virtuoso mounts as many rows as fit, rows collapsed to nothing would all fit
// at once, and later rows shown at full height would sit in view ahead of the
// reveal.
const FOLD_REVEAL_ROWS = 20

type AggregatedItem = {
  type: 'single'
  item: TimelineEvent
} | {
  type: 'aggregated'
  first: TimelineEvent
  last: TimelineEvent
  count: number
  reason: string
} | {
  type: 'folded'
  // Stable across live updates, so an opened fold stays open as rows arrive.
  key: string
  latest: TimelineEvent
  // Newest first.
  others: TimelineEvent[]
}

// An open fold's children are rows of their own, so jump-to-time can reach
// them and a large open fold mounts only what is near the viewport.
type ListRow = {
  type: 'header'
  key: string
  label: string
  count: number
} | {
  type: 'activity'
  key: string
  item: AggregatedItem
  eventId: string
  fromMs: number
  toMs: number
  lastInGroup: boolean
  foldRowsShown: boolean
} | {
  type: 'foldChild'
  key: string
  foldKey: string
  event: TimelineEvent
  eventId: string
  fromMs: number
  toMs: number
  lastInFold: boolean
  lastInGroup: boolean
}

const TIME_RANGES: { value: TimeRange; label: string }[] = [
  { value: '5m', label: '5 min' },
  { value: '30m', label: '30 min' },
  { value: '1h', label: '1 hour' },
  { value: '6h', label: '6 hours' },
  { value: '24h', label: '24 hours' },
  { value: 'all', label: 'All' },
]

export function TimelineList({ events, isLoading, onRefresh, onQueryChange, hasLimitedAccess, namespaces, onViewChange, currentView = 'list', onResourceClick, initialFilter, initialTimeRange, rangeOptions = TIME_RANGES, hideRangeSelector = false, showDeleted: showDeletedProp, onShowDeletedChange, search: searchProp, onSearchChange, activityFilter: activityFilterProp, onActivityFilterChange, kindFilter: kindFilterProp, onKindFilterChange, onVisibleWindowChange, scrollToMs, compact = false, selectedEventId, onSelectEvent, truncatedAt, isTruncated, truncationMessage, showRoutine = false, onShowRoutineChange, routineEvents, foldPerResource }: TimelineListProps) {
  const [searchInternal, setSearchInternal] = useState('')
  const searchTerm = searchProp ?? searchInternal
  const setSearchTerm = onSearchChange ?? setSearchInternal
  // Coalesce typing bursts: re-filtering the aggregated list per keystroke is
  // what makes the search box feel dead on large timelines. Clearing flushes
  // immediately.
  const debouncedSearchTerm = useDebouncedValue(searchTerm, 300, (v) => v === '')
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
  const [openFolds, setOpenFolds] = useState<Set<string>>(() => new Set())

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
  const matchesListFilters = useCallback((item: TimelineEvent) => {
    if (!matchesActivityFilter(item, activityTypeFilter)) return false
    if (kindFilter.length > 0 && !kindFilter.includes(item.kind)) return false
    if (!showDeleted && item.eventType === 'delete') return false
    if (!matchesTimelineSearch(item, debouncedSearchTerm)) return false
    return true
  }, [activityTypeFilter, kindFilter, debouncedSearchTerm, showDeleted])
  const filteredActivity = useMemo(() => (events ? events.filter(matchesListFilters) : []), [events, matchesListFilters])
  // Counted through the same filters, so the number is exactly what the toggle adds or removes.
  const routineCount = useMemo(
    () => (routineEvents ? routineEvents.filter(matchesListFilters).length : 0),
    [routineEvents, matchesListFilters],
  )
  const canShowRoutine = !showRoutine && routineCount > 0 && !!onShowRoutineChange

  // Aggregate repeated events for the same resource with the same reason
  const aggregateEvents = (items: TimelineEvent[]): AggregatedItem[] => {
    if (items.length === 0) return []

    // Group events by resource+reason
    const groups = new Map<string, TimelineEvent[]>()
    const singleEvents: TimelineEvent[] = []

    for (const item of items) {
      if (item.id === selectedEventId) {
        singleEvents.push(item)
        continue
      }
      if (foldPerResource?.(item)) {
        const key = `fold:${item.kind}:${item.namespace}:${item.name}`
        const existing = groups.get(key) || []
        existing.push(item)
        groups.set(key, existing)
        continue
      }
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
    for (const [key, events] of groups.entries()) {
      if (events.length >= 2 && key.startsWith('fold:')) {
        events.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
        result.push({ type: 'folded', key, latest: events[0], others: events.slice(1) })
      } else if (events.length >= 2) {
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
      const newest = (x: AggregatedItem) => x.type === 'aggregated' ? x.last : x.type === 'folded' ? x.latest : x.item
      const timeA = new Date(newest(a).timestamp).getTime()
      const timeB = new Date(newest(b).timestamp).getTime()
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
  }, [filteredActivity, compact, foldPerResource, selectedEventId])

  const [foldMotion, setFoldMotion] = useState<ReadonlyMap<string, 'opening' | 'closing'>>(() => new Map())
  const listRows = useMemo<ListRow[]>(() => {
    const rows: ListRow[] = []
    for (const group of groupedActivity) {
      rows.push({ type: 'header', key: `header:${group.label}`, label: group.label, count: group.items.length })
      group.items.forEach((item, index) => {
        const lastInGroup = index === group.items.length - 1
        if (item.type === 'folded') {
          const key = `${group.label}:${item.key}`
          const latestMs = new Date(item.latest.timestamp).getTime()
          const children = !openFolds.has(key) ? []
            : foldMotion.has(key) ? item.others.slice(0, FOLD_REVEAL_ROWS) : item.others
          rows.push({
            type: 'activity', key, item, eventId: item.latest.id,
            // Open, the children report their own times; closed, the head
            // stands for the whole fold.
            fromMs: children.length > 0 ? latestMs : new Date(item.others[item.others.length - 1].timestamp).getTime(),
            toMs: latestMs, lastInGroup, foldRowsShown: children.length > 0,
          })
          children.forEach((event, indexInFold) => {
            const ms = new Date(event.timestamp).getTime()
            rows.push({
              type: 'foldChild', key: `${key}:${event.id}`, foldKey: key, event, eventId: event.id,
              fromMs: ms, toMs: ms, lastInFold: indexInFold === children.length - 1, lastInGroup,
            })
          })
          return
        }
        const first = item.type === 'aggregated' ? item.first : item.item
        const last = item.type === 'aggregated' ? item.last : item.item
        rows.push({
          type: 'activity',
          key: `${group.label}:${item.type === 'aggregated' ? `agg-${item.first.id}-${item.last.id}` : item.item.id}`,
          item, eventId: first.id,
          fromMs: new Date(first.timestamp).getTime(),
          toMs: new Date(last.timestamp).getTime(),
          lastInGroup, foldRowsShown: false,
        })
      })
    }
    return rows
  }, [groupedActivity, openFolds, foldMotion])
  // A row's identity across updates, independent of its index and time group:
  // its oldest event, which newer arrivals joining the row don't change.
  const anchorId = (row: ListRow) => row.type === 'header' ? row.key
    : row.type === 'foldChild' ? `event:${row.eventId}`
    : row.item.type === 'folded' ? `fold:${row.item.others[row.item.others.length - 1].id}`
    : `event:${row.eventId}`

  useEffect(() => {
    setOpenFolds((previous) => {
      if (previous.size === 0) return previous
      const keys = new Set(listRows.map((row) => row.key))
      const next = new Set([...previous].filter((key) => keys.has(key)))
      return next.size === previous.size ? previous : next
    })
  }, [listRows])

  // Closing keeps the first rows mounted while they animate shut, then drops
  // them; reduced motion skips straight to the end state.
  const foldTimers = useRef(new Map<string, number>())
  useEffect(() => {
    const timers = foldTimers.current
    return () => timers.forEach((timer) => window.clearTimeout(timer))
  }, [])
  const setFoldPhase = useCallback((key: string, phase: 'opening' | 'closing' | null) => {
    window.clearTimeout(foldTimers.current.get(key))
    foldTimers.current.delete(key)
    setFoldMotion((previous) => {
      if (previous.get(key) === (phase ?? undefined)) return previous
      const next = new Map(previous)
      if (phase) next.set(key, phase)
      else next.delete(key)
      return next
    })
    if (!phase) return
    foldTimers.current.set(key, window.setTimeout(() => {
      foldTimers.current.delete(key)
      setFoldMotion((previous) => {
        const next = new Map(previous)
        next.delete(key)
        return next
      })
      if (phase === 'closing') setOpenFolds((previous) => {
        const next = new Set(previous)
        next.delete(key)
        return next
      })
    }, DURATION_DISCLOSURE))
  }, [])
  const isFoldOpen = useCallback((key: string) => openFolds.has(key) && foldMotion.get(key) !== 'closing', [openFolds, foldMotion])
  const toggleFold = useCallback((key: string) => {
    const animate = !prefersReducedMotion()
    if (isFoldOpen(key)) {
      if (animate) {
        setFoldPhase(key, 'closing')
        return
      }
      setFoldPhase(key, null)
      setOpenFolds((previous) => {
        const next = new Set(previous)
        next.delete(key)
        return next
      })
      return
    }
    setOpenFolds((previous) => new Set(previous).add(key))
    setFoldPhase(key, animate ? 'opening' : null)
  }, [isFoldOpen, setFoldPhase])

  // Only rows near the viewport are mounted, so the visible window reported to
  // a host scrubber (its lens) comes from the rendered items' offsets and time
  // spans, not from the DOM.
  const virtuosoRef = useRef<VirtuosoHandle>(null)
  const [scroller, setScroller] = useState<HTMLElement | null>(null)
  const renderedItems = useRef<ListItem<ListRow>[]>([])
  const [listReady, setListReady] = useState(false)
  // The reader's place: a row (by identity, not index), its offset from the
  // top of the scrollport, and the scroll position it was taken at. Re-applied
  // when rows are inserted above it or rows above it change height; Virtuoso
  // turns off the browser's own scroll anchoring.
  const anchor = useRef<{ id: string; delta: number; scrollTop: number } | null>(null)
  const committedRows = useRef<ListRow[] | null>(null)
  const handleScrollerRef = useCallback((element: HTMLElement | Window | null) => {
    const next = element instanceof HTMLElement ? element : null
    setScroller(next)
    if (next) return
    setListReady(false)
    renderedItems.current = []
    anchor.current = null
  }, [])
  const visibleWindowRaf = useRef<number | null>(null)
  // Set while the range is re-derived on leaving full-mount mode; the
  // intermediate positions are not the reader's.
  const rewindowing = useRef(false)
  const scrolledToRef = useRef<number | null>(null)
  const reportedWindow = useRef<{ callback: typeof onVisibleWindowChange; fromMs: number; toMs: number } | null>(null)
  const reportVisibleWindow = useCallback(() => {
    if (!onVisibleWindowChange || rewindowing.current) return
    // Until a requested jump has landed, the rows on screen are not the ones
    // the host asked to see.
    if (scrollToMs != null && scrolledToRef.current !== scrollToMs && (isLoading || listRows.length > 0)) return
    let fromMs = Infinity
    let toMs = -Infinity
    if (scroller && !isLoading && listRows.length > 0) {
      const top = scroller.scrollTop
      const bottom = top + scroller.clientHeight
      // The rendered range includes overscan; only its intersection with the
      // actual scrollport belongs in the host's lens.
      for (const row of renderedItems.current) {
        if (row.offset + row.size < top || row.offset > bottom) continue
        const data = row.data
        if (!data || data.type === 'header') continue
        if (Number.isFinite(data.fromMs)) fromMs = Math.min(fromMs, data.fromMs)
        if (Number.isFinite(data.toMs)) toMs = Math.max(toMs, data.toMs)
      }
    }
    const previous = reportedWindow.current
    if (previous?.callback === onVisibleWindowChange && previous.fromMs === fromMs && previous.toMs === toMs) return
    reportedWindow.current = { callback: onVisibleWindowChange, fromMs, toMs }
    onVisibleWindowChange(fromMs <= toMs ? { fromMs, toMs } : null)
  }, [onVisibleWindowChange, scroller, isLoading, listRows, scrollToMs])
  const captureAnchor = useCallback(() => {
    const rows = committedRows.current
    if (!scroller || !rows) return
    const top = scroller.scrollTop
    for (const item of renderedItems.current) {
      // Virtuoso reports the new rows before they are committed here; their
      // offsets still describe the old layout.
      if (rows[item.index] !== item.data || !item.data) return
      if (item.size > 0 && item.offset + item.size > top) {
        anchor.current = { id: anchorId(item.data), delta: item.offset - top, scrollTop: top }
        return
      }
    }
  }, [scroller])
  // A re-measured row above the reader (a fold whose newest card got longer, a
  // narrower window wrapping text) moves the anchor without any scroll, far
  // enough to leave the rendered range. Only while the scroll position is the
  // one the anchor was taken at: any scroll, the user's or a jump, re-captures.
  const holdAnchor = useCallback(() => {
    const place = anchor.current
    const rows = committedRows.current
    if (!place || !rows || !scroller) return false
    const top = scroller.scrollTop
    if (top <= 0 || top !== place.scrollTop) return false
    // Rows not yet committed here: the layout effect below handles that change.
    if (renderedItems.current.some((rendered) => rows[rendered.index] !== rendered.data)) return true
    const item = renderedItems.current.find((rendered) => rendered.data && anchorId(rendered.data) === place.id)
    const index = item ? item.index : rows.findIndex((row) => anchorId(row) === place.id)
    if (index < 0) return false
    if (!item) {
      virtuosoRef.current?.scrollToIndex({ index, align: 'start', offset: -place.delta })
      anchor.current = { ...place, scrollTop: scroller.scrollTop }
    } else if (Math.abs(item.offset - top - place.delta) >= 1) {
      // A plain scroll: scrollToIndex keeps re-aiming for a while as rows are
      // measured, which would undo a scroll the user starts meanwhile.
      virtuosoRef.current?.scrollTo({ top: item.offset - place.delta })
      anchor.current = { ...place, scrollTop: scroller.scrollTop }
    }
    return true
  }, [scroller])
  // A focused card that scrolls out of the mounted range is removed from the
  // DOM and focus falls to the body. Focus the same control in the card's new
  // copy if there is one (it can be remounted at once), else the list. Checked
  // a frame later: Chromium blurs the card before removing it, and the rows
  // Virtuoso reports are committed after its callback.
  const focusedInList = useRef<HTMLElement | null>(null)
  const restoreFocusLater = useCallback(() => {
    if (!focusedInList.current || !scroller) return
    requestAnimationFrame(() => {
      const focused = focusedInList.current
      if (!focused || !scroller.isConnected) return
      if (focused.isConnected) {
        // Still in the DOM: focus left by the user's choice.
        if (!scroller.contains(document.activeElement) || document.activeElement === scroller) focusedInList.current = null
        return
      }
      focusedInList.current = null
      if (document.activeElement !== document.body && document.activeElement) return
      const card = focused.closest<HTMLElement>('[data-event-id]')
      const position = card ? [...card.querySelectorAll('button')].indexOf(focused as HTMLButtonElement) : -1
      const copy = card && [...scroller.querySelectorAll<HTMLElement>('[data-event-id]')].find((element) => element.dataset.eventId === card.dataset.eventId)
      const replacement = position >= 0 ? copy?.querySelectorAll('button')[position] : undefined
      ;(replacement ?? scroller).focus({ preventScroll: true })
    })
  }, [scroller])
  const handleItemsRendered = useCallback((items: ListItem<ListRow>[]) => {
    renderedItems.current = items
    setListReady(items.some((row) => row.size > 0))
    if (rewindowing.current) return
    if (!holdAnchor()) captureAnchor()
    reportVisibleWindow()
    restoreFocusLater()
  }, [holdAnchor, captureAnchor, reportVisibleWindow, restoreFocusLater])
  useEffect(() => {
    if (!scroller) return
    const onFocusIn = (event: FocusEvent) => {
      focusedInList.current = event.target instanceof HTMLElement && event.target !== scroller ? event.target : null
    }
    const onFocusOut = (event: FocusEvent) => {
      if (!scroller.contains(event.relatedTarget as Node | null)) restoreFocusLater()
    }
    scroller.addEventListener('focusin', onFocusIn)
    scroller.addEventListener('focusout', onFocusOut)
    return () => {
      scroller.removeEventListener('focusin', onFocusIn)
      scroller.removeEventListener('focusout', onFocusOut)
    }
  }, [scroller, restoreFocusLater])
  const handleListScroll = useCallback(() => {
    if (rewindowing.current) return
    // The event for a position this list set itself arrives late; only a real
    // move takes a new anchor, else a stale layout could replace the reader's.
    if (!scroller || scroller.scrollTop !== anchor.current?.scrollTop) captureAnchor()
    if (!onVisibleWindowChange || visibleWindowRaf.current != null) return
    visibleWindowRaf.current = requestAnimationFrame(() => {
      visibleWindowRaf.current = null
      reportVisibleWindow()
    })
  }, [captureAnchor, onVisibleWindowChange, reportVisibleWindow, scroller])
  useEffect(() => {
    reportVisibleWindow()
    return () => {
      if (visibleWindowRaf.current != null) {
        cancelAnimationFrame(visibleWindowRaf.current)
        visibleWindowRaf.current = null
      }
    }
  }, [reportVisibleWindow])

  // Rows inserted above the reader (new events in a newest-first list, a new
  // group header, an aggregate moving to the top) would otherwise slide the
  // text being read down out of view. At the very top, new events simply
  // appear above.
  useLayoutEffect(() => {
    const previous = committedRows.current
    committedRows.current = listRows
    const place = anchor.current
    if (!previous || previous === listRows || !place || !scroller || scroller.scrollTop <= 0) return
    const index = listRows.findIndex((row) => anchorId(row) === place.id)
    if (index < 0 || index === previous.findIndex((row) => anchorId(row) === place.id)) return
    virtuosoRef.current?.scrollToIndex({ index, align: 'start', offset: -place.delta })
    anchor.current = { ...place, scrollTop: scroller.scrollTop }
  }, [listRows, scroller])

  // Carry the swimlane's view window into the list: once rows exist, scroll so
  // the row nearest `scrollToMs` sits at the top. Applied once per `scrollToMs`
  // (a fresh view switch), so it never fights the user's own scrolling after.
  useEffect(() => {
    // While a fold opens only its first rows are listed; the target may be later.
    if (isLoading || !scroller || !listReady || scrollToMs == null || scrolledToRef.current === scrollToMs || foldMotion.size > 0) return
    let bestIndex = -1
    let bestDelta = Infinity
    listRows.forEach((row, index) => {
      if (row.type === 'header') return
      const delta = Math.abs(row.fromMs - scrollToMs)
      if (delta < bestDelta) { bestDelta = delta; bestIndex = index }
    })
    if (bestIndex >= 0) {
      const before = scroller.scrollTop
      scrolledToRef.current = scrollToMs
      virtuosoRef.current?.scrollToIndex({ index: bestIndex, align: 'start' })
      // A jump that lands where the list already sits emits no scroll event or
      // range change, so report here. After a real jump the rendered items are
      // still the old ones until Virtuoso re-renders, which reports by itself.
      if (scroller.scrollTop === before) reportVisibleWindow()
    }
  }, [scrollToMs, listRows, listReady, scroller, isLoading, reportVisibleWindow, foldMotion])

  // Bidirectional sync: when the selection changes externally (a swimlane marker
  // click), scroll the matching card into view, mounted or not. Re-runs on the
  // row set so a selection made before the rows exist still lands. Smooth only
  // for a card already rendered: a smooth scroll to a distant one crawls
  // through rows Virtuoso has yet to measure.
  useEffect(() => {
    if (isLoading || !scroller || !listReady || !selectedEventId) return
    const index = listRows.findIndex((row) => row.type !== 'header' && row.eventId === selectedEventId)
    if (index < 0) return
    const rendered = renderedItems.current
    const nearby = rendered.length > 0 && index >= rendered[0].index && index <= rendered[rendered.length - 1].index
    virtuosoRef.current?.scrollToIndex({ index, align: 'center', behavior: nearby && !prefersReducedMotion() ? 'smooth' : 'auto' })
  }, [selectedEventId, listRows, listReady, scroller, isLoading])

  const mountAllRows = compact || listRows.length <= FULL_MOUNT_MAX_ROWS
  // Virtuoso only re-derives its rendered range when the viewport nears that
  // range's edge, so leaving full-mount mode while scrolled would keep every row
  // mounted. A scroll to the top and straight back, in one task so nothing in
  // between paints, makes it window around the reader again. (Remounting it
  // instead blanks the list for four frames.)
  const wasMountAll = useRef(mountAllRows)
  useLayoutEffect(() => {
    const was = wasMountAll.current
    wasMountAll.current = mountAllRows
    if (!was || mountAllRows || !scroller) return
    // After the commit, once Virtuoso has rendered the new rows.
    queueMicrotask(() => {
      const top = scroller.scrollTop
      if (top <= 0 || !scroller.isConnected) return
      rewindowing.current = true
      scroller.scrollTop = 0
      scroller.dispatchEvent(new Event('scroll'))
      scroller.scrollTop = top
      scroller.dispatchEvent(new Event('scroll'))
      rewindowing.current = false
      if (!holdAnchor()) captureAnchor()
      reportVisibleWindow()
      restoreFocusLater()
    })
  }, [mountAllRows, scroller, holdAnchor, captureAnchor, reportVisibleWindow, restoreFocusLater])

  const listComponents = useMemo(() => ({
    Footer: () => (
      <div className="px-4 pb-4">
        {truncatedAt != null && (isTruncated ?? events.length >= truncatedAt) && (
          <div className="flex items-center justify-center gap-2 pt-10 pb-4 text-sm text-theme-text-tertiary">
            <Clock className="h-4 w-4 opacity-50" />
            <span>{truncationMessage ?? `Showing the newest ${truncatedAt.toLocaleString()} events in this range — narrow the query to see older ones`}</span>
          </div>
        )}
      </div>
    ),
  }), [truncatedAt, isTruncated, events.length, truncationMessage])

  const toggleExpanded = useCallback((id: string) => setExpandedItem((previous) => (previous === id ? null : id)), [])
  // Rows are memoized on this, so Virtuoso's measuring re-renders skip the cards.
  const rowContext = useMemo<RowContext>(() => ({
    expandedItem, toggleExpanded, onResourceClick, compact, selectedEventId, onSelectEvent, foldMotion, isFoldOpen, toggleFold,
  }), [expandedItem, toggleExpanded, onResourceClick, compact, selectedEventId, onSelectEvent, foldMotion, isFoldOpen, toggleFold])

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


      {onShowRoutineChange && (
        <RoutineActivityToggle shown={showRoutine} count={routineCount} onChange={onShowRoutineChange} />
      )}

      {/* Timeline content */}
      <div className="flex-1 min-h-0 overflow-auto">
        {isLoading ? (
          <PaneLoader label="Loading timeline…" className="h-full" />
        ) : filteredActivity.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
            <Clock className="w-12 h-12 mb-4 opacity-50" />
            {(() => {
              const activeFilters = describeActiveFilters({ search: searchTerm, activityFilter: activityTypeFilter, kindFilter, showDeleted })
              return (
                <>
                  {canShowRoutine ? (
                    <>
                      <p className="text-lg">All matching activity is routine</p>
                      <p className="text-sm mt-2 max-w-md text-center">
                        {pluralize(routineCount, 'routine event')} {routineCount === 1 ? 'matches' : 'match'}. Warnings and failures would show here.
                      </p>
                      <div className="mt-4 flex items-center gap-2">
                        <button
                          type="button"
                          onClick={() => onShowRoutineChange?.(true)}
                          className="btn-brand flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm"
                        >
                          <Eye className="w-3.5 h-3.5" />
                          Show routine activity
                        </button>
                        {activeFilters && (
                          <button
                            type="button"
                            onClick={clearAllFilters}
                            className="flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-lg text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors"
                          >
                            <X className="w-3.5 h-3.5" />
                            Clear filters
                          </button>
                        )}
                      </div>
                    </>
                  ) : (
                    <>
                      <p className="text-lg">No activity found</p>
                      <p className="text-sm mt-2">
                        {activeFilters || 'Activity will appear here when cluster changes occur'}
                      </p>
                    </>
                  )}
                  {activeFilters && !canShowRoutine && (
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
          <Virtuoso
            ref={virtuosoRef}
            className="h-full w-full focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent"
            scrollerRef={handleScrollerRef}
            onScroll={handleListScroll}
            data={listRows}
            computeItemKey={(_index, row) => row.key}
            initialItemCount={mountAllRows ? listRows.length : 10}
            increaseViewportBy={mountAllRows ? FULL_MOUNT_VIEWPORT_PX : WINDOWED_VIEWPORT_PX}
            itemsRendered={handleItemsRendered}
            components={listComponents}
            itemContent={(index, row) => <TimelineRow row={row} first={index === 0} context={rowContext} />}
          />
        )}
      </div>
    </div>
  )
}

interface RowContext {
  expandedItem: string | null
  toggleExpanded: (id: string) => void
  onResourceClick?: NavigateToResource
  compact: boolean
  selectedEventId?: string | null
  onSelectEvent?: (id: string | null) => void
  foldMotion: ReadonlyMap<string, 'opening' | 'closing'>
  isFoldOpen: (key: string) => boolean
  toggleFold: (key: string) => void
}

const TimelineRow = memo(function TimelineRow({ row, first, context }: { row: ListRow; first: boolean; context: RowContext }) {
  const { expandedItem, toggleExpanded, onResourceClick, compact, selectedEventId, onSelectEvent } = context
  const card = (item: TimelineEvent) => (
    <ActivityCard
      item={item}
      expanded={expandedItem === item.id}
      onToggle={() => toggleExpanded(item.id)}
      onResourceClick={onResourceClick}
      compact={compact}
      selected={selectedEventId === item.id}
      onSelect={() => onSelectEvent?.(selectedEventId === item.id ? null : item.id)}
    />
  )
  if (row.type === 'header') return (
    <div className={clsx('px-4 pb-3', first ? 'pt-4' : 'pt-6')}>
      <div className="flex items-center gap-2">
        <Clock className="w-4 h-4 text-theme-text-tertiary" />
        <span className="text-sm font-medium text-theme-text-secondary">{row.label}</span>
        <span className="text-xs text-theme-text-disabled">({pluralize(row.count, 'item')})</span>
      </div>
    </div>
  )
  if (row.type === 'foldChild') return (
    <div className="px-4">
      {/* min-h-px: Virtuoso rejects zero-height rows, which a row collapsed mid-animation would be. */}
      <div className={clsx('ml-6 min-h-px border-l-2 border-theme-border pl-4', row.lastInFold && !row.lastInGroup && 'pb-2')}>
        <FoldReveal phase={context.foldMotion.get(row.foldKey)}>
          <div className="pl-3">
            <div className={clsx('border-l-2 border-theme-border pl-3', !row.lastInFold && 'pb-2')}>
              <div data-event-id={row.eventId} data-ts-from={row.fromMs} data-ts-to={row.toMs}>
                {card(row.event)}
              </div>
            </div>
          </div>
        </FoldReveal>
      </div>
    </div>
  )
  const aggItem = row.item
  return (
    <div className="px-4">
      <div className={clsx('ml-6 border-l-2 border-theme-border pl-4', !row.lastInGroup && !row.foldRowsShown && 'pb-2')}>
        <div data-event-id={row.eventId} data-ts-from={row.fromMs} data-ts-to={row.toMs}>
          {aggItem.type === 'folded' ? (
            <>
              {card(aggItem.latest)}
              <FoldedProblems
                latest={aggItem.latest}
                others={aggItem.others}
                open={context.isFoldOpen(row.key)}
                onToggle={() => context.toggleFold(row.key)}
              />
            </>
          ) : aggItem.type === 'aggregated' ? (
            <AggregatedActivityCard
              first={aggItem.first}
              last={aggItem.last}
              count={aggItem.count}
              reason={aggItem.reason}
              expanded={expandedItem === aggItem.first.id}
              onToggle={() => toggleExpanded(aggItem.first.id)}
              onResourceClick={onResourceClick}
              compact={compact}
              selected={selectedEventId === aggItem.first.id}
              onSelect={() => onSelectEvent?.(selectedEventId === aggItem.first.id ? null : aggItem.first.id)}
            />
          ) : card(aggItem.item)}
        </div>
      </div>
    </div>
  )
})

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
                disabled={!item.apiVersion && builtinGroupForKind(item.kind) === undefined}
                onClick={(e) => {
                  e.stopPropagation()
                  if (!item.apiVersion && builtinGroupForKind(item.kind) === undefined) return
                  onResourceClick?.({
                    kind: kindToPluralWithGroup(
                      item.kind,
                      apiVersionToGroup(item.apiVersion),
                    ),
                    namespace: item.namespace,
                    name: item.name,
                    group: apiVersionToGroup(item.apiVersion),
                  })
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
                    • {formatCompactAge(item.createdAt)} old
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
              <CollapseChevron open={expanded} inheritColor className="w-4 h-4" />
            </button>
          )}
        </div>

        {/* Expanded details - only for items with diffs. A list holds many
            cards, each diff is real work to render: mount on first open. */}
        {hasExpandableContent && item.diff && (
          <Collapse open={expanded} mountLazily>
            <div className="mt-3 pt-3 border-t-subtle">
              <div className="text-xs text-theme-text-tertiary mb-2">Changes:</div>
              <DiffViewer diff={item.diff} />
            </div>
          </Collapse>
        )}
      </div>
    </div>
  )
}

const ROUTINE_ACTIVITY_INFO =
  'The everyday steps Kubernetes takes on its own: pods being created, scheduled and started, replica sets scaling, and jobs that a CronJob ran successfully. Hidden by default to keep the list short. Warnings, failures and changes to your deployments, cron jobs and other top-level resources always show.'

// Its own row under the toolbar: the filter row has no width to spare, and the
// hide must stay visible at every width.
function RoutineActivityToggle({ shown, count, onChange }: { shown: boolean; count: number; onChange: (shown: boolean) => void }) {
  return (
    <div className="flex items-center gap-1.5 border-b border-theme-border px-4 py-1.5 text-xs text-theme-text-secondary">
      <label className="flex cursor-pointer items-center gap-1.5">
        <input
          type="checkbox"
          checked={shown}
          onChange={(e) => onChange(e.target.checked)}
          className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
        />
        Show routine activity
      </label>
      <Tooltip content={ROUTINE_ACTIVITY_INFO} delay={150} position="bottom" wrapperClassName="shrink-0">
        <button type="button" aria-label="About routine activity" className="flex cursor-default rounded text-theme-text-tertiary/70 hover:text-theme-text-secondary">
          <Info className="h-3.5 w-3.5" />
        </button>
      </Tooltip>
      <span className="tabular-nums text-theme-text-tertiary">
        · {count > 0 ? `${count.toLocaleString()} ${shown ? 'shown' : 'hidden'}` : shown ? 'none match' : 'none hidden'}
      </span>
    </div>
  )
}

// What the rest of a folded group holds, in the words its rows use: each K8s
// event reason with its count (BackOff ×8), then the status changes.
function describeFolded(others: TimelineEvent[]): string {
  const reasons = new Map<string, number>()
  let changes = 0
  for (const e of others) {
    // A K8s event row already stands for `count` occurrences.
    if (e.source === 'k8s_event' && e.reason) reasons.set(e.reason, (reasons.get(e.reason) ?? 0) + (e.count ?? 1))
    else changes++
  }
  const parts = [...reasons.entries()].sort((a, b) => b[1] - a[1]).map(([r, n]) => (n > 1 ? `${r} ×${n}` : r))
  if (changes > 0) parts.push(pluralize(changes, 'status change'))
  return parts.join(', ')
}

// The rest of a child resource's problems in one time group, behind one line
// under its newest card, so a crash-looping pod doesn't fill the list. The
// cards themselves are list rows of their own, below this one.
function FoldedProblems({ latest, others, open, onToggle }: { latest: TimelineEvent; others: TimelineEvent[]; open: boolean; onToggle: () => void }) {
  return (
    <div className="pl-3">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex items-center gap-1 rounded px-1 py-0.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <CollapseChevron open={open} inheritColor className="h-3.5 w-3.5" />
        {others.length} more on this {latest.kind}: {describeFolded(others)}
      </button>
      <Collapse open={open}>
        <div className="h-2" />
      </Collapse>
    </div>
  )
}

// One fold child's card, revealed on the disclosure clock when its fold opens
// and closed on it when the fold closes. A row mounted later by scrolling is
// shown at once. The first frame must render collapsed for the open to animate.
function FoldReveal({ phase, children }: { phase: 'opening' | 'closing' | undefined; children: ReactNode }) {
  const [revealed, setRevealed] = useState(phase !== 'opening')
  useEffect(() => {
    if (revealed) return
    let inner = 0
    const outer = requestAnimationFrame(() => { inner = requestAnimationFrame(() => setRevealed(true)) })
    return () => {
      cancelAnimationFrame(outer)
      cancelAnimationFrame(inner)
    }
  }, [revealed])
  return <Collapse open={revealed && phase !== 'closing'}>{children}</Collapse>
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
                disabled={!first.apiVersion && builtinGroupForKind(first.kind) === undefined}
                onClick={(e) => {
                  e.stopPropagation()
                  if (!first.apiVersion && builtinGroupForKind(first.kind) === undefined) return
                  onResourceClick?.({
                    kind: kindToPluralWithGroup(
                      first.kind,
                      apiVersionToGroup(first.apiVersion),
                    ),
                    namespace: first.namespace,
                    name: first.name,
                    group: apiVersionToGroup(first.apiVersion),
                  })
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
            <CollapseChevron open={expanded} inheritColor className="w-4 h-4" />
          </button>
        </div>

        {/* Expanded details */}
        <Collapse open={expanded}>
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
        </Collapse>
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
