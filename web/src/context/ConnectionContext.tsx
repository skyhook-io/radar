import { createContext, useContext, useState, useCallback, useEffect, useRef, ReactNode } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type { ContextInfo } from '../types'
import { getApiBase } from '../api/config'
import { apiFetch } from '../api/client'

export type ConnectionStateType = 'connected' | 'disconnected' | 'connecting'

export interface ConnectionState {
  state: ConnectionStateType
  context: string
  clusterName?: string
  error?: string
  errorType?: string // config, auth, rbac, network, timeout, tls, unknown
  progressMessage?: string
}

interface ConnectionStatusResponse extends ConnectionState {
  contexts: ContextInfo[]
}

interface PolledConnectionStatus extends ConnectionStatusResponse {
  sseGenerationAtStart: number
}

interface ConnectionContextValue {
  connection: ConnectionState
  contexts: ContextInfo[]
  retry: () => void
  isRetrying: boolean
  updateFromSSE: (status: ConnectionState) => void
}

class ConnectionRetryError extends Error {
  errorType?: string

  constructor(message: string, errorType?: string) {
    super(message)
    this.name = 'ConnectionRetryError'
    this.errorType = errorType
  }
}

const ConnectionContext = createContext<ConnectionContextValue | null>(null)
const AUTO_RETRY_INITIAL_DELAY_MS = 10000
const AUTO_RETRY_MAX_DELAY_MS = 60000
const CONNECTION_STATUS_FALLBACK_POLL_MS = 30000

export function shouldAutoRetryConnection(errorType?: string): boolean {
  return errorType !== 'config' && errorType !== 'rbac'
}

export function shouldApplyPolledConnection(
  currentState: ConnectionStateType,
  polledState: ConnectionStateType,
  pollSSEGeneration: number,
  currentSSEGeneration: number,
): boolean {
  return pollSSEGeneration === currentSSEGeneration
    && (currentState !== 'connected' || polledState !== 'connecting')
}

async function fetchConnectionStatus(sseGenerationAtStart: number): Promise<PolledConnectionStatus> {
  // apiFetch handles a 401 globally (re-auth redirect). These endpoints are
  // no longer auth-exempt, so a session that expires while the connection-
  // error screen is parked open must route through that path rather than
  // surfacing as a misleading "cannot connect to cluster" error.
  const response = await apiFetch(`${getApiBase()}/connection`)
  if (!response.ok) {
    throw new Error('Failed to fetch connection status')
  }
  const status = await response.json() as ConnectionStatusResponse
  return { ...status, sseGenerationAtStart }
}

