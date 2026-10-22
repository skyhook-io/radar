import { useState, useCallback, useMemo } from 'react'
import {
  TimelineList as TimelineListUI,
  eventsForApplication,
  isTimelineProblem,
  type ActivityTypeFilter,
  type ActivityFilterKey,
  type AppMembershipIndex,
  type TimeRange,
  type Topology,
} from '@skyhook-io/k8s-ui'
import type { TimelineEvent } from '../../types'
import { useTimelineSource } from '../../context/TimelineSource'
import { isManagedTimelineEvent } from '../../api/timelineSource'
import { TimelineRetentionBanner } from './TimelineRetentionNote'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import type { NavigateToResource } from '../../utils/navigation'
import { TimelineLoadError, TimelineStaleNote } from './TimelineLoadError'
import { useStickyError } from './useStickyError'
import { ViewModeToggle } from './ViewModeToggle'

export type { ActivityTypeFilter, ActivityFilterKey }

// Cap on the list result — applied client-side in both modes over the loaded
// window (applyClientFilters slices). Generous so a busy query window isn't
// silently truncated; the list is already bounded to the selection range, so
// this only caps pathological bursts. Surfaced as `truncatedAt` so a window
// that hits it shows an end-of-list note instead of dropping silently.
const LIST_FETCH_LIMIT = 2000
const APP_SCOPED_FETCH_LIMIT = 10000

function isChildProblem(e: TimelineEvent): boolean {
  return isManagedTimelineEvent(e) && isTimelineProblem(e)
}

const byNewest = (a: TimelineEvent, b: TimelineEvent) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()

// Splits the list's rows into owner-level rows, problems on child resources,
// and routine child activity, each newest first under its own `limit`. Separate
// budgets keep the promises the toggle makes: a burst of owner rows can't crowd
// out child problems, and showing routine activity only ever adds rows.
export function splitRoutineActivity(allRows: TimelineEvent[], limit: number) {
  const owners: TimelineEvent[] = []
  const childProblems: TimelineEvent[] = []
  const routine: TimelineEvent[] = []
  for (const e of allRows) {
    const bucket = !isManagedTimelineEvent(e) ? owners : isTimelineProblem(e) ? childProblems : routine
    bucket.push(e)
  }
  const base = [...owners.slice(0, limit), ...childProblems.slice(0, limit)].sort(byNewest)
  const shownRoutine = routine.slice(0, limit)
  return {
    withoutRoutine: base,
    withRoutine: [...base, ...shownRoutine].sort(byNewest),
    routine: shownRoutine,
    truncated: owners.length > limit || childProblems.length > limit,
    routineTruncated: routine.length > limit,
  }
}

interface TimelineListProps {
  namespaces: string[]
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: NavigateToResource
  initialFilter?: ActivityTypeFilter
  initialTimeRange?: TimeRange
  showDeleted: boolean
  onShowDeletedChange: (showDeleted: boolean) => void
  // Routine activity (normal steps of pods, replica sets and other child
  // resources) is hidden unless the user opts in. Problems always show.
  showRoutine: boolean
  onShowRoutineChange: (showRoutine: boolean) => void
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
  onOpenDiagnostics?: () => void
}

export function TimelineList({ namespaces, onViewChange, currentView, onResourceClick, initialFilter, initialTimeRange, showDeleted, onShowDeletedChange, showRoutine, onShowRoutineChange, search, onSearchChange, activityFilter, onActivityFilterChange, kindFilter, onKindFilterChange, selectionWindow, onVisibleWindowChange, scrollToMs, focusedAppIndex, appScoped = false, topology, appScopeLoading = false, onOpenDiagnostics }: TimelineListProps) {
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
  // The application-scoped list keeps every row: its app filter needs the pods.
  const splitRows = !appScoped
  // Uncapped when splitting: each part gets its own cap below. The rows come
  // from the loaded ring, so this costs no extra fetch.
  const { data: fetchedEvents, isLoading, isFetching, isError, error, refetch } = timelineSource.useEvents({
    namespaces,
    kinds: queryParams.kinds,
    timeRange: queryParams.timeRange,
    includeK8sEvents: true,
    includeManaged: true,
    includeDeleted: showDeleted,
    limit: splitRows ? undefined : fetchLimit,
    fromMs: selectionWindow?.fromMs,
    toMs: selectionWindow?.toMs,
  })
  const loadError = useStickyError(error, fetchedEvents != null)
  const split = useMemo(
    () => (splitRows && fetchedEvents ? splitRoutineActivity(fetchedEvents, fetchLimit) : undefined),
    [splitRows, fetchedEvents, fetchLimit],
  )
  const events = useMemo(() => {
    const unscoped = split ? (showRoutine ? split.withRoutine : split.withoutRoutine) : fetchedEvents ?? []
    return appScoped
      ? focusedAppIndex
        ? eventsForApplication(unscoped, topology, focusedAppIndex)
        : []
      : unscoped
  }, [appScoped, focusedAppIndex, topology, split, showRoutine, fetchedEvents])
  const sourceTruncated = split
    ? split.truncated || (showRoutine && split.routineTruncated)
    : (fetchedEvents?.length ?? 0) >= fetchLimit

  // Full-screen error only when nothing is loaded; a failing background poll
  // with data on screen keeps rendering (data before error).
  if (loadError && !fetchedEvents) {
    return (
      <div className="flex flex-col h-full">
        {currentView && onViewChange && (
          <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
            <div />
            <ViewModeToggle viewMode={currentView} onViewModeChange={onViewChange} />
          </div>
        )}
        <TimelineLoadError
          className="flex-1"
          error={loadError}
          retrying={isFetching}
          onRetry={() => refetch()}
          onOpenDiagnostics={onOpenDiagnostics}
        />
      </div>
    )
  }

  // Failing background polls with rows on screen: keep the data, say it may
  // be stale. Only when the list owns its own range — under a scrubber the
  // view-level banner already reports the shared failure, and a second note
  // would double up.
  const staleNote = isError && fetchedEvents && !selectionWindow ? (
    <TimelineStaleNote error={error} subject="list" onRetry={() => refetch()} />
  ) : null

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      {/* Under a scrubber TimelineView already shows the note above it. */}
      {!selectionWindow && <TimelineRetentionBanner />}
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
          showRoutine={showRoutine}
          onShowRoutineChange={split ? onShowRoutineChange : undefined}
          routineEvents={split?.routine}
          foldPerResource={split && !showRoutine ? isChildProblem : undefined}
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
            : split && sourceTruncated
              ? 'Some older activity in this range is not listed. Narrow the time range to see it.'
              : undefined}
        />
      </div>
    </div>
  )
}
