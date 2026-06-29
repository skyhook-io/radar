import { ServiceAccountRenderer as BaseServiceAccountRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceAccountRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useRBACSubject } from '../../../api/rbac'

interface ServiceAccountRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function ServiceAccountRenderer({ data, onNavigate }: ServiceAccountRendererProps) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const { data: rbacData, isLoading, error } = useRBACSubject(
    'ServiceAccount',
    namespace,
    name,
    !!name,
  )
  return (
    <BaseServiceAccountRenderer
      data={data}
      rbacData={rbacData ?? null}
      rbacLoading={isLoading}
      rbacError={error as Error | null}
      onNavigate={onNavigate}
    />
  )
}
