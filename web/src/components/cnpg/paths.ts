import type { CNPGDimension } from '@skyhook-io/k8s-ui'
import { cnpgDetailPath } from './routes'

export function cnpgClusterFullPath(namespace: string, name: string, ctx?: string, tab?: string): string {
  return cnpgDetailPath({ plural: 'clusters', namespace, name }, ctx, tab)
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
