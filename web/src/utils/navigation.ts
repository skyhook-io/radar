import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'
import { kindToPlural } from '@skyhook-io/k8s-ui/utils/navigation'
import type { SelectedResource } from '@skyhook-io/k8s-ui/types/core'

// Re-export shared navigation utilities from @skyhook-io/k8s-ui.
export { kindToPlural, pluralToKind, refToSelectedResource, apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
export type { NavigateToResource } from '@skyhook-io/k8s-ui/utils/navigation'

/**
 * Build a /workload/:kind/:namespace/:name URL, preserving the API group as a
 * query param so the WorkloadView can resolve CRDs with colliding kind names.
 * Namespaced workloads only — WorkloadView requires a namespace. For arbitrary
 * kinds (including cluster-scoped) use resourcePath.
 */
export function buildWorkloadPath(resource: SelectedResource): string {
  const kind = encodeURIComponent(resource.kind)
  const namespace = encodeURIComponent(resource.namespace)
  const name = encodeURIComponent(resource.name)
  const base = `/workload/${kind}/${namespace}/${name}`
  return resource.group ? `${base}?apiGroup=${encodeURIComponent(resource.group)}` : base
}

/**
 * Build a /resources/:plural?resource=:namespace/:name URL — the deep link that
 * opens a resource's detail drawer in the resources view. Cluster-scoped
 * resources use ?resource=:name (no slash); the API group rides in ?apiGroup=
 * to disambiguate CRD/core kind collisions. This is the exact form the
 * ResourcesView mount effect parses (the `?resource=` reader in
 * packages/k8s-ui/src/components/resources/ResourcesView.tsx) — keep the two in
 * lockstep.
 *
 * Unlike buildWorkloadPath, this opens the detail drawer for ANY kind,
 * including cluster-scoped resources. Returns a basename-relative path;
 * embedders (Radar Hub) prepend their cluster prefix (e.g. /c/:id).
 */
export function resourcePath(resource: SelectedResource): string {
  const params = new URLSearchParams()
  // No name → nothing to open; the kind list is the sane fallback.
  if (resource.name) {
    params.set('resource', resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name)
  }
  if (resource.group) params.set('apiGroup', resource.group)
  const query = params.toString()
  return `/resources/${kindToPlural(resource.kind)}${query ? `?${query}` : ''}`
}

// radar-specific: open URL in system browser (desktop app support)
export function openExternal(url: string): void {
  fetch(apiUrl('/desktop/open-url'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ url }),
  })
    .then((res) => {
      if (!res.ok) {
        window.open(url, '_blank')
      }
    })
    .catch(() => {
      window.open(url, '_blank')
    })
}
