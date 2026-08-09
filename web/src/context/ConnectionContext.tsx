import { createContext, useContext, useState, useCallback, useEffect, useRef, ReactNode } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getApiBase } from '../api/config'
import { apiFetch } from '../api/client'

export type ConnectionStateType = 'connected' | 'disconnected' | 'connecting'

export interface ConnectionState {
  state: ConnectionStateType
  context: string
  clusterName?: string
  error?: string
  errorType?: string // config, auth, auth-rejected, auth-plugin-stuck, rbac, network, timeout, tls, unknown
  progressMessage?: string
}

interface ConnectionStatusResponse extends ConnectionState {
  // Server-side auth recovery owns the episode: suppress browser auto-retry
  // even when errorType has flipped to a non-auth value.
  authRecoveryOwed?: boolean
}

interface PolledConnectionStatus extends ConnectionStatusResponse {
  sseGenerationAtStart: number
}

interface ConnectionContextValue {
  connection: ConnectionState
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
  // Auth-shaped failures are excluded: the server runs its own backoff
  // reconnect loop for those, and each browser retry would invoke the exec
  // credential plugin again. Manual "Retry Connection" stays available for
  // an immediate check.
  return errorType !== 'config' && errorType !== 'rbac' && !errorType?.startsWith('auth')
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
  //
  // ?contexts=0: context enumeration re-reads kubeconfig files under the
  // client write lock on the server — too costly for a perpetual poll, and
  // nothing here consumes the list (ContextSwitcher has its own query).
  const response = await apiFetch(`${getApiBase()}/connection?contexts=0`)
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
  // Fed by the status poll only (SSE frames don't carry the field, and must
  // not clear it): while the server's recovery loop owns the episode, browser
  // auto-retry stands down even if errorType flips to a non-auth value.
  const serverOwnsRecoveryRef = useRef(false)
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

  // Mirror for the poll-apply effect: it needs the current state to detect a
  // disconnected→connected transition, which a functional updater can't
  // surface without side effects inside the updater. Deliberately written
  // during render (not in an effect): when an SSE frame and a poll land in the
  // same commit, the poll effect must already see the SSE-updated state or it
  // would double-run the connected cache refresh.
  const connectionRef = useRef(connection)
  connectionRef.current = connection

  // Cache refresh shared by every path that lands on 'connected'.
  const lastCacheRefreshAtRef = useRef(0)
  const refreshCachesOnConnect = useCallback(() => {
    const firstConnect = !hasConnectedRef.current
    hasConnectedRef.current = true
    // Poll and SSE can both observe the same reconnect within moments of each
    // other (SSE always writes a connected frame on stream open); refreshing
    // twice cancels and refires every in-flight bootstrap fetch.
    if (Date.now() - lastCacheRefreshAtRef.current < 1500) {
      return
    }
    lastCacheRefreshAtRef.current = Date.now()
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
  }, [queryClient])

  // Poll quickly while connecting and slowly otherwise so a dropped SSE state
  // frame cannot leave the UI stuck on stale connection state. Runs in hidden
  // tabs and catches up on focus — a parked tab is exactly where a dropped SSE
  // frame otherwise strands stale state.
  const { data, dataUpdatedAt, error: pollError } = useQuery<PolledConnectionStatus>({
    queryKey: ['connection-status'],
    queryFn: () => fetchConnectionStatus(sseGenerationRef.current),
    staleTime: 500, // Allow frequent refetches while connecting
    refetchInterval: connection.state === 'connecting' ? 500 : CONNECTION_STATUS_FALLBACK_POLL_MS,
    refetchIntervalInBackground: true,
    refetchOnWindowFocus: true,
  })

  // The poll is the safety net for dropped SSE frames — if it starts failing
  // the UI may freeze on stale state, so leave a trace.
  useEffect(() => {
    if (pollError) {
      console.warn('[connection] status poll failed:', pollError)
    }
  }, [pollError])

  // Update state from query result. dataUpdatedAt is a deliberate dep:
  // structural sharing keeps `data`'s identity stable across byte-identical
  // polls, so without it a result dropped by the retry-in-flight guard below
  // would never be re-applied — a stuck error screen when SSE is down.
  useEffect(() => {
    if (!data) return
    if (data.authRecoveryOwed !== undefined) {
      serverOwnsRecoveryRef.current = data.authRecoveryOwed
    }
    // A poll resolving mid-retry would flip the UI back to the error screen
    // while the retry is still running; the retry's own result supersedes it.
    if (manualRetryPendingRef.current || autoRetryInFlightRef.current) return
    const current = connectionRef.current
    if (!shouldApplyPolledConnection(
      current.state,
      data.state,
      data.sseGenerationAtStart,
      sseGenerationRef.current,
    )) {
      return
    }
    const becameConnected = current.state !== 'connected' && data.state === 'connected'
    setConnection({
      state: data.state,
      context: data.context,
      clusterName: data.clusterName,
      error: data.error,
      errorType: data.errorType,
      progressMessage: data.progressMessage,
    })
    if (becameConnected) {
      refreshCachesOnConnect()
    }
  }, [data, dataUpdatedAt, refreshCachesOnConnect])

  // Retry mutation
  const retryMutation = useMutation({
    mutationFn: retryConnection,
    onMutate: () => {
      manualRetryPendingRef.current = true
      // Until SSE re-delivers state, a failed retry may report its own error
      // rather than deferring to a stale "connected"
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
      if (result.state === 'connected') {
        // Keep the first-connect bookkeeping and the double-refresh window
        // honest — the SSE frame that follows must not re-invalidate.
        hasConnectedRef.current = true
        lastCacheRefreshAtRef.current = Date.now()
      }
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
      // A poll that resolved mid-retry was deliberately dropped; refetch now
      // rather than leaving the UI on the retry's outcome until the next
      // 30s tick (a failed retry against a healthy server would otherwise
      // park the error screen).
      queryClient.invalidateQueries({ queryKey: ['connection-status'] })
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
        // Checked at fire time (not arm time): the poll may have learned
        // mid-wait that the server's recovery loop owns this episode.
        if (serverOwnsRecoveryRef.current) {
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
            if (result.state === 'connected') {
              hasConnectedRef.current = true
              lastCacheRefreshAtRef.current = Date.now()
            }
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
            // Mirror the manual-retry onSettled: re-poll so a status update
            // dropped by the mid-retry guard isn't stranded until next tick.
            queryClient.invalidateQueries({ queryKey: ['connection-status'] })
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
    // Feed the retry-race guards: a live SSE stream means retry failures must
    // not clobber a recovery it already delivered. The generation bump lets
    // in-flight polls detect they raced this frame.
    sseActiveRef.current = true
    sseGenerationRef.current += 1
    setConnection(prev => {
      // Don't transition back to 'connecting' from 'connected'. This happens when the
      // pod restarts and the SSE reconnects while the new pod's K8s cache is still
      // syncing. Hiding the main content here causes a flash — keep the 'connected'
      // state and wait for either 'connected' (sync done) or 'disconnected' (failure).
      // (shouldApplyPolledConnection encodes the same suppression for the poll path.)
      if (prev.state === 'connected' && status.state === 'connecting') {
        return prev
      }
      return status
    })

    if (status.state === 'connected') {
      refreshCachesOnConnect()
    }
  }, [refreshCachesOnConnect])

  const value: ConnectionContextValue = {
    connection,
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
