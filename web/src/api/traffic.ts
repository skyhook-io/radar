import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type { TrafficSourcesResponse, TrafficFlowsResponse, TrafficRecordsResponse } from '../types'
import { fetchJSON as fetchApiJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'
import { apiUrl, getAuthHeaders, getCredentialsMode } from './config'
import { readErrorBody } from './httpErrors'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(apiUrl(path), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (!response.ok) {
    const error = await readErrorBody(response)
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

// What the flows and records queries share: the namespaces in view, the
// window, and the traffic the view hides. The hidden traffic is sent so the
// source can drop it before it counts against its limits; the view still
// filters it too.
export interface TrafficScope {
  namespaces?: string[]
  since?: string // Duration like "5m", "1h"
  excludeNamespaces?: readonly string[]
  excludeHost?: boolean
}

function trafficScopeParams({ namespaces = [], since, excludeNamespaces, excludeHost }: TrafficScope): URLSearchParams {
  const params = new URLSearchParams()
  // With several namespaces in view the server reads them from the saved pick.
  if (namespaces.length === 1) params.set('namespace', namespaces[0])
  if (since) params.set('since', since)
  if (excludeNamespaces?.length) params.set('excludeNamespaces', excludeNamespaces.join(','))
  if (excludeHost) params.set('excludeHost', 'true')
  return params
}

function trafficScopeKey({ namespaces = [], since, excludeNamespaces, excludeHost }: TrafficScope) {
  return [namespaces, since, excludeNamespaces ?? [], !!excludeHost]
}

// Get traffic flows
export interface UseTrafficFlowsOptions extends TrafficScope {
  enabled?: boolean
}

export function useTrafficFlows(options: UseTrafficFlowsOptions = {}) {
  const { enabled = true } = options
  const queryString = trafficScopeParams(options).toString()

  return useQuery<TrafficFlowsResponse>({
    queryKey: ['traffic-flows', ...trafficScopeKey(options)],
    queryFn: () => fetchJSON(`/traffic/flows${queryString ? `?${queryString}` : ''}`),
    staleTime: 5000, // 5 seconds
    enabled,
    retry: 1,
  })
}

/** An endpoint as the server's aggregation keys it. */
export interface TrafficEndpointRef {
  namespace?: string
  name: string
  kind?: string
}

/** One caller → callee edge of the server's aggregation. */
export interface TrafficEndpointPair {
  source: TrafficEndpointRef
  destination: TrafficEndpointRef
}

/** The query string carries the selection; the server refuses one over 16 KiB. */
export const MAX_TRAFFIC_MATCH_CHARS = 12_000

export interface UseTrafficRecordsOptions extends TrafficScope {
  /** The selection as raw edges, or null for no selection. */
  pairs: TrafficEndpointPair[] | null
  enabled?: boolean
}

// The newest records behind a graph selection. The flows response carries only
// a capped sample of records, so a selected edge is looked up on its own —
// otherwise a quiet edge on a busy cluster would show nothing.
export function useTrafficRecords(options: UseTrafficRecordsOptions) {
  const { pairs, enabled = true } = options
  const { guard, gatedKey, support } = useRadarFeature('trafficRecords')
  const match = pairs && pairs.length > 0 ? JSON.stringify({ pairs }) : null
  const tooLarge = match !== null && match.length > MAX_TRAFFIC_MATCH_CHARS
  const params = trafficScopeParams(options)
  if (match && !tooLarge) params.set('match', match)

  const query = useQuery<TrafficRecordsResponse>({
    queryKey: ['traffic-flows', 'records', ...trafficScopeKey(options), match, ...gatedKey],
    queryFn: () => guard(() => fetchApiJSON<TrafficRecordsResponse>(`/traffic/flows/records?${params.toString()}`)),
    staleTime: 5000,
    enabled: enabled && match !== null && !tooLarge && support !== 'unsupported',
    retry: shouldRetryRadarQuery,
  })
  return { ...query, tooLarge, supported: support !== 'unsupported' }
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
      const response = await fetch(apiUrl('/traffic/source'), {
        method: 'POST',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({ source }),
      })
      if (!response.ok) {
        const error = await readErrorBody(response)
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
      const response = await fetch(apiUrl('/traffic/connect'), {
        method: 'POST',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      })
      if (!response.ok) {
        const error = await readErrorBody(response)
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
