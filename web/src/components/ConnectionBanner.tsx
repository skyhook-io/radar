import { useState, useEffect, useRef } from 'react'
import { X, Radio } from 'lucide-react'
import { useConnection } from '../context/ConnectionContext'

/**
 * ConnectionBanner shows a dismissible banner at the top of the app when
 * the cluster connection is degraded (reconnecting or SSE disconnected).
 *
 * - Yellow: reconnecting / SSE paused
 * - Red: disconnected with error
 *
 * Hidden when connected normally.
 */
export function ConnectionBanner({ sseConnected }: { sseConnected: boolean }) {
  const { connection } = useConnection()
  const [dismissed, setDismissed] = useState(false)
  const prevStateRef = useRef(connection.state)

  // Reset dismissed state when connection state changes
  useEffect(() => {
    if (prevStateRef.current !== connection.state) {
      setDismissed(false)
      prevStateRef.current = connection.state
    }
  }, [connection.state])

  // Also reset if SSE disconnects
  useEffect(() => {
    if (!sseConnected) {
      setDismissed(false)
    }
  }, [sseConnected])

  // Don't show anything when fully connected and SSE is active
  if (connection.state === 'connected' && sseConnected) return null

  // Don't show if dismissed
  if (dismissed) return null

  // Don't show the banner when the full-page connecting/error views are visible
  // (those handle the connecting and disconnected states already)
  if (connection.state === 'connecting' || connection.state === 'disconnected') return null

  // Only case left: connected to K8s but SSE is down
  if (connection.state === 'connected' && !sseConnected) {
    return (
      <div className="flex items-center justify-between px-4 py-1.5 bg-amber-500/10 border-b border-amber-500/20 text-amber-300 text-xs">
        <div className="flex items-center gap-2">
          <Radio className="w-3.5 h-3.5" />
          <span>Live updates paused — reconnecting to event stream...</span>
        </div>
        <button
          onClick={() => setDismissed(true)}
          className="p-0.5 hover:bg-amber-500/20 rounded"
        >
          <X className="w-3 h-3" />
        </button>
      </div>
    )
  }

  return null
}
