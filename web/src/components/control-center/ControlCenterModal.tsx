import { useState, useEffect, useCallback, useMemo } from 'react'
import { X, Layers, ExternalLink, Power, RefreshCw, Activity, Cpu, AlertCircle } from 'lucide-react'
import { Badge } from '@skyhook-io/k8s-ui'
import { getBasename, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { Tooltip } from '../ui/Tooltip'

export interface ClusterSession {
  context: string
  slug: string
  isCurrent: boolean
  isPrimary: boolean
  status: 'active' | 'starting' | 'sleeping'
  port?: number
  pid?: number
  lastAccess?: string
  createdAt?: string
}

interface ControlCenterModalProps {
  open: boolean
  onClose: () => void
}

export function ControlCenterModal({ open, onClose }: ControlCenterModalProps) {
  const [sessions, setSessions] = useState<ClusterSession[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [releasingContext, setReleasingContext] = useState<string | null>(null)
  const [releasingAll, setReleasingAll] = useState(false)

  // In worker tabs, getBasename() ends with /c/{slug}. Strip it so API calls reach the parent server.
  const parentBase = useMemo(() => {
    const base = getBasename()
    return base.replace(/\/c\/[^/]+$/, '')
  }, [])

  const buildParentApiUrl = useCallback((endpoint: string) => {
    return `${parentBase}/api${endpoint}`
  }, [parentBase])

  const fetchSessions = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(buildParentApiUrl('/control-center/sessions'), {
        headers: getAuthHeaders(),
        credentials: getCredentialsMode(),
      })
      if (res.ok) {
        const data = await res.json()
        setSessions(data)
      } else {
        setError(`Failed to retrieve cluster sessions (${res.status} ${res.statusText})`)
      }
    } catch (err) {
      console.error('Failed to load control center sessions:', err)
      setError('Unable to connect to the cluster supervisor.')
    } finally {
      setLoading(false)
    }
  }, [buildParentApiUrl])

  useEffect(() => {
    if (open) {
      fetchSessions()
    }
  }, [open, fetchSessions])

  const handleRelease = async (slugOrContext: string) => {
    setReleasingContext(slugOrContext)
    try {
      const res = await fetch(buildParentApiUrl(`/control-center/sessions/${encodeURIComponent(slugOrContext)}/release`), {
        method: 'POST',
        headers: getAuthHeaders(),
        credentials: getCredentialsMode(),
      })
      if (res.ok) {
        await fetchSessions()
      }
    } catch (err) {
      console.error(`Failed to release session for ${slugOrContext}:`, err)
    } finally {
      setReleasingContext(null)
    }
  }

  const handleReleaseAll = async () => {
    setReleasingAll(true)
    try {
      const res = await fetch(buildParentApiUrl('/control-center/release-all'), {
        method: 'POST',
        headers: getAuthHeaders(),
        credentials: getCredentialsMode(),
      })
      if (res.ok) {
        await fetchSessions()
      }
    } catch (err) {
      console.error('Failed to release all background sessions:', err)
    } finally {
      setReleasingAll(false)
    }
  }

  if (!open) return null

  const activeWorkers = sessions.filter(s => !s.isPrimary && s.status === 'active')

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-theme-base/80 backdrop-blur-sm animate-in fade-in duration-150">
      <div
        className="w-full max-w-2xl bg-theme-surface border border-theme-border rounded-xl shadow-theme-lg overflow-hidden flex flex-col max-h-[85vh]"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-6 py-4 border-b border-theme-border bg-theme-base/50">
          <div className="flex items-center gap-2.5">
            <div className="p-2 rounded-lg bg-theme-elevated text-theme-text-primary">
              <Layers className="w-5 h-5" />
            </div>
            <div>
              <h2 className="text-base font-semibold text-theme-text-primary">Cluster Control Center</h2>
              <p className="text-xs text-theme-text-tertiary">
                Manage multi-cluster sessions and release idle memory & connections
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Tooltip content="Refresh session statuses" position="bottom">
              <button
                onClick={fetchSessions}
                disabled={loading}
                className="p-1.5 rounded-md hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors disabled:opacity-50"
              >
                <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
              </button>
            </Tooltip>
            <button
              onClick={onClose}
              className="p-1.5 rounded-md hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        <div className="grid grid-cols-3 gap-2 px-6 py-3 border-b border-theme-border bg-theme-elevated/40 text-xs">
          <div className="flex items-center gap-2">
            <Activity className="w-3.5 h-3.5 text-theme-text-secondary" />
            <span className="text-theme-text-secondary">Available Clusters:</span>
            <span className="font-semibold text-theme-text-primary">{sessions.length}</span>
          </div>
          <div className="flex items-center gap-2">
            <Cpu className="w-3.5 h-3.5 text-theme-text-secondary" />
            <span className="text-theme-text-secondary">Active Workers:</span>
            <span className="font-semibold text-theme-text-primary">{activeWorkers.length}</span>
          </div>
          <div className="flex items-center justify-end">
            {activeWorkers.length > 0 && (
              <button
                onClick={handleReleaseAll}
                disabled={releasingAll}
                className="flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium text-warning-text bg-theme-elevated hover:bg-theme-hover border border-theme-border rounded-md transition-colors"
              >
                <Power className="w-3 h-3" />
                {releasingAll ? 'Releasing...' : 'Release All Workers'}
              </button>
            )}
          </div>
        </div>

        <div className="p-6 overflow-y-auto space-y-3 flex-1">
          {error && (
            <div className="p-4 rounded-lg border border-theme-border bg-theme-elevated flex items-start gap-3">
              <AlertCircle className="w-4 h-4 text-warning-text shrink-0 mt-0.5" />
              <div className="flex-1 text-xs">
                <p className="text-theme-text-primary font-medium">{error}</p>
                <button
                  onClick={fetchSessions}
                  className="mt-2 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary underline"
                >
                  Retry
                </button>
              </div>
            </div>
          )}

          {!error && sessions.length === 0 && !loading && (
            <div className="text-center py-8 text-xs text-theme-text-tertiary">
              No cluster sessions available.
            </div>
          )}

          {sessions.map(s => {
            const isReleasing = releasingContext === (s.slug || s.context)
            const targetUrl = parentBase ? `${parentBase}/c/${s.slug}/` : `/c/${s.slug}/`

            return (
              <div
                key={s.slug || s.context}
                className="flex items-center justify-between p-3.5 rounded-lg border border-theme-border bg-theme-base/60 hover:bg-theme-base transition-colors"
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="flex flex-col min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-theme-text-primary truncate">
                        {s.context}
                      </span>
                      {s.isPrimary && (
                        <Badge severity="info" size="sm">
                          Primary
                        </Badge>
                      )}
                      {s.status === 'active' ? (
                        <Badge severity="success" size="sm">
                          Active
                        </Badge>
                      ) : (
                        <Badge severity="neutral" size="sm">
                          Sleeping
                        </Badge>
                      )}
                    </div>
                    <div className="text-[11px] text-theme-text-tertiary flex items-center gap-2 mt-0.5">
                      {s.port && <span>Port: {s.port}</span>}
                      {s.pid && <span>PID: {s.pid}</span>}
                      {s.status === 'sleeping' && <span>0 RAM used (idle)</span>}
                    </div>
                  </div>
                </div>

                <div className="flex items-center gap-2 shrink-0">
                  <a
                    href={targetUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-theme-elevated hover:bg-theme-hover text-theme-text-primary rounded-md transition-colors"
                  >
                    <span>Open Tab</span>
                    <ExternalLink className="w-3 h-3 text-theme-text-tertiary" />
                  </a>

                  {!s.isPrimary && s.status === 'active' && (
                    <button
                      onClick={() => handleRelease(s.slug || s.context)}
                      disabled={isReleasing}
                      className="flex items-center gap-1 px-2.5 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary bg-theme-hover rounded-md transition-colors disabled:opacity-50 border border-theme-border"
                    >
                      <Power className="w-3 h-3" />
                      <span>{isReleasing ? 'Freeing...' : 'Sleep'}</span>
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>

        <div className="flex items-center justify-between px-6 py-3 border-t border-theme-border bg-theme-base/30 text-xs text-theme-text-tertiary">
          <span>Workers automatically idle after 15 minutes of inactivity.</span>
          <button
            onClick={onClose}
            className="px-3 py-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
