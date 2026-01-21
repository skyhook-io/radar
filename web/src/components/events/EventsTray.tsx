import { memo } from 'react'
import { X, Plus, RefreshCw, Trash2, AlertCircle, Info } from 'lucide-react'
import { clsx } from 'clsx'
import type { K8sEvent } from '../../types'

interface EventsTrayProps {
  events: K8sEvent[]
  onClose: () => void
  onEventClick: (event: K8sEvent) => void
}

function formatTimeAgo(timestamp: number): string {
  const seconds = Math.floor((Date.now() - timestamp) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

function getOperationIcon(operation: string) {
  switch (operation) {
    case 'add':
      return Plus
    case 'update':
      return RefreshCw
    case 'delete':
      return Trash2
    default:
      return Info
  }
}

function getOperationColor(operation: string): string {
  switch (operation) {
    case 'add':
      return 'text-green-400 bg-green-500/10'
    case 'update':
      return 'text-blue-400 bg-blue-500/10'
    case 'delete':
      return 'text-red-400 bg-red-500/10'
    default:
      return 'text-theme-text-secondary bg-theme-hover/30'
  }
}

function getKindColor(kind: string): string {
  switch (kind) {
    case 'Pod':
      return 'text-lime-400'
    case 'Deployment':
      return 'text-emerald-400'
    case 'Service':
      return 'text-blue-400'
    case 'Ingress':
      return 'text-violet-400'
    case 'ReplicaSet':
      return 'text-green-400'
    case 'Event':
      return 'text-yellow-400'
    default:
      return 'text-theme-text-secondary'
  }
}

export const EventsTray = memo(function EventsTray({
  events,
  onClose,
  onEventClick,
}: EventsTrayProps) {
  return (
    <div className="w-80 bg-theme-surface border-l border-theme-border flex flex-col">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-theme-border">
        <h2 className="font-semibold text-theme-text-primary flex items-center gap-2">
          <AlertCircle className="w-4 h-4 text-blue-400" />
          Events
          <span className="text-xs text-theme-text-secondary font-normal">
            ({events.length})
          </span>
        </h2>
        <button
          onClick={onClose}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {/* Events list */}
      <div className="flex-1 overflow-y-auto">
        {events.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-secondary">
            <AlertCircle className="w-8 h-8 mb-2 opacity-50" />
            <p className="text-sm">No recent events</p>
          </div>
        ) : (
          <div className="table-divide-subtle">
            {events.map((event, index) => {
              const Icon = getOperationIcon(event.operation)
              return (
                <button
                  key={`${event.kind}-${event.namespace}-${event.name}-${event.timestamp}-${index}`}
                  onClick={() => onEventClick(event)}
                  className="w-full px-4 py-3 text-left hover:bg-theme-elevated/50 transition-colors"
                >
                  <div className="flex items-start gap-3">
                    <div
                      className={clsx(
                        'p-1.5 rounded',
                        getOperationColor(event.operation)
                      )}
                    >
                      <Icon className="w-3.5 h-3.5" />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span
                          className={clsx(
                            'text-xs font-medium',
                            getKindColor(event.kind)
                          )}
                        >
                          {event.kind}
                        </span>
                        <span className="text-xs text-theme-text-tertiary">
                          {event.operation}
                        </span>
                      </div>
                      <p className="text-sm text-theme-text-primary truncate mt-0.5">
                        {event.name}
                      </p>
                      <div className="flex items-center gap-2 mt-1">
                        {event.namespace && (
                          <span className="text-xs text-theme-text-tertiary">
                            {event.namespace}
                          </span>
                        )}
                        {event.timestamp && (
                          <span className="text-xs text-theme-text-tertiary">
                            {formatTimeAgo(event.timestamp)}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                </button>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
})
