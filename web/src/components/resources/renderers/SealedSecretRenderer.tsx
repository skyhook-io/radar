import { SealedSecretRenderer as BaseSealedSecretRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/SealedSecretRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useSealedSecretsController } from '../../../api/sealedSecrets'

/** Host wrapper adding the controller and sealing-key section the package renderer cannot fetch. */
export function SealedSecretRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const query = useSealedSecretsController()
  const controller = query.support === 'unsupported' ? undefined : query.data ?? (query.isError ? null : undefined)
  return <BaseSealedSecretRenderer data={data} onNavigate={onNavigate} controller={controller} />
}
