import { useState, useMemo, useRef } from 'react'
import { TimelineList } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'
import { useChanges, useTopology } from '../../api/client'
import type { Topology } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'

// Stable empty array to avoid creating new references on every render
const EMPTY_EVENTS: never[] = []

// Helper to check if topology has meaningfully changed
function topologyContentEqual(a: Topology | undefined, b: Topology | undefined): boolean {
  if (a === b) return true
  if (!a || !b) return false
  if (a.nodes.length !== b.nodes.length) return false
  if (a.edges.length !== b.edges.length) return false
  // Compare node IDs (fast check for structural changes)
  const aNodeIds = a.nodes.map(n => n.id).sort().join(',')
  const bNodeIds = b.nodes.map(n => n.id).sort().join(',')
  return aNodeIds === bNodeIds
}

import type { TimeRange } from '../../types'

export type TimelineViewMode = 'list' | 'swimlane'
export type { ActivityTypeFilter } from './TimelineList'

interface TimelineViewProps {
  namespaces: string[]
  onResourceClick?: NavigateToResource
  initialViewMode?: TimelineViewMode
  initialFilter?: 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy'
  initialTimeRange?: TimeRange
}

export function TimelineView({ namespaces, onResourceClick, initialViewMode, initialFilter, initialTimeRange }: TimelineViewProps) {
  const [viewMode, setViewMode] = useState<TimelineViewMode>(initialViewMode ?? 'swimlane')

  // Fetch all activity - zoom controls what's visible in the UI
  const { data: activity, isLoading } = useChanges({
    namespaces,
    timeRange: 'all', // Fetch all available data, zoom controls the view
    includeK8sEvents: true,
    includeManaged: true, // Include Pods, ReplicaSets, etc. for hierarchical view
    limit: 10000, // Fetch all available events
  })

  // Fetch topology for service stack grouping
  const { data: rawTopology } = useTopology(namespaces, 'resources')

  // Stabilize topology reference to prevent unnecessary lane recomputation
  // Only update the stable topology when the content meaningfully changes
  const topologyRef = useRef<Topology | undefined>(undefined)
  const stableTopology = useMemo(() => {
    if (topologyContentEqual(topologyRef.current, rawTopology)) {
      return topologyRef.current
    }
    topologyRef.current = rawTopology
    return rawTopology
  }, [rawTopology])

  // Use stable reference for events to prevent unnecessary re-renders
  const events = activity ?? EMPTY_EVENTS

  if (viewMode === 'swimlane') {
    return (
      <TimelineSwimlanes
        events={events}
        isLoading={isLoading}
        onResourceClick={onResourceClick}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        topology={stableTopology}
        namespaces={namespaces}
      />
    )
  }

  return (
    <TimelineList
      namespaces={namespaces}
      currentView={viewMode}
      onViewChange={setViewMode}
      onResourceClick={onResourceClick}
      initialFilter={initialFilter}
      initialTimeRange={initialTimeRange}
    />
  )
}
