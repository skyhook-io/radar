import { useMutation, useQuery } from '@tanstack/react-query'
import type {
  PolicyResourceResponse,
  PolicyCoverageResponse,
  PolicyQueuedResponse,
  CNPGCatalogUsersResponse,
  VeleroStoredBackupsResponse,
  VeleroRunMessagesResponse,
} from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import { hasPolicyReports, useAPIResources } from './apiResources'
import { isRadarFeatureUnsupported } from './radarFeatures'

const POLICY_NOT_INSTALLED: PolicyResourceResponse = {
  evaluated: false,
  status: 'not_installed',
  liveUpdates: false,
  counts: { pass: 0, fail: 0, warn: 0, error: 0, skip: 0 },
  findings: [],
}

// /api/policy/resource/{kind}/{namespace}/{name}
//
// Policy results change when the engine rescans rather than on every render, so
// a short stale window keeps drawer navigation instant without going stale in a
// way an operator would notice.
export function usePolicyResource(kind: string, namespace: string, name: string, enabled = true) {
  const guard = useRadarFeature('policyResource')
  const { data: apiResources } = useAPIResources()
  const policyEngineAbsent = apiResources !== undefined && !hasPolicyReports(apiResources)
  return useQuery<PolicyResourceResponse>({
    queryKey: ['policy', 'resource', kind, namespace, name],
    queryFn: () =>
      guard(() =>
        fetchJSON<PolicyResourceResponse>(
          `/policy/resource/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`,
        ),
      ).catch((error: unknown) => {
        // A Radar too old for policy results, on a cluster with no policy
        // engine, gets the answer a current Radar would give: not installed.
        // Prompting an upgrade there would promise a section that stays empty.
        if (isRadarFeatureUnsupported(error) && policyEngineAbsent) return POLICY_NOT_INSTALLED
        throw error
      }),
    enabled: enabled && !!kind && !!namespace && !!name,
    staleTime: 15000,
    // A 403 is a settled answer about this identity, not a blip — retrying
    // would just repeat the denial on every drawer open.
    retry: false,
  })
}

// /api/policy/policies/{policy}
//
// The inverse lookup: every resource one policy recorded an outcome for. A
// namespaced Kyverno Policy reports as "namespace/name", so the namespace is
// passed through and the server tries both shapes.
/**
 * Whether a cached coverage result describes the same thing the caller is asking
 * about now — same policy, namespace and view — so only a changed `limit`
 * separates them. Exported for tests: the alternative is showing one policy's
 * findings on another policy's page.
 */
export function isSameCoverageSubject(
  prevKey: readonly unknown[] | undefined,
  policy: string,
  namespace: string,
  viewFilter: string,
): boolean {
  if (!prevKey || prevKey.length < 6) return false
  return prevKey[2] === policy && prevKey[3] === namespace && prevKey[5] === viewFilter
}

// `limit` raises the per-rule subject bound. It is part of the query key so
// asking for more is a separate fetch rather than a mutation of the cached one,
// and the default response stays cached for every other drawer open.
export function usePolicyCoverage(
  policy: string,
  namespace?: string,
  enabled = true,
  limit?: number,
  /**
   * Whatever identifies the caller's current namespace view. The server applies
   * that filter from session state, so it changes the response body without
   * changing the URL — it has to be in the key or the cache serves the previous
   * scope's answer under the new scope's heading.
   */
  viewFilter = '',
) {
  return useQuery<PolicyCoverageResponse>({
    queryKey: ['policy', 'coverage', policy, namespace ?? '', limit ?? 0, viewFilter],
    queryFn: () => {
      const params = new URLSearchParams()
      if (namespace) params.set('namespace', namespace)
      if (limit) params.set('limit', String(limit))
      const qs = params.toString()
      return fetchJSON<PolicyCoverageResponse>(
        `/policy/policies/${encodeURIComponent(policy)}${qs ? `?${qs}` : ''}`,
      )
    },
    enabled: enabled && !!policy,
    staleTime: 15000,
    retry: false,
    // Raising the limit is a new query key, and without this the section would
    // fall back to its full loading state — asking to see MORE resources would
    // briefly remove the ones already on screen.
    //
    // Only across a limit change, though. The drawer reuses this observer when
    // it opens a different policy, so reusing unconditionally renders one
    // policy's resources and counts under another's name, with no loading state
    // to suggest they are not its own.
    placeholderData: (prev, prevQuery) =>
      isSameCoverageSubject(prevQuery?.queryKey, policy, namespace ?? '', viewFilter)
        ? prev
        : undefined,
  })
}

