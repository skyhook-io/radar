import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from './client'

// useNamespaceQuotas fetches a namespace's ResourceQuota objects via
// /api/resources/resourcequotas?namespace=<ns> (a bare array). Backs the
// NamespaceRenderer quota-usage section — quota saturation is otherwise
// surfaced nowhere in the UI, yet it's exactly why a namespace stops
// admitting new pods.
export function useNamespaceQuotas(namespace: string, enabled = true) {
  return useQuery<any[]>({
    queryKey: ['resourcequotas', namespace],
    queryFn: () => fetchJSON<any[]>(`/resources/resourcequotas?namespace=${encodeURIComponent(namespace)}`),
    enabled: enabled && !!namespace,
    staleTime: 15000,
  })
}
