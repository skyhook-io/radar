import { useState, useMemo, useRef, useCallback, useEffect } from 'react'
import { clsx } from 'clsx'
import {
  AlertCircle,
  AlertTriangle,
  RefreshCw,
  ZoomIn,
  ZoomOut,
  ChevronRight,
  Search,
  X,
  List,
  GanttChart,
  ArrowUpDown,
  Clock,
  MemoryStick,
  Package,
  Ban,
  Box,
  Gauge,
  HardDrive,
  Timer,
  RotateCcw,
} from 'lucide-react'
import type { TimelineEvent, Topology } from '../../types'
import { isChangeEvent, isHistoricalEvent, isOperation } from '../../types'
import { DiffViewer } from './DiffViewer'
import { getOperationColor, getHealthBadgeColor, getEventTypeColor } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'
import { buildResourceHierarchy, isProblematicEvent, type ResourceLane as BaseResourceLane } from '../../utils/resource-hierarchy'
import {
  formatAxisTime,
  formatFullTime,
  buildHealthSpans,
  HealthSpan,
  timeToX as sharedTimeToX,
} from './shared'

interface TimelineSwimlanesProps {
  events: TimelineEvent[]
  isLoading?: boolean
  onResourceClick?: (kind: string, namespace: string, name: string) => void
  viewMode?: 'list' | 'swimlane'
  onViewModeChange?: (mode: 'list' | 'swimlane') => void
  topology?: Topology
  namespace?: string
}

interface ResourceLane extends BaseResourceLane {
  scoreBreakdown?: ScoreBreakdown // Debug: interestingness score breakdown
}

// Score breakdown for debugging
interface ScoreBreakdown {
  total: number
  kind: number
  problematic: number
  variety: number
  addDelete: number
  children: number
  empty: number
  systemNs: number
  recent5m: number
  recent30m: number
  noisy: number
  details: string
}

// Calculate "interestingness" score for sorting lanes
// Higher score = more interesting = should appear higher in list
function calculateInterestingness(lane: ResourceLane): number {
  return calculateInterestingnessWithBreakdown(lane).total
}

function calculateInterestingnessWithBreakdown(lane: ResourceLane): ScoreBreakdown {
  const allEvents = [...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]
  const breakdown: ScoreBreakdown = {
    total: 0, kind: 0, problematic: 0, variety: 0, addDelete: 0,
    children: 0, empty: 0, systemNs: 0, recent5m: 0, recent30m: 0, noisy: 0, details: ''
  }

  // 1. Base: Kind priority (tiebreaker, lower values than before)
  const kindScores: Record<string, number> = {
    // GitOps controllers - top priority
    Application: 55, // ArgoCD Application
    Kustomization: 55, HelmRelease: 55, // FluxCD controllers
    GitRepository: 52, OCIRepository: 52, HelmRepository: 52, // FluxCD sources
    // Core workloads
    Deployment: 50, Rollout: 50, StatefulSet: 50, DaemonSet: 50,
    Service: 45, Ingress: 45,
    Job: 40, CronJob: 40, Workflow: 40, CronWorkflow: 40,
    Pod: 30,
    HPA: 25,
    ReplicaSet: 20,
    ConfigMap: 10, Secret: 10, PVC: 10,
  }
  breakdown.kind = kindScores[lane.kind] || 15

  // 2. Primary: Recency (dominates) - events in last 5 minutes
  const now = Date.now()
  const fiveMinutesAgo = now - 5 * 60 * 1000
  const thirtyMinutesAgo = now - 30 * 60 * 1000

  const eventsLast5m = allEvents.filter(e => new Date(e.timestamp).getTime() > fiveMinutesAgo)
  const eventsLast30m = allEvents.filter(e => {
    const t = new Date(e.timestamp).getTime()
    return t > thirtyMinutesAgo && t <= fiveMinutesAgo
  })

  breakdown.recent5m = Math.min(eventsLast5m.length * 30, 150)
  breakdown.recent30m = Math.min(eventsLast30m.length * 10, 50)

  // 3. Secondary: Problems (important signal) - +40 each, max 200
  const problematicCount = allEvents.filter(e => isProblematicEvent(e)).length
  breakdown.problematic = Math.min(problematicCount * 40, 200)

  // 4. Tertiary: Activity type
  const operations = new Set(allEvents.map(e => e.eventType).filter(t => isOperation(t as any)))
  breakdown.variety = operations.size * 10 // Up to 30 for all three types

  // Add/delete with caps
  const addCount = allEvents.filter(e => e.eventType === 'add').length
  const deleteCount = allEvents.filter(e => e.eventType === 'delete').length
  breakdown.addDelete = Math.min(addCount * 3, 30) + Math.min(deleteCount * 5, 30)

  // 5. Children bonus (flat, just organizational)
  if (lane.children && lane.children.length > 0) {
    breakdown.children = 10
  }

  // 6. Empty lane penalty (parent with 0 own events)
  if (lane.events.length === 0) {
    breakdown.empty = -30
  }

  // 7. System namespaces penalty
  const systemNamespaces = ['kube-system', 'kube-public', 'kube-node-lease', 'gke-managed-system']
  if (systemNamespaces.includes(lane.namespace)) {
    breakdown.systemNs = -30
  }

  // 8. Noisy penalty (many updates with no variety)
  const updateCount = allEvents.filter(e => e.eventType === 'update').length
  if (updateCount > 10 && operations.size === 1) {
    breakdown.noisy = -Math.min(updateCount, 40)
  }

  breakdown.total = breakdown.kind + breakdown.problematic + breakdown.variety +
    breakdown.addDelete + breakdown.children + breakdown.empty + breakdown.systemNs +
    breakdown.recent5m + breakdown.recent30m + breakdown.noisy

  // Build details string
  const parts: string[] = []
  parts.push(`kind:${breakdown.kind}`)
  if (breakdown.recent5m) parts.push(`5m:${breakdown.recent5m}`)
  if (breakdown.recent30m) parts.push(`30m:${breakdown.recent30m}`)
  if (breakdown.problematic) parts.push(`warn:${breakdown.problematic}`)
  if (breakdown.variety) parts.push(`var:${breakdown.variety}`)
  if (breakdown.addDelete) parts.push(`a/d:${breakdown.addDelete}`)
  if (breakdown.children) parts.push(`child:${breakdown.children}`)
  if (breakdown.empty) parts.push(`empty:${breakdown.empty}`)
  if (breakdown.systemNs) parts.push(`sys:${breakdown.systemNs}`)
  if (breakdown.noisy) parts.push(`noisy:${breakdown.noisy}`)
  breakdown.details = parts.join(' ')

  return breakdown
}

