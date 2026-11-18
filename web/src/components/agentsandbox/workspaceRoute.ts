import type { WorkspaceRoute } from '../../integrations/workspaceRoute'
import { AGENT_SANDBOX_OVERVIEW } from './routes'

export const agentSandboxWorkspaceRoute: WorkspaceRoute = {
  id: 'agent-sandbox',
  path: AGENT_SANDBOX_OVERVIEW.path,
  label: 'Agent Sandbox',
  navView: 'resources',
  // Sandboxes, claims and warm pools are namespaced app objects: the filter applies.
  namespaceScope: 'view',
  pageTitle() {
    return AGENT_SANDBOX_OVERVIEW.title
  },
  // The Overview follows the active cluster; there is no per-object page to pin.
  contextSwitchSearch() {
    return new URLSearchParams()
  },
}
