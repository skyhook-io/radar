import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { Database, FileCheck2, Settings2, ShieldCheck, Waypoints } from 'lucide-react'
import { buildCNPGFleet, type CNPGFleet, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useCNPGWorkspace } from '../../api/cnpg'
import { CNPG_SCREENS, type CNPGScreen } from './routes'

export const CNPG_SIDEBAR_CATEGORY = 'CloudNativePG'

const ICONS: Record<CNPGScreen, SidebarCategoryWorkspace['destinations'][number]['icon']> = {
  overview: Database,
  protection: ShieldCheck,
  declarations: FileCheck2,
  pooling: Waypoints,
  operator: Settings2,
}

export function cnpgDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === 'postgresql.cnpg.io')
}

export function useCNPGFleet(namespaces: string[], enabled = true) {
  const query = useCNPGWorkspace(namespaces, { enabled })
  const fleet = useMemo<CNPGFleet | null>(() => (query.data?.installed ? buildCNPGFleet(query.data) : null), [query.data])
  return { query, fleet }
}

function destinationCount(screen: CNPGScreen, fleet: CNPGFleet | null): { count?: number | null; title?: string } {
  if (!fleet) return {}
  const lowerBound = fleet.incompleteKinds.length > 0 ? ' Some CloudNativePG data is not readable, so this is a lower bound.' : ''
  switch (screen) {
    case 'overview':
      return { count: fleet.attentionCount, title: `${fleet.attentionCount} clusters need attention.${lowerBound}` }
    case 'protection':
      return { count: fleet.categoryCounts.protection, title: `${fleet.categoryCounts.protection} clusters with failing backups or WAL archiving.${lowerBound}` }
    case 'declarations':
      return { count: fleet.categoryCounts.declarations, title: `${fleet.categoryCounts.declarations} clusters with declarations that are not reconciled.${lowerBound}` }
    case 'pooling':
      return { count: fleet.categoryCounts.pooling, title: `${fleet.categoryCounts.pooling} clusters with Pooler problems.${lowerBound}` }
    default:
      return {}
  }
}

/**
 * The CloudNativePG workspace entries for the Resources sidebar. Returns
 * undefined when CNPG is not discovered, so clusters without the operator see
 * an unchanged sidebar.
 */
export function useCNPGSidebarWorkspace({
  apiResources,
  namespaces,
  active,
}: {
  apiResources: APIResource[] | undefined
  namespaces: string[]
  active?: { screen: CNPGScreen; child?: { label: string; title?: string } }
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useNavigate()
  const discovered = cnpgDiscovered(apiResources)
  const { fleet } = useCNPGFleet(namespaces, discovered)
  const nsKey = namespaces.join(',')

  return useMemo(() => {
    if (!discovered) return undefined
    const destinations = CNPG_SCREENS.map((s) => {
      const { count, title } = destinationCount(s.id, fleet)
      return {
        id: s.id,
        label: s.label,
        icon: ICONS[s.id],
        count,
        countTitle: title,
        active: active?.screen === s.id,
        child: active?.screen === s.id ? active.child : undefined,
        onSelect: () => navigate(s.path),
      }
    })
    return {
      [CNPG_SIDEBAR_CATEGORY]: {
        destinations,
        defaultKindsCollapsed: !!active,
        scopeNote: nsKey ? `Counts for namespace ${nsKey.split(',').join(', ')}` : undefined,
      },
    }
  }, [discovered, fleet, active?.screen, active?.child?.label, active?.child?.title, navigate, nsKey]) // eslint-disable-line react-hooks/exhaustive-deps
}
