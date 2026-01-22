import { useState } from 'react'
import { TimelineList } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'
import { useChanges, useTopology } from '../../api/client'
import type { TimeRange } from '../../types'

interface TimelineViewProps {
  namespace: string
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

export type TimelineViewMode = 'list' | 'swimlane'

export function TimelineView({ namespace, onResourceClick }: TimelineViewProps) {
  const [viewMode, setViewMode] = useState<TimelineViewMode>('swimlane')
  const [timeRange] = useState<TimeRange>('1h')

  // Fetch activity for swimlane view (shared with list)
  const { data: activity, isLoading } = useChanges({
    namespace: namespace || undefined,
    timeRange,
    includeK8sEvents: true,
    includeManaged: true, // Include Pods, ReplicaSets, etc. for hierarchical view
    limit: 1000,
  })

  // Fetch topology for service stack grouping
  const { data: topology } = useTopology(namespace, 'resources')

  if (viewMode === 'swimlane') {
    return (
      <TimelineSwimlanes
        events={activity || []}
        isLoading={isLoading}
        filterTimeRange={timeRange}
        onResourceClick={onResourceClick}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        topology={topology}
      />
    )
  }

  return (
    <TimelineList
      namespace={namespace}
      currentView={viewMode}
      onViewChange={setViewMode}
      onResourceClick={onResourceClick}
    />
  )
}
