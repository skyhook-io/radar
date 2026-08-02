import { useState, useCallback, useMemo } from 'react'
import {
  SEVERITY_TEXT,
  TimelineList as TimelineListUI,
  eventsForApplication,
  type ActivityTypeFilter,
  type ActivityFilterKey,
  type AppMembershipIndex,
  type TimeRange,
  type Topology,
} from '@skyhook-io/k8s-ui'
import { useTimelineSource } from '../../context/TimelineSource'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import type { NavigateToResource } from '../../utils/navigation'
import { AlertTriangle, RefreshCw } from 'lucide-react'

export type { ActivityTypeFilter, ActivityFilterKey }

// Cap on the list result — applied client-side in both modes over the loaded
// window (applyClientFilters slices). Generous so a busy query window isn't
// silently truncated; the list is already bounded to the selection range, so
// this only caps pathological bursts. Surfaced as `truncatedAt` so a window
// that hits it shows an end-of-list note instead of dropping silently.
const LIST_FETCH_LIMIT = 2000
const APP_SCOPED_FETCH_LIMIT = 10000

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
  // local mode once the scrubber owns the range), it drives the query window and
  // hides the built-in range dropdown so the list can't drift from the
  // swimlane/URL. In both modes a window the loaded ring covers slices it
  // client-side with no fetch; a frozen selection older than a truncated ring's
  // oldest row fetches its own server window (see createRingEventsHook).
  selectionWindow?: { fromMs: number; toMs: number }
  // Time span of the rows visible in the list's scrollport — the host renders
  // it as the scrubber lens so scrolling the list moves the lens.
  onVisibleWindowChange?: (window: { fromMs: number; toMs: number } | null) => void
  // Carries the swimlane's view window into the list on view switch (scroll target).
  scrollToMs?: number
  focusedAppIndex?: AppMembershipIndex
  appScoped?: boolean
  topology?: Topology
  appScopeLoading?: boolean
}

export function TimelineList({ namespaces, onViewChange, currentView, onResourceClick, initialFilter, initialTimeRange, showDeleted, onShowDeletedChange, search, onSearchChange, activityFilter, onActivityFilterChange, kindFilter, onKindFilterChange, selectionWindow, onVisibleWindowChange, scrollToMs, focusedAppIndex, appScoped = false, topology, appScopeLoading = false }: TimelineListProps) {
  const hasLimitedAccess = useHasLimitedAccess()
  const timelineSource = useTimelineSource()
  const [queryParams, setQueryParams] = useState<{ timeRange: TimeRange; kinds: string[] }>({
    timeRange: initialTimeRange ?? '1h',
    kinds: [],
  })

  const handleQueryChange = useCallback((params: { timeRange: TimeRange; kinds: string[] }) => {
    setQueryParams(params)
  }, [])

  const fetchLimit = appScoped ? APP_SCOPED_FETCH_LIMIT : LIST_FETCH_LIMIT
  const { data: fetchedEvents, isLoading, isError, error, refetch } = timelineSource.useEvents({
    namespaces,
    kinds: queryParams.kinds,
    timeRange: queryParams.timeRange,
    includeK8sEvents: true,
    includeManaged: appScoped,
    includeDeleted: showDeleted,
    limit: fetchLimit,
    fromMs: selectionWindow?.fromMs,
    toMs: selectionWindow?.toMs,
  })
  const events = useMemo(() => {
    const unscoped = fetchedEvents ?? []
    return appScoped
      ? focusedAppIndex
        ? eventsForApplication(unscoped, topology, focusedAppIndex)
        : []
      : unscoped
  }, [appScoped, focusedAppIndex, topology, fetchedEvents])
  const sourceTruncated = (fetchedEvents?.length ?? 0) >= fetchLimit

  // Full-screen error only when nothing is loaded; a failing background poll
  // with data on screen keeps rendering (data before error).
  if (isError && !fetchedEvents) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-3">
        <AlertTriangle className="w-10 h-10 text-amber-400/70" />
        <p className="text-base">Failed to load timeline data</p>
        {error?.message?.trim() && (
          <p className="max-w-md px-6 text-center text-sm text-theme-text-tertiary">{error.message.trim()}</p>
        )}
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

  // Failing background polls with rows on screen: keep the data, say it may
  // be stale. Only when the list owns its own range — under a scrubber the
  // view-level banner already reports the shared failure, and a second note
  // would double up.
  const staleNote = isError && fetchedEvents && !selectionWindow ? (
    <div className="flex items-center gap-1.5 border-b border-theme-border px-4 py-1.5 text-xs text-theme-text-tertiary">
      <AlertTriangle className={`h-3.5 w-3.5 shrink-0 ${SEVERITY_TEXT.warning}`} />
      Live updates are failing — the list may be stale.
      <button type="button" onClick={() => refetch()} className="underline hover:text-theme-text-primary">
        Retry now
      </button>
    </div>
  ) : null

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      {staleNote}
      <div className="min-h-0 flex-1">
        <TimelineListUI
          events={events}
          isLoading={isLoading || appScopeLoading}
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
          truncatedAt={fetchLimit}
          isTruncated={sourceTruncated}
          truncationMessage={appScoped && sourceTruncated
            ? `Showing application activity found in the newest ${fetchLimit.toLocaleString()} events in this range — narrow the query to see older activity`
            : undefined}
        />
      </div>
    </div>
  )
}
