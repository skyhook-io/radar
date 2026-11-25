import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { LONGHORN_NAMESPACE_FILTER_TOOLTIP, LONGHORN_OVERVIEW, LONGHORN_VOLUMES, parseLonghornRoute } from './routes'

export const longhornWorkspaceRoute: WorkspaceRoute = {
  id: 'longhorn',
  path: '/longhorn',
  label: 'Longhorn',
  navView: 'resources',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: LONGHORN_NAMESPACE_FILTER_TOOLTIP,
  pageTitle(pathname) {
    return parseLonghornRoute(pathname) === 'volumes' ? LONGHORN_VOLUMES.title : LONGHORN_OVERVIEW.title
  },
  // No contextSwitchSearch: Longhorn has no object pages for a ctx pin to guard, so a switch drops the query.
}
