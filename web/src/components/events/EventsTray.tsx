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
      return 'text-slate-400 bg-slate-500/10'
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
      return 'text-slate-400'
  }
}

export const EventsTray = memo(function EventsTray({
  events,
  onClose,
  onEventClick,
}: EventsTrayProps) {
  return (
    <div className="w-80 bg-slate-800 border-l border-slate-700 flex flex-col">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-slate-700">
        <h2 className="font-semibold text-white flex items-center gap-2">
          <AlertCircle className="w-4 h-4 text-indigo-400" />
          Events
          <span className="text-xs text-slate-400 font-normal">
            ({events.length})
          </span>
        </h2>
        <button
          onClick={onClose}
          className="p-1 text-slate-400 hover:text-white hover:bg-slate-700 rounded"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {/* Events list */}
      <div className="flex-1 overflow-y-auto">
        {events.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-slate-400">
            <AlertCircle className="w-8 h-8 mb-2 opacity-50" />
            <p className="text-sm">No recent events</p>
          </div>
        ) : (
          <div className="divide-y divide-slate-700">
            {events.map((event, index) => {
              const Icon = getOperationIcon(event.operation)
              return (
                <button
                  key={`${event.kind}-${event.namespace}-${event.name}-${event.timestamp}-${index}`}
                  onClick={() => onEventClick(event)}
                  className="w-full px-4 py-3 text-left hover:bg-slate-700/50 transition-colors"
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
                        <span className="text-xs text-slate-500">
                          {event.operation}
                        </span>
                      </div>
                      <p className="text-sm text-white truncate mt-0.5">
                        {event.name}
                      </p>
                      <div className="flex items-center gap-2 mt-1">
                        {event.namespace && (
                          <span className="text-xs text-slate-500">
                            {event.namespace}
                          </span>
                        )}
                        {event.timestamp && (
                          <span className="text-xs text-slate-500">
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
