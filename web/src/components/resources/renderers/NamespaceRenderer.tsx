import { NamespaceRenderer as BaseNamespaceRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/NamespaceRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useRBACNamespace } from '../../../api/rbac'
import { useNamespaceLimitRanges, useNamespaceQuotas } from '../../../api/quotas'
import { isForbiddenError } from '../../../api/client'

interface NamespaceRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function NamespaceRenderer({ data, onNavigate }: NamespaceRendererProps) {
  const name = data?.metadata?.name ?? ''
  const { data: rbacData, isLoading, error } = useRBACNamespace(name, !!name)
  const { data: quotaData, error: quotaError } = useNamespaceQuotas(name, !!name)
  // 403 → the user can't see quotas; hide the section (same posture as the
  // RBAC sections). Surface other errors (500/503) so a quota-constrained
  // namespace doesn't silently render as quota-free.
  const quotaErr = quotaError && !isForbiddenError(quotaError) ? (quotaError as Error) : null
  // LimitRanges keep their 403: the section states the denial rather than
  // hiding, because "no rules apply here" is the wrong thing to imply.
  const { data: limitRangeData, isLoading: limitRangeLoading, error: limitRangeError } = useNamespaceLimitRanges(name, !!name)
  return (
    <BaseNamespaceRenderer
      data={data}
      rbacData={rbacData ?? null}
      rbacLoading={isLoading}
      rbacError={error as Error | null}
      quotaData={quotaData}
      quotaError={quotaErr}
      limitRangeData={limitRangeData}
      limitRangeLoading={limitRangeLoading}
      limitRangeError={limitRangeError}
      onNavigate={onNavigate}
    />
  )
}
