import { useMemo } from 'react'
import { CalendarClock, HardDrive } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { VELERO_GROUP, VELERO_SCREENS, type VeleroScreen } from './routes'

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
 * without counts. Undefined when Velero is not discovered, or the connected
 * Radar is known to predate the views.
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
  return useMemo(() => {
    if (!available) return undefined
    return {
      [VELERO_SIDEBAR_CATEGORY]: {
        destinations: VELERO_SCREENS.map((s) => ({
          id: s.id,
          label: s.label,
          icon: ICONS[s.id],
          active: active === s.id,
          onSelect: () => navigate(s.path),
        })),
        defaultKindsCollapsed: !!active,
      },
    }
  }, [available, active, navigate])
}
