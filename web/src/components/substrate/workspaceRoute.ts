import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { SUBSTRATE_NAMESPACE_FILTER_TOOLTIP, SUBSTRATE_PATH, SUBSTRATE_SCREENS, parseSubstrateRoute } from './routes'

export const substrateWorkspaceRoute: WorkspaceRoute = {
  id: 'substrate',
  path: SUBSTRATE_PATH,
  label: 'Agent Substrate',
  navView: 'resources',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: SUBSTRATE_NAMESPACE_FILTER_TOOLTIP,
  pageTitle(pathname) {
    const route = parseSubstrateRoute(pathname)
    return route.actor?.name ?? route.template?.name ?? route.pool?.name ?? SUBSTRATE_SCREENS[route.screen].title
  },
  contextSwitchSearch(pathname, search) {
    // An object's page keeps its context pin, so a same-named actor in another cluster never opens.
    const next = new URLSearchParams()
    const context = search.get('ctx')
    const route = parseSubstrateRoute(pathname)
    if (context && (route.actor || route.template || route.pool)) next.set('ctx', context)
    return next
  },
}
