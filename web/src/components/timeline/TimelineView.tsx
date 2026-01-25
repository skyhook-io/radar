import { useState, useMemo, useRef } from 'react'
import { TimelineList } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'
import { useChanges, useTopology } from '../../api/client'
import type { Topology } from '../../types'

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

interface TimelineViewProps {
  namespace: string
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

export type TimelineViewMode = 'list' | 'swimlane'

export function TimelineView({ namespace, onResourceClick }: TimelineViewProps) {
  const [viewMode, setViewMode] = useState<TimelineViewMode>('swimlane')

  // Fetch all activity - zoom controls what's visible in the UI
  const { data: activity, isLoading } = useChanges({
    namespace: namespace || undefined,
    timeRange: 'all', // Fetch all available data, zoom controls the view
    includeK8sEvents: true,
    includeManaged: true, // Include Pods, ReplicaSets, etc. for hierarchical view
    limit: 10000, // Fetch all available events
  })

  // Fetch topology for service stack grouping
  const { data: rawTopology } = useTopology(namespace, 'resources')

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
        namespace={namespace}
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
