import type { WorkspaceRoute } from '../../integrations/workspaceRoute'

export const loggingWorkspaceRoute: WorkspaceRoute = {
  id: 'logging',
  path: '/logging',
  label: 'Logging',
  navView: 'resources',
  namespaceScope: 'view',
  pageTitle: () => 'Logging pipeline',
}
