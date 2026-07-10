import { useState, useCallback } from 'react'
import { TimelineList as TimelineListUI, type ActivityTypeFilter, type ActivityFilterKey } from '@skyhook-io/k8s-ui'
import type { TimeRange } from '@skyhook-io/k8s-ui'
import { useTimelineSource } from '../../context/TimelineSource'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import type { NavigateToResource } from '../../utils/navigation'
import { AlertTriangle, RefreshCw } from 'lucide-react'

export type { ActivityTypeFilter, ActivityFilterKey }

// Server-side cap on the list fetch. Generous so a busy query window isn't
// silently truncated — the list is already bounded to the selection range, so
// this only caps pathological bursts. Surfaced to the list as `truncatedAt` so a
// window that does hit it shows an end-of-list note instead of dropping silently.
const LIST_FETCH_LIMIT = 2000

interface TimelineListProps {
  namespaces: string[]
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: NavigateToResource
  initialFilter?: ActivityTypeFilter
  initialTimeRange?: TimeRange
  showDeleted: boolean
  onShowDeletedChange: (showDeleted: boolean) => void
  // Shared filter state lifted to TimelineView so it survives the view switch.
  search: string
  onSearchChange: (value: string) => void
  activityFilter: ActivityFilterKey[]
  onActivityFilterChange: (keys: ActivityFilterKey[]) => void
  kindFilter: string[]
  onKindFilterChange: (kinds: string[]) => void
  // The shared scrubber selection [from,to]. When set (retained mode always;
  // local mode once the scrubber owns the range), it drives the fetch window and
  // hides the built-in range dropdown so the list can't drift from the
  // swimlane/URL. Retained scopes server-side; local loads the ring and bounds it
  // client-side (see useLocalEvents).
  selectionWindow?: { fromMs: number; toMs: number }
  // LIVE mode: quantize the base fetch so the sliding window doesn't churn the
  // query key every tick.
  sliding?: boolean
  // Time span of the rows visible in the list's scrollport — the host renders
  // it as the scrubber lens so scrolling the list moves the lens.
  onVisibleWindowChange?: (window: { fromMs: number; toMs: number } | null) => void
  // Carries the swimlane's view window into the list on view switch (scroll target).
  scrollToMs?: number
}

export function TimelineList({ namespaces, onViewChange, currentView, onResourceClick, initialFilter, initialTimeRange, showDeleted, onShowDeletedChange, search, onSearchChange, activityFilter, onActivityFilterChange, kindFilter, onKindFilterChange, selectionWindow, sliding, onVisibleWindowChange, scrollToMs }: TimelineListProps) {
  const hasLimitedAccess = useHasLimitedAccess()
  const timelineSource = useTimelineSource()
  const [queryParams, setQueryParams] = useState<{ timeRange: TimeRange; kinds: string[] }>({
    timeRange: initialTimeRange ?? '1h',
    kinds: [],
  })

  const handleQueryChange = useCallback((params: { timeRange: TimeRange; kinds: string[] }) => {
    setQueryParams(params)
  }, [])

  const { data: events = [], isLoading, isError, refetch } = timelineSource.useEvents({
    namespaces,
    kinds: queryParams.kinds,
    timeRange: queryParams.timeRange,
    includeK8sEvents: true,
    includeDeleted: showDeleted,
    limit: LIST_FETCH_LIMIT,
    fromMs: selectionWindow?.fromMs,
    toMs: selectionWindow?.toMs,
    sliding,
  })

  if (isError) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-3">
        <AlertTriangle className="w-10 h-10 text-amber-400/70" />
        <p className="text-base">Failed to load timeline data</p>
        <button
          onClick={() => refetch()}
          className="flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border-light rounded-lg hover:bg-theme-hover transition-colors"
        >
          <RefreshCw className="w-3.5 h-3.5" />
          Try again
        </button>
      </div>
    )
  }

  return (
    <TimelineListUI
      events={events}
      isLoading={isLoading}
      onQueryChange={handleQueryChange}
      hasLimitedAccess={hasLimitedAccess}
      namespaces={namespaces}
      onViewChange={onViewChange}
      currentView={currentView}
      onResourceClick={onResourceClick}
      initialFilter={initialFilter}
      initialTimeRange={initialTimeRange}
      hideRangeSelector={!!selectionWindow}
      showDeleted={showDeleted}
      onShowDeletedChange={onShowDeletedChange}
      search={search}
      onSearchChange={onSearchChange}
      activityFilter={activityFilter}
      onActivityFilterChange={onActivityFilterChange}
      kindFilter={kindFilter}
      onKindFilterChange={onKindFilterChange}
      onVisibleWindowChange={onVisibleWindowChange}
      scrollToMs={scrollToMs}
      truncatedAt={LIST_FETCH_LIMIT}
    />
  )
}
