import { useQuery } from '@tanstack/react-query'
import type { SealedSecretsController } from '@skyhook-io/k8s-ui/components/resources/renderers/SealedSecretRenderer'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// Re-ask while Radar's Secret cache is still syncing, so the keys appear on
// their own instead of waiting out the stale time.
export function sealedSecretsRefetchInterval(data: SealedSecretsController | undefined): number | false {
  return data?.keysState === 'loading' ? 3000 : false
}

// The sealed-secrets controller, its sealing keys (names and dates only) and
// the SealedSecret totals the caller can list. Disabled on an older Radar.
export function useSealedSecretsController() {
  const { support, guard, gatedKey } = useRadarFeature('sealedSecretsController')
  const query = useQuery({
    queryKey: ['sealedsecrets', 'controller', ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<SealedSecretsController>('/sealedsecrets/controller', signal)),
    enabled: support !== 'unsupported',
    refetchInterval: (q) => sealedSecretsRefetchInterval(q.state.data),
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
