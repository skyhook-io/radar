import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'
import type { SelectedResource } from '@skyhook-io/k8s-ui/types/core'

// Re-export shared navigation utilities from @skyhook-io/k8s-ui.
export { kindToPlural, pluralToKind, refToSelectedResource, apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
export type { NavigateToResource } from '@skyhook-io/k8s-ui/utils/navigation'

/**
 * Build a /workload/:kind/:namespace/:name URL, preserving the API group as a
 * query param so the WorkloadView can resolve CRDs with colliding kind names.
 */
export function buildWorkloadPath(resource: SelectedResource): string {
  const kind = encodeURIComponent(resource.kind)
  const namespace = encodeURIComponent(resource.namespace)
  const name = encodeURIComponent(resource.name)
  const base = `/workload/${kind}/${namespace}/${name}`
  return resource.group ? `${base}?apiGroup=${encodeURIComponent(resource.group)}` : base
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
