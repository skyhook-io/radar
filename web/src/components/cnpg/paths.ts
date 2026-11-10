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

/** The Cluster page's tabs, in order: the task tabs, then the record (activity, logs, configuration, YAML). */
export const CNPG_CLUSTER_TAB_ORDER = ['overview', 'replication', 'storage', 'performance', 'backups', 'activity', 'logs', 'spec', 'yaml']

/** The tab that explains a health dimension: Serving and Replication in Replication, Storage in Storage, Backups in Backups. */
export function cnpgDimensionTab(id: CNPGDimension['id']): string {
  switch (id) {
    case 'protection':
      return 'backups'
    case 'storage':
      return 'storage'
    default:
      return 'replication'
  }
}

const TAB_LABEL: Record<string, string> = { replication: 'Replication', storage: 'Storage', backups: 'Backups' }

/** The name of the tab cnpgDimensionTab opens, for a link that leads there. */
export function cnpgDimensionTabLabel(id: CNPGDimension['id']): string {
  return TAB_LABEL[cnpgDimensionTab(id)]
}

/** The Cluster page tab where a health dimension is explained (see cnpgDimensionTab). */
export function cnpgDimensionPath(namespace: string, name: string, ctx: string | undefined, id: CNPGDimension['id']): string {
  return cnpgClusterFullPath(namespace, name, ctx, cnpgDimensionTab(id))
}

/**
 * `target` as a tab change on the detail page already open, or null when it
 * is another page. A tab change keeps the page's other params, drops the
 * previous tab's section unless `target` names one, and is applied like a tab
 * click (replacing the history entry) so the page's return label still leads
 * where it says.
 */
export function cnpgWithinDetail(currentPathname: string, currentSearch: string, target: string): string | null {
  const [path, query = ''] = target.split('?')
  if (path !== currentPathname) return null
  const params = new URLSearchParams(currentSearch)
  for (const k of ['section', 'charts', 'instance', 'validate']) params.delete(k)
  for (const [k, v] of new URLSearchParams(query)) params.set(k, v)
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}
