import { createContext, useContext, useState, useCallback, useEffect, useRef, ReactNode } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type { ContextInfo } from '../types'

const API_BASE = '/api'

export type ConnectionStateType = 'connected' | 'disconnected' | 'connecting'

export interface ConnectionState {
  state: ConnectionStateType
  context: string
  clusterName?: string
  error?: string
  errorType?: string // auth, network, timeout, unknown
  progressMessage?: string
}

interface ConnectionStatusResponse extends ConnectionState {
  contexts: ContextInfo[]
}

interface ConnectionContextValue {
  connection: ConnectionState
  contexts: ContextInfo[]
  retry: () => void
  isRetrying: boolean
  updateFromSSE: (status: ConnectionState) => void
}

const ConnectionContext = createContext<ConnectionContextValue | null>(null)

async function fetchConnectionStatus(): Promise<ConnectionStatusResponse> {
  const response = await fetch(`${API_BASE}/connection`)
  if (!response.ok) {
    throw new Error('Failed to fetch connection status')
  }
  return response.json()
}

async function retryConnection(): Promise<ConnectionState> {
  const response = await fetch(`${API_BASE}/connection/retry`, { method: 'POST' })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
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
  // Track if SSE has started delivering connection_state events
  // Once SSE is active, it becomes the authoritative source for connection state
  const sseActiveRef = useRef(false)

  // Fetch initial connection status
  // Poll while connecting to get progress updates (SSE not established yet)
  const { data } = useQuery<ConnectionStatusResponse>({
    queryKey: ['connection-status'],
    queryFn: fetchConnectionStatus,
    staleTime: 500, // Allow frequent refetches while connecting
    refetchInterval: connection.state === 'connecting' ? 500 : false, // Poll every 500ms while connecting
    refetchOnWindowFocus: false,
  })

  // Update state from query result
  // Once SSE is active, only update contexts from poll (SSE handles connection state)
  useEffect(() => {
    if (data) {
      // Always update contexts from poll data
      setContexts(data.contexts || [])
      // Only update connection state from poll if SSE hasn't taken over
      if (!sseActiveRef.current) {
        setConnection({
          state: data.state,
          context: data.context,
          clusterName: data.clusterName,
          error: data.error,
          errorType: data.errorType,
          progressMessage: data.progressMessage,
        })
      }
    }
  }, [data])

  // Retry mutation
  const retryMutation = useMutation({
    mutationFn: retryConnection,
    onMutate: () => {
      // Reset SSE active flag - polling can provide state until SSE reconnects
      sseActiveRef.current = false
      // Set connecting state while retrying
      setConnection(prev => ({
        ...prev,
        state: 'connecting',
        error: undefined,
        errorType: undefined,
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
      setConnection(prev => ({
        ...prev,
        state: 'disconnected',
        error: error.message,
        progressMessage: undefined,
      }))
    },
  })

  const retry = useCallback(() => {
    retryMutation.mutate()
  }, [retryMutation])

  // Handler for SSE connection_state events
  const updateFromSSE = useCallback((status: ConnectionState) => {
    // Mark SSE as active - it's now the authoritative source for connection state
    sseActiveRef.current = true
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

    // If we just connected, invalidate queries to fetch fresh data
    if (status.state === 'connected') {
      queryClient.invalidateQueries()
    }
  }, [queryClient])

  const value: ConnectionContextValue = {
    connection,
    contexts,
    retry,
    isRetrying: retryMutation.isPending,
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