export function TimelineSwimlanes({ events, isLoading, onResourceClick, viewMode, onViewModeChange, topology, namespace }: TimelineSwimlanesProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const [zoom, setZoom] = useState(1)
  const [panOffset, setPanOffset] = useState(0)
  const [selectedEvent, setSelectedEvent] = useState<TimelineEvent | null>(null)
  const [isDragging, setIsDragging] = useState(false)
  const [dragStart, setDragStart] = useState({ x: 0, offset: 0 })
  const [searchTerm, setSearchTerm] = useState('')
  const [expandedLanes, setExpandedLanes] = useState<Set<string>>(new Set())
  const [hasAutoZoomed, setHasAutoZoomed] = useState(false)
  const [groupByApp, setGroupByApp] = useState(true) // Group by app.kubernetes.io/name label

  // Stable lane ordering - use ref to avoid render loop (lanes depends on order, order depends on lanes)
  const laneOrderRef = useRef<Map<string, number>>(new Map())
  const [sortVersion, setSortVersion] = useState(0) // Increment to re-sort lanes

  // Stable "now" time - captured once on mount, only changes when user interacts
  // This prevents the time window from auto-shifting and causing re-renders
  const [stableNow] = useState(() => Date.now())

  // Auto-adjust zoom based on event distribution (only once on initial load)
  useEffect(() => {
    if (hasAutoZoomed || events.length === 0) return

    const now = Date.now()
    const timestamps = events.map(e => new Date(e.timestamp).getTime())
    const oldestEvent = Math.min(...timestamps)
    const eventAge = now - oldestEvent

    // Zoom levels: 0.25 (15m), 0.5 (30m), 1 (1h), 2 (2h), etc.
    // Pick the smallest zoom that fits all events with some margin
    let optimalZoom = 1
    if (eventAge < 10 * 60 * 1000) { // < 10 minutes
      optimalZoom = 0.25 // 15m window
    } else if (eventAge < 20 * 60 * 1000) { // < 20 minutes
      optimalZoom = 0.5 // 30m window
    } else if (eventAge < 45 * 60 * 1000) { // < 45 minutes
      optimalZoom = 1 // 1h window
    } else if (eventAge < 90 * 60 * 1000) { // < 90 minutes
      optimalZoom = 2 // 2h window
    }
    // else keep default 1h

    setZoom(optimalZoom)
    setHasAutoZoomed(true)
  }, [events, hasAutoZoomed])

  // Keyboard shortcuts
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Escape closes detail panel or blurs input
      if (e.key === 'Escape') {
        if (selectedEvent) {
          setSelectedEvent(null)
          return
        }
        if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
          (e.target as HTMLElement).blur()
        }
        return
      }
      // Don't handle other shortcuts when typing
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        return
      }
      // / or Cmd/Ctrl+K to focus search
      if (e.key === '/' || ((e.metaKey || e.ctrlKey) && e.key === 'k')) {
        e.preventDefault()
        searchInputRef.current?.focus()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [selectedEvent])

  // Filter events by search term
  const filteredEvents = useMemo(() => {
    if (!searchTerm) return events

    const term = searchTerm.toLowerCase()
    return events.filter(e =>
      e.name.toLowerCase().includes(term) ||
      e.kind.toLowerCase().includes(term) ||
      e.namespace?.toLowerCase().includes(term) ||
      e.reason?.toLowerCase().includes(term) ||
      e.message?.toLowerCase().includes(term)
    )
  }, [events, searchTerm])

  // Build hierarchical lanes using owner references + topology edges
  // Uses the shared utility from utils/resource-hierarchy.ts
  const lanes = useMemo(() => {
    // Build the hierarchy using the shared utility
    const baseLanes = buildResourceHierarchy({
      events: filteredEvents,
      topology,
      groupByApp,
    })

    // Add score breakdown to each lane (specific to swimlanes view)
    const lanesWithScores: ResourceLane[] = baseLanes.map(lane => ({
      ...lane,
      scoreBreakdown: calculateInterestingnessWithBreakdown(lane),
    }))

    // Sort by interestingness score (highest first)
    return lanesWithScores.sort((a, b) => {
      const aScore = a.scoreBreakdown?.total ?? calculateInterestingness(a)
      const bScore = b.scoreBreakdown?.total ?? calculateInterestingness(b)
      return bScore - aScore
    })
  }, [filteredEvents, topology, sortVersion, groupByApp])

  // Re-sort lanes by interestingness score
  const handleRefreshSort = useCallback(() => {
    // Reset lane order to force re-sort by interestingness
    laneOrderRef.current = new Map()
    setSortVersion(v => v + 1)
  }, [])

  // Toggle lane expansion
  const toggleLane = useCallback((laneId: string) => {
    setExpandedLanes(prev => {
      const next = new Set(prev)
      if (next.has(laneId)) {
        next.delete(laneId)
      } else {
        next.add(laneId)
      }
      return next
    })
  }, [])

  // Calculate visible time range
  const visibleTimeRange = useMemo(() => {
    const windowMs = zoom * 60 * 60 * 1000
    const end = stableNow - panOffset
    const start = end - windowMs
    return { start, end, windowMs, now: stableNow }
  }, [zoom, panOffset, stableNow])

  // Filter out lanes with no events in the visible time window
  const visibleLanes = useMemo(() => {
    const { start, end } = visibleTimeRange
    return lanes.filter(lane => {
      const allLaneEvents = lane.allEventsSorted || []
      return allLaneEvents.some(e => {
        const t = new Date(e.timestamp).getTime()
        return t >= start && t <= end
      })
    })
  }, [lanes, visibleTimeRange])

  // Generate time axis ticks
  const axisTicks = useMemo(() => {
    const { start, end } = visibleTimeRange
    const ticks: { time: number; label: string }[] = []

    let intervalMs: number
    if (zoom <= 0.25) {
      intervalMs = 2 * 60 * 1000 // 2 min intervals for 15m window
    } else if (zoom <= 0.5) {
      intervalMs = 5 * 60 * 1000 // 5 min intervals for 30m window
    } else if (zoom <= 1) {
      intervalMs = 10 * 60 * 1000
    } else if (zoom <= 3) {
      intervalMs = 30 * 60 * 1000
    } else if (zoom <= 6) {
      intervalMs = 60 * 60 * 1000
    } else if (zoom <= 24) {
      intervalMs = 2 * 60 * 60 * 1000 // 2 hour intervals
    } else if (zoom <= 72) {
      intervalMs = 6 * 60 * 60 * 1000 // 6 hour intervals for up to 3 days
    } else {
      intervalMs = 24 * 60 * 60 * 1000 // 1 day intervals for larger windows
    }

    const firstTick = Math.ceil(start / intervalMs) * intervalMs

    for (let t = firstTick; t <= end; t += intervalMs) {
      ticks.push({
        time: t,
        label: formatAxisTime(new Date(t)),
      })
    }

    return ticks
  }, [visibleTimeRange, zoom])

  // Convert timestamp to X position (0-100%)
  const timeToX = useCallback(
    (timestamp: number): number => {
      const { start, windowMs } = visibleTimeRange
      return ((timestamp - start) / windowMs) * 100
    },
    [visibleTimeRange]
  )

  // Predefined zoom levels (in hours): 15m, 30m, 1h, 2h, 4h, 8h, 12h, 1d, 2d, 3d, 7d
  const ZOOM_LEVELS = [0.25, 0.5, 1, 2, 4, 8, 12, 24, 48, 72, 168]

  // Zoom handlers - snap to predefined levels
  const handleZoomIn = () => setZoom((z) => {
    const idx = ZOOM_LEVELS.findIndex(level => level >= z)
    return ZOOM_LEVELS[Math.max(0, idx - 1)]
  })
  const handleZoomOut = () => setZoom((z) => {
    const idx = ZOOM_LEVELS.findIndex(level => level > z)
    return ZOOM_LEVELS[Math.min(ZOOM_LEVELS.length - 1, idx === -1 ? ZOOM_LEVELS.length - 1 : idx)]
  })

  // Pan with mouse drag
  const handleMouseDown = (e: React.MouseEvent) => {
    if (e.button !== 0) return
    setIsDragging(true)
    setDragStart({ x: e.clientX, offset: panOffset })
  }

  const handleMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!isDragging || !containerRef.current) return

      const containerWidth = containerRef.current.clientWidth
      const dx = e.clientX - dragStart.x
      const { windowMs } = visibleTimeRange

      const timePerPixel = windowMs / containerWidth
      const newOffset = dragStart.offset - dx * timePerPixel

      setPanOffset(Math.max(0, newOffset))
    },
    [isDragging, dragStart, visibleTimeRange]
  )

  const handleMouseUp = useCallback(() => {
    setIsDragging(false)
  }, [])

  useEffect(() => {
    if (isDragging) {
      window.addEventListener('mousemove', handleMouseMove)
      window.addEventListener('mouseup', handleMouseUp)
      return () => {
        window.removeEventListener('mousemove', handleMouseMove)
        window.removeEventListener('mouseup', handleMouseUp)
      }
    }
  }, [isDragging, handleMouseMove, handleMouseUp])

  // Wheel zoom - snap to predefined levels
  const handleWheel = useCallback((e: React.WheelEvent) => {
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault()
      setZoom((z) => {
        const currentIdx = ZOOM_LEVELS.findIndex(level => level >= z)
        const idx = currentIdx === -1 ? ZOOM_LEVELS.length - 1 : currentIdx
        if (e.deltaY > 0) {
          // Zoom out - go to next larger level
          return ZOOM_LEVELS[Math.min(ZOOM_LEVELS.length - 1, idx + 1)]
        } else {
          // Zoom in - go to next smaller level
          return ZOOM_LEVELS[Math.max(0, idx - 1)]
        }
      })
    }
  }, [])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full w-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading timeline...
      </div>
    )
  }

  // Compute empty state info (but don't early return - we need the toolbar visible)
  const hasFilteredEvents = visibleLanes.length === 0 && events.length > 0 && filteredEvents.length === 0

  return (
    <div className="flex flex-col h-full w-full">
      {/* Toolbar with search and zoom */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border bg-theme-surface/30">
        <div className="flex items-center gap-4">
          {/* Search */}
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
            <input
              ref={searchInputRef}
              type="text"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              placeholder="Search... (/ or ⌘K)"
              className="w-80 pl-9 pr-8 py-1.5 text-sm bg-theme-elevated border border-theme-border-light rounded-lg text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
            />
            {searchTerm && (
              <button
                onClick={() => setSearchTerm('')}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-theme-text-tertiary hover:text-theme-text-primary"
              >
                <X className="w-4 h-4" />
              </button>
            )}
          </div>
          {/* Zoom controls */}
          <div className="flex items-center gap-2">
            <button
              onClick={handleZoomIn}
              className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              title="Zoom in (Ctrl+scroll)"
            >
              <ZoomIn className="w-4 h-4" />
            </button>
            <button
              onClick={handleZoomOut}
              className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              title="Zoom out (Ctrl+scroll)"
            >
              <ZoomOut className="w-4 h-4" />
            </button>
            <span className="text-xs text-theme-text-tertiary">
              {zoom < 1 ? `${Math.round(zoom * 60)}m` : zoom >= 24 ? `${Math.round(zoom / 24)}d` : `${zoom}h`} window
            </span>
            {panOffset > 0 && (
              <button
                onClick={() => setPanOffset(0)}
                className="px-2 py-1 text-xs text-blue-600 dark:text-blue-300 hover:text-blue-700 dark:hover:text-blue-200 hover:bg-theme-elevated rounded"
                title="Jump to current time"
              >
                → Now
              </button>
            )}
          </div>
          {/* Sort by latest */}
          <button
            onClick={handleRefreshSort}
            className="flex items-center gap-1.5 px-2 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
            title="Re-sort by importance"
          >
            <ArrowUpDown className="w-3.5 h-3.5" />
            Sort
          </button>
        </div>
        <div className="flex items-center gap-4">
          {/* Legend with hover tooltips */}
          <div className="flex items-center gap-3 text-xs text-theme-text-secondary">
            {/* Event dot legend */}
            <LegendItem color="bg-green-500" label="created" description="Resource was created" />
            <LegendItem color="bg-blue-500" label="modified" description="Resource was updated/changed" />
            <LegendItem color="bg-red-500" label="deleted" description="Resource was removed" />
            <LegendItem color="bg-amber-500" label="warning" description="Warning event (CrashLoopBackOff, Failed, etc.)" />
            <LegendItem color="bg-theme-text-tertiary" label="historical" description="Inferred from resource metadata (creation time, etc.)" dashed />
            {/* Health bar legend - separator and bars */}
            <span className="w-px h-3 bg-theme-border-light mx-1" />
            <HealthBarLegendItem color="bg-green-500/60" label="healthy" description="Resource is fully operational" />
            <HealthBarLegendItem color="bg-blue-500/60" label="rolling" description="Expected degradation during deployment rollout" />
            <HealthBarLegendItem color="bg-amber-500/60" label="degraded" description="Unexpected partial availability" />
            <HealthBarLegendItem color="bg-red-500/60" label="unhealthy" description="Resource is failing or not ready" />
          </div>
          <span className="text-xs text-theme-text-tertiary">
            {visibleLanes.length} resource{visibleLanes.length !== 1 ? 's' : ''} · {filteredEvents.length} event
            {filteredEvents.length !== 1 ? 's' : ''}
            {searchTerm && ` (filtered)`}
          </span>
          {/* Group by app toggle */}
          <Tooltip content="Group related resources (Deployment, Service, Pod) by their app.kubernetes.io/name label" position="bottom">
            <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary cursor-pointer hover:text-theme-text-primary">
              <input
                type="checkbox"
                checked={groupByApp}
                onChange={(e) => setGroupByApp(e.target.checked)}
                className="w-3.5 h-3.5 rounded border-theme-border-light bg-theme-elevated text-blue-500 focus:ring-blue-500 focus:ring-offset-0"
              />
              <span className="border-b border-dotted border-theme-text-tertiary">Group by app</span>
            </label>
          </Tooltip>
          {/* View toggle */}
          {onViewModeChange && (
            <div className="flex items-center gap-1 bg-theme-elevated rounded-lg p-1">
              <button
                onClick={() => onViewModeChange('list')}
                className={`flex items-center gap-1.5 px-2 py-1 text-xs rounded-md transition-colors ${
                  viewMode === 'list' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                }`}
              >
                <List className="w-3.5 h-3.5" />
                List
              </button>
              <button
                onClick={() => onViewModeChange('swimlane')}
                className={`flex items-center gap-1.5 px-2 py-1 text-xs rounded-md transition-colors ${
                  viewMode === 'swimlane' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                }`}
              >
                <GanttChart className="w-3.5 h-3.5" />
                Timeline
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Timeline container */}
      <div className="flex-1 overflow-y-auto overflow-x-hidden">
        <div
          ref={containerRef}
          className="min-w-full"
          onMouseDown={handleMouseDown}
          onWheel={handleWheel}
          style={{ cursor: isDragging ? 'grabbing' : 'grab' }}
        >
          {/* Time axis header */}
          <div className="sticky top-0 z-30 bg-theme-surface border-b border-theme-border">
            <div className="flex">
              <div className="w-80 shrink-0 border-r border-theme-border px-3 py-2">
                <span className="text-xs font-medium text-theme-text-secondary">Resource</span>
              </div>
              <div className="flex-1 relative h-8 mr-8">
                {axisTicks.map((tick) => {
                  const x = timeToX(tick.time)
                  if (x < 0 || x > 100) return null
                  return (
                    <div
                      key={tick.time}
                      className="absolute top-0 bottom-0 flex flex-col items-center"
                      style={{ left: `${x}%` }}
                    >
                      <div className="h-2 w-px bg-theme-hover" />
                      <span className="text-xs text-theme-text-tertiary mt-0.5">{tick.label}</span>
                    </div>
                  )
                })}
                {/* "Now" marker in header */}
                {(() => {
                  const nowX = timeToX(visibleTimeRange.now)
                  if (nowX < 0 || nowX > 100) return null
                  return (
                    <div
                      className="absolute top-0 bottom-0 flex flex-col items-center z-20"
                      style={{ left: `${nowX}%` }}
                    >
                      <div className="h-2 w-0.5 bg-purple-500" />
                      <span className="text-xs text-purple-500 font-medium mt-0.5">Now</span>
                    </div>
                  )
                })()}
              </div>
            </div>
          </div>

          {/* Swimlanes or empty state */}
          {visibleLanes.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-64 text-theme-text-tertiary">
              <AlertCircle className="w-12 h-12 mb-4 opacity-50" />
              {hasFilteredEvents ? (
                <>
                  <p className="text-lg">No matching events</p>
                  <p className="text-sm mt-1">
                    {searchTerm ? `No results for "${searchTerm}"` : 'Try adjusting your filters'}
                  </p>
                  {namespace && <p className="text-sm mt-1 text-theme-text-disabled">Searching in namespace: {namespace}</p>}
                </>
              ) : (
                <>
                  <p className="text-lg">No events yet</p>
                  <p className="text-sm mt-1">Events will appear here as resources change</p>
                </>
              )}
            </div>
          ) : (
          <div className="relative">
            {/* "Now" line through swimlanes */}
            {(() => {
              const nowX = timeToX(visibleTimeRange.now)
              if (nowX < 0 || nowX > 100) return null
              return (
                <div
                  className="absolute top-0 bottom-0 w-0.5 bg-purple-500/50 z-10 pointer-events-none"
                  style={{ left: `calc(320px + (100% - 320px - 32px) * ${nowX / 100})` }}
                />
              )
            })()}
            {visibleLanes.map((lane) => {
              const isExpanded = expandedLanes.has(lane.id)
              const hasChildren = lane.children && lane.children.length > 0

              return (
                <div key={lane.id}>
                  {/* Parent lane */}
                  <div className="border-b-subtle">
                    <div className="flex">
                      {/* Lane label */}
                      <div className="w-80 shrink-0 border-r border-theme-border px-3 py-2 flex items-center gap-1">
                        {/* Expand/collapse button */}
                        {hasChildren ? (
                          <button
                            onClick={() => toggleLane(lane.id)}
                            className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                          >
                            <ChevronRight className={clsx(
                              'w-3 h-3 transition-transform',
                              isExpanded && 'rotate-90'
                            )} />
                          </button>
                        ) : (
                          <div className="w-4" />
                        )}
                        <div
                          className="flex-1 min-w-0 cursor-pointer hover:bg-theme-surface/30 rounded px-1 -mx-1 group"
                          onClick={() => onResourceClick?.(lane.kind, lane.namespace, lane.name)}
                        >
                          <div className="flex items-center gap-1">
                            <span className={clsx(
                              'text-xs px-1 py-0.5 rounded',
                              lane.isWorkload ? 'bg-blue-500/15 dark:bg-blue-900/50 text-blue-700 dark:text-blue-300' : 'bg-theme-elevated text-theme-text-secondary'
                            )}>
                              {lane.kind}
                            </span>
                            {hasChildren && (
                              <span className="text-xs text-theme-text-tertiary">
                                +{lane.children!.length}
                              </span>
                            )}
                            {/* Issue count badge */}
                            {(() => {
                              const allEvents = lane.allEventsSorted || []
                              const issueCount = allEvents.filter(e => isCriticalIssue(e)).length
                              if (issueCount === 0) return null
                              return (
                                <Tooltip content={`${issueCount} critical issue${issueCount > 1 ? 's' : ''} (OOMKilled, CrashLoopBackOff, etc.)`} position="top">
                                  <span className="flex items-center gap-0.5 text-xs px-1 py-0.5 rounded bg-red-500/15 text-red-600 dark:text-red-300">
                                    <AlertTriangle className="w-3 h-3" />
                                    {issueCount}
                                  </span>
                                </Tooltip>
                              )
                            })()}
                          </div>
                          <div className="text-sm text-theme-text-primary break-words group-hover:text-blue-600 dark:group-hover:text-blue-300 group-hover:underline cursor-pointer">
                            {lane.name}
                          </div>
                          <div className="text-xs text-theme-text-tertiary">{lane.namespace}</div>
                        </div>
                      </div>

                      {/* Events track - ALWAYS shows all events (summary view) */}
                      <div className="flex-1 relative h-12 mr-8">
                        {/* Health bar background layer */}
                        <HealthBarTrack
                          events={lane.allEventsSorted || []}
                          startTime={visibleTimeRange.start}
                          windowMs={visibleTimeRange.windowMs}
                          now={visibleTimeRange.now}
                        />
                        {/* Event markers layer (on top of health bars) */}
                        <div className="absolute inset-0 z-10">
                          {/* All events combined: own + children, pre-sorted in memo so important events render on top */}
                          {(lane.allEventsSorted || []).map((event, eventIdx) => {
                              const x = timeToX(new Date(event.timestamp).getTime())
                              if (x < 0 || x > 100) return null
                              return (
                                <EventMarker
                                  key={`summary-${event.id}-${eventIdx}`}
                                  event={event}
                                  x={x}
                                  selected={selectedEvent?.id === event.id}
                                  onClick={() => setSelectedEvent(selectedEvent?.id === event.id ? null : event)}
                                />
                              )
                            })}
                        </div>
                      </div>
                    </div>
                  </div>

                  {/* Child lanes (when expanded) - includes parent as first row */}
                  {isExpanded && hasChildren && (
                    <div className="border-l-2 border-blue-500/40 ml-3 bg-theme-surface/30">
                      {/* Parent's own events as first row (only if it has events) */}
                      {lane.events.length > 0 && (
                        <div className="border-b-subtle">
                          <div className="flex">
                            <div
                              className="w-[19.25rem] shrink-0 border-r border-theme-border/50 pl-4 pr-3 py-1.5 flex items-center gap-2 cursor-pointer hover:bg-theme-elevated/30 group"
                              onClick={() => onResourceClick?.(lane.kind, lane.namespace, lane.name)}
                            >
                              <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-1">
                                  <span className="text-xs px-1 py-0.5 rounded bg-blue-500/15 dark:bg-blue-900/50 text-blue-700 dark:text-blue-300">
                                    {lane.kind}
                                  </span>
                                </div>
                                <div className="text-sm text-theme-text-secondary break-words group-hover:text-blue-600 dark:group-hover:text-blue-300 group-hover:underline cursor-pointer">
                                  {lane.name}
                                </div>
                              </div>
                            </div>
                            <div className="flex-1 relative h-10 mr-8">
                              {/* Health bar background layer */}
                              <HealthBarTrack
                                events={lane.events}
                                startTime={visibleTimeRange.start}
                                windowMs={visibleTimeRange.windowMs}
                                now={visibleTimeRange.now}
                              />
                              {/* Event markers layer */}
                              <div className="absolute inset-0 z-10">
                                {lane.events.map((event, eventIdx) => {
                                  const x = timeToX(new Date(event.timestamp).getTime())
                                  if (x < 0 || x > 100) return null
                                  return (
                                    <EventMarker
                                      key={`expanded-${event.id}-${eventIdx}`}
                                      event={event}
                                      x={x}
                                      selected={selectedEvent?.id === event.id}
                                      onClick={() => setSelectedEvent(selectedEvent?.id === event.id ? null : event)}
                                      small
                                    />
                                  )
                                })}
                              </div>
                            </div>
                          </div>
                        </div>
                      )}
                      {/* Children */}
                      {lane.children!.map((child, idx) => (
                        <div key={child.id} className={clsx(
                          'border-b-subtle',
                          idx === lane.children!.length - 1 && 'border-b-0'
                        )}>
                          <div className="flex">
                            {/* Child lane label - indented */}
                            <div
                              className="w-[19.25rem] shrink-0 border-r border-theme-border/50 pl-4 pr-3 py-1.5 flex items-center gap-2 cursor-pointer hover:bg-theme-elevated/30 group"
                              onClick={() => onResourceClick?.(child.kind, child.namespace, child.name)}
                            >
                              <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-1">
                                  <span className="text-xs px-1 py-0.5 rounded bg-theme-elevated/50 text-theme-text-secondary">
                                    {child.kind}
                                  </span>
                                </div>
                                <div className="text-sm text-theme-text-secondary break-words group-hover:text-blue-600 dark:group-hover:text-blue-300 group-hover:underline cursor-pointer">
                                  {child.name}
                                </div>
                              </div>
                            </div>

                            {/* Child events track */}
                            <div className="flex-1 relative h-10 mr-8">
                              {/* Health bar background layer */}
                              <HealthBarTrack
                                events={child.events}
                                startTime={visibleTimeRange.start}
                                windowMs={visibleTimeRange.windowMs}
                                now={visibleTimeRange.now}
                              />
                              {/* Event markers layer */}
                              <div className="absolute inset-0 z-10">
                                {child.events.map((event, eventIdx) => {
                                  const x = timeToX(new Date(event.timestamp).getTime())
                                  if (x < 0 || x > 100) return null
                                  return (
                                    <EventMarker
                                      key={`${child.id}-${event.id}-${eventIdx}`}
                                      event={event}
                                      x={x}
                                      selected={selectedEvent?.id === event.id}
                                      onClick={() => setSelectedEvent(selectedEvent?.id === event.id ? null : event)}
                                      small
                                    />
                                  )
                                })}
                              </div>
                            </div>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
          )}
        </div>
      </div>

      {/* Event detail panel */}
      {selectedEvent && (
        <EventDetailPanel event={selectedEvent} onClose={() => setSelectedEvent(null)} onResourceClick={onResourceClick} />
      )}
    </div>
  )
}

// Legend item with hover tooltip
interface LegendItemProps {
  color: string
  label: string
  description: string
  dashed?: boolean
}

function LegendItem({ color, label, description, dashed }: LegendItemProps) {
  return (
    <Tooltip content={description} position="top">
      <span className="flex items-center gap-1 cursor-help">
        <span className={clsx(
          'w-2 h-2 rounded-full',
          dashed ? 'border border-dashed border-current bg-transparent' : color
        )} />
        <span>{label}</span>
      </span>
    </Tooltip>
  )
}

// Health bar legend item - shows a bar instead of a dot
function HealthBarLegendItem({ color, label, description }: LegendItemProps) {
  return (
    <Tooltip content={description} position="top">
      <span className="flex items-center gap-1 cursor-help">
        <span className={clsx('w-4 h-2 rounded-sm', color)} />
        <span>{label}</span>
      </span>
    </Tooltip>
  )
}

// Health bar track component that renders health spans as background
interface HealthBarTrackProps {
  events: TimelineEvent[]
  startTime: number
  windowMs: number
  now: number
}

function HealthBarTrack({ events, startTime, windowMs, now }: HealthBarTrackProps) {
  // Filter to change events for health state computation
  const changeEvents = events.filter(e => isChangeEvent(e))

  // Build health spans from events
  const { spans, createdAt, createdBeforeWindow } = buildHealthSpans(
    changeEvents,
    startTime,
    now,
    events // All events for createdAt extraction
  )

  if (spans.length === 0) return null

  return (
    <div className="absolute inset-0 z-0">
      {spans.map((span, i) => {
        const left = sharedTimeToX(span.start, startTime, windowMs)
        const right = sharedTimeToX(span.end, startTime, windowMs)
        const width = right - left

        // Skip spans outside visible range
        if (right < 0 || left > 100) return null

        // Clamp to visible range
        const clampedLeft = Math.max(0, left)
        const clampedWidth = Math.min(100 - clampedLeft, width - (clampedLeft - left))

        if (clampedWidth <= 0) return null

        return (
          <HealthSpan
            key={i}
            health={span.health}
            left={clampedLeft}
            width={clampedWidth}
            createdBefore={createdBeforeWindow && i === 0 ? new Date(createdAt!) : undefined}
          />
        )
      })}
    </div>
  )
}

// Critical issue reasons that should be prominently highlighted with icons
// This should align with PROBLEMATIC_REASONS in resource-hierarchy.ts
const CRITICAL_ISSUE_REASONS = new Set([
  // Container state issues
  'BackOff', 'CrashLoopBackOff', 'Failed', 'Error',
  'OOMKilling', 'OOMKilled',
  'CreateContainerConfigError', 'CreateContainerError', 'RunContainerError',
  'InvalidImageName', 'ErrImagePull', 'ImagePullBackOff',
  'ContainerStatusUnknown',

  // Pod scheduling/lifecycle issues
  'FailedScheduling', 'FailedMount', 'FailedAttachVolume',
  'FailedCreate', 'FailedDelete', 'Unhealthy', 'Killing', 'Evicted',
  'FailedSync', 'FailedValidation',
  'FailedPreStopHook', 'FailedPostStartHook',
  'HostPortConflict', 'InsufficientMemory', 'InsufficientCPU',

  // Node conditions
  'NodeNotReady', 'NetworkNotReady', 'KubeletNotReady',
  'MemoryPressure', 'DiskPressure', 'PIDPressure',
  'NodeStatusUnknown',

  // Deployment/workload issues
  'ProgressDeadlineExceeded', 'ReplicaFailure',
  'MinimumReplicasUnavailable',

  // HPA issues
  'FailedGetScale', 'FailedRescale', 'FailedUpdateScale',
  'FailedGetResourceMetric', 'FailedComputeMetricsReplicas',

  // PVC/storage issues
  'ProvisioningFailed', 'FailedBinding', 'VolumeFailedDelete',

  // Job issues
  'DeadlineExceeded', 'BackoffLimitExceeded',
])

// Get the appropriate icon for a critical issue
function getIssueIcon(reason: string | undefined): React.ComponentType<{ className?: string }> | null {
  if (!reason) return null

  // Memory issues (OOM)
  if (reason === 'OOMKilled' || reason === 'OOMKilling' ||
      reason === 'InsufficientMemory' || reason === 'MemoryPressure') return MemoryStick

  // Crash/restart issues
  if (reason === 'CrashLoopBackOff' || reason === 'BackOff') return RefreshCw

  // Image pull issues
  if (reason === 'ImagePullBackOff' || reason === 'ErrImagePull' || reason === 'InvalidImageName') return Package

  // Container creation/runtime errors
  if (reason === 'CreateContainerConfigError' || reason === 'CreateContainerError' ||
      reason === 'RunContainerError' || reason === 'ContainerStatusUnknown') return Box

  // Scheduling/mount/node issues
  if (reason === 'FailedScheduling' || reason === 'FailedMount' || reason === 'FailedAttachVolume' ||
      reason === 'NodeNotReady' || reason === 'NetworkNotReady' || reason === 'KubeletNotReady' ||
      reason === 'NodeStatusUnknown' || reason === 'HostPortConflict') return Ban

  // Resource pressure (disk, CPU, PID)
  if (reason === 'DiskPressure' || reason === 'PIDPressure' || reason === 'InsufficientCPU') return Gauge

  // Deployment rollout issues
  if (reason === 'ProgressDeadlineExceeded' || reason === 'ReplicaFailure' ||
      reason === 'MinimumReplicasUnavailable') return RotateCcw

  // HPA scaling issues
  if (reason === 'FailedGetScale' || reason === 'FailedRescale' || reason === 'FailedUpdateScale' ||
      reason === 'FailedGetResourceMetric' || reason === 'FailedComputeMetricsReplicas') return Gauge

  // PVC/storage issues
  if (reason === 'ProvisioningFailed' || reason === 'FailedBinding' || reason === 'VolumeFailedDelete') return HardDrive

  // Job timeout issues
  if (reason === 'DeadlineExceeded' || reason === 'BackoffLimitExceeded') return Timer

  // Probe failures and general unhealthy
  if (reason === 'Unhealthy') return AlertTriangle

  // General failures - use warning circle
  if (reason.startsWith('Failed') || reason === 'Evicted' || reason === 'Killing' || reason === 'Error') return AlertCircle

  return null
}

// Check if event is a critical issue that deserves special highlighting
function isCriticalIssue(event: TimelineEvent): boolean {
  return !!(event.reason && CRITICAL_ISSUE_REASONS.has(event.reason))
}

interface EventMarkerProps {
  event: TimelineEvent
  x: number
  selected?: boolean
  onClick: () => void
  dimmed?: boolean // For aggregated child events
  small?: boolean // For child lane events
}

function EventMarker({ event, x, selected, onClick, dimmed, small }: EventMarkerProps) {
  const isChange = isChangeEvent(event)
  const isProblematic = isProblematicEvent(event) // Includes warnings + problematic reasons like BackOff
  const isHistorical = isHistoricalEvent(event)
  const isCritical = isCriticalIssue(event)
  const IssueIcon = getIssueIcon(event.reason)

  const getMarkerStyle = () => {
    // Historical events use outline style (border instead of fill)
    // Non-historical use solid fill
    if (isHistorical) {
      // Outline style for historical - visible border, subtle background
      if (isProblematic) {
        return 'bg-amber-500/20 border-2 border-dashed border-amber-500/60'
      }
      if (isChange) {
        switch (event.eventType) {
          case 'add':
            return 'bg-green-500/20 border-2 border-dashed border-green-500/60'
          case 'delete':
            return 'bg-red-500/20 border-2 border-dashed border-red-500/60'
          case 'update':
            return 'bg-blue-500/20 border-2 border-dashed border-blue-500/60'
        }
      }
      return 'bg-theme-hover/30 border-2 border-dashed border-theme-border-light'
    }

    // Critical issues get red background to stand out
    if (isCritical) {
      return 'bg-red-500'
    }

    // Solid fill for real-time events
    const opacity = dimmed ? '/50' : ''
    // Problematic events (warnings, BackOff, etc.) are always amber/orange
    if (isProblematic) {
      return `bg-amber-500${opacity}`
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add':
          return `bg-green-500${opacity}`
        case 'delete':
          return `bg-red-500${opacity}`
        case 'update':
          return `bg-blue-500${opacity}`
      }
    }
    return `bg-theme-text-tertiary${opacity}`
  }

  const markerClasses = getMarkerStyle()

  // Build tooltip text - focus on what happened, explain the color meaning
  const getRelativeTime = (timestamp: string) => {
    const diff = Date.now() - new Date(timestamp).getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return 'just now'
    if (mins < 60) return `${mins}m ago`
    const hours = Math.floor(mins / 60)
    if (hours < 24) return `${hours}h ago`
    return `${Math.floor(hours / 24)}d ago`
  }

  // Get human-readable operation label with color indicator
  const getOperationLabel = () => {
    if (isProblematic) {
      return `⚠ ${event.reason || 'Warning'}`
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add': return '● Created'
        case 'delete': return '● Deleted'
        case 'update': return '● Modified'
        default: return '● Changed'
      }
    }
    if (event.reason) {
      return `● ${event.reason}`
    }
    return '● Event'
  }

  const tooltipLines: string[] = []
  tooltipLines.push(getOperationLabel())
  if (event.message) {
    // Truncate long messages
    const msg = event.message.length > 60 ? event.message.slice(0, 60) + '...' : event.message
    tooltipLines.push(msg)
  }
  tooltipLines.push(getRelativeTime(event.timestamp))
  if (isHistoricalEvent(event)) tooltipLines.push('(from metadata)')

  const tooltipText = tooltipLines.join(' · ')

  // Critical issues get larger markers with icons
  if (isCritical && IssueIcon && !small) {
    return (
      <Tooltip
        content={tooltipText}
        position="top"
        delay={100}
        wrapperClassName="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 z-20"
        wrapperStyle={{ left: `${x}%` }}
      >
        <button
          className={clsx(
            'rounded-full transition-all flex items-center justify-center',
            'w-5 h-5',
            markerClasses,
            selected ? 'ring-2 ring-white ring-offset-2 ring-offset-theme-base scale-125' : 'hover:scale-110',
            'shadow-sm'
          )}
          onClick={(e) => {
            e.stopPropagation()
            onClick()
          }}
        >
          <IssueIcon className="w-3 h-3 text-white" />
        </button>
      </Tooltip>
    )
  }

  return (
    <Tooltip
      content={tooltipText}
      position="top"
      delay={100}
      wrapperClassName={clsx(
        'absolute top-1/2 -translate-y-1/2 -translate-x-1/2',
        dimmed ? 'z-5' : isHistorical ? 'z-5' : 'z-10'
      )}
      wrapperStyle={{ left: `${x}%` }}
    >
      <button
        className={clsx(
          'rounded-full transition-all',
          small ? 'w-2.5 h-2.5' : 'w-3 h-3',
          markerClasses,
          selected ? 'ring-2 ring-white ring-offset-2 ring-offset-theme-base scale-150' : 'hover:scale-125'
        )}
        onClick={(e) => {
          e.stopPropagation()
          onClick()
        }}
      />
    </Tooltip>
  )
}

interface EventDetailPanelProps {
  event: TimelineEvent
  onClose: () => void
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

function EventDetailPanel({ event, onClose, onResourceClick }: EventDetailPanelProps) {
  const isChange = isChangeEvent(event)
  const isHistorical = isHistoricalEvent(event)
  const isProblematic = isProblematicEvent(event)

  return (
    <div className={clsx(
      "fixed bottom-0 left-0 right-0 z-50 border-t p-4 max-h-72 overflow-auto shadow-2xl",
      isProblematic ? "border-amber-300 dark:border-amber-700 bg-amber-50 dark:bg-amber-950" : "border-theme-border bg-theme-surface"
    )}>
      <div className="flex items-start justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="text-xs px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-secondary">
              {event.kind}
            </span>
            <button
              onClick={() => onResourceClick?.(event.kind, event.namespace, event.name)}
              className="text-theme-text-primary font-medium hover:text-blue-600 dark:hover:text-blue-300 cursor-pointer"
            >
              {event.name}
            </button>
            {event.namespace && (
              <span className="text-xs text-theme-text-tertiary">in {event.namespace}</span>
            )}
            {isHistorical && (
              <span className="text-xs px-1.5 py-0.5 bg-theme-hover rounded text-theme-text-secondary flex items-center gap-1">
                <Clock className="w-3 h-3" />
                historical
              </span>
            )}
          </div>
          <div className="text-xs text-theme-text-tertiary mt-1">
            {formatFullTime(new Date(event.timestamp))}
            {isHistorical && event.reason && (
              <span className="ml-2 text-theme-text-secondary">({event.reason})</span>
            )}
          </div>
        </div>
        <button
          onClick={onClose}
          className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          title="Close (Esc)"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {isChange ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className={clsx('text-sm font-medium', isOperation(event.eventType) && getOperationColor(event.eventType))}>
              {event.eventType}
            </span>
            {event.healthState && event.healthState !== 'unknown' && (
              <span className={clsx('text-xs px-1.5 py-0.5 rounded', getHealthBadgeColor(event.healthState))}>
                {event.healthState}
              </span>
            )}
          </div>
          {event.diff && <DiffViewer diff={event.diff} />}
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className={clsx('text-sm font-medium', isProblematic ? 'text-amber-700 dark:text-amber-300' : 'text-green-700 dark:text-green-300')}>
              {event.reason}
            </span>
            {event.eventType && (
              <span className={clsx('text-xs px-1.5 py-0.5 rounded', getEventTypeColor(event.eventType))}>
                {event.eventType}
              </span>
            )}
            {event.count && event.count > 1 && (
              <span className="text-xs text-theme-text-tertiary">x{event.count}</span>
            )}
          </div>
          {event.message && <p className={clsx("text-sm", isProblematic ? "text-amber-700 dark:text-amber-200" : "text-theme-text-secondary")}>{event.message}</p>}
        </div>
      )}
    </div>
  )
}
