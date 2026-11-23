import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useIntegrationSidebars } from '../../integrations/sidebarWorkspaces'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { useAgentSandboxWorkspace } from '../../api/agentSandbox'
import { AgentSandboxOverview } from './AgentSandboxOverview'
import { agentSandboxDiscovered } from './data'
import { AgentSandboxWorkspaceGate } from './workspace'

/**
 * The Agent Sandbox Overview, from the server's summary, inside Resources
 * with the same sidebar. The namespace filter applies: Sandboxes, claims and
 * warm pools live in the namespaces of the apps that use them. The controller
 * is read in its own namespace whatever the filter.
 */
export function AgentSandboxView({ namespaces, selectedResource, onOpenResource, onCloseResource, onClearNamespaces }: WorkspaceScreenProps) {
  const compactNavigation = useMediaQuery('(max-width: 1100px)')
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Agent Sandbox is absent; the server is asked then.
  const discovered = agentSandboxDiscovered(apiResources) || discoveryFailed
  const query = useAgentSandboxWorkspace(namespaces, { enabled: discovered })
  const sidebarWorkspace = useIntegrationSidebars({ apiResources, namespaces, agentSandbox: { active: true } })
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} compactNavigation={compactNavigation}>
      <AgentSandboxWorkspaceGate query={query} discoveryPending={discoveryPending} discovered={discovered}>
        {(data) => <AgentSandboxOverview data={data} onClearNamespaces={onClearNamespaces} onInspect={inspect} inspected={drawerTarget} />}
      </AgentSandboxWorkspaceGate>
    </WorkspaceFrame>
  )
}
