import { useQuery } from '@tanstack/react-query'
import type { TraefikRoutesResponse } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// One IngressRoute's rule facts, with usedBy for its middlewares.
export function useTraefikRouteFacts(namespace: string, name: string) {
  const { support, guard, gatedKey } = useRadarFeature('traefikRoutes')
  const query = useQuery({
    queryKey: ['traefik', 'routes', 'route', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<TraefikRoutesResponse>(`/traefik/routes?namespace=${encodeURIComponent(namespace)}&name=${encodeURIComponent(name)}`, signal)),
    enabled: !!namespace && !!name && support !== 'unsupported',
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}

// The IngressRoute rules that use one Middleware (at most 200).
export function useTraefikMiddlewareUsers(namespace: string, name: string) {
  const { support, guard, gatedKey } = useRadarFeature('traefikRoutes')
  const query = useQuery({
    queryKey: ['traefik', 'routes', 'middleware', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<TraefikRoutesResponse>(`/traefik/routes?middleware=${encodeURIComponent(`${namespace}/${name}`)}`, signal)),
    enabled: !!namespace && !!name && support !== 'unsupported',
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
