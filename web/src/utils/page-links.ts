import { useLocation, useNavigate } from 'react-router-dom'

/**
 * The label for "← back" on the page a push lands on: the title of the page
 * being left, which Radar keeps in the document title.
 */
export function currentPageLabel(): string {
  return document.title.replace(/\s*·\s*Radar$/, '') || 'previous page'
}

/**
 * Radar's Issues page narrowed to all issues for one subject.
 * The subject travels as `resource=ns/name`, never `namespace=`: App reads a bare
 * `namespace` as the view filter, and a URL without `namespaces` clears it.
 */
export function issuesPathForSubject(subject: { kind: string; group?: string; namespace: string; name: string }, viewNamespaces?: string | null): string {
  const params = new URLSearchParams()
  if (viewNamespaces) params.set('namespaces', viewNamespaces)
  params.set('kind', subject.kind)
  if (subject.group) params.set('apiGroup', subject.group)
  params.set('resource', subject.namespace ? `${subject.namespace}/${subject.name}` : subject.name)
  return `/issues?${params}`
}

// A manual selection replaces the deep link so Back still returns to its origin.
export function useClearLinkedIssue() {
  const location = useLocation()
  const navigate = useNavigate()
  return () => {
    const params = new URLSearchParams(location.search)
    if (!params.has('issue')) return
    params.delete('issue')
    navigate({ pathname: location.pathname, search: params.toString(), hash: location.hash }, { replace: true, state: location.state })
  }
}

export const RIGHTSIZING_SCAN_PATH = '/cost/rightsizing'

export function isRightsizingScanPath(pathname: string): boolean {
  return pathname === RIGHTSIZING_SCAN_PATH || pathname === `${RIGHTSIZING_SCAN_PATH}/`
}
