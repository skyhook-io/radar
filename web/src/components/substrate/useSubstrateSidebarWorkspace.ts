import { useMemo } from 'react'
import { Bot } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useSubstrateWorkspace } from '../../api/substrateWorkspace'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { AGENTS_SIDEBAR_CATEGORY } from '../workspace/sidebar'
import { substrateAttentionCount, substrateScreenCounts } from './attention'
import { SUBSTRATE_GROUP, SUBSTRATE_POOLS_PATH, SUBSTRATE_SCREENS, SUBSTRATE_WORKERPOOL_KIND, type SubstrateScreen } from './routes'

/** Substrate's heading among the Agents row's destinations, when another runtime shares the row. */
export const SUBSTRATE_SIDEBAR_SECTION = 'Substrate'

/** How often the sidebar's badge alone refreshes the workspace read. */
export const SIDEBAR_POLL_MS = 60_000

export function substrateDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === SUBSTRATE_GROUP)
}

/**
 * Agent Substrate in the sidebar's Integrations, as its part of the Agents
 * row: what needs attention across it, and the Overview, Actors, Templates,
 * Workers and Radar's WorkerPool table, each with what needs attention there.
 * SandboxConfig and CSIDriverConfig stay under Other kinds. Undefined when
 * Substrate is not discovered, or the connected Radar is known to predate the
 * views. The namespace filter does not apply.
 */
export function useSubstrateSidebarWorkspace({
  apiResources,
  active,
}: {
  apiResources: APIResource[] | undefined
  /** The screen on show, highlighted; an object's page highlights its screen or kind instead. */
  active?: SubstrateScreen
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('substrateWorkspace')
  const available = substrateDiscovered(apiResources) && support !== 'unsupported'
  // Slow: a Substrate screen polls the same query faster while it is open,
  // and the query refetches at the fastest interval any observer asks for.
  const { data } = useSubstrateWorkspace(available, { pollMs: SIDEBAR_POLL_MS })
  const counts = useMemo(() => (data ? { total: substrateAttentionCount(data), screens: substrateScreenCounts(data) } : undefined), [data])
  return useMemo(() => {
    if (!available) return undefined
    const screen = (id: Exclude<SubstrateScreen, 'overview'>) => {
      const c = counts?.screens[id]
      return {
        id,
        label: SUBSTRATE_SCREENS[id].label,
        section: SUBSTRATE_SIDEBAR_SECTION,
        active: active === id,
        count: c?.count,
        countLowerBound: c?.lowerBound,
        countTitle: c?.title,
        onSelect: () => navigate(SUBSTRATE_SCREENS[id].path),
      }
    }
    const pools = counts?.screens.pools
    return {
      [AGENTS_SIDEBAR_CATEGORY]: {
        tools: 'Agent Substrate',
        icon: Bot,
        count: counts?.total.count,
        countLowerBound: counts?.total.lowerBound,
        countTitle: counts?.total.title,
        destinations: [
          { id: 'substrate-overview', label: SUBSTRATE_SCREENS.overview.label, section: SUBSTRATE_SIDEBAR_SECTION, active: active === 'overview', onSelect: () => navigate(SUBSTRATE_SCREENS.overview.path) },
          screen('actors'),
          screen('templates'),
          screen('workers'),
          {
            id: 'pools',
            label: 'Pools',
            section: SUBSTRATE_SIDEBAR_SECTION,
            kind: SUBSTRATE_WORKERPOOL_KIND,
            count: pools?.count,
            countLowerBound: pools?.lowerBound,
            countTitle: pools?.title,
            onSelect: () => navigate(SUBSTRATE_POOLS_PATH),
          },
        ],
      },
    }
  }, [available, active, navigate, counts])
}
