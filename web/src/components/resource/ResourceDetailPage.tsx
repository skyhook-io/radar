import { useState, useMemo, useEffect, useRef } from 'react'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { clsx } from 'clsx'
import {
  ArrowLeft,
  RefreshCw,
  Trash2,
  ChevronRight,
  Layers,
  Server,
  Terminal,
  FileText,
  Activity,
  MoreVertical,
  RotateCcw,
  Scale,
  Copy,
  Check,
} from 'lucide-react'
import type { TimelineEvent, TimeRange, ResourceRef, Relationships } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { refToSelectedResource } from '../../utils/navigation'
import { isChangeEvent, isHistoricalEvent } from '../../types'
import { useChanges, useResourceWithRelationships, usePodLogs, useDeleteResource, useTopology } from '../../api/client'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { getKindBadgeColor, getHealthBadgeColor } from '../../utils/badge-colors'
import { buildResourceHierarchy, getAllEventsFromHierarchy, isProblematicEvent, type ResourceLane } from '../../utils/resource-hierarchy'
import {
  ZOOM_LEVELS,
  type ZoomLevel,
  formatAxisTime,
  EventMarker,
  EventDotLegend,
  HealthSpanLegend,
  HealthSpan,
  ZoomControls,
  buildHealthSpans,
  timeToX,
  calculateTimeRange,
} from '../timeline/shared'
import { stringify as yamlStringify } from 'yaml'

type TabType = 'events' | 'logs' | 'info' | 'yaml'

interface ResourceDetailPageProps {
  kind: string
  namespace: string
  name: string
  onBack: () => void
  onNavigateToResource?: NavigateToResource
}

export function ResourceDetailPage({
  kind,
  namespace,
  name,
  onBack,
  onNavigateToResource,
}: ResourceDetailPageProps) {
  const [activeTab, setActiveTab] = useState<TabType>('events')
  const [selectedEventId, setSelectedEventId] = useState<string | null>(null)
  const [zoom, setZoom] = useState<ZoomLevel>(1) // 1 hour default
  const [selectedPod, setSelectedPod] = useState<string | null>(null)

  // Fetch resource with relationships
  const { data: resourceResponse, isLoading: resourceLoading } = useResourceWithRelationships<any>(kind, namespace, name)
  const resource = resourceResponse?.resource
  const relationships = resourceResponse?.relationships

  // Fetch topology for hierarchy building
  const { data: topology } = useTopology([namespace], 'resources')

  // Convert zoom level to TimeRange for API
  const timeRange: TimeRange = zoom <= 0.5 ? '30m' : zoom <= 1 ? '1h' : zoom <= 6 ? '6h' : zoom <= 24 ? '24h' : 'all'

  // Fetch events - fetch from all namespaces to capture related resources in other namespaces
  const { data: allEvents, isLoading: eventsLoading } = useChanges({
    namespaces: [namespace],
    timeRange,
    includeK8sEvents: true,
    includeManaged: true,
    limit: 1000, // Increased limit to capture more related events
  })

  // Build resource hierarchy using the shared utility
  // This finds all related events (transitive children, topology relationships, app labels)
  const resourceLanes = useMemo(() => {
    if (!allEvents) return []
    return buildResourceHierarchy({
      events: allEvents,
      topology,
      rootResource: { kind, namespace, name },
      groupByApp: true,
    })
  }, [allEvents, topology, kind, namespace, name])

  // Get all events flattened for stats and compatibility
  const resourceEvents = useMemo(() => {
    return getAllEventsFromHierarchy(resourceLanes)
  }, [resourceLanes])

  // Extract metadata from resource
  const metadata = useMemo(() => extractMetadata(kind, resource), [kind, resource])

  // Get pods from relationships (only direct pods, for Services)
  const pods = relationships?.pods || []

  // Get warning count and stats
  const warningCount = resourceEvents.filter(isProblematicEvent).length
  const stats = useMemo(() => extractStats(kind, resource, resourceEvents), [kind, resource, resourceEvents])

  // Get child resources from the hierarchy for timeline swimlanes and logs tab
  // Extract all Pod children from the hierarchy
  const childPods = useMemo(() => {
    if (resourceLanes.length === 0) return []
    const rootLane = resourceLanes[0]
    const pods: { name: string; namespace: string; events: TimelineEvent[] }[] = []

    // Helper to recursively collect pod lanes
    const collectPods = (lane: ResourceLane) => {
      if (lane.kind === 'Pod') {
        pods.push({ name: lane.name, namespace: lane.namespace, events: lane.events })
      }
      lane.children?.forEach(collectPods)
    }

    // Collect from children (not the root itself unless it's a Pod)
    rootLane.children?.forEach(collectPods)
    if (rootLane.kind === 'Pod') {
      pods.push({ name: rootLane.name, namespace: rootLane.namespace, events: rootLane.events })
    }

    return pods
  }, [resourceLanes])

  return (
    <div className="flex flex-col h-full w-full bg-theme-base">
      {/* Compact Header */}
      <div className="shrink-0 border-b border-theme-border bg-theme-surface">
        <div className="px-4 py-3 flex items-start gap-4">
          {/* Back + Title */}
          <button
            onClick={onBack}
            className="p-1.5 mt-0.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
          >
            <ArrowLeft className="w-5 h-5" />
          </button>

          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-3 mb-1">
              <h1 className="text-lg font-semibold text-theme-text-primary truncate">{name}</h1>
            </div>
            <div className="flex items-center gap-4 text-sm text-theme-text-secondary">
              <span className={clsx('px-2 py-0.5 rounded text-xs font-medium', getKindBadgeColor(kind))}>{kind}</span>
              <span>Namespace: <span className="text-theme-text-primary">{namespace}</span></span>
              {metadata.find(m => m.label === 'Image') && (
                <span className="truncate max-w-md font-mono text-xs">{metadata.find(m => m.label === 'Image')?.value}</span>
              )}
              {relationships?.owner && (
                <span>Owner: <button onClick={() => onNavigateToResource?.(refToSelectedResource(relationships.owner!))} className="text-blue-500 hover:underline">{relationships.owner.name}</button></span>
              )}
            </div>
          </div>

          {/* Actions */}
          <div className="flex items-center gap-2">
            <ActionsDropdown kind={kind} namespace={namespace} name={name} onBack={onBack} />
          </div>
        </div>

        {/* Stats Bar - Komodor style */}
        <div className="px-4 py-3 bg-theme-base/50 border-t border-theme-border flex items-stretch gap-6">
          <StatBox
            label="HEALTH"
            value={stats.health}
            valueClass={stats.health === 'HEALTHY' ? 'text-green-500' : stats.health === 'DEGRADED' ? 'text-yellow-500' : 'text-red-500'}
          />
          <StatBox label="REPLICAS" value={stats.replicas} />
          {stats.restarts !== undefined && stats.restarts > 0 && (
            <StatBox label="RESTARTS" value={stats.restarts.toLocaleString()} valueClass={stats.restarts > 10 ? 'text-amber-500' : undefined} />
          )}
          {stats.reason && (
            <StatBox label="REASON" value={stats.reason} valueClass="text-red-400 text-sm" />
          )}
          <div className="border-l border-theme-border mx-2" />
          {stats.lastChange && (
            <StatBox label="LAST CHANGE" value={stats.lastChange} />
          )}
          {warningCount > 0 && (
            <StatBox label="WARNINGS" value={warningCount.toString()} valueClass="text-amber-500" />
          )}
        </div>

        {/* Tabs */}
        <div className="px-4 flex gap-1 border-t border-theme-border">
          <TabButton active={activeTab === 'events'} onClick={() => setActiveTab('events')}>
            <Activity className="w-4 h-4" />
            Events
            {resourceEvents.length > 0 && (
              <span className="ml-1 px-1.5 py-0.5 text-xs bg-theme-elevated rounded">{resourceEvents.length}</span>
            )}
          </TabButton>
          <TabButton active={activeTab === 'logs'} onClick={() => setActiveTab('logs')}>
            <Terminal className="w-4 h-4" />
            Logs
          </TabButton>
          <TabButton active={activeTab === 'info'} onClick={() => setActiveTab('info')}>
            <Layers className="w-4 h-4" />
            Info
          </TabButton>
          <TabButton active={activeTab === 'yaml'} onClick={() => setActiveTab('yaml')}>
            <FileText className="w-4 h-4" />
            YAML
          </TabButton>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-hidden">
        {activeTab === 'events' && (
          <EventsTab
            events={resourceEvents}
            resourceLanes={resourceLanes}
            isLoading={eventsLoading}
            zoom={zoom}
            onZoomChange={setZoom}
            resourceKind={kind}
            resourceName={name}
            selectedEventId={selectedEventId}
            onSelectEvent={setSelectedEventId}
          />
        )}
        {activeTab === 'logs' && (
          <LogsTab
            pods={[...pods, ...childPods.map(p => ({ kind: 'Pod', namespace: p.namespace, name: p.name }))]}
            namespace={namespace}
            selectedPod={selectedPod}
            onSelectPod={setSelectedPod}
          />
        )}
        {activeTab === 'info' && (
          <InfoTab
            resource={resource}
            relationships={relationships}
            isLoading={resourceLoading}
            onNavigate={onNavigateToResource}
            kind={kind}
          />
        )}
        {activeTab === 'yaml' && (
          <YamlTab resource={resource} isLoading={resourceLoading} />
        )}
      </div>
    </div>
  )
}

