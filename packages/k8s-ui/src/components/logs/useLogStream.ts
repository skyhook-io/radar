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
}

/**
 * Manages an SSE log stream: EventSource lifecycle, isStreaming state, cleanup.
 * Callers provide a factory function that creates the EventSource with current params.
 */
export function useLogStream() {
  const [isStreaming, setIsStreaming] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)

  const stopStreaming = useCallback(() => {
    eventSourceRef.current?.close()
    eventSourceRef.current = null
    setIsStreaming(false)
  }, [])

  const startStreaming = useCallback((
    create: () => EventSource,
    handlers: LogStreamHandlers,
    errorContext = 'Log stream error',
  ) => {
    eventSourceRef.current?.close()
    const es = create()

    es.addEventListener('connected', (event) => {
      setIsStreaming(true)
      if (handlers.onConnected) {
        try { handlers.onConnected(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse connected event:', e)
        }
      }
    })

    es.addEventListener('log', (event) => {
      try { handlers.onLog(JSON.parse((event as MessageEvent).data)) } catch (e) {
        console.error('Failed to parse log event:', e)
      }
    })

    es.addEventListener('pod_added', (event) => {
      if (handlers.onPodAdded) {
        try { handlers.onPodAdded(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse pod_added event:', e)
        }
      }
    })

    es.addEventListener('pod_removed', (event) => {
      if (handlers.onPodRemoved) {
        try { handlers.onPodRemoved(JSON.parse((event as MessageEvent).data)) } catch (e) {
          console.error('Failed to parse pod_removed event:', e)
        }
      }
    })

    es.addEventListener('end', () => setIsStreaming(false))

    es.addEventListener('error', (event) => {
      handleSSEError(event, errorContext, () => { setIsStreaming(false); es.close() })
    })

    eventSourceRef.current = es
  }, [])

  // Cleanup on unmount
  useEffect(() => () => { eventSourceRef.current?.close() }, [])

  return { isStreaming, startStreaming, stopStreaming }
}
