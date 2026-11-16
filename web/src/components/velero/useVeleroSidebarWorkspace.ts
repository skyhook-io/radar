import { useMemo } from 'react'
import { CalendarClock } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useVeleroWorkspace } from '../../api/velero'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { VELERO_GROUP, VELERO_OVERVIEW } from './routes'
import { veleroAttentionCount } from './attention'

/** The Resources sidebar category Velero kinds are listed under (api-resources.ts). */
export const VELERO_SIDEBAR_CATEGORY = 'Velero'

export function veleroDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === VELERO_GROUP)
}

/**
 * The Velero entry for the Resources sidebar: the Overview, the one screen no
 * single kind's list answers, with how much needs attention. Its kinds follow
 * as usual; Schedule and BackupStorageLocation open Radar's own tables.
 * Undefined when Velero is not discovered, or the connected Radar is known to
 * predate the views. The count covers every installation: the namespace
 * filter does not apply to Velero.
 */
export function useVeleroSidebarWorkspace({
  apiResources,
  active,
  child,
}: {
  apiResources: APIResource[] | undefined
  /** The Overview, or a Velero object's page, is on screen. */
  active?: boolean
  /** The object whose page is open, listed under the Overview. */
  child?: { label: string; title?: string }
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('veleroWorkspace')
  const available = veleroDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useVeleroWorkspace({ enabled: available })
  const count = useMemo(() => (data?.installed ? veleroAttentionCount(data) : undefined), [data])
  return useMemo(() => {
    if (!available) return undefined
    return {
      [VELERO_SIDEBAR_CATEGORY]: {
        destinations: [
          {
            id: 'overview',
            label: VELERO_OVERVIEW.label,
            icon: CalendarClock,
            count: count?.count,
            countLowerBound: count?.lowerBound,
            countTitle: count?.title,
            active: !!active,
            child: active ? child : undefined,
            onSelect: () => navigate(VELERO_OVERVIEW.path),
          },
        ],
      },
    }
  }, [available, active, child?.label, child?.title, navigate, count]) // eslint-disable-line react-hooks/exhaustive-deps
}
