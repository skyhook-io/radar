import { RESOURCE_LIST_FILTER_PARAMS } from '../components/resources/resource-utils'
import { RESOURCE_LIST_PATH } from './navigation'
import { createTabStorage } from './tabStorage'

// The params that shape each section's list. Anything else (the global
// picker's params, a drawer, a detail opened on top of the list, a one-off
// modal) belongs to one visit, so a param a view adds later is forgotten until
// it is listed here. A section that isn't listed returns to its bare path.
const REMEMBERED: Record<string, readonly string[]> = {
  resources: ['apiGroup', ...RESOURCE_LIST_FILTER_PARAMS],
  topology: ['mode', 'group'],
  timeline: ['view', 'window', 'from', 'to', 'activity', 'kinds', 'deleted', 'routine', 'pinnedOnly', 'q', 'grouping', 'sort', 'app', 'scopeNamespaces', 'time'],
  gitops: ['q', 'sync', 'health', 'project', 'automation', 'labels', 'lifecycle', 'kind'],
  applications: ['q', 'health', 'class', 'type', 'env', 'source', 'system'],
  checks: ['q', 'severity', 'category', 'framework'],
  cnpg: ['q', 'cat', 'filter'],
}

function listSearch(view: string, search: string): string {
  const remembered = REMEMBERED[view] ?? []
  const params = new URLSearchParams(search)
  for (const key of [...params.keys()]) {
    if (!remembered.includes(key)) params.delete(key)
  }
  const query = params.toString()
  return query ? `?${query}` : ''
}

/**
 * The tab's last list URL per section, per cluster. The URL stays the source
 * of truth: entries are recorded from it and restored by navigating back to
 * them. Kept per tab (see createTabStorage), never shared with other tabs.
 */
export function createViewMemory(cluster: string) {
  const sections = createTabStorage()
  const sectionKey = (view: string) => `radar-section:${cluster}:${view}`

  return {
    /** Records a location. Only list routes are remembered, not detail pages. */
    record(view: string, pathname: string, search: string) {
      const path = pathname.replace(/\/+$/, '') || '/'
      const query = listSearch(view, search)
      const resourceList = view === 'resources' && RESOURCE_LIST_PATH.test(path)
      if (!resourceList && path !== (view === 'home' ? '/' : `/${view}`)) return
      sections.set(sectionKey(view), path + query)
    },
    /** The section's last list URL, without cross-view params. */
    sectionPath(view: string): string | undefined {
      return sections.get(sectionKey(view))
    },
  }
}

export type ViewMemory = ReturnType<typeof createViewMemory>
