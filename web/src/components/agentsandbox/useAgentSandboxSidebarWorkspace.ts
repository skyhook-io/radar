import { useMemo } from 'react'
import { Bot } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useAgentSandboxWorkspace, type AgentSandboxSidebar } from '../../api/agentSandbox'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { AGENTS_SIDEBAR_CATEGORY } from '../workspace/sidebar'
import { agentSandboxDiscovered } from './data'
import { AGENT_SANDBOX_OVERVIEW, SANDBOXES_PATH, SANDBOX_CLAIMS_PATH, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOLS_PATH, SANDBOX_WARM_POOL_KIND } from './routes'
import { sidebarCount } from './workspace'

/** Agent Sandbox's heading among the Agents row's destinations, when another runtime shares the row. */
export const AGENT_SANDBOX_SIDEBAR_SECTION = 'Sandbox'

const SCREENS = [
  { kind: SANDBOX_KIND, key: 'sandboxes', label: 'Sandboxes', path: SANDBOXES_PATH, noun: ['sandbox', 'sandboxes'] },
  { kind: SANDBOX_CLAIM_KIND, key: 'claims', label: 'Claims', path: SANDBOX_CLAIMS_PATH, noun: ['claim', 'claims'] },
  { kind: SANDBOX_WARM_POOL_KIND, key: 'warmPools', label: 'Warm pools', path: SANDBOX_WARM_POOLS_PATH, noun: ['warm pool', 'warm pools'] },
] as const satisfies readonly { kind: unknown; label: string; path: string; key: keyof Omit<AgentSandboxSidebar, 'total'>; noun: readonly [string, string] }[]

/**
 * Agent Sandbox in the sidebar's Integrations, as its part of the Agents row:
 * what needs attention, and the Overview and Radar's three tables (each with
 * what needs attention there); SandboxTemplate stays under Other kinds. The counts
 * are the server's (/api/agentsandbox/workspace) and follow the namespace
 * filter. Undefined when Agent Sandbox is not discovered, or the connected
 * Radar is known to predate the overview.
 */
export function useAgentSandboxSidebarWorkspace({
  apiResources,
  namespaces,
  active,
}: {
  apiResources: APIResource[] | undefined
  namespaces: string[]
  /** The Overview is on screen. */
  active?: boolean
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('agentSandboxWorkspace')
  const available = agentSandboxDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useAgentSandboxWorkspace(namespaces, { enabled: available })
  const sidebar = data?.installed ? data.sidebar : undefined
  const nsKey = (data?.namespaces ?? namespaces).join(', ')
  return useMemo(() => {
    if (!available) return undefined
    const total = sidebar ? sidebarCount(sidebar.total, ['object', 'objects']) : undefined
    const scope = nsKey ? `namespace ${nsKey}` : 'all namespaces'
    return {
      [AGENTS_SIDEBAR_CATEGORY]: {
        tools: 'Agent Sandbox',
        icon: Bot,
        count: total?.count,
        countLowerBound: total?.lowerBound,
        countTitle: total ? `${total.title} Scope: ${scope}.` : undefined,
        countScope: scope,
        destinations: [
          { id: 'agent-sandbox-overview', label: AGENT_SANDBOX_OVERVIEW.label, section: AGENT_SANDBOX_SIDEBAR_SECTION, active: !!active, onSelect: () => navigate(AGENT_SANDBOX_OVERVIEW.path) },
          ...SCREENS.map(({ kind, key, label, path, noun }) => {
            const c = sidebar ? sidebarCount(sidebar[key], [noun[0], noun[1]]) : undefined
            return { id: kind.name, label, section: AGENT_SANDBOX_SIDEBAR_SECTION, kind, count: c?.count, countLowerBound: c?.lowerBound, countTitle: c?.title, onSelect: () => navigate(path) }
          }),
        ],
        scopeNote: nsKey ? `Counts for namespace ${nsKey}` : undefined,
      },
    }
  }, [available, active, navigate, sidebar, nsKey])
}
