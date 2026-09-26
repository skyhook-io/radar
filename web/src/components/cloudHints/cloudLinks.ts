import { kindToPluralWithGroup } from '../../utils/navigation'
import type { CloudAlertSubject } from './cloudHints'

// Deep links from a Radar that already knows its Radar Cloud cluster.

// Radar Cloud cluster URLs are <origin>/c/<clusterId>.
export function clusterIdFromUrl(url: string | undefined): string | undefined {
  if (!url) return undefined
  try {
    const m = new URL(url).pathname.match(/^\/c\/([A-Za-z0-9_-]{1,64})(?:\/|$)/)
    return m?.[1]
  } catch {
    return undefined
  }
}

function base64Url(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const RULE_QUERY = 'utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal&utm_content=alert-rule-link'

// Opens Radar Cloud's alert-rule form prefilled for this one issue. The issue
// rides in the fragment, so resource names never reach a server log; Radar
// Cloud validates it and opens a draft the person saves. Needs the issue's
// stable ID: without one there is nothing to pin, and no link.
export function alertRuleUrl(
  appUrl: string,
  subject: CloudAlertSubject,
  where: { clusterId?: string; context?: string },
): string | null {
  if (!subject.issueId) return null
  const payload = {
    v: 1,
    issue_id: subject.issueId,
    ...(where.clusterId ? { cluster_id: where.clusterId } : where.context ? { context: where.context } : {}),
    ...(subject.categoryRaw ? { category: subject.categoryRaw } : {}),
    kind: subject.kind,
    name: subject.name,
    ...(subject.namespace ? { namespace: subject.namespace } : {}),
  }
  return `${appUrl}/settings/organization/notifications?${RULE_QUERY}#radar-rule=${base64Url(JSON.stringify(payload))}`
}

// The same resource inside the cluster's page in Radar Cloud, where the hosted
// Investigate action sits. Opening it starts nothing: the person clicks
// Investigate there, under the org's consent and budget. The cluster URL may
// already carry a query (tracking tags), which is kept. Anything that isn't a
// Radar Cloud cluster page gets no deep link.
export function cloudResourceUrl(clusterUrl: string, subject: CloudAlertSubject): string | null {
  const clusterId = clusterIdFromUrl(clusterUrl)
  if (!clusterId) return null
  const url = new URL(clusterUrl)
  const plural = kindToPluralWithGroup(subject.kind, subject.group ?? '')
  url.pathname = `/c/${clusterId}/resources/${encodeURIComponent(plural)}`
  url.searchParams.set('resource', subject.namespace ? `${subject.namespace}/${subject.name}` : subject.name)
  if (subject.group) url.searchParams.set('apiGroup', subject.group)
  else url.searchParams.delete('apiGroup')
  return url.toString()
}
