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
  Globe,
  Monitor,
  PenLine,
} from 'lucide-react'
import { clsx } from 'clsx'
import { useToast } from '../ui/Toast'

interface PortForwardSession {
  id: string
  namespace: string
  podName: string
  podPort: number
  localPort: number
  listenAddress: string
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
  const [editingPortId, setEditingPortId] = useState<string | null>(null)
  const [editPortValue, setEditPortValue] = useState('')
  const [changingPortId, setChangingPortId] = useState<string | null>(null)
  const queryClient = useQueryClient()
  const { showSuccess, showError } = useToast()

  // Fetch active port forwards
  const { data: sessions = [], isLoading } = useQuery<PortForwardSession[]>({
    queryKey: ['portforwards'],
    queryFn: async () => {
      const res = await fetch('/api/portforwards')
      if (!res.ok) throw new Error('Failed to fetch port forwards')
      return res.json()
    },
    refetchInterval: 30000, // Poll every 30 seconds - sessions are invalidated on user actions
  })

  // Stop port forward mutation
  const stopMutation = useMutation({
    mutationFn: async (id: string) => {
      const res = await fetch(`/api/portforwards/${id}`, { method: 'DELETE' })
      if (!res.ok) throw new Error('Failed to stop port forward')
      return res.json()
    },
    meta: {
      errorMessage: 'Failed to stop port forward',
      successMessage: 'Port forward stopped',
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    },
  })

