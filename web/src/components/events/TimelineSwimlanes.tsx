import { useState, useMemo, useRef, useCallback, useEffect } from 'react'
import { clsx } from 'clsx'
import {
  AlertCircle,
  CheckCircle,
  Plus,
  Trash2,
  RefreshCw,
  ZoomIn,
  ZoomOut,
  ChevronRight,
} from 'lucide-react'
import type { TimelineEvent, TimeRange } from '../../types'
import { isWorkloadKind } from '../../types'
import { DiffViewer } from './DiffViewer'

interface TimelineSwimlanesProps {
  events: TimelineEvent[]
  isLoading?: boolean
  filterTimeRange?: TimeRange
  onResourceClick?: (kind: string, namespace: string, name: string) => void
}

interface ResourceLane {
  id: string
  kind: string
  namespace: string
  name: string
  events: TimelineEvent[]
  isWorkload: boolean
}

function formatAxisTime(date: Date): string {
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function formatFullTime(date: Date): string {
  return date.toLocaleString()
}

export function TimelineSwimlanes({ events, isLoading, filterTimeRange: _filterTimeRange = '1h', onResourceClick }: TimelineSwimlanesProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [zoom, setZoom] = useState(1)
  const [panOffset, setPanOffset] = useState(0)
  const [selectedEvent, setSelectedEvent] = useState<TimelineEvent | null>(null)
  const [isDragging, setIsDragging] = useState(false)
  const [dragStart, setDragStart] = useState({ x: 0, offset: 0 })

  // Group events by resource
  const lanes = useMemo(() => {
    const laneMap = new Map<string, ResourceLane>()

    for (const event of events) {
      const laneId = `${event.kind}/${event.namespace}/${event.name}`
      if (!laneMap.has(laneId)) {
        laneMap.set(laneId, {
          id: laneId,
          kind: event.kind,
          namespace: event.namespace,
          name: event.name,
          events: [],
          isWorkload: isWorkloadKind(event.kind),
        })
      }
      laneMap.get(laneId)!.events.push(event)
    }

    return Array.from(laneMap.values()).sort((a, b) => {
      const aLatest = Math.max(...a.events.map((e) => new Date(e.timestamp).getTime()))
      const bLatest = Math.max(...b.events.map((e) => new Date(e.timestamp).getTime()))
      return bLatest - aLatest
    })
  }, [events])

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
    if (zoom <= 0.5) {
      intervalMs = 5 * 60 * 1000
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
      <div className="flex items-center justify-center h-full text-slate-500">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading timeline...
      </div>
    )
  }

  if (lanes.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-slate-500">
        <AlertCircle className="w-12 h-12 mb-4 opacity-50" />
        <p className="text-lg">No events to display</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Zoom controls */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-slate-700 bg-slate-800/30">
        <div className="flex items-center gap-2">
          <button
            onClick={handleZoomIn}
            className="p-1.5 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
            title="Zoom in"
          >
            <ZoomIn className="w-4 h-4" />
          </button>
          <button
            onClick={handleZoomOut}
            className="p-1.5 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
            title="Zoom out"
          >
            <ZoomOut className="w-4 h-4" />
          </button>
          <span className="text-xs text-slate-500 ml-2">
            {zoom < 1 ? `${Math.round(zoom * 60)}m` : `${zoom}h`} window
          </span>
        </div>
        <div className="text-xs text-slate-500">
          {lanes.length} resource{lanes.length !== 1 ? 's' : ''} · {events.length} event
          {events.length !== 1 ? 's' : ''}
        </div>
      </div>

      {/* Timeline container */}
      <div className="flex-1 overflow-auto">
        <div
          ref={containerRef}
          className="min-w-full"
          onMouseDown={handleMouseDown}
          onWheel={handleWheel}
          style={{ cursor: isDragging ? 'grabbing' : 'grab' }}
        >
          {/* Time axis header */}
          <div className="sticky top-0 z-10 bg-slate-800 border-b border-slate-700">
            <div className="flex">
              <div className="w-48 flex-shrink-0 border-r border-slate-700 px-3 py-2">
                <span className="text-xs font-medium text-slate-400">Resource</span>
              </div>
              <div className="flex-1 relative h-8">
                {axisTicks.map((tick) => {
                  const x = timeToX(tick.time)
                  if (x < 0 || x > 100) return null
                  return (
                    <div
                      key={tick.time}
                      className="absolute top-0 bottom-0 flex flex-col items-center"
                      style={{ left: `${x}%` }}
                    >
                      <div className="h-2 w-px bg-slate-600" />
                      <span className="text-xs text-slate-500 mt-0.5">{tick.label}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          </div>

          {/* Swimlanes */}
          <div>
            {lanes.map((lane) => (
              <div key={lane.id} className="border-b border-slate-700/50">
                <div className="flex">
                  {/* Lane label - clickable to navigate */}
                  <div
                    className="w-48 flex-shrink-0 border-r border-slate-700 px-3 py-2 flex items-center gap-2 cursor-pointer hover:bg-slate-800/50 group"
                    onClick={() => onResourceClick?.(lane.kind, lane.namespace, lane.name)}
                  >
                    <ChevronRight className="w-3 h-3 text-slate-500 group-hover:text-indigo-400" />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1">
                        <span className={clsx(
                          'text-xs px-1 py-0.5 rounded',
                          lane.isWorkload ? 'bg-blue-900/50 text-blue-400' : 'bg-slate-700 text-slate-400'
                        )}>
                          {lane.kind}
                        </span>
                      </div>
                      <div className="text-sm text-white truncate group-hover:text-indigo-300" title={lane.name}>
                        {lane.name}
                      </div>
                      <div className="text-xs text-slate-500 truncate">{lane.namespace}</div>
                    </div>
                  </div>

                  {/* Events track */}
                  <div className="flex-1 relative h-12">
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
                        />
                      )
                    })}
                  </div>
                </div>
              </div>
            ))}
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
}

function EventMarker({ event, x, selected, onClick }: EventMarkerProps) {
  const isChange = event.type === 'change'
  const isWarning = event.eventType === 'Warning'

  const getMarkerStyle = () => {
    if (isChange) {
      switch (event.operation) {
        case 'add':
          return { bg: 'bg-green-500' }
        case 'delete':
          return { bg: 'bg-red-500' }
        case 'update':
          return { bg: 'bg-blue-500' }
      }
    }
    if (isWarning) {
      return { bg: 'bg-amber-500' }
    }
    return { bg: 'bg-slate-400' }
  }

  const getIcon = () => {
    if (isChange) {
      switch (event.operation) {
        case 'add':
          return <Plus className="w-2 h-2" />
        case 'delete':
          return <Trash2 className="w-2 h-2" />
        case 'update':
          return <RefreshCw className="w-2 h-2" />
      }
    }
    if (isWarning) {
      return <AlertCircle className="w-2 h-2" />
    }
    return <CheckCircle className="w-2 h-2" />
  }

  const style = getMarkerStyle()

  return (
    <button
      className={clsx(
        'absolute top-1/2 -translate-y-1/2 -translate-x-1/2 rounded-full flex items-center justify-center transition-all',
        'w-5 h-5',
        style.bg,
        'text-white',
        selected ? 'ring-2 ring-white ring-offset-2 ring-offset-slate-900 scale-125' : 'hover:scale-110',
        'z-10'
      )}
      style={{ left: `${x}%` }}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
      title={`${event.kind}/${event.name} - ${isChange ? event.operation : event.reason}`}
    >
      {getIcon()}
    </button>
  )
}

interface EventDetailPanelProps {
  event: TimelineEvent
  onClose: () => void
}

function EventDetailPanel({ event, onClose }: EventDetailPanelProps) {
  const isChange = event.type === 'change'
  const isWarning = event.eventType === 'Warning'

  return (
    <div className="border-t border-slate-700 bg-slate-800 p-4 max-h-64 overflow-auto">
      <div className="flex items-start justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="text-xs px-1.5 py-0.5 bg-slate-700 rounded text-slate-300">
              {event.kind}
            </span>
            <span className="text-white font-medium">{event.name}</span>
            {event.namespace && (
              <span className="text-xs text-slate-500">in {event.namespace}</span>
            )}
          </div>
          <div className="text-xs text-slate-500 mt-1">{formatFullTime(new Date(event.timestamp))}</div>
        </div>
        <button
          onClick={onClose}
          className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
        >
          ×
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
            <span className={clsx('text-sm font-medium', isWarning ? 'text-amber-300' : 'text-green-300')}>
              {event.reason}
            </span>
            <span
              className={clsx(
                'text-xs px-1.5 py-0.5 rounded',
                isWarning ? 'bg-amber-500/20 text-amber-400' : 'bg-green-500/20 text-green-400'
              )}
            >
              {event.eventType}
            </span>
            {event.count && event.count > 1 && (
              <span className="text-xs text-slate-500">x{event.count}</span>
            )}
          </div>
          {event.message && <p className="text-sm text-slate-400">{event.message}</p>}
        </div>
      )}
    </div>
  )
}
