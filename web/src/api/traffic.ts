import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type { TrafficSourcesResponse, TrafficFlowsResponse } from '../types'

const API_BASE = '/api'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// Connection info returned by connect endpoint
export interface TrafficConnectionInfo {
  connected: boolean
  localPort?: number
  address?: string
  namespace?: string
  serviceName?: string
  contextName?: string
  error?: string
}

// Get available traffic sources and recommendations
export function useTrafficSources() {
  return useQuery<TrafficSourcesResponse>({
    queryKey: ['traffic-sources'],
    queryFn: () => fetchJSON('/traffic/sources'),
    staleTime: 30000, // 30 seconds
    retry: 1,
  })
}

// Get traffic flows
export interface UseTrafficFlowsOptions {
  namespace?: string
  since?: string // Duration like "5m", "1h"
  enabled?: boolean
}

export function useTrafficFlows(options: UseTrafficFlowsOptions = {}) {
  const { namespace, since, enabled = true } = options

  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  if (since) params.set('since', since)
  const queryString = params.toString()

  return useQuery<TrafficFlowsResponse>({
    queryKey: ['traffic-flows', namespace, since],
    queryFn: () => fetchJSON(`/traffic/flows${queryString ? `?${queryString}` : ''}`),
    staleTime: 5000, // 5 seconds
    enabled,
    retry: 1,
  })
}

// Get active traffic source
export function useActiveTrafficSource() {
  return useQuery<{ active: string }>({
    queryKey: ['traffic-source-active'],
    queryFn: () => fetchJSON('/traffic/source'),
    staleTime: 60000, // 1 minute
  })
}

// Set active traffic source
export function useSetTrafficSource() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (source: string) => {
      const response = await fetch(`${API_BASE}/traffic/source`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source }),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    meta: {
      errorMessage: 'Failed to change traffic source',
      successMessage: 'Traffic source changed',
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['traffic-source-active'] })
      queryClient.invalidateQueries({ queryKey: ['traffic-flows'] })
    },
  })
}

// Refetch traffic sources (for polling during wizard)
export function useRefetchTrafficSources() {
  const queryClient = useQueryClient()
  return () => queryClient.invalidateQueries({ queryKey: ['traffic-sources'] })
}

// Get traffic connection status
export function useTrafficConnectionStatus() {
  return useQuery<TrafficConnectionInfo>({
    queryKey: ['traffic-connection'],
    queryFn: () => fetchJSON('/traffic/connection'),
    staleTime: 5000, // 5 seconds
  })
}

// Connect to traffic source (starts port-forward if needed)
export function useTrafficConnect() {
  const queryClient = useQueryClient()

  return useMutation<TrafficConnectionInfo, Error>({
    mutationFn: async () => {
      const response = await fetch(`${API_BASE}/traffic/connect`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(error.error || `HTTP ${response.status}`)
      }
      return response.json()
    },
    onSuccess: () => {
      // Invalidate flows to refetch with new connection
      queryClient.invalidateQueries({ queryKey: ['traffic-flows'] })
      queryClient.invalidateQueries({ queryKey: ['traffic-connection'] })
    },
  })
}
