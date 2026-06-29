import { RoleBindingRenderer as BaseRoleBindingRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/RoleBindingRenderer'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useResource } from '../../../api/client'

interface RoleBindingRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

// Reuses the generic resource endpoint to fetch the referenced Role/ClusterRole
// so the base renderer can show an inline rules preview. A dedicated /api/rbac
// "rules-only" endpoint would be tighter, but the generic one is already
// cached and respects per-user RBAC the same way.
export function RoleBindingRenderer({ data, onNavigate }: RoleBindingRendererProps) {
  const roleRef = data?.roleRef ?? {}
  const isClusterRole = roleRef.kind === 'ClusterRole'
  const kind = isClusterRole ? 'clusterroles' : 'roles'
  // For ClusterRole the namespace is cluster-scoped (empty); for Role the
  // binding's namespace is the role's namespace (Roles are namespaced and
  // RoleBindings can only reference Roles in their own namespace).
  const namespace = isClusterRole ? '' : (data?.metadata?.namespace ?? '')
  const name = roleRef.name ?? ''

  const { data: role, isLoading, error } = useResource<any>(kind, namespace, name)
  // `role` is undefined while loading, then the resource, then potentially
  // null on 404 / 403. Pass [] for "loaded but no rules" so the base
  // renderer's `rules === null` branch fires only on outright fetch failure
  // (orphan or forbidden); `roleRulesError` then disambiguates the two.
  const rules =
    isLoading
      ? undefined
      : role
        ? (role.rules ?? [])
        : null

  return (
    <BaseRoleBindingRenderer
      data={data}
      onNavigate={onNavigate}
      roleRules={rules ?? null}
      roleRulesLoading={isLoading}
      roleRulesError={error}
    />
  )
}
