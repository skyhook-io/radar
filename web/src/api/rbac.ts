import { useQuery, useQueryClient } from '@tanstack/react-query'
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

function retryRBACQuery(failureCount: number, error: unknown): boolean {
  return !isRBACCacheUnavailable(error) && shouldRetryRadarQuery(failureCount, error)
}

// Pauses one RBAC query, and only while its stored answer is the settled
// "can't read RBAC" 503; any other error still refetches when the drawer opens.
function useRBACAnswerSettled(queryKey: readonly unknown[]): boolean {
  return isRBACCacheUnavailable(useQueryClient().getQueryState(queryKey)?.error)
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
  const queryKey = ['rbac', 'subject', kind, namespace, name]
  const settled = useRBACAnswerSettled(queryKey)
  return useQuery<RBACSubjectResponse>({
    queryKey,
    retry: retryRBACQuery,
    queryFn: () => fetchJSON<RBACSubjectResponse>(path),
    enabled: enabled && !settled && !!name && (kind !== 'ServiceAccount' || !!namespace),
    staleTime: 15000,
  })
}

// /api/rbac/role/{kind}/{namespace}/{name}  (use "_" for ClusterRole's empty namespace)
export function useRBACRole(kind: 'Role' | 'ClusterRole', namespace: string, name: string, enabled = true) {
  const nsSegment = kind === 'ClusterRole' ? '_' : encodeURIComponent(namespace)
  const queryKey = ['rbac', 'role', kind, namespace, name]
  const settled = useRBACAnswerSettled(queryKey)
  return useQuery<RBACRoleResponse>({
    queryKey,
    retry: retryRBACQuery,
    queryFn: () => fetchJSON<RBACRoleResponse>(`/rbac/role/${kind}/${nsSegment}/${encodeURIComponent(name)}`),
    enabled: enabled && !settled && !!name && (kind !== 'Role' || !!namespace),
    staleTime: 15000,
  })
}

// /api/rbac/namespace/{namespace}
export function useRBACNamespace(namespace: string, enabled = true) {
  const queryKey = ['rbac', 'namespace', namespace]
  const settled = useRBACAnswerSettled(queryKey)
  return useQuery<RBACNamespaceResponse>({
    queryKey,
    retry: retryRBACQuery,
    queryFn: () => fetchJSON<RBACNamespaceResponse>(`/rbac/namespace/${encodeURIComponent(namespace)}`),
    enabled: enabled && !settled && !!namespace,
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
