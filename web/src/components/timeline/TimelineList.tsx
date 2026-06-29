import { useState, useCallback } from 'react'
import { TimelineList as TimelineListUI, type ActivityTypeFilter } from '@skyhook-io/k8s-ui'
import type { TimeRange } from '@skyhook-io/k8s-ui'
import { useChanges } from '../../api/client'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import type { NavigateToResource } from '../../utils/navigation'
import { AlertTriangle, RefreshCw } from 'lucide-react'

export type { ActivityTypeFilter }

interface TimelineListProps {
  namespaces: string[]
  onViewChange?: (view: 'list' | 'swimlane') => void
  currentView?: 'list' | 'swimlane'
  onResourceClick?: NavigateToResource
  initialFilter?: ActivityTypeFilter
  initialTimeRange?: TimeRange
  showDeleted: boolean
  onShowDeletedChange: (showDeleted: boolean) => void
}

export function TimelineList({ namespaces, onViewChange, currentView, onResourceClick, initialFilter, initialTimeRange, showDeleted, onShowDeletedChange }: TimelineListProps) {
  const hasLimitedAccess = useHasLimitedAccess()
  const [queryParams, setQueryParams] = useState<{ timeRange: TimeRange; kind?: string }>({
    timeRange: initialTimeRange ?? '1h',
  })

  const handleQueryChange = useCallback((params: { timeRange: TimeRange; kind?: string }) => {
    setQueryParams(params)
  }, [])

  const { data: events = [], isLoading, isError, refetch } = useChanges({
    namespaces,
    kind: queryParams.kind,
    timeRange: queryParams.timeRange,
    includeK8sEvents: true,
    includeDeleted: showDeleted,
    limit: 500,
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
      onRefresh={refetch}
      onQueryChange={handleQueryChange}
      hasLimitedAccess={hasLimitedAccess}
      namespaces={namespaces}
      onViewChange={onViewChange}
      currentView={currentView}
      onResourceClick={onResourceClick}
      initialFilter={initialFilter}
      initialTimeRange={initialTimeRange}
      showDeleted={showDeleted}
      onShowDeletedChange={onShowDeletedChange}
    />
  )
}
