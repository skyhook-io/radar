import { useState, useMemo, useRef } from 'react'
import { Network } from 'lucide-react'
import { TimelineList } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'
import { useChanges, useTopology } from '../../api/client'
import type { Topology } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { LargeClusterNamespacePicker } from '../shared/LargeClusterNamespacePicker'

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
  requiresNamespaceFilter?: boolean
  availableNamespaces?: { name: string }[]
  onNamespaceSelect?: (ns: string) => void
}

export function TimelineView({ namespaces, onResourceClick, initialViewMode, initialFilter, initialTimeRange, requiresNamespaceFilter, availableNamespaces, onNamespaceSelect }: TimelineViewProps) {
  // Force list view on large clusters without namespace filter
  const effectiveInitialMode = requiresNamespaceFilter ? 'list' : (initialViewMode ?? 'swimlane')
  const [viewMode, setViewMode] = useState<TimelineViewMode>(effectiveInitialMode)

  // Only fetch heavy swimlane data when actually showing swimlanes
  const showSwimlanes = viewMode === 'swimlane' && !requiresNamespaceFilter

  // Fetch all activity - zoom controls what's visible in the UI
  // Only fetch heavy 10k dataset for swimlanes; list view fetches its own 500
  const { data: activity, isLoading } = useChanges({
    namespaces,
    timeRange: 'all',
    includeK8sEvents: true,
    includeManaged: true,
    limit: 10000,
    enabled: showSwimlanes,
  })

  // Fetch topology for service stack grouping — skip on large clusters (empty anyway)
  const { data: rawTopology } = useTopology(namespaces, 'resources', { enabled: showSwimlanes })

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
    // Large cluster without namespace: show picker instead of swimlanes
    if (requiresNamespaceFilter) {
      return (
        <div className="flex-1 flex flex-col">
          {/* Toolbar with view toggle so user can switch back to list */}
          <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
            <div />
            <ViewModeToggle viewMode={viewMode} onViewModeChange={setViewMode} />
          </div>
          <div className="flex-1 flex items-center justify-center">
            <div className="max-w-md w-full mx-4 text-center">
              <div className="bg-theme-surface border border-theme-border rounded-xl shadow-lg p-6">
                <div className="w-12 h-12 mx-auto mb-4 rounded-full bg-skyhook-500/10 flex items-center justify-center">
                  <Network className="w-6 h-6 text-blue-400" />
                </div>
                <h2 className="text-lg font-semibold text-theme-text-primary mb-2">
                  Large Cluster Detected
                </h2>
                <p className="text-sm text-theme-text-secondary mb-5">
                  Swimlane view requires a namespace filter on large clusters.
                  Select a namespace or switch to list view.
                </p>
                <div className="relative">
                  <LargeClusterNamespacePicker
                    namespaces={availableNamespaces}
                    onSelect={(ns) => onNamespaceSelect?.(ns)}
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      )
    }

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

function ViewModeToggle({ viewMode, onViewModeChange }: { viewMode: TimelineViewMode, onViewModeChange: (mode: TimelineViewMode) => void }) {
  return (
    <div className="flex items-center gap-1 bg-theme-base rounded-lg p-0.5 border border-theme-border">
      <button
        type="button"
        onClick={() => onViewModeChange('list')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'list' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        List
      </button>
      <button
        type="button"
        onClick={() => onViewModeChange('swimlane')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'swimlane' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        Swimlane
      </button>
    </div>
  )
}
