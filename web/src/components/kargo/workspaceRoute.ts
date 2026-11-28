import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { KARGO_NAMESPACE_FILTER_TOOLTIP, KARGO_PATH, KARGO_TITLE } from './routes'

export const kargoWorkspaceRoute: WorkspaceRoute = {
  id: 'kargo',
  path: KARGO_PATH,
  label: 'Kargo',
  navView: 'resources',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: KARGO_NAMESPACE_FILTER_TOOLTIP,
  pageTitle() {
    return KARGO_TITLE
  },
  // Project names belong to the cluster they were picked in, so a switch
  // starts from the new cluster's first project.
  contextSwitchSearch() {
    return new URLSearchParams()
  },
}
