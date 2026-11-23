import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { parseKagentRoute } from './routes'
export const kagentWorkspaceRoute: WorkspaceRoute = {
  id: 'kagent',
  path: '/kagent',
  label: 'kagent',
  navView: 'resources',
  namespaceScope: 'view',
  pageTitle: (path) => {
    const r = parseKagentRoute(path)
    return r.detail?.name ?? `kagent ${r.screen === 'mcpservers' ? 'MCP servers' : r.screen}`
  },
  contextSwitchSearch(path, search) {
    const next = new URLSearchParams()
    if (parseKagentRoute(path).detail && search.has('ctx')) next.set('ctx', search.get('ctx')!)
    return next
  },
}
