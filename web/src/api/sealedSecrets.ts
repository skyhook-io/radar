import { useQuery } from '@tanstack/react-query'
import type { SealedSecretsController } from '@skyhook-io/k8s-ui/components/resources/renderers/SealedSecretRenderer'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// The sealed-secrets controller, its sealing keys (names and dates only) and
// the SealedSecret totals the caller can list. Disabled on an older Radar.
export function useSealedSecretsController(enabled: boolean) {
  const { support, guard, gatedKey } = useRadarFeature('sealedSecretsController')
  const query = useQuery({
    queryKey: ['sealedsecrets', 'controller', ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<SealedSecretsController>('/sealedsecrets/controller', signal)),
    enabled: enabled && support !== 'unsupported',
    staleTime: 30_000,
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}
