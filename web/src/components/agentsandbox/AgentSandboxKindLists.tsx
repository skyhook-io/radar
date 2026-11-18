import { useMemo, type ComponentType } from 'react'
import type { HostKind, KindListProps } from '../../integrations/resourceHost'
import { useAPIResources } from '../../api/apiResources'
import { useRadarFeature } from '../../api/client'
import { useAgentSandboxWorkspace } from '../../api/agentSandbox'
import { serverReadings, type ServerReadings } from './labels'
import { agentSandboxDiscovered, useAgentSandboxLists, type AgentSandboxListKey } from './data'
import { SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'
import { AgentSandboxGate, type AgentSandboxScreenProps } from './shared'
import { ClaimsScreen, SandboxesScreen, WarmPoolsScreen } from './AgentSandboxTables'

/**
 * An Agent Sandbox kind's list on its Resources route, as Radar reads it
 * rather than as a bare table, with the namespace filter applied. Rows open
 * the Resources page's drawer.
 */
function kindList(Screen: ComponentType<AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }>, kind: HostKind, keys: readonly AgentSandboxListKey[]) {
  return function AgentSandboxKindList({ namespaces, inspected, onInspect, onClearNamespaces, onCreate }: KindListProps) {
    const { data: apiResources, isPending, isError } = useAPIResources()
    const discovered = agentSandboxDiscovered(apiResources) || isError
    const query = useAgentSandboxLists({ namespaces, apiResources, enabled: discovered })
    // The server's summary (shared with the sidebar's badges) colors each row
    // as its Needs attention row does and says which warm pools can't schedule
    // their sandboxes; an older Radar has none, and the readings stand.
    const { support } = useRadarFeature('agentSandboxWorkspace')
    const summary = useAgentSandboxWorkspace(namespaces, { enabled: discovered && support !== 'unsupported' })
    const server = useMemo(() => serverReadings(summary.data), [summary.data])
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
        <AgentSandboxGate data={query.data} keys={keys} discoveryPending={isPending} discovered={discovered} isFetching={query.isFetching} refetch={query.refetch} refreshQueries={query.refreshQueries}>
          <Screen data={query.data} namespaces={namespaces} onClearNamespaces={onClearNamespaces} onInspect={onInspect} inspected={inspected} onCreate={() => onCreate(kind)} server={server} />
        </AgentSandboxGate>
      </div>
    )
  }
}

export const SandboxesKindList = kindList(SandboxesScreen, SANDBOX_KIND, ['sandboxes'])
export const SandboxClaimsKindList = kindList(ClaimsScreen, SANDBOX_CLAIM_KIND, ['claims'])
export const SandboxWarmPoolsKindList = kindList(WarmPoolsScreen, SANDBOX_WARM_POOL_KIND, ['warmPools'])
