import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { VELERO_NAMESPACE_FILTER_TOOLTIP, VELERO_OVERVIEW, parseVeleroRoute } from './routes'

export const veleroWorkspaceRoute: WorkspaceRoute = {
  id: 'velero',
  path: '/velero',
  label: 'Velero',
  navView: 'resources',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: VELERO_NAMESPACE_FILTER_TOOLTIP,
  pageTitle(pathname) {
    return parseVeleroRoute(pathname).detail?.name ?? VELERO_OVERVIEW.title
  },
  contextSwitchSearch(pathname, search) {
    // A page's context pin keeps a same-named object from another cluster from opening.
    const next = new URLSearchParams()
    const context = search.get('ctx')
    if (context && pathname.startsWith('/velero/')) next.set('ctx', context)
    return next
  },
}
