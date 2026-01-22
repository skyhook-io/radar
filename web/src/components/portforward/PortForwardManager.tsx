import { useState, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  X,
  ExternalLink,
  Copy,
  Check,
  Trash2,
  Radio,
  Loader2,
  ChevronDown,
  ChevronUp,
  Plug,
} from 'lucide-react'
import { clsx } from 'clsx'

interface PortForwardSession {
  id: string
  namespace: string
  podName: string
  podPort: number
  localPort: number
  serviceName?: string
  startedAt: string
  status: 'running' | 'stopped' | 'error'
  error?: string
}

interface PortForwardManagerProps {
  onClose?: () => void
  minimized?: boolean
  onToggleMinimize?: () => void
}

export function PortForwardManager({
  onClose,
  minimized = false,
  onToggleMinimize,
}: PortForwardManagerProps) {
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const queryClient = useQueryClient()

  // Fetch active port forwards
  const { data: sessions = [], isLoading } = useQuery<PortForwardSession[]>({
    queryKey: ['portforwards'],
    queryFn: async () => {
      const res = await fetch('/api/portforwards')
      if (!res.ok) throw new Error('Failed to fetch port forwards')
      return res.json()
    },
    refetchInterval: 2000, // Poll for updates
  })

  // Stop port forward mutation
  const stopMutation = useMutation({
    mutationFn: async (id: string) => {
      const res = await fetch(`/api/portforwards/${id}`, { method: 'DELETE' })
      if (!res.ok) throw new Error('Failed to stop port forward')
      return res.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    },
  })

  const handleCopyUrl = useCallback((session: PortForwardSession) => {
    navigator.clipboard.writeText(`http://localhost:${session.localPort}`)
    setCopiedId(session.id)
    setTimeout(() => setCopiedId(null), 2000)
  }, [])

  const handleOpenUrl = useCallback((session: PortForwardSession) => {
    window.open(`http://localhost:${session.localPort}`, '_blank')
  }, [])

  // Show both running and error sessions (not stopped)
  const activeSessions = sessions.filter((s) => s.status !== 'stopped')
  const errorSessions = sessions.filter((s) => s.status === 'error')

  if (activeSessions.length === 0 && !isLoading) {
    return null // Don't show if no active sessions
  }

  if (minimized) {
    return (
      <button
        onClick={onToggleMinimize}
        className="fixed bottom-4 left-4 z-40 flex items-center gap-2 px-3 py-2 bg-slate-800 border border-slate-700 rounded-lg shadow-lg hover:bg-slate-700 transition-colors"
      >
        <Radio className={clsx('w-4 h-4', errorSessions.length > 0 ? 'text-red-400' : 'text-green-400 animate-pulse')} />
        <span className="text-sm text-slate-300">
          {activeSessions.length} port forward{activeSessions.length !== 1 ? 's' : ''}
          {errorSessions.length > 0 && <span className="text-red-400 ml-1">({errorSessions.length} failed)</span>}
        </span>
        <ChevronUp className="w-4 h-4 text-slate-400" />
      </button>
    )
  }

  return (
    <div className="fixed bottom-4 left-4 z-40 w-80 bg-slate-800 border border-slate-700 rounded-lg shadow-2xl overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 bg-slate-700/50 border-b border-slate-700">
        <div className="flex items-center gap-2">
          <Plug className="w-4 h-4 text-blue-400" />
          <span className="text-sm font-medium text-slate-200">Port Forwards</span>
          <span className="text-xs px-1.5 py-0.5 bg-slate-600 rounded text-slate-300">
            {activeSessions.length}
          </span>
          {errorSessions.length > 0 && (
            <span className="text-xs px-1.5 py-0.5 bg-red-500/20 rounded text-red-400">
              {errorSessions.length} failed
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {onToggleMinimize && (
            <button
              onClick={onToggleMinimize}
              className="p-1 text-slate-400 hover:text-white hover:bg-slate-600 rounded"
            >
              <ChevronDown className="w-4 h-4" />
            </button>
          )}
          {onClose && (
            <button
              onClick={onClose}
              className="p-1 text-slate-400 hover:text-white hover:bg-slate-600 rounded"
            >
              <X className="w-4 h-4" />
            </button>
          )}
        </div>
      </div>

      {/* Sessions list */}
      <div className="max-h-64 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center p-4">
            <Loader2 className="w-5 h-5 text-slate-400 animate-spin" />
          </div>
        ) : activeSessions.length === 0 ? (
          <div className="p-4 text-center text-sm text-slate-500">
            No active port forwards
          </div>
        ) : (
          <div className="divide-y divide-slate-700/50">
            {activeSessions.map((session) => (
              <div key={session.id} className={clsx(
                'p-3',
                session.status === 'error' ? 'bg-red-500/10' : 'hover:bg-slate-700/30'
              )}>
                <div className="flex items-start justify-between gap-2">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span
                        className={clsx(
                          'w-2 h-2 rounded-full flex-shrink-0',
                          session.status === 'running' ? 'bg-green-500' : 'bg-red-500'
                        )}
                      />
                      <span className="text-sm text-slate-200 font-medium truncate">
                        {session.serviceName || session.podName}
                      </span>
                      {session.status === 'error' && (
                        <span className="text-xs px-1.5 py-0.5 bg-red-500/20 rounded text-red-400">
                          Failed
                        </span>
                      )}
                    </div>
                    <div className="mt-1 text-xs text-slate-500">
                      {session.namespace} · Port {session.podPort}
                    </div>
                    {session.status === 'error' && session.error && (
                      <div className="mt-1.5 text-xs text-red-400 bg-red-500/10 px-2 py-1 rounded">
                        {session.error}
                      </div>
                    )}
                    {session.status === 'running' && (
                      <div className="mt-1.5 flex items-center gap-2">
                        <code className="text-xs bg-slate-900 px-2 py-1 rounded text-blue-400">
                          localhost:{session.localPort}
                        </code>
                      </div>
                    )}
                  </div>

                  <div className="flex items-center gap-1 flex-shrink-0">
                    {session.status === 'running' && (
                      <>
                        <button
                          onClick={() => handleCopyUrl(session)}
                          className="p-1.5 text-slate-400 hover:text-white hover:bg-slate-600 rounded"
                          title="Copy URL"
                        >
                          {copiedId === session.id ? (
                            <Check className="w-3.5 h-3.5 text-green-400" />
                          ) : (
                            <Copy className="w-3.5 h-3.5" />
                          )}
                        </button>
                        <button
                          onClick={() => handleOpenUrl(session)}
                          className="p-1.5 text-slate-400 hover:text-white hover:bg-slate-600 rounded"
                          title="Open in browser"
                        >
                          <ExternalLink className="w-3.5 h-3.5" />
                        </button>
                      </>
                    )}
                    <button
                      onClick={() => stopMutation.mutate(session.id)}
                      disabled={stopMutation.isPending}
                      className="p-1.5 text-slate-400 hover:text-red-400 hover:bg-slate-600 rounded disabled:opacity-50"
                      title={session.status === 'error' ? 'Dismiss' : 'Stop'}
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// Hook for starting port forwards
export function useStartPortForward() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (req: {
      namespace: string
      podName?: string
      serviceName?: string
      podPort: number
      localPort?: number
    }) => {
      const res = await fetch('/api/portforwards', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      })
      if (!res.ok) {
        const error = await res.json()
        throw new Error(error.error || 'Failed to start port forward')
      }
      return res.json() as Promise<PortForwardSession>
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    },
  })
}

// Hook for getting active port forwards count (includes errors)
export function usePortForwardCount() {
  const { data: sessions = [] } = useQuery<PortForwardSession[]>({
    queryKey: ['portforwards'],
    queryFn: async () => {
      const res = await fetch('/api/portforwards')
      if (!res.ok) return []
      return res.json()
    },
    refetchInterval: 5000,
  })

  // Count both running and error sessions (not stopped)
  return sessions.filter((s) => s.status !== 'stopped').length
}
