import { getAgentRuntimeStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { DetailSlots, HostResource, ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { AgentSandboxSummary, type AgentSandboxSummaryKind } from './AgentSandboxSummary'
import { ReadingBadge } from './shared'
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

const SUMMARY_KINDS = [SANDBOX_KIND, SANDBOX_CLAIM_KIND, SANDBOX_WARM_POOL_KIND]

function summaryKind(resource: HostResource): AgentSandboxSummaryKind | undefined {
  const plural = kindToPluralWithGroup(resource.kind, resource.group ?? '')
  return SUMMARY_KINDS.find((k) => k.name === plural && k.group === resource.group)?.kind as AgentSandboxSummaryKind | undefined
}

/**
 * Agent Sandbox's Sandbox, SandboxClaim and SandboxWarmPool lists are Radar's
 * own tables. Their drawers lead with the reading the workspace and Issues
 * give, wherever they open; the standard renderer is "Spec & status". The
 * header chip is the lists' plain-words reading, the raw reason on hover.
 */
export const agentSandboxResourceHost: ResourceHost = {
  id: 'agent-sandbox',
  drawerNavigation: { ownsPath: ownsAgentSandboxPath, Component: AgentSandboxDrawerTrailBack },
  resources: SUMMARY_KINDS.map((k) => ({ name: k.name, group: k.group })),
  detailOwnership: ['summary'],
  detailSlots: (resource): DetailSlots => {
    const kind = summaryKind(resource)
    if (!kind) return {}
    return {
      renderSummary: ({ apiKind, group, namespace, name, resource: data, onNavigate, leadContent }) =>
        data?.kind === kind ? <AgentSandboxSummary kind={kind} apiKind={apiKind} group={group} namespace={namespace} name={name} resource={data} onNavigate={onNavigate} leadContent={leadContent} /> : null,
      renderStatusBadge: (data: any) => {
        const status = getAgentRuntimeStatus(data)
        return status ? <ReadingBadge status={status} /> : null
      },
    }
  },
  kindLists: [
    { kind: SANDBOX_KIND, title: 'Sandboxes', mode: always, Component: SandboxesKindList },
    { kind: SANDBOX_CLAIM_KIND, title: 'Sandbox claims', mode: always, Component: SandboxClaimsKindList },
    { kind: SANDBOX_WARM_POOL_KIND, title: 'Sandbox warm pools', mode: always, Component: SandboxWarmPoolsKindList },
  ],
}
