import { useMemo } from 'react'
import { Boxes } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useAgentSandboxWorkspace, type AgentSandboxSidebar } from '../../api/agentSandbox'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { agentSandboxDiscovered } from './data'
import { AGENT_SANDBOX_OVERVIEW, SANDBOXES_PATH, SANDBOX_CLAIMS_PATH, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOLS_PATH, SANDBOX_WARM_POOL_KIND } from './routes'
import { sidebarCount } from './workspace'

/** The Resources sidebar category Agent Sandbox's kinds are listed under (api-resources.ts). */
export const AGENT_SANDBOX_SIDEBAR_CATEGORY = 'Agent Sandbox'

/** The job Agent Sandbox does, its name in the sidebar's Integrations; "Agent Sandbox" sits beneath it. */
export const AGENT_SANDBOX_SIDEBAR_LABEL = 'Sandboxes'

const SCREENS = [
  { kind: SANDBOX_KIND, key: 'sandboxes', label: 'Sandboxes', path: SANDBOXES_PATH, noun: ['sandbox', 'sandboxes'] },
  { kind: SANDBOX_CLAIM_KIND, key: 'claims', label: 'Claims', path: SANDBOX_CLAIMS_PATH, noun: ['claim', 'claims'] },
  { kind: SANDBOX_WARM_POOL_KIND, key: 'warmPools', label: 'Warm pools', path: SANDBOX_WARM_POOLS_PATH, noun: ['warm pool', 'warm pools'] },
] as const satisfies readonly { kind: unknown; label: string; path: string; key: keyof Omit<AgentSandboxSidebar, 'total' | 'controller'>; noun: readonly [string, string] }[]

/**
 * Agent Sandbox in the sidebar's Integrations: one row with what needs
 * attention, opening to the Overview and Radar's three tables (each with what
 * needs attention there); SandboxTemplate stays under Other kinds. The counts
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
  const nsKey = namespaces.join(', ')
  return useMemo(() => {
    if (!available) return undefined
    const total = sidebar ? sidebarCount(sidebar.total, ['row', 'rows'], sidebar.controller) : undefined
    return {
      [AGENT_SANDBOX_SIDEBAR_CATEGORY]: {
        label: AGENT_SANDBOX_SIDEBAR_LABEL,
        icon: Boxes,
        count: total?.count,
        countLowerBound: total?.lowerBound,
        countTitle: total?.title,
        destinations: [
          { id: 'overview', label: AGENT_SANDBOX_OVERVIEW.label, active: !!active, onSelect: () => navigate(AGENT_SANDBOX_OVERVIEW.path) },
          ...SCREENS.map(({ kind, key, label, path, noun }) => {
            const c = sidebar ? sidebarCount(sidebar[key], [noun[0], noun[1]]) : undefined
            return { id: kind.name, label, kind, count: c?.count, countLowerBound: c?.lowerBound, countTitle: c?.title, onSelect: () => navigate(path) }
          }),
        ],
        scopeNote: nsKey ? `Counts for namespace ${nsKey}` : undefined,
      },
    }
  }, [available, active, navigate, sidebar, nsKey])
}
