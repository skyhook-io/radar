import { useMemo } from 'react'
import { CalendarClock, HardDrive } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useVeleroWorkspace } from '../../api/velero'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { VELERO_GROUP, VELERO_SCREENS, type VeleroScreen } from './routes'
import { veleroDestinationCounts } from './attention'

/** The Resources sidebar category Velero kinds are listed under (api-resources.ts). */
export const VELERO_SIDEBAR_CATEGORY = 'Velero'

const ICONS: Record<VeleroScreen, SidebarCategoryWorkspace['destinations'][number]['icon']> = {
  overview: CalendarClock,
  locations: HardDrive,
}

export function veleroDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === VELERO_GROUP)
}

/**
 * The Velero workspace entries for the Resources sidebar: its destinations,
 * each with what needs attention there. Undefined when Velero is not
 * discovered, or the connected Radar is known to predate the views. The counts
 * cover every installation: the namespace filter does not apply to Velero.
 */
export function useVeleroSidebarWorkspace({
  apiResources,
  active,
}: {
  apiResources: APIResource[] | undefined
  active?: VeleroScreen
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('veleroWorkspace')
  const available = veleroDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useVeleroWorkspace({ enabled: available })
  const counts = useMemo(() => (data?.installed ? veleroDestinationCounts(data) : undefined), [data])
  return useMemo(() => {
    if (!available) return undefined
    return {
      [VELERO_SIDEBAR_CATEGORY]: {
        destinations: VELERO_SCREENS.map((s) => {
          const c = counts?.[s.id]
          return {
            id: s.id,
            label: s.label,
            icon: ICONS[s.id],
            count: c?.count,
            countLowerBound: c?.lowerBound,
            countTitle: c?.title,
            active: active === s.id,
            onSelect: () => navigate(s.path),
          }
        }),
        defaultKindsCollapsed: !!active,
      },
    }
  }, [available, active, navigate, counts])
}