  // Toggle listen address (restart with different address)
  const [togglingId, setTogglingId] = useState<string | null>(null)
  const toggleListenAddress = async (session: PortForwardSession) => {
    const newAddress = session.listenAddress === '0.0.0.0' ? '127.0.0.1' : '0.0.0.0'
    setTogglingId(session.id)
    try {
      const delRes = await fetch(`/api/portforwards/${session.id}`, { method: 'DELETE' })
      if (!delRes.ok) {
        throw new Error(`Failed to stop existing port forward (HTTP ${delRes.status})`)
      }
      const res = await fetch('/api/portforwards', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          namespace: session.namespace,
          podName: session.podName || undefined,
          serviceName: session.serviceName || undefined,
          podPort: session.podPort,
          localPort: session.localPort,
          listenAddress: newAddress,
        }),
      })
      if (!res.ok) {
        const error = await res.json().catch(() => ({}))
        throw new Error(error.error || 'Failed to restart port forward')
      }
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
    } catch (error) {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      const msg = error instanceof Error ? error.message : 'Failed to change network access'
      showError('Failed to change network access', msg)
      console.error('Failed to toggle listen address:', error)
    } finally {
      setTogglingId(null)
    }
  }

  const changeLocalPort = async (session: PortForwardSession, newPort: number) => {
    if (newPort === session.localPort) {
      setEditingPortId(null)
      return
    }
    setChangingPortId(session.id)
    setEditingPortId(null)
    try {
      const delRes = await fetch(`/api/portforwards/${session.id}`, { method: 'DELETE' })
      if (!delRes.ok) {
        throw new Error(`Failed to stop existing port forward (HTTP ${delRes.status})`)
      }
      const res = await fetch('/api/portforwards', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          namespace: session.namespace,
          podName: session.podName || undefined,
          serviceName: session.serviceName || undefined,
          podPort: session.podPort,
          localPort: newPort,
          listenAddress: session.listenAddress,
        }),
      })
      if (!res.ok) {
        const error = await res.json().catch(() => ({}))
        throw new Error(error.error || 'Failed to restart port forward')
      }
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      showSuccess('Port forward updated', `Now listening on localhost:${newPort}`)
    } catch (error) {
      queryClient.invalidateQueries({ queryKey: ['portforwards'] })
      const msg = error instanceof Error ? error.message : 'Failed to change local port'
      showError('Port forward lost', `Forward on port ${session.localPort} was stopped but port ${newPort} failed: ${msg}`)
      console.error('Failed to change local port:', error)
    } finally {
      setChangingPortId(null)
    }
  }

  const handleCopyUrl = useCallback((session: PortForwardSession) => {
    // Always use localhost for copy (works on the machine running Radar)
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
                          'w-2 h-2 rounded-full shrink-0',
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
                        {editingPortId === session.id ? (
                          <div className="flex items-center text-xs bg-slate-900 rounded text-blue-400 font-mono">
                            <span className="pl-2 py-1 text-slate-500 select-none">
                              {session.listenAddress === '0.0.0.0' ? '0.0.0.0' : 'localhost'}:
                            </span>
                            <input
                              type="number"
                              autoFocus
                              min={1}
                              max={65535}
                              value={editPortValue}
                              onChange={(e) => setEditPortValue(e.target.value)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter') {
                                  const val = Number(editPortValue)
                                  if (isNaN(val) || val < 1 || val > 65535 || !Number.isInteger(val)) {
                                    showError('Invalid port', 'Port must be a number between 1 and 65535')
                                    return
                                  }
                                  changeLocalPort(session, val)
                                } else if (e.key === 'Escape') {
                                  setEditingPortId(null)
                                }
                              }}
                              onBlur={() => setEditingPortId(null)}
                              className="w-16 bg-transparent border-none pr-2 py-1 text-blue-400 font-mono text-xs outline-none [appearance:textfield] [&::-webkit-outer-spin-button]:appearance-none [&::-webkit-inner-spin-button]:appearance-none"
                            />
                          </div>
                        ) : (
                          <code
                            className={clsx(
                              'group/port text-xs bg-slate-900 px-2 py-1 rounded text-blue-400 transition-all inline-flex items-center gap-1',
                              changingPortId === session.id
                                ? 'opacity-50'
                                : 'cursor-pointer hover:ring-1 hover:ring-blue-500/50'
                            )}
                            title="Click to change local port"
                            onClick={() => {
                              if (changingPortId || togglingId) return
                              setEditingPortId(session.id)
                              setEditPortValue(String(session.localPort))
                            }}
                          >
                            {changingPortId === session.id && (
                              <Loader2 className="w-3 h-3 animate-spin inline mr-1" />
                            )}
                            {session.listenAddress === '0.0.0.0' ? '0.0.0.0' : 'localhost'}:{session.localPort}
                            <PenLine className="w-3 h-3 text-slate-500 opacity-0 group-hover/port:opacity-100 transition-opacity" />
                          </code>
                        )}
                        <button
                          onClick={() => toggleListenAddress(session)}
                          disabled={togglingId === session.id || changingPortId === session.id}
                          className={clsx(
                            'flex items-center gap-1 px-1.5 py-0.5 text-xs rounded transition-colors',
                            session.listenAddress === '0.0.0.0'
                              ? 'bg-amber-500/20 text-amber-400 hover:bg-amber-500/30'
                              : 'bg-slate-700 text-slate-400 hover:bg-slate-600 hover:text-slate-200'
                          )}
                          title={session.listenAddress === '0.0.0.0'
                            ? 'Click to switch to localhost only'
                            : 'Click to allow access from other machines'
                          }
                        >
                          {togglingId === session.id ? (
                            <Loader2 className="w-3 h-3 animate-spin" />
                          ) : session.listenAddress === '0.0.0.0' ? (
                            <Globe className="w-3 h-3" />
                          ) : (
                            <Monitor className="w-3 h-3" />
                          )}
                          {session.listenAddress === '0.0.0.0' ? 'network' : 'local'}
                        </button>
                      </div>
                    )}
                  </div>

                  <div className="flex items-center gap-1 shrink-0">
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
      listenAddress?: string // "127.0.0.1" (default) or "0.0.0.0"
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
    meta: {
      errorMessage: 'Failed to start port forward',
      successMessage: 'Port forward started',
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
    refetchInterval: 30000, // Poll every 30 seconds
  })

  // Count both running and error sessions (not stopped)
  return sessions.filter((s) => s.status !== 'stopped').length
}