// /api/policy/policies/{policy}/queued
//
// Kyverno records UpdateRequests in its own namespace whatever namespace the
// policy lives in, so this cannot be a client-side list: omitting the namespace
// on the generic resources endpoint means "apply the caller's namespace view
// filter", and a reader narrowed to their own namespace would be answered for
// the wrong scope. The server reads cluster-wide, gated on the caller's ability
// to list the kind — the same shape as the RBAC reverse lookups.
//
// No view filter in the key: the answer does not depend on one.
export function usePolicyQueued(policy: string, namespace = '', enabled = true) {
  return useQuery<PolicyQueuedResponse>({
    queryKey: ['policy', 'queued', policy, namespace],
    queryFn: () =>
      fetchJSON<PolicyQueuedResponse>(
        `/policy/policies/${encodeURIComponent(policy)}/queued${
          namespace ? `?namespace=${encodeURIComponent(namespace)}` : ''
        }`,
      ),
    enabled: enabled && !!policy,
    // These live for seconds, so a long stale window would describe a queue that
    // has already drained.
    staleTime: 5000,
    retry: false,
  })
}

// /api/cnpg/{imagecatalogs/{namespace}|clusterimagecatalogs}/{name}/clusters
//
// A ClusterImageCatalog is cluster-scoped and referenceable from any namespace,
// so the answer's scope is not the subject's. Asking the generic resources
// endpoint without a namespace inherits the caller's namespace view filter and
// would report "nothing uses this" on the strength of whichever namespaces they
// happen to be showing — an all-clear before an edit, derived from a browsing
// preference. Read server-side, gated on listing Clusters.
export function useCNPGCatalogUsers(name: string, namespace = '', enabled = true) {
  const path = namespace
    ? `/cnpg/imagecatalogs/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/clusters`
    : `/cnpg/clusterimagecatalogs/${encodeURIComponent(name)}/clusters`
  return useQuery<CNPGCatalogUsersResponse>({
    queryKey: ['cnpg', 'catalog-users', namespace, name],
    queryFn: () => fetchJSON<CNPGCatalogUsersResponse>(path),
    enabled: enabled && !!name,
    staleTime: 15000,
    retry: false,
  })
}

// /api/velero/backupstoragelocations/{namespace}/{name}/backups
//
// The inverse of the location a Backup names. Server-side and RBAC-gated for the
// same reason as the other reverse lookups: "this location holds nothing" is a
// sentence someone reads before deciding whether they can still restore, and it
// must not come from a browsing filter or a lookup that failed.
export function useVeleroStoredBackups(namespace: string, name: string, enabled = true) {
  return useQuery<VeleroStoredBackupsResponse>({
    queryKey: ['velero', 'stored-backups', namespace, name],
    queryFn: () =>
      fetchJSON<VeleroStoredBackupsResponse>(
        `/velero/backupstoragelocations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backups`,
      ),
    enabled: enabled && !!namespace && !!name,
    staleTime: 15000,
    retry: false,
  })
}

// POST /api/velero/{kind}/{namespace}/{name}/messages
//
// A mutation, like useRevealPodEnvironment — the closest thing this project
// already has: data that is deliberately not on the object, fetched only when
// someone asks for it. Reading a run's warnings makes Velero create a
// DownloadRequest and pulls an object out of storage, which is not work to do on
// every drawer open. The counts are already there to answer "is anything wrong".
//
// Mutation state is not keyed, so it is only correct while the drawer remounts
// the renderer as the selected resource changes. It does — verified by
// collapsing the cache key and watching the data still not leak across a switch.
export function useVeleroRunMessages(kind: 'backups' | 'restores', namespace: string, name: string) {
  return useMutation<VeleroRunMessagesResponse, Error, void>({
    mutationFn: () =>
      fetchJSON<VeleroRunMessagesResponse>(
        `/velero/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/messages`,
        { method: 'POST' },
      ),
    // No meta.errorMessage: the failures here are the interesting part — a
    // stopped controller, a denial, unreachable storage — and each is rendered
    // in place next to the counts rather than thrown away in a toast.
  })
}
