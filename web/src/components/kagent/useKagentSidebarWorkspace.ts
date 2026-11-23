import { useMemo } from 'react'
import { Bot } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { AGENTS_SIDEBAR_CATEGORY } from '../workspace/sidebar'
import { KAGENT_AGENTS_PATH, KAGENT_AGENT_KIND } from './routes'

/** kagent's heading among the Agents row's destinations. */
export const KAGENT_SIDEBAR_SECTION = 'kagent'

export function kagentAgentsServed(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === KAGENT_AGENT_KIND.group && r.name === KAGENT_AGENT_KIND.name)
}

/**
 * kagent in the sidebar's Integrations, as its part of the Agents row: the
 * Agent list, with Radar's status column. kagent's other kinds stay under
 * Other kinds. No count: Radar has no kagent summary endpoint, and the list's
 * statuses are the answer. Undefined when the cluster doesn't serve kagent's
 * Agents.
 */
export function useKagentSidebarWorkspace({ apiResources }: { apiResources: APIResource[] | undefined }): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const served = kagentAgentsServed(apiResources)
  return useMemo(() => {
    if (!served) return undefined
    return {
      [AGENTS_SIDEBAR_CATEGORY]: {
        tools: 'kagent',
        icon: Bot,
        destinations: [
          { id: 'kagent-agents', label: 'Agents', section: KAGENT_SIDEBAR_SECTION, kind: KAGENT_AGENT_KIND, onSelect: () => navigate(KAGENT_AGENTS_PATH) },
        ],
      },
    }
  }, [served, navigate])
}
