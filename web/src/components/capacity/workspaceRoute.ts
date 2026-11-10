import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { parseCapacityRoute } from './routes'

export const capacityWorkspaceRoute: WorkspaceRoute = {
  id: 'capacity',
  path: '/capacity',
  label: 'Capacity',
  navView: 'capacity',
  namespaceScope: 'cluster',
  namespaceFilterTooltip: 'Capacity is reported across the cluster — the namespace filter doesn’t apply here.',
  pageTitle(pathname) {
    const route = parseCapacityRoute(pathname)
    if (route.poolName) return route.poolName
    if (route.topTab === 'demand') return 'Capacity Demand'
    if (route.topTab === 'activity') return 'Capacity Activity'
    return 'Capacity'
  },
}