// Stats extraction
function extractStats(kind: string, resource: any, events: TimelineEvent[]): {
  health: string
  replicas: string
  restarts?: number
  reason?: string
  lastChange?: string
} {
  const health = resource ? determineHealth(kind, resource).toUpperCase() : 'UNKNOWN'
  let replicas = '-'
  let restarts: number | undefined
  let reason: string | undefined

  if (resource?.status) {
    const status = resource.status
    const spec = resource.spec || {}

    if (['Deployment', 'Rollout', 'StatefulSet', 'DaemonSet', 'ReplicaSet'].includes(kind)) {
      const ready = status.readyReplicas || 0
      const total = spec.replicas ?? status.replicas ?? 0
      replicas = `${ready}/${total}`
    }

    if (kind === 'Pod') {
      replicas = status.phase || '-'
      const containerStatuses = status.containerStatuses || []
      restarts = containerStatuses.reduce((sum: number, c: any) => sum + (c.restartCount || 0), 0)

      // Get reason from waiting container or last termination
      for (const cs of containerStatuses) {
        if (cs.state?.waiting?.reason) {
          reason = `${cs.state.waiting.reason}${cs.state.waiting.message ? ': ' + cs.state.waiting.message.slice(0, 50) : ''}`
          break
        }
        if (cs.lastState?.terminated?.reason) {
          reason = `${cs.lastState.terminated.reason}${cs.lastState.terminated.exitCode !== undefined ? ' - Exit code: ' + cs.lastState.terminated.exitCode : ''}`
        }
      }
    }
  }

  // Last change from events
  const lastChange = events.length > 0
    ? formatRelativeTime(new Date(events[0].timestamp))
    : undefined

  return { health, replicas, restarts, reason, lastChange }
}

