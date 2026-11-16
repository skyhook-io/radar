import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { VELERO_OVERVIEW } from './routes'

export const veleroWorkspaceRoute: WorkspaceRoute = {
  id: 'velero',
  path: '/velero',
  label: 'Velero',
  navView: 'resources',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: 'Velero is shown for every installation you can read — the namespace filter doesn’t apply here.',
  pageTitle() {
    return VELERO_OVERVIEW.title
  },
}
