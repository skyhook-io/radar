import { useQuery } from '@tanstack/react-query'
import type { SealedSecretsController } from '@skyhook-io/k8s-ui/components/resources/renderers/SealedSecretRenderer'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery, type RadarFeatureSupport } from './radarFeatures'

// Re-ask while Radar's Secret cache is still syncing, so the keys appear on
// their own instead of waiting out the stale time.
export function sealedSecretsRefetchInterval(data: SealedSecretsController | undefined): number | false {
  return data?.keysState === 'loading' || data?.controllerState === 'loading' ? 3000 : false
}

// Support is part of the key, so an answer fetched before capabilities
// loaded is asked again once support is confirmed. The namespace is too: the
// controller is the one serving the SealedSecret's namespace.
export function sealedSecretsQueryKey(support: RadarFeatureSupport, namespace: string) {
  return ['sealedsecrets', 'controller', namespace, support] as const
}

export function sealedSecretsControllerPath(namespace: string) {
  return namespace ? `/sealedsecrets/controller?sealedSecretNamespace=${encodeURIComponent(namespace)}` : '/sealedsecrets/controller'
}

// The sealed-secrets controller serving `namespace`, its sealing keys (names
// and dates only) and the SealedSecret totals the caller can list. Disabled
// on an older Radar.
export function useSealedSecretsController(namespace: string) {
  const { support, guard } = useRadarFeature('sealedSecretsController')
  const query = useQuery({
    queryKey: sealedSecretsQueryKey(support, namespace),
    queryFn: ({ signal }) => guard(() => fetchJSON<SealedSecretsController>(sealedSecretsControllerPath(namespace), signal)),
    enabled: support !== 'unsupported',
    refetchInterval: (q) => sealedSecretsRefetchInterval(q.state.data),
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
