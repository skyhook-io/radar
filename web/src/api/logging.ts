import { useQuery } from '@tanstack/react-query'
import type { LoggingPodShipping } from '@skyhook-io/k8s-ui/types/core'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// Which Logging operator Flows ship one pod's logs. enabled: the
// logging.banzaicloud.io group is discovered.
export function useLoggingPodShipping(namespace: string, name: string, enabled: boolean) {
  const { support, guard, gatedKey } = useRadarFeature('loggingPodShipping')
  const query = useQuery({
    queryKey: ['logging-pod', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<LoggingPodShipping>(`/logging/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`, signal)),
    enabled: enabled && !!namespace && !!name && support === 'supported',
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
