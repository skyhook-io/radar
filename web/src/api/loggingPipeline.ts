import { useQuery } from '@tanstack/react-query'
import type { LogPipeline } from '@skyhook-io/k8s-ui/types/core'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// How logs flow through each installed shipper, for the namespaces in view.
export function useLoggingPipeline(namespaces: string[], enabled = true) {
  const { support, guard, gatedKey } = useRadarFeature('loggingPipeline')
  const ns = namespaces.join(',')
  const query = useQuery({
    queryKey: ['logging', 'pipeline', ns, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<LogPipeline>(`/logging/pipeline${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal)),
    enabled,
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
