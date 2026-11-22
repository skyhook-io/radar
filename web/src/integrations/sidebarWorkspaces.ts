import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../types'
import { useCNPGSidebarWorkspace } from '../components/cnpg/useCNPGSidebarWorkspace'
import { useVeleroSidebarWorkspace } from '../components/velero/useVeleroSidebarWorkspace'
import { useSubstrateSidebarWorkspace } from '../components/substrate/useSubstrateSidebarWorkspace'
import { useAgentSandboxSidebarWorkspace } from '../components/agentsandbox/useAgentSandboxSidebarWorkspace'
import { useKagentSidebarWorkspace } from '../components/kagent/useKagentSidebarWorkspace'
import { useMergedSidebarWorkspaces } from '../components/workspace/sidebar'

type Workspaces = Record<string, SidebarCategoryWorkspace>

/** Each integration's own options, for the view that is that integration's screen. */
export interface IntegrationSidebarOptions {
  cnpg?: Omit<Parameters<typeof useCNPGSidebarWorkspace>[0], 'apiResources' | 'namespaces'>
  velero?: Omit<Parameters<typeof useVeleroSidebarWorkspace>[0], 'apiResources'>
  substrate?: Omit<Parameters<typeof useSubstrateSidebarWorkspace>[0], 'apiResources'>
  agentSandbox?: Omit<Parameters<typeof useAgentSandboxSidebarWorkspace>[0], 'apiResources' | 'namespaces'>
}

/**
 * Every integration's part of the Resources sidebar, merged, in the order the
 * rows list them. Each workspace view (Resources, CloudNativePG, Velero,
 * Agent Substrate, Agent Sandbox) renders the same sidebar, so they all call
 * this rather than the integrations' hooks: an integration added here shows
 * on every screen.
 */
export function useIntegrationSidebars({
  apiResources,
  namespaces,
  cnpg,
  velero,
  substrate,
  agentSandbox,
}: { apiResources: APIResource[] | undefined; namespaces: string[] } & IntegrationSidebarOptions): Workspaces | undefined {
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces, ...cnpg })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources, ...velero })
  const substrateSidebar = useSubstrateSidebarWorkspace({ apiResources, ...substrate })
  const sandboxSidebar = useAgentSandboxSidebarWorkspace({ apiResources, namespaces, ...agentSandbox })
  const kagentSidebar = useKagentSidebarWorkspace({ apiResources })
  return useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar, substrateSidebar, sandboxSidebar, kagentSidebar)
}
