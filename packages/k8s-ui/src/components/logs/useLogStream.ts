import { useState, useRef, useCallback, useEffect } from 'react'
import { handleSSEError } from '../../utils/log-format'

export interface LogStreamHandlers {
  /** Called when stream connects with parsed event data. setIsStreaming(true) is called automatically. */
  onConnected?: (data: unknown) => void
  /** Called for each log event with parsed event data */
  onLog: (data: unknown) => void
  /** Called when new pods are discovered during streaming (workload logs only) */
  onPodAdded?: (data: unknown) => void
  /** Called when pods are terminated during streaming (workload logs only) */
  onPodRemoved?: (data: unknown) => void
  /** Called when the server ends the stream cleanly */
  onEnd?: (data: unknown) => void
}

/**
 * Manages an SSE log stream: EventSource lifecycle, isStreaming state, cleanup.
 * Callers provide a factory function that creates the EventSource with current params.
 */
export function useLogStream() {
  const [isStreaming, setIsStreaming] = useState(false)
  // Set when the connection fails (and not on a clean end — see endedRef).
  const [streamError, setStreamError] = useState<string | null>(null)
  // True from a start attempt until the stream first settles (connected / end /
  // error / stop). Lets callers show a connecting spinner that won't reappear
  // after a clean end. Starts true so an auto-stream viewer paints the spinner
  // immediately instead of flashing the empty state.
  const [connecting, setConnecting] = useState(true)
  const eventSourceRef = useRef<EventSource | null>(null)
  // EventSource fires a generic 'error' on the normal close that follows the
  // server's 'end'; this distinguishes a clean end from a real failure.
  const endedRef = useRef(false)

  const stopStreaming = useCallback(() => {
    eventSourceRef.current?.close()
    eventSourceRef.current = null
    setIsStreaming(false)
    setConnecting(false)
    setStreamError(null)
  }, [])

  const startStreaming = useCallback((
    create: () => EventSource,
    handlers: LogStreamHandlers,
    errorContext = 'Log stream error',
  ) => {
    eventSourceRef.current?.close()
    endedRef.current = false
    setStreamError(null)
    setConnecting(true)
    const es = create()
    // Ignore events from a superseded source: closing/replacing an EventSource
    // (Stop, container switch, restart) can fire a late, async 'error' that
    // would otherwise corrupt the new stream's state or show a false failure.
    const isCurrent = () => eventSourceRef.current === es

    es.addEventListener('connected', (event) => {
      if (!isCurrent()) return
      setIsStreaming(true)
      setConnecting(false)
      if (handlers.onConnected) {
        try { handlers.onConnected(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse connected event:', e)
        }
      }
    })

    es.addEventListener('log', (event) => {
      if (!isCurrent()) return
      setConnecting(false)
      try { handlers.onLog(JSON.parse((event as MessageEvent).data)) } catch (e) {
        console.error('Failed to parse log event:', e)
      }
    })

    es.addEventListener('pod_added', (event) => {
      if (!isCurrent()) return
      if (handlers.onPodAdded) {
        try { handlers.onPodAdded(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse pod_added event:', e)
        }
      }
    })

    es.addEventListener('pod_removed', (event) => {
      if (!isCurrent()) return
      if (handlers.onPodRemoved) {
        try { handlers.onPodRemoved(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse pod_removed event:', e)
        }
      }
    })

    es.addEventListener('end', (event) => {
      if (!isCurrent()) return
      endedRef.current = true
      setIsStreaming(false)
      setConnecting(false)
      if (handlers.onEnd) {
        try { handlers.onEnd(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse end event:', e)
        }
      }
    })

    es.addEventListener('error', (event) => {
      if (!isCurrent()) { es.close(); return }
      setIsStreaming(false)
      setConnecting(false)
      es.close()
      // The browser fires 'error' on the normal close that follows a clean
      // 'end'; that's not a failure, so don't log it or surface it.
      if (endedRef.current) return
      handleSSEError(event, errorContext, () => {})
      setStreamError(errorContext)
    })

    eventSourceRef.current = es
  }, [])

  // Cleanup on unmount
  useEffect(() => () => { eventSourceRef.current?.close() }, [])

  return { isStreaming, streamError, connecting, startStreaming, stopStreaming }
}
