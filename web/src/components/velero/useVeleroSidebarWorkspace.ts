import { useMemo } from 'react'
import { Archive } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useVeleroWorkspace } from '../../api/velero'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { VELERO_BACKUP_KIND, VELERO_DETAIL_KINDS, VELERO_GROUP, VELERO_LOCATION_KIND, VELERO_OVERVIEW, VELERO_RESTORE_KIND, VELERO_SCHEDULE_KIND } from './routes'
import { veleroAttentionCount, veleroScreenCounts, type VeleroScreen } from './attention'

/** The Resources sidebar category Velero kinds are listed under (api-resources.ts). */
export const VELERO_SIDEBAR_CATEGORY = 'Velero'

/** The job Velero does, its name in the sidebar's Integrations; "Velero" sits beneath it. */
export const VELERO_SIDEBAR_LABEL = 'Backups'

export function veleroDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === VELERO_GROUP)
}

const SCREENS = [
  { kind: VELERO_BACKUP_KIND, label: 'Backups' },
  { kind: VELERO_SCHEDULE_KIND, label: 'Schedules' },
  { kind: VELERO_RESTORE_KIND, label: 'Restores' },
  { kind: VELERO_LOCATION_KIND, label: 'Locations' },
] as const

/**
 * Velero in the sidebar's Integrations: one row with what needs attention
 * across every installation, opening to the Overview and Radar's four tables
 * (each with what needs attention there), with Velero's other kinds folded
 * beneath. Undefined when Velero is not discovered, or the connected Radar is
 * known to predate the views. The namespace filter does not apply to Velero.
 */
export function useVeleroSidebarWorkspace({
  apiResources,
  active,
}: {
  apiResources: APIResource[] | undefined
  /** The Overview is on screen. */
  active?: boolean
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('veleroWorkspace')
  const available = veleroDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useVeleroWorkspace({ enabled: available })
  const counts = useMemo(() => (data?.installed ? { total: veleroAttentionCount(data), screens: veleroScreenCounts(data) } : undefined), [data])
  return useMemo(() => {
    if (!available) return undefined
    return {
      [VELERO_SIDEBAR_CATEGORY]: {
        label: VELERO_SIDEBAR_LABEL,
        icon: Archive,
        count: counts?.total.count,
        countLowerBound: counts?.total.lowerBound,
        countTitle: counts?.total.title,
        destinations: [
          { id: 'overview', label: VELERO_OVERVIEW.label, active: !!active, onSelect: () => navigate(VELERO_OVERVIEW.path) },
          ...SCREENS.map(({ kind, label }) => {
            const c = counts?.screens[kind.name as VeleroScreen]
            return {
              id: kind.name,
              label,
              kind,
              count: c?.count,
              countLowerBound: c?.lowerBound,
              countTitle: c?.title,
              onSelect: () => navigate(VELERO_DETAIL_KINDS[kind.name].listPath),
            }
          }),
        ],
      },
    }
  }, [available, active, navigate, counts])
}
