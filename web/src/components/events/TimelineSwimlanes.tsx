import { useState, useMemo, useRef, useCallback, useEffect } from 'react'
import { clsx } from 'clsx'
import {
  AlertCircle,
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
} from 'lucide-react'
import type { TimelineEvent, TimeRange, Topology } from '../../types'
import { isWorkloadKind } from '../../types'
import { DiffViewer } from './DiffViewer'

interface TimelineSwimlanesProps {
  events: TimelineEvent[]
  isLoading?: boolean
  filterTimeRange?: TimeRange
  onResourceClick?: (kind: string, namespace: string, name: string) => void
  viewMode?: 'list' | 'swimlane'
  onViewModeChange?: (mode: 'list' | 'swimlane') => void
  topology?: Topology
}

interface ResourceLane {
  id: string
  kind: string
  namespace: string
  name: string
  events: TimelineEvent[]
  isWorkload: boolean
  children?: ResourceLane[] // Child resources (Pods, ReplicaSets)
  childEventCount?: number // Total events across all children
}

function formatAxisTime(date: Date): string {
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function formatFullTime(date: Date): string {
  return date.toLocaleString()
}

// Event reasons that indicate problems even if eventType is "Normal"
const PROBLEMATIC_REASONS = new Set([
  'BackOff', 'CrashLoopBackOff', 'Failed', 'FailedScheduling', 'FailedMount',
  'FailedAttachVolume', 'FailedCreate', 'FailedDelete', 'Unhealthy', 'Killing',
  'Evicted', 'OOMKilling', 'OOMKilled', 'NodeNotReady', 'NetworkNotReady',
  'FailedSync', 'FailedValidation', 'InvalidImageName', 'ErrImagePull',
  'ImagePullBackOff', 'FailedPreStopHook', 'FailedPostStartHook',
])

// Check if an event is actually problematic (warning or error condition)
function isProblematicEvent(event: TimelineEvent): boolean {
  if (event.eventType === 'Warning') return true
  if (event.reason && PROBLEMATIC_REASONS.has(event.reason)) return true
  return false
}

// Known noisy resources that update constantly (leases, locks, status tracking)
const NOISY_NAME_PATTERNS = [
  /^kube-scheduler$/,
  /^kube-controller-manager$/,
  /-leader-election$/,
  /-lock$/,
  /-lease$/,
  /^cluster-autoscaler-status$/,
  /^cluster-kubestore$/,
  /^datadog-leader-election$/,
  /^cert-manager-controller$/,
]

// Event kind = K8s Event objects themselves (not their content) - always noise
// Their content is captured via k8s_event type, tracking their lifecycle is useless
const NOISY_KINDS = new Set(['Lease', 'Endpoints', 'EndpointSlice', 'Event'])

// Check if an event is "routine noise" (constant heartbeats, lease updates, etc.)
function isRoutineEvent(event: TimelineEvent): boolean {
  // K8s Event objects - their lifecycle (add/update/delete) is always noise
  // We capture their content via k8s_event type, not by tracking the Event resource
  if (event.kind === 'Event' && event.type === 'change') return true

  // Only filter update operations for other kinds - adds and deletes are interesting
  if (event.operation !== 'update') return false

  // Known noisy resource kinds (for updates)
  if (NOISY_KINDS.has(event.kind)) return true

  // Known noisy resource name patterns
  if (NOISY_NAME_PATTERNS.some(pattern => pattern.test(event.name))) return true

  // ConfigMaps with noisy suffixes
  if (event.kind === 'ConfigMap') {
    if (event.name.endsWith('-lock') ||
        event.name.endsWith('-lease') ||
        event.name.endsWith('-leader') ||
        event.name.includes('kubestore')) {
      return true
    }
  }

  return false
}

// Calculate "interestingness" score for sorting lanes
// Higher score = more interesting = should appear higher in list
function calculateInterestingness(lane: ResourceLane): number {
  let score = 0
  const allEvents = [...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]

  // 1. Resource type priority (workloads are more interesting than config)
  const kindScores: Record<string, number> = {
    Deployment: 100, StatefulSet: 100, DaemonSet: 100,
    Service: 90, Ingress: 90,
    Pod: 70, ReplicaSet: 50,
    Job: 80, CronJob: 80,
    HPA: 60,
    ConfigMap: 20, Secret: 10, PVC: 30,
  }
  score += kindScores[lane.kind] || 40

  // 2. Problematic events (warnings, BackOff, etc.) are very interesting (+50 each, max 200)
  const problematicCount = allEvents.filter(e => isProblematicEvent(e)).length
  score += Math.min(problematicCount * 50, 200)

  // 3. Event variety is interesting (mix of add/update/delete vs just updates)
  const operations = new Set(allEvents.map(e => e.operation).filter(Boolean))
  score += operations.size * 15 // Up to 45 for all three types

  // 4. Add/delete events are more interesting than updates
  const addCount = allEvents.filter(e => e.operation === 'add').length
  const deleteCount = allEvents.filter(e => e.operation === 'delete').length
  score += addCount * 10 + deleteCount * 15

  // 5. Having children (grouped resources) is interesting
  if (lane.children && lane.children.length > 0) {
    score += 30 + Math.min(lane.children.length * 5, 50)
  }

  // 6. System namespaces are less interesting
  const systemNamespaces = ['kube-system', 'kube-public', 'kube-node-lease', 'gke-managed-system']
  if (systemNamespaces.includes(lane.namespace)) {
    score -= 50
  }

  // 7. Recency bonus (events in last 5 minutes)
  const fiveMinutesAgo = Date.now() - 5 * 60 * 1000
  const recentEvents = allEvents.filter(e => new Date(e.timestamp).getTime() > fiveMinutesAgo)
  score += Math.min(recentEvents.length * 5, 30)

  // 8. Penalty for excessive repeated updates (noisy resources)
  const updateCount = allEvents.filter(e => e.operation === 'update').length
  if (updateCount > 10 && operations.size === 1) {
    // Many updates but no variety = noisy, penalize
    score -= Math.min(updateCount * 2, 60)
  }

  return score
}

export function TimelineSwimlanes({ events, isLoading, filterTimeRange: _filterTimeRange = '1h', onResourceClick, viewMode, onViewModeChange, topology }: TimelineSwimlanesProps) {
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
  const [showRoutineUpdates, setShowRoutineUpdates] = useState(false)

  // Stable lane ordering - tracks the order lanes were first seen
  const [laneOrder, setLaneOrder] = useState<Map<string, number>>(new Map())
  const [sortVersion, setSortVersion] = useState(0) // Increment to re-sort lanes

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
    let filtered = events

    // Filter out routine/noisy updates unless toggled on
    if (!showRoutineUpdates) {
      filtered = filtered.filter(e => !isRoutineEvent(e))
    }

    // Apply search filter
    if (searchTerm) {
      const term = searchTerm.toLowerCase()
      filtered = filtered.filter(e =>
        e.name.toLowerCase().includes(term) ||
        e.kind.toLowerCase().includes(term) ||
        e.namespace?.toLowerCase().includes(term) ||
        e.reason?.toLowerCase().includes(term) ||
        e.message?.toLowerCase().includes(term)
      )
    }

    return filtered
  }, [events, searchTerm, showRoutineUpdates])

  // Count how many events are being filtered
  const routineEventCount = useMemo(() => {
    return events.filter(e => isRoutineEvent(e)).length
  }, [events])

  // Build hierarchical lanes using owner references + topology edges
  const lanes = useMemo(() => {
    const laneMap = new Map<string, ResourceLane>()

    // Helper: convert topology node ID to lane ID format
    const nodeIdToLaneId = (nodeId: string): string | null => {
      const parts = nodeId.split('-')
      if (parts.length < 3) return null
      const kind = parts[0]
      const namespace = parts[1]
      const name = parts.slice(2).join('-')
      const kindMap: Record<string, string> = {
        pod: 'Pod', service: 'Service', deployment: 'Deployment',
        replicaset: 'ReplicaSet', statefulset: 'StatefulSet', daemonset: 'DaemonSet',
        ingress: 'Ingress', configmap: 'ConfigMap', secret: 'Secret',
        job: 'Job', cronjob: 'CronJob', hpa: 'HPA', podgroup: 'PodGroup'
      }
      return `${kindMap[kind] || kind}/${namespace}/${name}`
    }

    // Track events that should be attached to their owner instead of their own lane
    const eventsToAttach: { event: TimelineEvent; ownerLaneId: string }[] = []

    // First pass: create lanes from events (but not for Events with owners)
    for (const event of filteredEvents) {
      // K8s Events with an owner (involvedObject) should attach to that resource, not get their own lane
      if (event.kind === 'Event' && event.owner) {
        const ownerLaneId = `${event.owner.kind}/${event.namespace}/${event.owner.name}`
        eventsToAttach.push({ event, ownerLaneId })
        continue
      }

      const laneId = `${event.kind}/${event.namespace}/${event.name}`
      if (!laneMap.has(laneId)) {
        laneMap.set(laneId, {
          id: laneId,
          kind: event.kind,
          namespace: event.namespace,
          name: event.name,
          events: [],
          isWorkload: isWorkloadKind(event.kind),
          children: [],
          childEventCount: 0,
        })
      }
      laneMap.get(laneId)!.events.push(event)
    }

    // Attach K8s Events to their owner lanes
    for (const { event, ownerLaneId } of eventsToAttach) {
      if (laneMap.has(ownerLaneId)) {
        // Owner exists, attach event to it
        laneMap.get(ownerLaneId)!.events.push(event)
      } else {
        // Owner doesn't exist yet, create lane for it
        const parts = ownerLaneId.split('/')
        laneMap.set(ownerLaneId, {
          id: ownerLaneId,
          kind: parts[0],
          namespace: parts[1],
          name: parts.slice(2).join('/'),
          events: [event],
          isWorkload: isWorkloadKind(parts[0]),
          children: [],
          childEventCount: 0,
        })
      }
    }

    // Build parent map from BOTH owner references AND topology edges
    const laneParent = new Map<string, string>() // childLaneId -> parentLaneId

    // Source 1: Owner references from events (most reliable for Deployment→RS→Pod)
    for (const [laneId, lane] of laneMap) {
      const eventWithOwner = lane.events.find(e => e.owner)
      if (eventWithOwner?.owner) {
        const ownerLaneId = `${eventWithOwner.owner.kind}/${lane.namespace}/${eventWithOwner.owner.name}`

        // Create parent lane if it doesn't exist (parent may have no events)
        if (!laneMap.has(ownerLaneId)) {
          laneMap.set(ownerLaneId, {
            id: ownerLaneId,
            kind: eventWithOwner.owner.kind,
            namespace: lane.namespace,
            name: eventWithOwner.owner.name,
            events: [],
            isWorkload: isWorkloadKind(eventWithOwner.owner.kind),
            children: [],
            childEventCount: 0,
          })
        }
        laneParent.set(laneId, ownerLaneId)
      }
    }

    // Source 2: Topology edges (for Service→Deployment, Ingress→Service, ConfigMap→Deployment)
    // Only process edges where AT LEAST ONE side exists in laneMap (has events)
    if (topology?.edges) {
      for (const edge of topology.edges) {
        const sourceLaneId = nodeIdToLaneId(edge.source)
        const targetLaneId = nodeIdToLaneId(edge.target)
        if (!sourceLaneId || !targetLaneId) continue

        // manages: Deployment→RS→Pod (already covered by owner refs, skip)
        if (edge.type === 'manages') continue

        // At least one side must have events
        const sourceExists = laneMap.has(sourceLaneId)
        const targetExists = laneMap.has(targetLaneId)
        if (!sourceExists && !targetExists) continue

        // exposes: Service→Deployment (Service is parent of Deployment)
        // routes-to: Ingress→Service (Ingress is parent of Service)
        if (edge.type === 'exposes' || edge.type === 'routes-to') {
          if (!laneParent.has(targetLaneId) && targetExists) {
            // Create parent lane if needed
            if (!sourceExists) {
              const parts = sourceLaneId.split('/')
              laneMap.set(sourceLaneId, {
                id: sourceLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(targetLaneId, sourceLaneId)
          }
        }

        // configures/uses: ConfigMap→Deployment (Deployment is parent of ConfigMap)
        if (edge.type === 'configures' || edge.type === 'uses') {
          if (!laneParent.has(sourceLaneId) && sourceExists) {
            // Create parent lane if needed
            if (!targetExists) {
              const parts = targetLaneId.split('/')
              laneMap.set(targetLaneId, {
                id: targetLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(sourceLaneId, targetLaneId)
          }
        }
      }
    }

    // Walk up parent chain to find root
    const findRoot = (laneId: string, visited = new Set<string>()): string => {
      if (visited.has(laneId)) return laneId
      visited.add(laneId)
      const parentId = laneParent.get(laneId)
      if (parentId && laneMap.has(parentId)) {
        return findRoot(parentId, visited)
      }
      return laneId
    }

    // Second pass: group children under their root
    const topLevelLanes: ResourceLane[] = []
    const childLaneIds = new Set<string>()

    for (const [laneId] of laneMap) {
      if (!laneParent.has(laneId)) continue
      const rootId = findRoot(laneId)
      if (rootId !== laneId && laneMap.has(rootId)) {
        const root = laneMap.get(rootId)!
        const child = laneMap.get(laneId)!
        root.children!.push(child)
        root.childEventCount = (root.childEventCount || 0) + child.events.length
        childLaneIds.add(laneId)
      }
    }

    // Collect top-level lanes (not children of anyone)
    for (const [laneId, lane] of laneMap) {
      if (!childLaneIds.has(laneId)) {
        // Sort children by kind priority then by latest event
        if (lane.children && lane.children.length > 0) {
          const kindPriority: Record<string, number> = {
            Service: 1, Deployment: 2, StatefulSet: 2, DaemonSet: 2,
            ReplicaSet: 3, Pod: 4, ConfigMap: 5, Secret: 5
          }
          lane.children.sort((a, b) => {
            const aPriority = kindPriority[a.kind] || 10
            const bPriority = kindPriority[b.kind] || 10
            if (aPriority !== bPriority) return aPriority - bPriority
            const aLatest = Math.max(...a.events.map((e) => new Date(e.timestamp).getTime()))
            const bLatest = Math.max(...b.events.map((e) => new Date(e.timestamp).getTime()))
            return bLatest - aLatest
          })
        }
        topLevelLanes.push(lane)
      }
    }

    // Stable sort: use existing lane order for known lanes, put new lanes at top
    return topLevelLanes.sort((a, b) => {
      const aOrder = laneOrder.get(a.id)
      const bOrder = laneOrder.get(b.id)

      // Both known: use stable order
      if (aOrder !== undefined && bOrder !== undefined) {
        return aOrder - bOrder
      }
      // New lanes go to top (sorted by interestingness among themselves)
      if (aOrder === undefined && bOrder === undefined) {
        return calculateInterestingness(b) - calculateInterestingness(a)
      }
      // New lane (undefined) goes before known lane
      return aOrder === undefined ? -1 : 1
    })
  }, [filteredEvents, topology, laneOrder, sortVersion])

  // Track lane order for stable sorting - record order as lanes appear
  useEffect(() => {
    setLaneOrder(prev => {
      const newOrder = new Map(prev)
      let maxOrder = prev.size > 0 ? Math.max(...prev.values()) : -1
      let changed = false

      for (const lane of lanes) {
        if (!newOrder.has(lane.id)) {
          maxOrder++
          newOrder.set(lane.id, maxOrder)
          changed = true
        }
      }

      return changed ? newOrder : prev
    })
  }, [lanes])

  // Re-sort lanes by interestingness score
  const handleRefreshSort = useCallback(() => {
    // Reset lane order to force re-sort by interestingness
    const newOrder = new Map<string, number>()
    const sorted = [...lanes].sort((a, b) => {
      return calculateInterestingness(b) - calculateInterestingness(a)
    })
    sorted.forEach((lane, idx) => newOrder.set(lane.id, idx))
    setLaneOrder(newOrder)
    setSortVersion(v => v + 1)
  }, [lanes])

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
    const now = Date.now()
    const windowMs = zoom * 60 * 60 * 1000
    const end = now - panOffset
    const start = end - windowMs
    return { start, end, windowMs }
  }, [zoom, panOffset])

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
    } else {
      intervalMs = 2 * 60 * 60 * 1000
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

  // Zoom handlers
  const handleZoomIn = () => setZoom((z) => Math.max(0.25, z / 1.5))
  const handleZoomOut = () => setZoom((z) => Math.min(24, z * 1.5))

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

  // Wheel zoom
  const handleWheel = useCallback((e: React.WheelEvent) => {
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault()
      const delta = e.deltaY > 0 ? 1.2 : 0.8
      setZoom((z) => Math.max(0.25, Math.min(24, z * delta)))
    }
  }, [])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading timeline...
      </div>
    )
  }

  if (lanes.length === 0) {
    const hasFilteredEvents = events.length > 0 && filteredEvents.length === 0
    const hasRoutineOnly = routineEventCount > 0 && routineEventCount === events.length

    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
        <AlertCircle className="w-12 h-12 mb-4 opacity-50" />
        {hasFilteredEvents ? (
          <>
            <p className="text-lg">No matching events</p>
            <p className="text-sm mt-1">
              {searchTerm ? `No results for "${searchTerm}"` : 'Try adjusting your filters'}
            </p>
          </>
        ) : hasRoutineOnly ? (
          <>
            <p className="text-lg">Only routine events</p>
            <p className="text-sm mt-1">
              {routineEventCount} routine event{routineEventCount !== 1 ? 's' : ''} hidden.{' '}
              <button
                onClick={() => setShowRoutineUpdates(true)}
                className="text-blue-400 hover:text-blue-300 underline"
              >
                Show them
              </button>
            </p>
          </>
        ) : (
          <>
            <p className="text-lg">No events yet</p>
            <p className="text-sm mt-1">Events will appear here as resources change</p>
          </>
        )}
      </div>
    )
  }

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
              {zoom < 1 ? `${Math.round(zoom * 60)}m` : `${zoom}h`} window
            </span>
            {panOffset > 0 && (
              <button
                onClick={() => setPanOffset(0)}
                className="px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-theme-elevated rounded"
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
          {/* Legend */}
          <div className="flex items-center gap-3 text-xs text-theme-text-secondary">
            <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-green-500" />add</span>
            <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-blue-500" />update</span>
            <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-red-500" />delete</span>
            <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-amber-500" />warning</span>
          </div>
          <span className="text-xs text-theme-text-tertiary">
            {lanes.length} resource{lanes.length !== 1 ? 's' : ''} · {filteredEvents.length} event
            {filteredEvents.length !== 1 ? 's' : ''}
            {searchTerm && ` (filtered)`}
          </span>
          {/* Routine updates toggle */}
          {routineEventCount > 0 && (
            <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary cursor-pointer hover:text-theme-text-secondary">
              <input
                type="checkbox"
                checked={showRoutineUpdates}
                onChange={(e) => setShowRoutineUpdates(e.target.checked)}
                className="w-3.5 h-3.5 rounded border-theme-border-light bg-theme-elevated text-blue-500 focus:ring-blue-500 focus:ring-offset-0"
              />
              <span>Show routine events ({routineEventCount})</span>
            </label>
          )}
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
          <div className="sticky top-0 z-10 bg-theme-surface border-b border-theme-border">
            <div className="flex">
              <div className="w-80 flex-shrink-0 border-r border-theme-border px-3 py-2">
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
              </div>
            </div>
          </div>

          {/* Swimlanes */}
          <div>
            {lanes.map((lane) => {
              const isExpanded = expandedLanes.has(lane.id)
              const hasChildren = lane.children && lane.children.length > 0

              return (
                <div key={lane.id}>
                  {/* Parent lane */}
                  <div className="border-b-subtle">
                    <div className="flex">
                      {/* Lane label */}
                      <div className="w-80 flex-shrink-0 border-r border-theme-border px-3 py-2 flex items-center gap-1">
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
                              lane.isWorkload ? 'bg-blue-900/50 text-blue-400' : 'bg-theme-elevated text-theme-text-secondary'
                            )}>
                              {lane.kind}
                            </span>
                            {hasChildren && (
                              <span className="text-xs text-theme-text-tertiary">
                                +{lane.children!.length}
                              </span>
                            )}
                          </div>
                          <div className="text-sm text-theme-text-primary break-words group-hover:text-blue-300">
                            {lane.name}
                          </div>
                          <div className="text-xs text-theme-text-tertiary">{lane.namespace}</div>
                        </div>
                      </div>

                      {/* Events track - ALWAYS shows all events (summary view) */}
                      <div className="flex-1 relative h-12 mr-8">
                        {/* All events combined: own + children, sorted so important events render on top */}
                        {[...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]
                          .sort((a, b) => {
                            // Priority: updates (0) < adds (1) < deletes (2) < problematic/warnings (3)
                            // Lower priority renders first (behind), higher renders last (on top)
                            const getPriority = (e: TimelineEvent) => {
                              if (isProblematicEvent(e)) return 3
                              if (e.operation === 'delete') return 2
                              if (e.operation === 'add') return 1
                              return 0 // updates and others
                            }
                            return getPriority(a) - getPriority(b)
                          })
                          .map((event) => {
                            const x = timeToX(new Date(event.timestamp).getTime())
                            if (x < 0 || x > 100) return null
                            return (
                              <EventMarker
                                key={event.id}
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

                  {/* Child lanes (when expanded) - includes parent as first row */}
                  {isExpanded && hasChildren && (
                    <div className="border-l-2 border-blue-500/40 ml-3 bg-theme-surface/30">
                      {/* Parent's own events as first row (only if it has events) */}
                      {lane.events.length > 0 && (
                        <div className="border-b-subtle">
                          <div className="flex">
                            <div
                              className="w-[19.25rem] flex-shrink-0 border-r border-theme-border/50 pl-4 pr-3 py-1.5 flex items-center gap-2 cursor-pointer hover:bg-theme-elevated/30 group"
                              onClick={() => onResourceClick?.(lane.kind, lane.namespace, lane.name)}
                            >
                              <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-1">
                                  <span className="text-xs px-1 py-0.5 rounded bg-blue-900/50 text-blue-400">
                                    {lane.kind}
                                  </span>
                                </div>
                                <div className="text-sm text-theme-text-secondary break-words group-hover:text-blue-300">
                                  {lane.name}
                                </div>
                              </div>
                            </div>
                            <div className="flex-1 relative h-10 mr-8">
                              {lane.events.map((event) => {
                                const x = timeToX(new Date(event.timestamp).getTime())
                                if (x < 0 || x > 100) return null
                                return (
                                  <EventMarker
                                    key={event.id}
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
                              className="w-[19.25rem] flex-shrink-0 border-r border-theme-border/50 pl-4 pr-3 py-1.5 flex items-center gap-2 cursor-pointer hover:bg-theme-elevated/30 group"
                              onClick={() => onResourceClick?.(child.kind, child.namespace, child.name)}
                            >
                              <div className="flex-1 min-w-0">
                                <div className="flex items-center gap-1">
                                  <span className="text-xs px-1 py-0.5 rounded bg-theme-elevated/50 text-theme-text-secondary">
                                    {child.kind}
                                  </span>
                                </div>
                                <div className="text-sm text-theme-text-secondary break-words group-hover:text-blue-300">
                                  {child.name}
                                </div>
                              </div>
                            </div>

                            {/* Child events track */}
                            <div className="flex-1 relative h-10 mr-8">
                              {child.events.map((event) => {
                                const x = timeToX(new Date(event.timestamp).getTime())
                                if (x < 0 || x > 100) return null
                                return (
                                  <EventMarker
                                    key={event.id}
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
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* Event detail panel */}
      {selectedEvent && (
        <EventDetailPanel event={selectedEvent} onClose={() => setSelectedEvent(null)} />
      )}
    </div>
  )
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
  const isChange = event.type === 'change'
  const isProblematic = isProblematicEvent(event) // Includes warnings + problematic reasons like BackOff
  const isHistorical = event.isHistorical

  const getMarkerStyle = () => {
    // Historical events use outline style (border instead of fill)
    // Non-historical use solid fill
    if (isHistorical) {
      // Outline style for historical - visible border, subtle background
      if (isProblematic) {
        return 'bg-amber-500/20 border-2 border-dashed border-amber-500/60'
      }
      if (isChange) {
        switch (event.operation) {
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

    // Solid fill for real-time events
    const opacity = dimmed ? '/50' : ''
    // Problematic events (warnings, BackOff, etc.) are always amber/orange
    if (isProblematic) {
      return `bg-amber-500${opacity}`
    }
    if (isChange) {
      switch (event.operation) {
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

  // No icons inside markers - at this size they just create visual noise
  // Colors alone distinguish event types, and dots overlap cleanly

  const markerClasses = getMarkerStyle()

  // Build tooltip text - focus on what happened, not redundant resource info
  const getRelativeTime = (timestamp: string) => {
    const diff = Date.now() - new Date(timestamp).getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return 'just now'
    if (mins < 60) return `${mins}m ago`
    const hours = Math.floor(mins / 60)
    if (hours < 24) return `${hours}h ago`
    return `${Math.floor(hours / 24)}d ago`
  }

  const tooltipLines: string[] = []
  if (isChange) {
    tooltipLines.push(`${event.operation?.toUpperCase() || 'change'}`)
  } else if (event.reason) {
    tooltipLines.push(event.reason)
  }
  if (event.message) {
    // Truncate long messages
    const msg = event.message.length > 60 ? event.message.slice(0, 60) + '...' : event.message
    tooltipLines.push(msg)
  }
  tooltipLines.push(getRelativeTime(event.timestamp))
  if (isHistorical) tooltipLines.push('(from resource metadata)')

  const tooltipText = tooltipLines.join(' · ')

  return (
    <button
      className={clsx(
        'absolute top-1/2 -translate-y-1/2 -translate-x-1/2 rounded-full transition-all group',
        small ? 'w-2.5 h-2.5' : 'w-3 h-3',
        markerClasses,
        selected ? 'ring-2 ring-white ring-offset-2 ring-offset-theme-base scale-150' : 'hover:scale-125',
        dimmed ? 'z-5' : isHistorical ? 'z-5' : 'z-10'
      )}
      style={{ left: `${x}%` }}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
    >
      <span className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 px-2 py-1 text-xs bg-theme-base text-theme-text-primary rounded whitespace-nowrap opacity-0 group-hover:opacity-100 pointer-events-none z-50 transition-opacity duration-75">
        {tooltipText}
      </span>
    </button>
  )
}

interface EventDetailPanelProps {
  event: TimelineEvent
  onClose: () => void
}

function EventDetailPanel({ event, onClose }: EventDetailPanelProps) {
  const isChange = event.type === 'change'
  const isProblematic = isProblematicEvent(event)

  return (
    <div className={clsx(
      "fixed bottom-0 left-0 right-0 z-50 border-t p-4 max-h-72 overflow-auto shadow-2xl",
      isProblematic ? "border-amber-600 bg-amber-950" : "border-theme-border bg-theme-surface"
    )}>
      <div className="flex items-start justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="text-xs px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-secondary">
              {event.kind}
            </span>
            <span className="text-theme-text-primary font-medium">{event.name}</span>
            {event.namespace && (
              <span className="text-xs text-theme-text-tertiary">in {event.namespace}</span>
            )}
            {event.isHistorical && (
              <span className="text-xs px-1.5 py-0.5 bg-theme-hover rounded text-theme-text-secondary flex items-center gap-1">
                <Clock className="w-3 h-3" />
                historical
              </span>
            )}
          </div>
          <div className="text-xs text-theme-text-tertiary mt-1">
            {formatFullTime(new Date(event.timestamp))}
            {event.isHistorical && event.reason && (
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
            <span
              className={clsx(
                'text-sm font-medium',
                event.operation === 'add' && 'text-green-400',
                event.operation === 'update' && 'text-blue-400',
                event.operation === 'delete' && 'text-red-400'
              )}
            >
              {event.operation}
            </span>
            {event.healthState && event.healthState !== 'unknown' && (
              <span
                className={clsx(
                  'text-xs px-1.5 py-0.5 rounded',
                  event.healthState === 'healthy' && 'bg-green-500/20 text-green-400',
                  event.healthState === 'degraded' && 'bg-yellow-500/20 text-yellow-400',
                  event.healthState === 'unhealthy' && 'bg-red-500/20 text-red-400'
                )}
              >
                {event.healthState}
              </span>
            )}
          </div>
          {event.diff && <DiffViewer diff={event.diff} />}
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className={clsx('text-sm font-medium', isProblematic ? 'text-amber-300' : 'text-green-300')}>
              {event.reason}
            </span>
            <span
              className={clsx(
                'text-xs px-1.5 py-0.5 rounded',
                isProblematic ? 'bg-amber-500/20 text-amber-400' : 'bg-green-500/20 text-green-400'
              )}
            >
              {event.eventType}
            </span>
            {event.count && event.count > 1 && (
              <span className="text-xs text-theme-text-tertiary">x{event.count}</span>
            )}
          </div>
          {event.message && <p className={clsx("text-sm", isProblematic ? "text-amber-200" : "text-theme-text-secondary")}>{event.message}</p>}
        </div>
      )}
    </div>
  )
}