function formatRelativeTime(date: Date): string {
  const now = Date.now()
  const diff = now - date.getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins} min ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} hour${hours > 1 ? 's' : ''} ago`
  const days = Math.floor(hours / 24)
  return `${days} day${days > 1 ? 's' : ''} ago`
}

function StatBox({ label, value, valueClass }: { label: string; value: string; valueClass?: string }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-theme-text-tertiary font-medium mb-0.5">{label}</div>
      <div className={clsx('text-sm font-semibold truncate', valueClass || 'text-theme-text-primary')}>{value}</div>
    </div>
  )
}

// Helper to extract key metadata from resource
function extractMetadata(kind: string, resource: any): { label: string; value: string }[] {
  if (!resource) return []
  const items: { label: string; value: string }[] = []

  const spec = resource.spec || {}
  const status = resource.status || {}

  switch (kind) {
    case 'Deployment':
    case 'StatefulSet':
    case 'DaemonSet':
      if (spec.replicas !== undefined) {
        const ready = status.readyReplicas || 0
        items.push({ label: 'Replicas', value: `${ready}/${spec.replicas}` })
      }
      // Get image from first container
      const containers = spec.template?.spec?.containers || []
      if (containers[0]?.image) {
        items.push({ label: 'Image', value: containers[0].image })
      }
      if (spec.selector?.matchLabels?.app) {
        items.push({ label: 'App', value: spec.selector.matchLabels.app })
      }
      break

    case 'ReplicaSet': {
      const rsReady = status.readyReplicas || 0
      const rsTotal = spec.replicas || status.replicas || 0
      items.push({ label: 'Replicas', value: `${rsReady}/${rsTotal}` })
      // Get image from first container
      const rsContainers = spec.template?.spec?.containers || []
      if (rsContainers[0]?.image) {
        items.push({ label: 'Image', value: rsContainers[0].image })
      }
      // Get owner deployment name from ownerReferences
      const ownerRefs = resource.metadata?.ownerReferences || []
      const deployOwner = ownerRefs.find((ref: any) => ref.kind === 'Deployment')
      if (deployOwner) {
        items.push({ label: 'Owner', value: deployOwner.name })
      }
      break
    }

    case 'Service':
      if (spec.type) items.push({ label: 'Type', value: spec.type })
      if (spec.clusterIP) items.push({ label: 'ClusterIP', value: spec.clusterIP })
      if (spec.ports?.length) {
        const ports = spec.ports.map((p: any) => `${p.port}${p.targetPort ? ':' + p.targetPort : ''}/${p.protocol || 'TCP'}`).join(', ')
        items.push({ label: 'Ports', value: ports })
      }
      break

    case 'Pod':
      if (status.phase) items.push({ label: 'Phase', value: status.phase })
      if (status.podIP) items.push({ label: 'Pod IP', value: status.podIP })
      if (spec.nodeName) items.push({ label: 'Node', value: spec.nodeName })
      const restarts = status.containerStatuses?.reduce((sum: number, c: any) => sum + (c.restartCount || 0), 0) || 0
      if (restarts > 0) items.push({ label: 'Restarts', value: String(restarts) })
      break

    case 'Ingress':
      const rules = spec.rules || []
      if (rules[0]?.host) items.push({ label: 'Host', value: rules[0].host })
      if (spec.ingressClassName) items.push({ label: 'Class', value: spec.ingressClassName })
      break

    case 'ConfigMap':
    case 'Secret':
      const dataKeys = Object.keys(resource.data || {})
      items.push({ label: 'Keys', value: dataKeys.length > 3 ? `${dataKeys.slice(0, 3).join(', ')}...` : dataKeys.join(', ') || '(empty)' })
      break

    case 'HorizontalPodAutoscaler':
      if (spec.minReplicas) items.push({ label: 'Min', value: String(spec.minReplicas) })
      if (spec.maxReplicas) items.push({ label: 'Max', value: String(spec.maxReplicas) })
      if (status.currentReplicas) items.push({ label: 'Current', value: String(status.currentReplicas) })
      break
  }

  return items
}

// Determine health from resource
function determineHealth(kind: string, resource: any): string {
  if (!resource) return 'unknown'
  const status = resource.status || {}

  switch (kind) {
    case 'Deployment':
    case 'StatefulSet':
    case 'DaemonSet': {
      const desired = resource.spec?.replicas || 0
      const ready = status.readyReplicas || 0
      const updated = status.updatedReplicas || 0
      if (ready === 0 && desired > 0) return 'unhealthy'
      if (ready < desired || updated < desired) return 'degraded'
      return 'healthy'
    }
    case 'ReplicaSet': {
      const desired = resource.spec?.replicas || 0
      const ready = status.readyReplicas || 0
      if (ready === 0 && desired > 0) return 'unhealthy'
      if (ready < desired) return 'degraded'
      return 'healthy'
    }
    case 'Pod': {
      const phase = status.phase
      if (phase === 'Running' || phase === 'Succeeded') {
        const containers = status.containerStatuses || []
        const allReady = containers.every((c: any) => c.ready)
        return allReady ? 'healthy' : 'degraded'
      }
      if (phase === 'Pending') return 'degraded'
      return 'unhealthy'
    }
    case 'Service':
      return 'healthy' // Services are always "healthy" if they exist
    case 'Gateway': {
      // Check conditions for Programmed/Accepted
      const gwConditions = status.conditions || []
      const programmed = gwConditions.find((c: any) => c.type === 'Programmed')
      const accepted = gwConditions.find((c: any) => c.type === 'Accepted')
      if (programmed?.status === 'True') return 'healthy'
      if (accepted?.status === 'True') return 'degraded'
      if (gwConditions.length > 0) return 'unhealthy'
      return 'unknown'
    }
    case 'HTTPRoute':
    case 'GRPCRoute':
    case 'TCPRoute':
    case 'TLSRoute': {
      const parents = status.parents || []
      if (parents.length === 0) return 'unknown'
      const acceptedCount = parents.filter((p: any) =>
        p.conditions?.some((c: any) => c.type === 'Accepted' && c.status === 'True')
      ).length
      if (acceptedCount === parents.length) return 'healthy'
      if (acceptedCount > 0) return 'degraded'
      return 'unhealthy'
    }
    default:
      return 'unknown'
  }
}

// Sub-components

function KindBadge({ kind }: { kind: string }) {
  return (
    <span className={clsx('text-xs px-2 py-0.5 rounded font-medium', getKindBadgeColor(kind))}>
      {kind}
    </span>
  )
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        'flex items-center gap-1.5 px-3 py-2 text-sm font-medium border-b-2 transition-colors',
        active
          ? 'text-theme-text-primary border-blue-500'
          : 'text-theme-text-secondary border-transparent hover:text-theme-text-primary hover:border-theme-border-light'
      )}
    >
      {children}
    </button>
  )
}

function ActionsDropdown({ kind, namespace, name, onBack }: { kind: string; namespace: string; name: string; onBack: () => void }) {
  const [open, setOpen] = useState(false)
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const deleteMutation = useDeleteResource()

  const handleDeleteConfirm = () => {
    deleteMutation.mutate(
      { kind: kind.toLowerCase() + 's', namespace, name }, // Convert kind to plural for API
      {
        onSuccess: () => {
          setShowDeleteConfirm(false)
          onBack()
        },
      }
    )
  }

  // Placeholder actions - would need backend support
  const actions = [
    { label: 'Describe', icon: FileText, action: () => console.log('describe') },
    { label: 'Restart', icon: RotateCcw, action: () => console.log('restart'), disabled: !['Deployment', 'Rollout', 'StatefulSet', 'DaemonSet'].includes(kind) },
    { label: 'Scale', icon: Scale, action: () => console.log('scale'), disabled: !['Deployment', 'Rollout', 'StatefulSet'].includes(kind) },
    { label: 'Delete', icon: Trash2, action: () => setShowDeleteConfirm(true), danger: true },
  ]

  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg"
      >
        <MoreVertical className="w-5 h-5" />
      </button>
      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute right-0 top-full mt-1 z-50 w-48 bg-theme-surface border border-theme-border rounded-lg shadow-xl py-1">
            {actions.map((action, i) => (
              <button
                key={i}
                onClick={() => { action.action(); setOpen(false) }}
                disabled={action.disabled}
                className={clsx(
                  'w-full px-3 py-2 text-sm text-left flex items-center gap-2 transition-colors',
                  action.disabled
                    ? 'text-theme-text-disabled cursor-not-allowed'
                    : action.danger
                    ? 'text-red-400 hover:bg-red-900/30'
                    : 'text-theme-text-secondary hover:bg-theme-elevated'
                )}
              >
                <action.icon className="w-4 h-4" />
                {action.label}
              </button>
            ))}
          </div>
        </>
      )}

      {/* Delete confirmation dialog */}
      <ConfirmDialog
        open={showDeleteConfirm}
        onClose={() => setShowDeleteConfirm(false)}
        onConfirm={handleDeleteConfirm}
        title="Delete Resource"
        message={`Are you sure you want to delete "${name}"?`}
        details={`This will permanently delete the ${kind} "${name}" from the "${namespace}" namespace.`}
        confirmLabel="Delete"
        variant="danger"
        isLoading={deleteMutation.isPending}
      />
    </div>
  )
}

// Tab content components

// New Events tab with Komodor-style timeline
function EventsTab({
  events,
  resourceLanes,
  isLoading,
  zoom,
  onZoomChange,
  resourceKind,
  resourceName,
  selectedEventId,
  onSelectEvent,
}: {
  events: TimelineEvent[]
  resourceLanes: ResourceLane[]
  isLoading: boolean
  zoom: ZoomLevel
  onZoomChange: (zoom: ZoomLevel) => void
  resourceKind: string
  resourceName: string
  selectedEventId: string | null
  onSelectEvent: (id: string | null) => void
}) {
  // Refs for row elements - keyed by row index (not event ID) to handle linked events properly
  const rowRefs = useRef<Map<number, HTMLTableRowElement>>(new Map())
  const tableContainerRef = useRef<HTMLDivElement>(null)

  // Track hovered event for bidirectional highlighting
  const [hoveredEventId, setHoveredEventId] = useState<string | null>(null)

  // Track first and last visible row indices for timeline indicator
  const [visibleRowRange, setVisibleRowRange] = useState<{ first: number; last: number } | null>(null)

  // Scroll to selected event when it changes (from timeline click)
  useEffect(() => {
    if (selectedEventId) {
      const eventIndex = events.findIndex(e => e.id === selectedEventId)
      if (eventIndex >= 0) {
        const row = rowRefs.current.get(eventIndex)
        if (row) {
          row.scrollIntoView({ behavior: 'smooth', block: 'center' })
        }
      }
    }
  }, [selectedEventId, events])

  // Track which rows are visible using IntersectionObserver
  useEffect(() => {
    if (!tableContainerRef.current || events.length === 0) return

    const visibleIndices = new Set<number>()

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const idx = parseInt(entry.target.getAttribute('data-row-index') || '-1', 10)
          if (idx >= 0) {
            if (entry.isIntersecting) {
              visibleIndices.add(idx)
            } else {
              visibleIndices.delete(idx)
            }
          }
        }

        if (visibleIndices.size > 0) {
          const indices = Array.from(visibleIndices)
          setVisibleRowRange({
            first: Math.min(...indices),
            last: Math.max(...indices),
          })
        } else {
          setVisibleRowRange(null)
        }
      },
      {
        root: tableContainerRef.current,
        threshold: 0.1,
      }
    )

    const timeoutId = setTimeout(() => {
      rowRefs.current.forEach((row) => observer.observe(row))
    }, 100)

    return () => {
      clearTimeout(timeoutId)
      observer.disconnect()
    }
  }, [events])

  // Calculate visible time range from visible row indices
  const visibleTimeRangeFromRows = useMemo(() => {
    if (!visibleRowRange || events.length === 0) return null

    const visibleEvents = events.slice(visibleRowRange.first, visibleRowRange.last + 1)
    if (visibleEvents.length === 0) return null

    const timestamps = visibleEvents.map(e => new Date(e.timestamp).getTime())
    const start = Math.min(...timestamps)
    const end = Math.max(...timestamps)

    const timeSpan = end - start
    const padding = Math.max(timeSpan * 0.1, 60000)

    return {
      start: start - padding,
      end: end + padding,
    }
  }, [events, visibleRowRange])

  // Time calculations using shared utilities
  const now = Date.now()
  const { start: startTime, windowMs } = calculateTimeRange(zoom, now)

  // Zoom controls
  const zoomIndex = ZOOM_LEVELS.indexOf(zoom)
  const canZoomIn = zoomIndex > 0
  const canZoomOut = zoomIndex < ZOOM_LEVELS.length - 1

  const handleZoomIn = () => {
    if (canZoomIn) {
      onZoomChange(ZOOM_LEVELS[zoomIndex - 1])
    }
  }

  const handleZoomOut = () => {
    if (canZoomOut) {
      onZoomChange(ZOOM_LEVELS[zoomIndex + 1])
    }
  }

  // Local timeToX helper
  const localTimeToX = (ts: number) => timeToX(ts, startTime, windowMs)

  // Build swimlanes from the hierarchical resource lanes
  const swimlanes = useMemo(() => {
    type SwimLane = {
      id: string
      label: string
      spans: { start: number; end: number; health: string }[]
      events: TimelineEvent[]
      createdAt?: number
      createdBeforeWindow: boolean
    }

    if (resourceLanes.length === 0) {
      const mainResourceEvents = events.filter(e => e.kind === resourceKind && e.name === resourceName)
      const healthResult = buildHealthSpans(mainResourceEvents.filter(e => isChangeEvent(e)), startTime, now, mainResourceEvents)
      return [{
        id: 'main',
        label: `${resourceKind}: ${resourceName}`,
        spans: healthResult.spans,
        events: mainResourceEvents,
        createdAt: healthResult.createdAt,
        createdBeforeWindow: healthResult.createdBeforeWindow,
      }]
    }

    const rootLane = resourceLanes[0]
    const lanes: SwimLane[] = []

    // Add the root lane
    // Pass all events as 4th param so buildHealthSpans can extract createdAt from K8s Events too
    const rootHealthResult = buildHealthSpans(rootLane.events.filter(e => isChangeEvent(e)), startTime, now, rootLane.events)
    lanes.push({
      id: rootLane.id,
      label: `${rootLane.kind}: ${rootLane.name.length > 40 ? rootLane.name.slice(0, 20) + '...' + rootLane.name.slice(-17) : rootLane.name}`,
      spans: rootHealthResult.spans,
      events: rootLane.events,
      createdAt: rootHealthResult.createdAt,
      createdBeforeWindow: rootHealthResult.createdBeforeWindow,
    })

    // Flatten children recursively
    const flattenChildren = (lane: ResourceLane): ResourceLane[] => {
      const children = lane.children || []
      return children.flatMap(child => [child, ...flattenChildren(child)])
    }

    const allChildren = flattenChildren(rootLane)

    // Sort by kind priority then by event count
    const kindPriority: Record<string, number> = {
      Service: 1, Deployment: 2, Rollout: 2, StatefulSet: 2, DaemonSet: 2,
      ReplicaSet: 3, ConfigMap: 4, Secret: 4, Gateway: 5, HTTPRoute: 4, GRPCRoute: 4,
      TCPRoute: 4, TLSRoute: 4, Ingress: 5, Pod: 6
    }

    allChildren.sort((a, b) => {
      const aPriority = kindPriority[a.kind] || 10
      const bPriority = kindPriority[b.kind] || 10
      if (aPriority !== bPriority) return aPriority - bPriority
      return b.events.length - a.events.length
    })

    // Take up to 6 children for display
    for (const child of allChildren.slice(0, 6)) {
      const childHealthResult = buildHealthSpans(child.events.filter(e => isChangeEvent(e)), startTime, now, child.events)
      lanes.push({
        id: child.id,
        label: `${child.kind}: ${child.name.length > 40 ? child.name.slice(0, 20) + '...' + child.name.slice(-17) : child.name}`,
        spans: childHealthResult.spans,
        events: child.events,
        createdAt: childHealthResult.createdAt,
        createdBeforeWindow: childHealthResult.createdBeforeWindow,
      })
    }

    return lanes
  }, [resourceLanes, events, resourceKind, resourceName, startTime, now])

  // Time axis ticks
  const tickCount = 8
  const ticks = Array.from({ length: tickCount + 1 }, (_, i) => {
    const t = startTime + (windowMs * i) / tickCount
    return { time: t, label: formatAxisTime(new Date(t)) }
  })

  // Format time range display
  const formatTimeRangeDisplay = () => {
    const start = new Date(startTime)
    const end = new Date(now)
    return `${start.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })} → ${end.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  }

  // Now marker position
  const nowX = localTimeToX(now)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading events...
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col overflow-hidden">
      {/* Timeline toolbar */}
      <div className="shrink-0 px-4 py-2 border-b border-theme-border bg-theme-surface/50 flex items-center justify-between">
        <span className="text-sm font-medium text-theme-text-secondary">Events ({events.length})</span>
        <div className="flex items-center gap-3">
          <ZoomControls
            zoom={zoom}
            onZoomIn={handleZoomIn}
            onZoomOut={handleZoomOut}
            canZoomIn={canZoomIn}
            canZoomOut={canZoomOut}
          />
          <span className="text-xs text-theme-text-tertiary">{formatTimeRangeDisplay()}</span>
        </div>
      </div>

      {/* Legend bar */}
      <div className="shrink-0 px-4 py-1.5 border-b border-theme-border bg-theme-surface/30 flex items-center justify-between">
        <HealthSpanLegend />
        <EventDotLegend />
      </div>

      {/* Swimlane Timeline */}
      <div className="shrink-0 border-b border-theme-border bg-theme-base relative">
        {/* "Now" marker - positioned across all swimlanes */}
        {nowX >= 0 && nowX <= 100 && (
          <div
            className="absolute top-0 bottom-0 w-0.5 bg-purple-500/50 z-20 pointer-events-none"
            style={{ left: `calc(256px + (100% - 256px) * ${nowX / 100})` }}
          >
            <span className="absolute -top-4 left-1/2 -translate-x-1/2 text-xs text-purple-500 font-medium whitespace-nowrap">
              now
            </span>
          </div>
        )}

        {swimlanes.map((lane) => (
          <div key={lane.id} className="flex border-b border-theme-border/50 last:border-b-0">
            {/* Lane label */}
            <div className="w-64 shrink-0 px-3 py-2 bg-theme-surface/50 border-r border-theme-border text-xs font-medium text-theme-text-secondary truncate">
              {lane.label}
            </div>
            {/* Lane track */}
            <div className="flex-1 relative h-10 bg-theme-base">
              {/* Visible range indicator */}
              {visibleTimeRangeFromRows && (
                <div
                  className="absolute top-0 bottom-0 bg-blue-500/10 border-x border-blue-500/30 pointer-events-none"
                  style={{
                    left: `${Math.max(0, localTimeToX(visibleTimeRangeFromRows.start))}%`,
                    width: `${Math.max(2, Math.min(100, localTimeToX(visibleTimeRangeFromRows.end)) - Math.max(0, localTimeToX(visibleTimeRangeFromRows.start)))}%`,
                  }}
                />
              )}

              {/* Health spans - only shown when resource exists */}
              {lane.spans.map((span, i) => {
                const left = Math.max(0, localTimeToX(span.start))
                const right = Math.min(100, localTimeToX(span.end))
                const width = right - left
                // Show "created before window" indicator on the first span if resource existed before visible window
                const showCreatedBefore = i === 0 && lane.createdBeforeWindow && lane.createdAt
                return (
                  <HealthSpan
                    key={i}
                    health={span.health}
                    left={left}
                    width={width}
                    title={`${span.health} (${new Date(span.start).toLocaleTimeString()} - ${new Date(span.end).toLocaleTimeString()})`}
                    createdBefore={showCreatedBefore ? new Date(lane.createdAt!) : undefined}
                  />
                )
              })}

              {/* Event markers - using shared EventMarker component */}
              {lane.events.map((evt, i) => {
                const x = localTimeToX(new Date(evt.timestamp).getTime())
                if (x < 0 || x > 100) return null
                return (
                  <EventMarker
                    key={`${evt.id}-${i}`}
                    event={evt}
                    x={x}
                    selected={selectedEventId === evt.id}
                    onClick={() => onSelectEvent(selectedEventId === evt.id ? null : evt.id)}
                    small
                  />
                )
              })}
            </div>
          </div>
        ))}

        {/* Time axis */}
        <div className="flex">
          <div className="w-64 shrink-0 bg-theme-surface/50 border-r border-theme-border" />
          <div className="flex-1 relative h-6 bg-theme-elevated/30">
            {ticks.map((tick, i) => {
              const x = localTimeToX(tick.time)
              return (
                <div
                  key={i}
                  className="absolute top-0 flex flex-col items-center"
                  style={{ left: `${x}%`, transform: 'translateX(-50%)' }}
                >
                  <div className="h-2 w-px bg-theme-border" />
                  <span className="text-xs text-theme-text-tertiary">{tick.label}</span>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* Events table */}
      <div ref={tableContainerRef} className="flex-1 overflow-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-theme-surface border-b border-theme-border z-10">
            <tr className="text-left text-xs text-theme-text-tertiary">
              <th className="px-4 py-2 font-medium w-32">Event Type</th>
              <th className="px-4 py-2 font-medium">Summary</th>
              <th className="px-4 py-2 font-medium w-40">Time</th>
              <th className="px-4 py-2 font-medium w-32">Resource</th>
              <th className="px-4 py-2 font-medium w-24">Status</th>
            </tr>
          </thead>
          <tbody className="table-divide-subtle">
            {events.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-4 py-8 text-center text-theme-text-tertiary">
                  No events in this time range
                </td>
              </tr>
            ) : (
              events.map((evt, evtIdx) => {
                const isSelected = selectedEventId === evt.id
                const isHovered = hoveredEventId === evt.id
                const isWarning = isProblematicEvent(evt)
                return (
                  <tr
                    key={`${evt.id}-${evtIdx}`}
                    ref={(el) => {
                      if (el) rowRefs.current.set(evtIdx, el)
                      else rowRefs.current.delete(evtIdx)
                    }}
                    data-row-index={evtIdx}
                    onClick={() => onSelectEvent(isSelected ? null : evt.id)}
                    onMouseEnter={() => setHoveredEventId(evt.id)}
                    onMouseLeave={() => setHoveredEventId(null)}
                    className={clsx(
                      'cursor-pointer transition-colors',
                      isSelected ? 'bg-blue-500/10' :
                      isHovered ? 'bg-blue-500/5' :
                      'hover:bg-theme-surface/50'
                    )}
                  >
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <EventDot event={evt} />
                        <span className={clsx('font-medium', isWarning ? 'text-amber-500' : 'text-theme-text-primary')}>
                          {isHistoricalEvent(evt) && evt.reason ? evt.reason : isChangeEvent(evt) ? evt.eventType : evt.reason}
                        </span>
                      </div>
                    </td>
                    <td className="px-4 py-3 text-theme-text-secondary">
                      {evt.message || evt.diff?.summary || '-'}
                    </td>
                    <td className="px-4 py-3 text-theme-text-tertiary">
                      {new Date(evt.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                    <td className="px-4 py-3">
                      <span className={clsx('text-xs px-1.5 py-0.5 rounded', getKindBadgeColor(evt.kind))}>
                        {evt.kind}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      {isWarning ? (
                        <span className="text-xs px-2 py-0.5 rounded bg-amber-500/20 text-amber-500">Active</span>
                      ) : evt.healthState ? (
                        <span className={clsx('text-xs px-2 py-0.5 rounded', getHealthBadgeColor(evt.healthState))}>{evt.healthState}</span>
                      ) : null}
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function EventDot({ event }: { event: TimelineEvent }) {
  const isWarning = isProblematicEvent(event)
  const isDelete = event.eventType === 'delete'
  const isAdd = event.eventType === 'add'

  return (
    <div className={clsx(
      'w-3 h-3 rounded-full shrink-0',
      isWarning ? 'bg-amber-500' :
      isDelete ? 'bg-red-500' :
      isAdd ? 'bg-green-500' :
      'bg-blue-500'
    )} />
  )
}

// Renamed from OverviewTab to InfoTab
function InfoTab({
  resource,
  relationships,
  isLoading,
  onNavigate,
  kind,
}: {
  resource: any
  relationships?: Relationships
  isLoading: boolean
  onNavigate?: NavigateToResource
  kind: string
}) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading...
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto p-4">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 max-w-5xl">
        {/* Related Resources */}
        <div className="bg-theme-surface/50 border border-theme-border rounded-lg p-4">
          <h3 className="text-sm font-medium text-theme-text-secondary mb-3 flex items-center gap-2">
            <Layers className="w-4 h-4" />
            Related Resources
          </h3>
          <RelatedResources relationships={relationships} isLoading={isLoading} onNavigate={onNavigate} />
        </div>

        {/* Resource Status */}
        {resource?.status && (
          <div className="bg-theme-surface/50 border border-theme-border rounded-lg p-4">
            <h3 className="text-sm font-medium text-theme-text-secondary mb-3 flex items-center gap-2">
              <Server className="w-4 h-4" />
              Status
            </h3>
            <StatusGrid status={resource.status} kind={kind} />
          </div>
        )}

        {/* Labels */}
        {resource?.metadata?.labels && Object.keys(resource.metadata.labels).length > 0 && (
          <div className="bg-theme-surface/50 border border-theme-border rounded-lg p-4">
            <h3 className="text-sm font-medium text-theme-text-secondary mb-3">Labels</h3>
            <div className="flex flex-wrap gap-1">
              {Object.entries(resource.metadata.labels).map(([k, v]) => (
                <span key={k} className="text-xs px-2 py-1 bg-theme-elevated rounded font-mono">
                  {k}={String(v)}
                </span>
              ))}
            </div>
          </div>
        )}

        {/* Annotations */}
        {resource?.metadata?.annotations && Object.keys(resource.metadata.annotations).length > 0 && (
          <div className="bg-theme-surface/50 border border-theme-border rounded-lg p-4">
            <h3 className="text-sm font-medium text-theme-text-secondary mb-3">Annotations</h3>
            <div className="space-y-1 max-h-40 overflow-auto">
              {Object.entries(resource.metadata.annotations).slice(0, 10).map(([k, v]) => (
                <div key={k} className="text-xs font-mono">
                  <span className="text-theme-text-tertiary">{k}:</span>{' '}
                  <span className="text-theme-text-secondary">{String(v).slice(0, 100)}{String(v).length > 100 ? '...' : ''}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function StatusGrid({ status, kind }: { status: any; kind: string }) {
  const items: { label: string; value: string | number; color?: string }[] = []

  if (['Deployment', 'Rollout', 'StatefulSet', 'DaemonSet'].includes(kind)) {
    items.push({ label: 'Ready', value: status.readyReplicas || 0, color: status.readyReplicas > 0 ? 'text-green-400' : 'text-theme-text-secondary' })
    items.push({ label: 'Available', value: status.availableReplicas || 0 })
    items.push({ label: 'Updated', value: status.updatedReplicas || 0 })
    if (status.unavailableReplicas) {
      items.push({ label: 'Unavailable', value: status.unavailableReplicas, color: 'text-red-400' })
    }
  } else if (kind === 'ReplicaSet') {
    const ready = status.readyReplicas || 0
    const replicas = status.replicas || 0
    items.push({ label: 'Ready', value: ready, color: ready === replicas && ready > 0 ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400' })
    items.push({ label: 'Replicas', value: replicas })
    if (status.availableReplicas !== undefined) {
      items.push({ label: 'Available', value: status.availableReplicas })
    }
    if (status.fullyLabeledReplicas !== undefined && status.fullyLabeledReplicas !== replicas) {
      items.push({ label: 'Labeled', value: status.fullyLabeledReplicas })
    }
  } else if (kind === 'Pod') {
    items.push({ label: 'Phase', value: status.phase || 'Unknown' })
    if (status.conditions) {
      const ready = status.conditions.find((c: any) => c.type === 'Ready')
      if (ready) {
        items.push({ label: 'Ready', value: ready.status, color: ready.status === 'True' ? 'text-green-400' : 'text-red-400' })
      }
    }
    // Show container restart counts if any
    if (status.containerStatuses) {
      const totalRestarts = status.containerStatuses.reduce((sum: number, c: any) => sum + (c.restartCount || 0), 0)
      if (totalRestarts > 0) {
        items.push({ label: 'Restarts', value: totalRestarts, color: totalRestarts > 5 ? 'text-red-400' : 'text-yellow-400' })
      }
    }
  }

  if (items.length === 0) {
    return <p className="text-sm text-theme-text-tertiary">No status information available</p>
  }

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
      {items.map((item, i) => (
        <div key={i}>
          <p className="text-xs text-theme-text-tertiary">{item.label}</p>
          <p className={clsx('text-lg font-semibold', item.color || 'text-theme-text-primary')}>{item.value}</p>
        </div>
      ))}
    </div>
  )
}

function RelatedResources({
  relationships,
  isLoading,
  onNavigate
}: {
  relationships?: Relationships
  isLoading?: boolean
  onNavigate?: NavigateToResource
}) {
  if (isLoading) {
    return <p className="text-sm text-theme-text-tertiary">Loading relationships...</p>
  }

  if (!relationships) {
    return <p className="text-sm text-theme-text-tertiary">No related resources</p>
  }

  const sections: { title: string; items: ResourceRef[] }[] = []

  if (relationships.owner) sections.push({ title: 'Owner', items: [relationships.owner] })
  if (relationships.services?.length) sections.push({ title: 'Services', items: relationships.services })
  if (relationships.ingresses?.length) sections.push({ title: 'Ingresses', items: relationships.ingresses })
  if (relationships.gateways?.length) sections.push({ title: 'Gateways', items: relationships.gateways })
  if (relationships.children?.length) sections.push({ title: 'Children', items: relationships.children.slice(0, 5) })
  if (relationships.configRefs?.length) sections.push({ title: 'Config', items: relationships.configRefs })
  if (relationships.pods?.length) sections.push({ title: 'Pods', items: relationships.pods.slice(0, 5) })

  if (sections.length === 0) {
    return <p className="text-sm text-theme-text-tertiary">No related resources</p>
  }

  return (
    <div className="space-y-3">
      {sections.map(section => (
        <div key={section.title}>
          <p className="text-xs text-theme-text-tertiary mb-1">{section.title}</p>
          <div className="space-y-1">
            {section.items.map(item => (
              <button
                key={`${item.kind}/${item.namespace}/${item.name}`}
                onClick={() => onNavigate?.(refToSelectedResource(item))}
                className="w-full text-left px-2 py-1.5 rounded hover:bg-theme-elevated/50 flex items-center gap-2 group"
              >
                <KindBadge kind={item.kind} />
                <span className="text-sm text-theme-text-secondary truncate flex-1 group-hover:text-theme-text-primary">{item.name}</span>
                <ChevronRight className="w-3 h-3 text-theme-text-disabled group-hover:text-theme-text-secondary" />
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function LogsTab({
  pods,
  namespace,
  selectedPod,
  onSelectPod,
}: {
  pods: ResourceRef[]
  namespace: string
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
}) {
  // Auto-select first pod if none selected
  useEffect(() => {
    if (pods.length > 0 && !selectedPod) {
      onSelectPod(pods[0].name)
    }
  }, [pods, selectedPod, onSelectPod])

  if (pods.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
        <Terminal className="w-12 h-12 mb-4 opacity-50" />
        <p>No pods available</p>
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col">
      {/* Pod selector - horizontal tabs */}
      {pods.length > 1 && (
        <div className="shrink-0 border-b border-theme-border bg-theme-surface/50 px-4 py-2 flex gap-2 overflow-x-auto">
          {pods.map(pod => (
            <button
              key={pod.name}
              onClick={() => onSelectPod(pod.name)}
              className={clsx(
                'px-3 py-1.5 text-sm rounded-lg whitespace-nowrap transition-colors',
                selectedPod === pod.name
                  ? 'bg-blue-500 text-theme-text-primary'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
              )}
            >
              {pod.name.length > 40 ? '...' + pod.name.slice(-37) : pod.name}
            </button>
          ))}
        </div>
      )}

      {/* Logs panel */}
      {selectedPod && (
        <div className="flex-1 min-h-0">
          <PodLogsPanel
            namespace={pods.find(p => p.name === selectedPod)?.namespace || namespace}
            podName={selectedPod}
            onClose={() => {}} // No close needed when it's the main content
            showHeader={pods.length === 1}
          />
        </div>
      )}
    </div>
  )
}

function PodLogsPanel({
  namespace,
  podName,
  onClose,
  showHeader = true
}: {
  namespace: string
  podName: string
  onClose: () => void
  showHeader?: boolean
}) {
  const [follow, setFollow] = useState(true)
  const [container, setContainer] = useState<string>('')
  const logsRef = useRef<HTMLPreElement>(null)

  const { data: logsData, isLoading, refetch: refetchLogs } = usePodLogs(namespace, podName, {
    container: container || undefined,
    tailLines: 500,
  })
  const [refetch, isRefreshAnimating] = useRefreshAnimation(refetchLogs)

  // Get container list from logs response
  const containers = logsData?.containers || []
  const logs = container && logsData?.logs ? logsData.logs[container] : Object.values(logsData?.logs || {})[0] || ''

  // Auto-scroll when following
  useEffect(() => {
    if (follow && logsRef.current) {
      logsRef.current.scrollTop = logsRef.current.scrollHeight
    }
  }, [logs, follow])

  // Auto-select first container
  useEffect(() => {
    if (containers.length > 0 && !container) {
      setContainer(containers[0])
    }
  }, [containers, container])

  return (
    <div className="flex flex-col h-full">
      {/* Controls bar - always shown for container selector, follow, refresh */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border bg-theme-surface/50">
        <div className="flex items-center gap-3">
          {showHeader && (
            <>
              <Terminal className="w-4 h-4 text-theme-text-secondary" />
              <span className="text-sm font-medium text-theme-text-primary">{podName}</span>
            </>
          )}
          {containers.length > 1 && (
            <select
              value={container}
              onChange={(e) => setContainer(e.target.value)}
              className="text-xs bg-theme-elevated border border-theme-border-light rounded px-2 py-1 text-theme-text-primary"
            >
              {containers.map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setFollow(!follow)}
            className={clsx(
              'px-2 py-1 text-xs rounded transition-colors',
              follow ? 'bg-blue-500 text-theme-text-primary' : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary'
            )}
          >
            {follow ? 'Following' : 'Follow'}
          </button>
          <button
            onClick={refetch}
            disabled={isRefreshAnimating}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50"
            title="Refresh"
          >
            <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
          </button>
          {showHeader && (
            <button
              onClick={onClose}
              className="p-1 text-theme-text-secondary hover:text-theme-text-primary"
            >
              ×
            </button>
          )}
        </div>
      </div>
      <pre
        ref={logsRef}
        className="flex-1 overflow-auto p-4 text-xs font-mono text-theme-text-secondary bg-theme-base"
      >
        {isLoading ? (
          <span className="text-theme-text-tertiary">Loading logs...</span>
        ) : logs ? (
          logs
        ) : (
          <span className="text-theme-text-tertiary">No logs available</span>
        )}
      </pre>
    </div>
  )
}

function YamlTab({ resource, isLoading }: { resource: any; isLoading: boolean }) {
  const [copied, setCopied] = useState(false)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading...
      </div>
    )
  }

  const yaml = resource ? yamlStringify(resource) : ''

  const handleCopy = () => {
    navigator.clipboard.writeText(yaml)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="h-full flex flex-col">
      <div className="px-4 py-2 border-b border-theme-border flex items-center justify-end">
        <button
          onClick={handleCopy}
          className="flex items-center gap-1.5 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          {copied ? 'Copied!' : 'Copy'}
        </button>
      </div>
      <pre className="flex-1 overflow-auto p-4 text-xs font-mono text-theme-text-secondary bg-theme-base">
        {yaml || 'No data available'}
      </pre>
    </div>
  )
}
