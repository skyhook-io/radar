import type { ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { SandboxClaimsKindList, SandboxWarmPoolsKindList, SandboxesKindList } from './AgentSandboxKindLists'
import { AGENT_SANDBOX_OVERVIEW, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_TEMPLATE_KIND, SANDBOX_WARM_POOL_KIND, ownsAgentSandboxPath } from './routes'

// Read client-side from the generic list endpoint every Radar serves, so no
// capability gates them.
const always = () => 'view' as const

const KIND_BY_PLURAL: Record<string, string> = Object.fromEntries(
  [SANDBOX_KIND, SANDBOX_CLAIM_KIND, SANDBOX_WARM_POOL_KIND, SANDBOX_TEMPLATE_KIND].map((k) => [k.name, k.kind]),
)

function AgentSandboxDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  return <DrawerTrailBack resource={resource} pathPrefix={AGENT_SANDBOX_OVERVIEW.path} kindLabel={(plural) => KIND_BY_PLURAL[plural]} />
}

/**
 * Agent Sandbox's Sandbox, SandboxClaim and SandboxWarmPool lists are Radar's
 * own tables. Drawers keep the standard renderer, whose status and banner
 * already read these kinds (resource-utils-agent-runtimes).
 */
export const agentSandboxResourceHost: ResourceHost = {
  id: 'agent-sandbox',
  drawerNavigation: { ownsPath: ownsAgentSandboxPath, Component: AgentSandboxDrawerTrailBack },
  kindLists: [
    { kind: SANDBOX_KIND, title: 'Sandboxes', mode: always, Component: SandboxesKindList },
    { kind: SANDBOX_CLAIM_KIND, title: 'Sandbox claims', mode: always, Component: SandboxClaimsKindList },
    { kind: SANDBOX_WARM_POOL_KIND, title: 'Sandbox warm pools', mode: always, Component: SandboxWarmPoolsKindList },
  ],
}
