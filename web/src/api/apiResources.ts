import { useQuery } from '@tanstack/react-query'
import type { APIResource } from '../types'
import { apiUrl, getAuthHeaders, getCredentialsMode } from './config'

// Re-export pure functions from package
export { categorizeResources, CORE_RESOURCES, formatGroupName, shortenGroupName, getKindLabel, getKindPlural } from '@skyhook-io/k8s-ui'
export type { ResourceCategory } from '@skyhook-io/k8s-ui'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(apiUrl(path), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// Fetch all API resources from the cluster
export function useAPIResources() {
  return useQuery<APIResource[]>({
    queryKey: ['api-resources'],
    queryFn: () => fetchJSON('/api-resources'),
    staleTime: 5 * 60 * 1000, // 5 minutes - resources don't change often
  })
}
