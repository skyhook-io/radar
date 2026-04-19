import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

// Re-export shared navigation utilities from @skyhook-io/k8s-ui.
export { kindToPlural, pluralToKind, refToSelectedResource } from '@skyhook-io/k8s-ui/utils/navigation'
export type { NavigateToResource } from '@skyhook-io/k8s-ui/utils/navigation'

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
