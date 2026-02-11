import { useMemo } from 'react'
import { clsx } from 'clsx'
import { Clock, ArrowRight, Shield } from 'lucide-react'
import { useChanges } from '../../api/client'
import { isChangeEvent } from '../../types'
import type { TimelineEvent } from '../../types'
import type { Topology } from '../../types'
import { useHasLimitedAccess } from '../../contexts/CapabilitiesContext'
import { buildResourceHierarchy, isProblematicEvent, type ResourceLane } from '../../utils/resource-hierarchy'
import { buildHealthSpans, timeToX } from '../timeline/shared'

interface ActivitySummaryProps {
  namespaces: string[]
  topology?: Topology | null
  onNavigate: () => void
}

const MAX_LANES = 6
const SPAN_MINUTES = 60

const HEALTH_SPAN_COLORS: Record<string, string> = {
  healthy: 'bg-green-500/50',
  rolling: 'bg-blue-500/50',
  degraded: 'bg-yellow-500/50',
  unhealthy: 'bg-red-500/50',
}

// Simplified interestingness scoring for the mini view
function scoreLane(lane: ResourceLane): number {
  const allEvents = [...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]
  let score = 0

  const kindScores: Record<string, number> = {
    Deployment: 50, Rollout: 50, StatefulSet: 50, DaemonSet: 50,
    Service: 45, Ingress: 45, Gateway: 45,
    HTTPRoute: 42, GRPCRoute: 42, TCPRoute: 42, TLSRoute: 42,
    Job: 40, CronJob: 40,
    Pod: 30, ReplicaSet: 20,
  }
  score += kindScores[lane.kind] || 15

  // Problematic events are most important
  score += allEvents.filter(e => isProblematicEvent(e)).length * 40

  // Recency bonus
  const fiveMinAgo = Date.now() - 5 * 60 * 1000
  score += Math.min(allEvents.filter(e => new Date(e.timestamp).getTime() > fiveMinAgo).length * 30, 150)

  // Children bonus
  if (lane.children && lane.children.length > 0) score += 10

  // System namespace penalty
  if (['kube-system', 'kube-public', 'kube-node-lease'].includes(lane.namespace)) score -= 30

  return score
}

// Short kind labels for compact display
const KIND_SHORT: Record<string, string> = {
  Deployment: 'Deploy',
  StatefulSet: 'SS',
  DaemonSet: 'DS',
  ReplicaSet: 'RS',
  Service: 'Svc',
  ConfigMap: 'CM',
  CronJob: 'CJ',
  Ingress: 'Ing',
  Gateway: 'GW',
  HTTPRoute: 'HR',
  GRPCRoute: 'gRPC',
  TCPRoute: 'TCP',
  TLSRoute: 'TLS',
}

export function ActivitySummary({ namespaces, topology, onNavigate }: ActivitySummaryProps) {
  const hasLimitedAccess = useHasLimitedAccess()
  const { data: events, isLoading, error } = useChanges({
    namespaces,
    timeRange: '1h',
    includeK8sEvents: true,
    includeManaged: true,
    limit: 1000,
  })

  const now = useMemo(() => Date.now(), [events])
  const spanMs = SPAN_MINUTES * 60 * 1000
  const startTime = now - spanMs

  const lanes = useMemo(() => {
    if (!events || events.length === 0) return []

    // Only use events within the visible window
    const windowEvents = events.filter(e => {
      const t = new Date(e.timestamp).getTime()
      return t >= startTime && t <= now
    })
    if (windowEvents.length === 0) return []

    const hierarchy = buildResourceHierarchy({
      events: windowEvents,
      topology: topology || undefined,
    })
    return hierarchy
      .sort((a, b) => scoreLane(b) - scoreLane(a))
      .slice(0, MAX_LANES)
  }, [events, startTime, now, topology])

  const hasActivity = lanes.length > 0

  return (
    <button
      onClick={onNavigate}
      className="group flex flex-col h-[260px] rounded-lg border-[3px] border-blue-500/30 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-blue-500/60 transition-all duration-200 text-left cursor-pointer"
    >
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
        <div className="flex items-center gap-2">
          <Clock className="w-4 h-4 text-blue-500" />
          <span className="text-sm font-semibold text-blue-500">Timeline</span>
        </div>
        <span className="text-xs text-theme-text-tertiary">last {SPAN_MINUTES}m</span>
      </div>

      {/* Mini swimlanes */}
      <div className="flex-1 min-h-0 px-4 py-1.5">
        {isLoading ? (
          <div className="flex items-center justify-center h-full py-4 text-xs text-theme-text-tertiary">
            Loading...
          </div>
        ) : error ? (
          <div className="flex items-center justify-center h-full py-4 text-xs text-theme-text-tertiary">
            Could not load activity
          </div>
        ) : !hasActivity ? (
          <div className="flex flex-col items-center justify-center h-full py-4 text-xs text-theme-text-tertiary">
            <span>No recent activity</span>
            {hasLimitedAccess && (
              <span className="flex items-center gap-1 mt-1.5 text-[11px] text-amber-400/80">
                <Shield className="w-3 h-3" />
                Some resource types are not monitored due to RBAC restrictions
              </span>
            )}
          </div>
        ) : (
          <div className="space-y-1">
            {lanes.map((lane) => (
              <MiniLane
                key={lane.id}
                lane={lane}
                startTime={startTime}
                now={now}
                spanMs={spanMs}
              />
            ))}

            {/* Time axis */}
            <div className="flex items-center justify-between text-[10px] text-theme-text-tertiary pt-1">
              <span>{SPAN_MINUTES}m ago</span>
              <span>now</span>
            </div>
          </div>
        )}
      </div>

      <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-end gap-1.5 text-xs font-medium text-blue-500 group-hover:text-blue-400 transition-colors">
        Open Timeline
        <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
      </div>
    </button>
  )
}

