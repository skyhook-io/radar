import { SecretProviderClassRenderer as BaseSecretProviderClassRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/SecretsStoreRenderers'
import {
  SECRETS_STORE_GROUP, SECRET_PROVIDER_CLASS_POD_STATUSES,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-secrets-store'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useResources } from '../../../api/client'
import { toSecretsStoreRead } from '../../../api/secretsStore'

/**
 * Host wrapper adding the reverse lookup the package renderer cannot do: which
 * Pods in this namespace mount the class, and what the driver recorded for each.
 */
export function SecretProviderClassRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const namespace = data?.metadata?.namespace ?? ''
  const pods = useResources<any>('pods', namespace, undefined, { enabled: !!namespace })
  const statuses = useResources<any>(SECRET_PROVIDER_CLASS_POD_STATUSES, namespace, SECRETS_STORE_GROUP, { enabled: !!namespace })
  return (
    <BaseSecretProviderClassRenderer
      data={data}
      pods={toSecretsStoreRead({ data: pods.data, coverage: pods.coverage, error: pods.error, isLoading: pods.isLoading })}
      statuses={toSecretsStoreRead({ data: statuses.data, coverage: statuses.coverage, error: statuses.error, isLoading: statuses.isLoading })}
      onNavigate={onNavigate}
    />
  )
}
