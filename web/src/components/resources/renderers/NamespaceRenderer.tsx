import { NamespaceRenderer as BaseNamespaceRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/NamespaceRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useRBACNamespace } from '../../../api/rbac'
import { useNamespaceQuotas } from '../../../api/quotas'

interface NamespaceRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function NamespaceRenderer({ data, onNavigate }: NamespaceRendererProps) {
  const name = data?.metadata?.name ?? ''
  const { data: rbacData, isLoading, error } = useRBACNamespace(name, !!name)
  // Quota fetch is best-effort: on error/forbidden we pass undefined so the
  // section stays hidden rather than showing a scary failure for a namespace
  // that simply has no quotas (the common case).
  const { data: quotaData } = useNamespaceQuotas(name, !!name)
  return (
    <BaseNamespaceRenderer
      data={data}
      rbacData={rbacData ?? null}
      rbacLoading={isLoading}
      rbacError={error as Error | null}
      quotaData={quotaData}
      onNavigate={onNavigate}
    />
  )
}