async function retryConnection(): Promise<ConnectionState> {
  const response = await apiFetch(`${getApiBase()}/connection/retry`, {
    method: 'POST',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' })) as { error?: string; errorType?: string }
    throw new ConnectionRetryError(error.error || `HTTP ${response.status}`, error.errorType)
  }
  return response.json()
}

export function ConnectionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [connection, setConnection] = useState<ConnectionState>({
    state: 'connecting',
    context: '',
  })
  const [contexts, setContexts] = useState<ContextInfo[]>([])
  const [isAutoRetrying, setIsAutoRetrying] = useState(false)
  // Track whether SSE has delivered connection state so retry races prefer its
  // immediate recovery signal over an older failed request.
  const sseActiveRef = useRef(false)
  const sseGenerationRef = useRef(0)
  // Track whether we've reached 'connected' at least once. Distinguishes the
  // initial connect (bootstrap queries already fetched while 'connecting') from
  // a reconnect after a drop (cache may be stale across the gap).
  const hasConnectedRef = useRef(false)
  const autoRetryInFlightRef = useRef(false)
  const autoRetryDelayRef = useRef(AUTO_RETRY_INITIAL_DELAY_MS)
  const manualRetryPendingRef = useRef(false)
  // Whether the QueryClient already held data when this provider mounted. A host
  // can share one client across cluster-scoped RadarApp mounts (see RadarApp's
  // `queryClient` prop); that client may carry another cluster's data under
  // identical keys, so a warm-at-mount cache must be fully refreshed on first
  // connect. A cold cache (standalone, or a per-cluster remount) takes the cheap
  // error-only path. Snapshot synchronously before this provider's own query
  // registers — ConnectionProvider is the outermost provider, so a fresh client
  // is genuinely empty here.
  const cacheWarmAtMountRef = useRef<boolean | null>(null)
  if (cacheWarmAtMountRef.current === null) {
    cacheWarmAtMountRef.current = queryClient.getQueryCache().getAll().length > 0
  }

  // Fetch initial connection status
  // Poll quickly while connecting and slowly otherwise so a dropped SSE state
  // frame cannot leave the UI stuck on stale connection state.
  const { data } = useQuery<PolledConnectionStatus>({
    queryKey: ['connection-status'],
    queryFn: () => fetchConnectionStatus(sseGenerationRef.current),
    staleTime: 500, // Allow frequent refetches while connecting
    refetchInterval: connection.state === 'connecting' ? 500 : CONNECTION_STATUS_FALLBACK_POLL_MS,
    refetchOnWindowFocus: false,
  })

  // Update state from query result
  useEffect(() => {
    if (data) {
      setContexts(data.contexts || [])
      setConnection(current => {
        if (!shouldApplyPolledConnection(
          current.state,
          data.state,
          data.sseGenerationAtStart,
          sseGenerationRef.current,
        )) {
          return current
        }
        return {
          state: data.state,
          context: data.context,
          clusterName: data.clusterName,
          error: data.error,
          errorType: data.errorType,
          progressMessage: data.progressMessage,
        }
      })
    }
  }, [data])

  // Retry mutation
  const retryMutation = useMutation({
    mutationFn: retryConnection,
    onMutate: () => {
      manualRetryPendingRef.current = true
      // Reset SSE active flag - polling can provide state until SSE reconnects
      sseActiveRef.current = false
      // Set connecting state while retrying
      setConnection(prev => ({
        ...prev,
        state: 'connecting',
        error: undefined,
        progressMessage: 'Connecting to cluster...',
      }))
    },
    onSuccess: (result) => {
      setConnection(result)
      // Clear all query cache to get fresh data from new connection
      queryClient.removeQueries()
      queryClient.invalidateQueries()
    },
    onError: (error: Error) => {
      const retryError = error as ConnectionRetryError
      setConnection(prev => {
        if (sseActiveRef.current && prev.state === 'connected') return prev
        return {
          ...prev,
          state: 'disconnected',
          error: error.message,
          errorType: retryError.errorType || prev.errorType,
          progressMessage: undefined,
        }
      })
    },
    onSettled: () => {
      manualRetryPendingRef.current = false
    },
  })
  useEffect(() => {
    manualRetryPendingRef.current = retryMutation.isPending
  }, [retryMutation.isPending])

  useEffect(() => {
    if (connection.state !== 'disconnected' || !shouldAutoRetryConnection(connection.errorType)) {
      autoRetryDelayRef.current = AUTO_RETRY_INITIAL_DELAY_MS
      return
    }
    let stopped = false
    let retryTimeout: number | undefined

    const scheduleRetry = () => {
      retryTimeout = window.setTimeout(() => {
        if (stopped) return
        if (manualRetryPendingRef.current || autoRetryInFlightRef.current) {
          scheduleRetry()
          return
        }

        autoRetryInFlightRef.current = true
        setIsAutoRetrying(true)
        let recovered = false
        retryConnection()
          .then((result) => {
            if (stopped) return
            recovered = true
            autoRetryDelayRef.current = AUTO_RETRY_INITIAL_DELAY_MS
            sseActiveRef.current = false
            setConnection(result)
            queryClient.removeQueries()
            queryClient.invalidateQueries()
          })
          .catch((error: Error) => {
            if (stopped) return
            const retryError = error as ConnectionRetryError
            setConnection(prev => {
              if (sseActiveRef.current && prev.state === 'connected') return prev
              return {
                ...prev,
                state: 'disconnected',
                error: error.message || prev.error,
                errorType: retryError.errorType || prev.errorType,
                progressMessage: undefined,
              }
            })
            autoRetryDelayRef.current = Math.min(autoRetryDelayRef.current * 2, AUTO_RETRY_MAX_DELAY_MS)
            // Keep the visible disconnected state until a retry succeeds.
          })
          .finally(() => {
            autoRetryInFlightRef.current = false
            setIsAutoRetrying(false)
            if (!stopped && !recovered) {
              scheduleRetry()
            }
          })
      }, autoRetryDelayRef.current)
    }

    scheduleRetry()

    return () => {
      stopped = true
      if (retryTimeout !== undefined) {
        window.clearTimeout(retryTimeout)
      }
    }
  }, [connection.errorType, connection.state, queryClient])

  const retry = useCallback(() => {
    if (retryMutation.isPending || autoRetryInFlightRef.current) return
    manualRetryPendingRef.current = true
    retryMutation.mutate()
  }, [retryMutation])

  // Handler for SSE connection_state events
  const updateFromSSE = useCallback((status: ConnectionState) => {
    // Mark SSE as active - it's now the authoritative source for connection state
    sseActiveRef.current = true
    sseGenerationRef.current += 1
    setConnection(prev => {
      // Don't transition back to 'connecting' from 'connected'. This happens when the
      // pod restarts and the SSE reconnects while the new pod's K8s cache is still
      // syncing. Hiding the main content here causes a flash — keep the 'connected'
      // state and wait for either 'connected' (sync done) or 'disconnected' (failure).
      if (prev.state === 'connected' && status.state === 'connecting') {
        return prev
      }
      return status
    })

    if (status.state === 'connected') {
      const firstConnect = !hasConnectedRef.current
      hasConnectedRef.current = true
      // A reconnect after a drop (cache stale across the gap), or a first connect
      // onto a client that already carried data at mount (shared across clusters),
      // refreshes the whole cache. A clean first connect only needs to recover the
      // bootstrap queries that 503'd while the cluster was still 'connecting'
      // (status === 'error'); the rest already fetched fresh during 'connecting',
      // so re-fetching the whole cache there would double-load every endpoint.
      if (!firstConnect || cacheWarmAtMountRef.current) {
        queryClient.invalidateQueries()
      } else {
        queryClient.invalidateQueries({ predicate: (q) => q.state.status === 'error' })
      }
    }
  }, [queryClient])

  const value: ConnectionContextValue = {
    connection,
    contexts,
    retry,
    isRetrying: retryMutation.isPending || isAutoRetrying,
    updateFromSSE,
  }

  return (
    <ConnectionContext.Provider value={value}>
      {children}
    </ConnectionContext.Provider>
  )
}

export function useConnection() {
  const context = useContext(ConnectionContext)
  if (!context) {
    throw new Error('useConnection must be used within ConnectionProvider')
  }
  return context
}
