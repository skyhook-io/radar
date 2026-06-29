import { RoleRenderer as BaseRoleRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/RoleRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useRBACRole } from '../../../api/rbac'

interface RoleRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function RoleRenderer({ data, onNavigate }: RoleRendererProps) {
  // ClusterRole has no namespace; Role does. The kind in the manifest is
  // authoritative — RoleRenderer is shared between both kinds via the
  // dispatch, so checking it here is the only way to know which we are.
  const kind: 'Role' | 'ClusterRole' = data?.kind === 'ClusterRole' ? 'ClusterRole' : 'Role'
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const { data: rbacRoleData, isLoading, error } = useRBACRole(kind, namespace, name, !!name)
  return (
    <BaseRoleRenderer
      data={data}
      rbacRoleData={rbacRoleData ?? null}
      rbacRoleLoading={isLoading}
      rbacRoleError={error as Error | null}
      onNavigate={onNavigate}
    />
  )
}
