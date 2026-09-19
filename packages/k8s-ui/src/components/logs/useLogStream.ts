import { useState, useRef, useCallback, useEffect } from 'react'
import { attachLogStreamListeners, type LogStreamHandlers } from './log-stream-listeners'

export type { LogStreamHandlers }

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
  // Set when the server closes the stream cleanly. Distinct from streamError:
  // a stream that ends is not a failure, but it is still not live, and the
  // viewer has to be able to say so.
  const [streamEnded, setStreamEnded] = useState<string | null>(null)
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
    // streamEnded is deliberately kept. A pod going terminal shuts the stream
    // down through here, and that is exactly when the reader needs to be told
    // it ended. Only a fresh start clears it.
  }, [])

  // Abandon the stream and everything said about it. Distinct from
  // stopStreaming, which keeps the end notice because a terminal pod shuts the
  // stream down through it and the reader still needs telling. Carrying that
  // across a source switch would put one container's ending over another's
  // output.
  const resetStream = useCallback(() => {
    eventSourceRef.current?.close()
    eventSourceRef.current = null
    endedRef.current = false
    setIsStreaming(false)
    setConnecting(false)
    setStreamError(null)
    setStreamEnded(null)
  }, [])

  const startStreaming = useCallback((
    create: () => EventSource,
    handlers: LogStreamHandlers,
    errorContext = 'Log stream error',
  ) => {
    eventSourceRef.current?.close()
    endedRef.current = false
    setStreamError(null)
    setStreamEnded(null)
    setConnecting(true)
    const es = create()
    attachLogStreamListeners(es, handlers, errorContext, {
      setIsStreaming,
      setConnecting,
      setStreamError,
      setStreamEnded,
      isCurrent: () => eventSourceRef.current === es,
      ended: endedRef,
    })
    eventSourceRef.current = es
  }, [])

  // Cleanup on unmount
  useEffect(() => () => { eventSourceRef.current?.close() }, [])

  return { isStreaming, streamError, streamEnded, connecting, startStreaming, stopStreaming, resetStream }
}
