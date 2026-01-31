import { useState } from 'react'
import { Bug, X, ChevronDown, ChevronUp } from 'lucide-react'
import { useRuntimeStats } from '../api/client'

function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const hours = Math.floor(seconds / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  return `${hours}h ${mins}m`
}

export function DebugOverlay() {
  const [visible, setVisible] = useState(true)
  const [expanded, setExpanded] = useState(false)
  const { data } = useRuntimeStats(visible)

  if (!visible) {
    return (
      <button
        onClick={() => setVisible(true)}
        className="fixed bottom-3 right-3 z-50 p-2 bg-theme-surface/90 border border-theme-border rounded-lg text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
        title="Show debug stats"
      >
        <Bug className="w-4 h-4" />
      </button>
    )
  }

  const runtime = data?.runtime

  return (
    <div className="fixed bottom-3 right-3 z-50 bg-theme-surface/95 border border-theme-border rounded-lg shadow-lg backdrop-blur-sm text-xs font-mono">
      {/* Header */}
      <div className="flex items-center gap-2 px-2 py-1.5 border-b border-theme-border/50">
        <Bug className="w-3 h-3 text-theme-text-tertiary" />
        <span className="text-theme-text-secondary">Debug</span>
        <div className="flex-1" />
        <button
          onClick={() => setExpanded(!expanded)}
          className="p-0.5 text-theme-text-tertiary hover:text-theme-text-secondary"
        >
          {expanded ? <ChevronDown className="w-3 h-3" /> : <ChevronUp className="w-3 h-3" />}
        </button>
        <button
          onClick={() => setVisible(false)}
          className="p-0.5 text-theme-text-tertiary hover:text-theme-text-secondary"
        >
          <X className="w-3 h-3" />
        </button>
      </div>

      {/* Stats */}
      <div className="px-2 py-1.5 space-y-0.5">
        {runtime ? (
          <>
            <div className="flex justify-between gap-4">
              <span className="text-theme-text-tertiary">Heap</span>
              <span className="text-theme-text-primary">{runtime.heapMB.toFixed(1)} MB</span>
            </div>
            {expanded && (
              <>
                <div className="flex justify-between gap-4">
                  <span className="text-theme-text-tertiary">Objects</span>
                  <span className="text-theme-text-primary">{runtime.heapObjectsK.toFixed(1)}K</span>
                </div>
                <div className="flex justify-between gap-4">
                  <span className="text-theme-text-tertiary">Goroutines</span>
                  <span className="text-theme-text-primary">{runtime.goroutines}</span>
                </div>
                <div className="flex justify-between gap-4">
                  <span className="text-theme-text-tertiary">Uptime</span>
                  <span className="text-theme-text-primary">{formatUptime(runtime.uptimeSeconds)}</span>
                </div>
                <div className="flex justify-between gap-4">
                  <span className="text-theme-text-tertiary">Resources</span>
                  <span className="text-theme-text-primary">{data?.resourceCount ?? '-'}</span>
                </div>
              </>
            )}
          </>
        ) : (
          <span className="text-theme-text-tertiary">Loading...</span>
        )}
      </div>
    </div>
  )
}
