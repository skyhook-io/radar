import { useMemo } from 'react'
import { HardDrive } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useLonghornWorkspace } from '../../api/longhorn'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { longhornAttentionCount } from './attention'
import { LONGHORN_GROUP, LONGHORN_OVERVIEW, LONGHORN_VOLUMES, type LonghornScreen } from './routes'

/** The Resources sidebar category longhorn.io kinds are listed under (api-resources.ts). */
export const LONGHORN_SIDEBAR_CATEGORY = 'Longhorn'

export function longhornDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === LONGHORN_GROUP)
}

/**
 * Longhorn in the sidebar's Integrations: one row counting faulted and
 * degraded Volumes, opening to the Overview and the Volumes screen. Undefined
 * when Longhorn is not discovered or the connected Radar predates the views.
 */
export function useLonghornSidebarWorkspace({ apiResources, active }: { apiResources: APIResource[] | undefined; active?: LonghornScreen }): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('longhornWorkspace')
  const available = longhornDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useLonghornWorkspace({ enabled: available })
  const count = useMemo(() => (data?.installed ? longhornAttentionCount(data) : undefined), [data])
  return useMemo(() => {
    if (!available) return undefined
    return {
      [LONGHORN_SIDEBAR_CATEGORY]: {
        label: 'Block storage',
        icon: HardDrive,
        count: count?.count,
        countLowerBound: count?.lowerBound,
        countTitle: count?.title,
        destinations: [
          { id: 'overview', label: LONGHORN_OVERVIEW.label, active: active === 'overview', onSelect: () => navigate(LONGHORN_OVERVIEW.path) },
          { id: 'volumes', label: LONGHORN_VOLUMES.label, active: active === 'volumes', onSelect: () => navigate(LONGHORN_VOLUMES.path) },
        ],
      },
    }
  }, [available, active, navigate, count])
}
