import { useQuery } from '@tanstack/react-query'
import type {
  RBACSubjectResponse,
  RBACRoleResponse,
  RBACWhoamiResponse,
  RBACNamespaceResponse,
} from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

// The server answers 503 "RBAC cache not available" when the identity Radar
// connects with can't read RBAC. That holds until the next connection, so a
// retry, or a refetch on every drawer open, only repeats the same answer.
// Reconnecting removes errored RBAC queries and a context switch removes all
// queries, so the next look after either asks again.
export function isRBACCacheUnavailable(error: unknown): boolean {
  const e = error as { status?: unknown; message?: unknown } | null
  return e?.status === 503 && typeof e.message === 'string' && e.message.includes('RBAC cache')
}

const settledRBACErrors = {
  retry: (failureCount: number, error: unknown) =>
    !isRBACCacheUnavailable(error) && shouldRetryRadarQuery(failureCount, error),
  retryOnMount: false,
}

// /api/rbac/subject/{kind}/{namespace}/{name}   (ServiceAccount)
// /api/rbac/subject/{kind}/{name}               (User/Group — no namespace)
export function useRBACSubject(kind: 'ServiceAccount' | 'User' | 'Group', namespace: string, name: string, enabled = true) {
  // Subject lookups depend on cluster-wide RBAC. They don't change often,
  // and operators bouncing between Pod/SA pages re-hit the same SA. Use a
  // 15s stale window so cross-page navigation is instant.
  const path =
    kind === 'ServiceAccount'
      ? `/rbac/subject/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`
      : `/rbac/subject/${kind}/${encodeURIComponent(name)}`
  return useQuery<RBACSubjectResponse>({
    queryKey: ['rbac', 'subject', kind, namespace, name],
    ...settledRBACErrors,
    queryFn: () => fetchJSON<RBACSubjectResponse>(path),
    enabled: enabled && !!name && (kind !== 'ServiceAccount' || !!namespace),
    staleTime: 15000,
  })
}

// /api/rbac/role/{kind}/{namespace}/{name}  (use "_" for ClusterRole's empty namespace)
export function useRBACRole(kind: 'Role' | 'ClusterRole', namespace: string, name: string, enabled = true) {
  const nsSegment = kind === 'ClusterRole' ? '_' : encodeURIComponent(namespace)
  return useQuery<RBACRoleResponse>({
    queryKey: ['rbac', 'role', kind, namespace, name],
    ...settledRBACErrors,
    queryFn: () => fetchJSON<RBACRoleResponse>(`/rbac/role/${kind}/${nsSegment}/${encodeURIComponent(name)}`),
    enabled: enabled && !!name && (kind !== 'Role' || !!namespace),
    staleTime: 15000,
  })
}

// /api/rbac/namespace/{namespace}
export function useRBACNamespace(namespace: string, enabled = true) {
  return useQuery<RBACNamespaceResponse>({
    queryKey: ['rbac', 'namespace', namespace],
    ...settledRBACErrors,
    queryFn: () => fetchJSON<RBACNamespaceResponse>(`/rbac/namespace/${encodeURIComponent(namespace)}`),
    enabled: enabled && !!namespace,
    staleTime: 15000,
  })
}

// /api/rbac/whoami?namespace=<ns>
export function useRBACWhoami(namespace: string, enabled = true) {
  return useQuery<RBACWhoamiResponse>({
    queryKey: ['rbac', 'whoami', namespace],
    queryFn: () => fetchJSON<RBACWhoamiResponse>(`/rbac/whoami?namespace=${encodeURIComponent(namespace)}`),
    enabled: enabled && !!namespace,
    staleTime: 30000,
  })
}