// A single compact swimlane row: [kind label + name] [health bar with event dots]
function MiniLane({ lane, startTime, now, spanMs }: {
  lane: ResourceLane
  startTime: number
  now: number
  spanMs: number
}) {
  const allEvents: TimelineEvent[] = lane.allEventsSorted || [
    ...lane.events,
    ...(lane.children?.flatMap(c => c.events) || []),
  ]
  const changeEvents = allEvents.filter(e => isChangeEvent(e))
  const { spans } = buildHealthSpans(changeEvents, startTime, now, allEvents)

  const hasProblems = allEvents.some(e => isProblematicEvent(e))
  const kindLabel = KIND_SHORT[lane.kind] || lane.kind

  return (
    <div className="flex items-center gap-2 h-5">
      {/* Label */}
      <div className="w-[6.5rem] shrink-0 flex items-center gap-1 min-w-0">
        <span className={clsx(
          'text-[9px] leading-none px-1 py-0.5 rounded shrink-0',
          hasProblems
            ? 'bg-amber-500/15 text-amber-600 dark:text-amber-300'
            : 'bg-theme-elevated text-theme-text-tertiary',
        )}>
          {kindLabel}
        </span>
        <span className="text-[11px] text-theme-text-secondary truncate">
          {lane.name}
        </span>
      </div>

      {/* Health bar track */}
      <div className="flex-1 relative h-3 bg-theme-border/20 rounded-sm overflow-hidden">
        {/* Health state spans */}
        {spans.map((span, i) => {
          const left = timeToX(span.start, startTime, spanMs)
          const right = timeToX(span.end, startTime, spanMs)
          const width = right - left
          if (width <= 0 || right < 0 || left > 100) return null

          const clampedLeft = Math.max(0, left)
          const clampedWidth = Math.min(100 - clampedLeft, width - (clampedLeft - left))
          if (clampedWidth <= 0) return null

          return (
            <div
              key={i}
              className={clsx(
                'absolute top-0 bottom-0 rounded-sm',
                HEALTH_SPAN_COLORS[span.health] || 'bg-gray-400/30',
              )}
              style={{ left: `${clampedLeft}%`, width: `${clampedWidth}%` }}
            />
          )
        })}

        {/* All event dots — small for normal, larger for critical */}
        {allEvents.map((event, idx) => {
          const x = timeToX(new Date(event.timestamp).getTime(), startTime, spanMs)
          if (x < 0 || x > 100) return null

          const isCritical = isProblematicEvent(event)

          return (
            <div
              key={`${event.id}-${idx}`}
              className={clsx(
                'absolute top-1/2 -translate-y-1/2 -translate-x-1/2 rounded-full',
                isCritical
                  ? 'w-2.5 h-2.5 bg-red-500 ring-1 ring-red-500/30 z-10'
                  : 'w-1.5 h-1.5',
                !isCritical && (
                  event.eventType === 'add' ? 'bg-green-500'
                    : event.eventType === 'delete' ? 'bg-red-500'
                      : event.eventType === 'update' ? 'bg-blue-500'
                        : 'bg-theme-text-tertiary'
                ),
              )}
              style={{ left: `${x}%` }}
            />
          )
        })}
      </div>
    </div>
  )
}
