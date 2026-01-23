import { useState, useEffect, useCallback, useRef } from 'react'
import type { Topology, K8sEvent, ViewMode } from '../types'

interface UseEventSourceReturn {
  topology: Topology | null
  events: K8sEvent[]
  connected: boolean
  reconnect: () => void
}

interface UseEventSourceOptions {
  onContextSwitchComplete?: () => void
  onContextSwitchProgress?: (message: string) => void
  onContextChanged?: (context: string) => void
}

const MAX_EVENTS = 100 // Keep last 100 events

export function useEventSource(
  namespace: string,
  viewMode: ViewMode = 'full',
  options?: UseEventSourceOptions
): UseEventSourceReturn {
  const [topology, setTopology] = useState<Topology | null>(null)
  const [events, setEvents] = useState<K8sEvent[]>([])
  const [connected, setConnected] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)
  const waitingForTopologyAfterSwitch = useRef(false)

  const connect = useCallback(() => {
    // Clean up existing connection
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
    }
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
    }

    // Build URL
    const params = new URLSearchParams()
    if (namespace) {
      params.set('namespace', namespace)
    }
    if (viewMode && viewMode !== 'full') {
      params.set('view', viewMode)
    }
    const url = `/api/events/stream${params.toString() ? `?${params}` : ''}`

    // Create new EventSource
    const es = new EventSource(url)
    eventSourceRef.current = es

    es.onopen = () => {
      console.log('SSE connected')
      setConnected(true)
    }

    es.onerror = (error) => {
      console.error('SSE error:', error)
      setConnected(false)
      es.close()

      // Reconnect after 3 seconds
      reconnectTimeoutRef.current = window.setTimeout(() => {
        console.log('SSE reconnecting...')
        connect()
      }, 3000)
    }

    // Handle topology updates
    es.addEventListener('topology', (event) => {
      try {
        const data = JSON.parse(event.data) as Topology
        setTopology(data)
        // If we were waiting for topology after a context switch, signal completion
        if (waitingForTopologyAfterSwitch.current) {
          waitingForTopologyAfterSwitch.current = false
          options?.onContextSwitchComplete?.()
        }
      } catch (e) {
        console.error('Failed to parse topology:', e)
      }
    })

    // Handle K8s events
    es.addEventListener('k8s_event', (event) => {
      try {
        const data = JSON.parse(event.data) as K8sEvent
        data.timestamp = Date.now()
        setEvents((prev) => [data, ...prev].slice(0, MAX_EVENTS))
      } catch (e) {
        console.error('Failed to parse event:', e)
      }
    })

    // Handle heartbeat (just log, keeps connection alive)
    es.addEventListener('heartbeat', () => {
      // Connection is alive
    })

    // Handle context switch progress events
    es.addEventListener('context_switch_progress', (event) => {
      try {
        const data = JSON.parse(event.data) as { message: string }
        options?.onContextSwitchProgress?.(data.message)
      } catch (e) {
        console.error('Failed to parse context_switch_progress event:', e)
      }
    })

    // Handle context changed event - clear state while new data loads
    es.addEventListener('context_changed', (event) => {
      try {
        const data = JSON.parse(event.data) as { context: string }
        console.log('Context changed to:', data.context)
        // Clear topology and events - new data will come via topology event
        setTopology(null)
        setEvents([])
        // Mark that we're waiting for new topology data
        waitingForTopologyAfterSwitch.current = true
        // Notify caller to invalidate caches (e.g., helm releases, resources)
        options?.onContextChanged?.(data.context)
      } catch (e) {
        console.error('Failed to parse context_changed event:', e)
      }
    })
  }, [namespace, viewMode])

  // Reconnect function for manual reconnection
  const reconnect = useCallback(() => {
    connect()
  }, [connect])

  // Connect on mount and when namespace/viewMode changes
  useEffect(() => {
    connect()

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close()
      }
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
      }
    }
  }, [connect])

  // Clear events when namespace changes
  useEffect(() => {
    setEvents([])
  }, [namespace])

  return {
    topology,
    events,
    connected,
    reconnect,
  }
}
