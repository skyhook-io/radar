import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { CNPG_SCREENS, parseCNPGRoute } from './routes'

export const cnpgWorkspaceRoute: WorkspaceRoute = {
  id: 'cnpg',
  path: '/cnpg',
  label: 'CloudNativePG',
  navView: 'resources',
  namespaceScope: 'view',
  pageTitle(pathname) {
    const route = parseCNPGRoute(pathname)
    if (route.detail) return route.detail.name
    const screen = CNPG_SCREENS.find(screen => screen.id === route.screen)
    return `CloudNativePG ${screen?.label ?? 'Clusters'}`
  },
  contextSwitchSearch(pathname, search) {
    // A detail's context pin prevents loading a same-named object after a switch.
    const next = new URLSearchParams()
    const context = search.get('ctx')
    if (context && pathname.startsWith('/cnpg/')) next.set('ctx', context)
    return next
  },
}
