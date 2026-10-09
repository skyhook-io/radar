import { CROSS_VIEW_PARAMS } from './navigation'

// The global picker owns the cross-view params. The rest open a drawer or a
// detail view on top of a list; they belong to one visit, not to the list the
// user returns to.
const NOT_REMEMBERED = new Set<string>([
  ...CROSS_VIEW_PARAMS,
  'resource', 'tab', 'full',
  'release', 'releaseStorage',
  'workload', 'run',
  'drawer', 'event',
])

const RESOURCE_LIST_PATH = /^\/resources\/[^/]+$/

function cleanSearch(search: string): string {
  const params = new URLSearchParams(search)
  for (const key of NOT_REMEMBERED) params.delete(key)
  const query = params.toString()
  return query ? `?${query}` : ''
}

/**
 * Session memory of the last list URL per section. The URL stays the source
 * of truth: entries are recorded from it and restored by navigating back to
 * them. In-memory only, so a reload forgets everything.
 */
export function createViewMemory() {
  const sections = new Map<string, string>()

  return {
    /** Records a location. Only list routes are remembered, not detail pages. */
    record(view: string, pathname: string, search: string) {
      const path = pathname.replace(/\/+$/, '') || '/'
      const query = cleanSearch(search)
      const resourceList = view === 'resources' && RESOURCE_LIST_PATH.test(path)
      if (!resourceList && path !== (view === 'home' ? '/' : `/${view}`)) return
      sections.set(view, path + query)
    },
    /** The section's last list URL, without cross-view params. */
    sectionPath(view: string): string | undefined {
      return sections.get(view)
    },
    clear() {
      sections.clear()
    },
  }
}

export type ViewMemory = ReturnType<typeof createViewMemory>
