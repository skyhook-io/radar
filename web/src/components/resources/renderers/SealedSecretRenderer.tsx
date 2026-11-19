import { SealedSecretRenderer as BaseSealedSecretRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/SealedSecretRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useSealedSecretsController } from '../../../api/sealedSecrets'
import { isRadarFeatureUnsupported } from '../../../api/radarFeatures'

/** Host wrapper adding the controller and sealing-key section the package renderer cannot fetch. */
export function SealedSecretRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const query = useSealedSecretsController(data?.metadata?.namespace ?? '')
  // An older Radar without the endpoint can answer before capabilities say
  // so; that is "no section", not a failed load.
  const unsupported = query.support === 'unsupported' || isRadarFeatureUnsupported(query.error, 'sealedSecretsController')
  const controller = unsupported ? undefined : query.data ?? (query.isError ? null : undefined)
  return <BaseSealedSecretRenderer data={data} onNavigate={onNavigate} controller={controller} />
}
