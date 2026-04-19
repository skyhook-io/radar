import { useState, useEffect, useCallback, useRef } from 'react'
import type { Topology, K8sEvent, ViewMode } from '../types'
import type { ConnectionState } from '../context/ConnectionContext'
import { getApiBase, getCredentialsMode } from '../api/config'

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
  onConnectionStateChange?: (status: ConnectionState) => void
  onDeferredReady?: () => void
  onK8sEvent?: (event: K8sEvent) => void
}

const MAX_EVENTS = 100 // Keep last 100 events

// Dynamic throttle based on cluster size - fast for small, protective for large
function getTopologyThrottleMs(nodeCount: number): number {
  if (nodeCount < 100) return 500    // Small clusters: 0.5s
  if (nodeCount < 300) return 1000   // Medium clusters: 1s
  if (nodeCount < 500) return 2000   // Large clusters: 2s
  return 3000                         // Very large clusters: 3s
}
const INITIAL_RECONNECT_DELAY_MS = 3000
const MAX_RECONNECT_DELAY_MS = 30000 // Cap at 30 seconds

export function useEventSource(
  namespaces: string[],
  viewMode: ViewMode = 'resources',
  options?: UseEventSourceOptions,
  /** When set, SSE reconnects with this namespace filter (for large clusters that require server-side filtering) */
  forceNamespaceFilter?: string[],
  /** When true, evaluates NetworkPolicies and annotates edges with allow/block/unprotected */
  showPolicyEffect?: boolean,
): UseEventSourceReturn {
  const [topology, setTopology] = useState<Topology | null>(null)
  const [events, setEvents] = useState<K8sEvent[]>([])
  const [connected, setConnected] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)
  const waitingForTopologyAfterSwitch = useRef(false)
  const reconnectDelayRef = useRef(INITIAL_RECONNECT_DELAY_MS) // Exponential backoff

  // Throttling state for topology updates
  const lastTopologyUpdateRef = useRef<number>(0)
  const pendingTopologyRef = useRef<Topology | null>(null)
  const throttleTimeoutRef = useRef<number | null>(null)
  const currentNodeCountRef = useRef<number>(0) // Track node count for dynamic throttle

  // Serialize namespaces for stable dependency (used for events clearing)
  const namespacesKey = namespaces.join(',')

  // SSE namespace filter: only used for large clusters that require server-side filtering.
  // Small/medium clusters get all-namespace data and filter on the frontend.
  const sseNamespaceFilter = forceNamespaceFilter?.join(',') || ''

  // Use ref to avoid stale closures while not triggering reconnection on callback changes
  const optionsRef = useRef(options)
  optionsRef.current = options

  const connect = useCallback(() => {
    // Clean up existing connection
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
      // Clear stale topology so consumers show loading state instead of old data
      setTopology(null)
    }
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
    }

    // Build URL — only pass namespace filter for large clusters (forceNamespaceFilter)
    const params = new URLSearchParams()
    if (sseNamespaceFilter) {
      params.set('namespaces', sseNamespaceFilter)
    }
    if (viewMode && viewMode !== 'resources') {
      params.set('view', viewMode)
    }
    if (showPolicyEffect) {
      params.set('policyEffect', 'true')
    }
    const url = `${getApiBase()}/events/stream${params.toString() ? `?${params}` : ''}`

    // Mirror the fetch credentials mode: 'include' sets withCredentials so
    // cookies flow cross-origin (embedded in Radar Hub); 'omit' turns them
    // off (pure-bearer auth). Default same-origin standalone behaves as
    // before.
    const es = new EventSource(url, { withCredentials: getCredentialsMode() === 'include' })
    eventSourceRef.current = es

    es.onopen = () => {
      console.log('SSE connected')
      setConnected(true)
      // Reset backoff on successful connection
      reconnectDelayRef.current = INITIAL_RECONNECT_DELAY_MS
    }

    es.onerror = (error) => {
      console.error('SSE error:', error)
      setConnected(false)
      es.close()

      // Reconnect with exponential backoff
      const delay = reconnectDelayRef.current
      reconnectTimeoutRef.current = window.setTimeout(() => {
        console.log(`SSE reconnecting after ${delay}ms...`)
        connect()
      }, delay)
      // Increase delay for next attempt (exponential backoff with cap)
      reconnectDelayRef.current = Math.min(delay * 1.5, MAX_RECONNECT_DELAY_MS)
    }

    // Handle topology updates with dynamic throttling based on cluster size
    es.addEventListener('topology', (event) => {
      try {
        const data = JSON.parse(event.data) as Topology
        const now = Date.now()
        const timeSinceLastUpdate = now - lastTopologyUpdateRef.current

        // Update node count for dynamic throttle calculation
        currentNodeCountRef.current = data.nodes?.length || 0
        const throttleMs = getTopologyThrottleMs(currentNodeCountRef.current)

        // If waiting for topology after context switch, update immediately
        if (waitingForTopologyAfterSwitch.current) {
          waitingForTopologyAfterSwitch.current = false
          lastTopologyUpdateRef.current = now
          setTopology(data)
          optionsRef.current?.onContextSwitchComplete?.()
          return
        }

        // Throttle updates: if we updated recently, queue this update
        if (timeSinceLastUpdate < throttleMs) {
          pendingTopologyRef.current = data

          // Schedule update for when throttle period ends (if not already scheduled)
          if (!throttleTimeoutRef.current) {
            const delay = throttleMs - timeSinceLastUpdate
            throttleTimeoutRef.current = window.setTimeout(() => {
              throttleTimeoutRef.current = null
              if (pendingTopologyRef.current) {
                lastTopologyUpdateRef.current = Date.now()
                currentNodeCountRef.current = pendingTopologyRef.current.nodes?.length || 0
                setTopology(pendingTopologyRef.current)
                pendingTopologyRef.current = null
              }
            }, delay)
          }
        } else {
          // Enough time has passed, update immediately
          lastTopologyUpdateRef.current = now
          pendingTopologyRef.current = null
          setTopology(data)
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
        optionsRef.current?.onK8sEvent?.(data)
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
        optionsRef.current?.onContextSwitchProgress?.(data.message)
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
        optionsRef.current?.onContextChanged?.(data.context)
      } catch (e) {
        console.error('Failed to parse context_changed event:', e)
      }
    })

    // Handle deferred informer sync completion — refetch dashboard data
    es.addEventListener('deferred_ready', () => {
      optionsRef.current?.onDeferredReady?.()
    })

    // Handle connection state events (for graceful startup)
    es.addEventListener('connection_state', (event) => {
      try {
        const data = JSON.parse(event.data) as ConnectionState
        optionsRef.current?.onConnectionStateChange?.(data)
      } catch (e) {
        console.error('Failed to parse connection_state event:', e)
      }
    })
  }, [sseNamespaceFilter, viewMode, showPolicyEffect])

  // Reconnect function for manual reconnection
  const reconnect = useCallback(() => {
    connect()
  }, [connect])

  // Connect on mount and when namespaces/viewMode changes
  useEffect(() => {
    connect()

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close()
      }
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
      }
      if (throttleTimeoutRef.current) {
        clearTimeout(throttleTimeoutRef.current)
      }
    }
  }, [connect])

  // Clear events when namespaces change
  useEffect(() => {
    setEvents([])
  }, [namespacesKey])

  return {
    topology,
    events,
    connected,
    reconnect,
  }
}
