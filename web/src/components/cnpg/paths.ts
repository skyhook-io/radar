import type { CNPGDimension } from '@skyhook-io/k8s-ui'
import { cnpgDetailPath } from './routes'

export function cnpgClusterFullPath(namespace: string, name: string, ctx?: string, tab?: string): string {
  return cnpgDetailPath({ plural: 'clusters', namespace, name }, ctx, tab)
}

/** The Cluster's page with its problems list open (`problems=all`). */
export function cnpgClusterProblemsPath(namespace: string, name: string, ctx?: string): string {
  const path = cnpgClusterFullPath(namespace, name, ctx)
  return `${path}${path.includes('?') ? '&' : '?'}problems=all`
}

/**
 * The label for "← back" on the page a push lands on: the title of the page
 * being left, which Radar keeps in the document title.
 */
export function currentPageLabel(): string {
  return document.title.replace(/\s*·\s*Radar$/, '') || 'previous page'
}

/**
 * Where a Cluster's health dimension is explained: Serving and Replication in
 * Runtime's Replication view (instances and their roles), Storage in Runtime's
 * Storage & WAL, Protection in the Protection tab. Tab and view are the URL
 * params the detail page and the Runtime tab read, so Back returns.
 */
export function cnpgDimensionPath(namespace: string, name: string, ctx: string | undefined, id: CNPGDimension['id']): string {
  switch (id) {
    case 'protection':
      return cnpgClusterFullPath(namespace, name, ctx, 'protection')
    case 'storage':
      return `${cnpgClusterFullPath(namespace, name, ctx, 'runtime')}&section=storage`
    default:
      return cnpgClusterFullPath(namespace, name, ctx, 'runtime')
  }
}

/**
 * `target` as a tab change on the detail page already open, or null when it
 * is another page. A tab change keeps the page's other params, drops the
 * previous Runtime view unless `target` names one, and is applied like a tab
 * click (replacing the history entry) so the page's return label still leads
 * where it says.
 */
export function cnpgWithinDetail(currentPathname: string, currentSearch: string, target: string): string | null {
  const [path, query = ''] = target.split('?')
  if (path !== currentPathname) return null
  const params = new URLSearchParams(currentSearch)
  params.delete('section')
  params.delete('validate')
  for (const [k, v] of new URLSearchParams(query)) params.set(k, v)
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

/**
 * Radar's Issues page narrowed to one subject (it has no link to a single issue).
 * The subject travels as `resource=ns/name`, never `namespace=`: App reads a bare
 * `namespace` as the view filter, and a URL without `namespaces` clears it.
 */
export function cnpgIssuesPath(subject: { kind: string; namespace: string; name: string }, viewNamespaces?: string | null): string {
  const params = new URLSearchParams()
  if (viewNamespaces) params.set('namespaces', viewNamespaces)
  params.set('kind', subject.kind)
  params.set('resource', subject.namespace ? `${subject.namespace}/${subject.name}` : subject.name)
  return `/issues?${params}`
}
