import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { Database, FileCheck2, Settings2, ShieldCheck, Waypoints } from 'lucide-react'
import { applyCNPGDisk, applyCNPGFleetMetrics, buildCNPGFleet, type CNPGDiskReading, type CNPGFleet, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useCNPGWorkspace } from '../../api/cnpg'
import { useRadarFeature } from '../../api/client'
import { useCNPGFleetDisk } from '../../api/cnpg-storage'
import { useCNPGFleetMetrics } from '../../api/cnpg-history'
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

// Disk use joins the fleet here, so low-disk clusters count toward Needs
// attention on every screen and sidebar badge that reads the fleet; measured
// replication lag and disk growth join the same way.
export function useCNPGFleet(namespaces: string[], enabled = true) {
  const query = useCNPGWorkspace(namespaces, { enabled })
  const disk = useCNPGFleetDisk(namespaces, enabled && !!query.data?.installed)
  const metrics = useCNPGFleetMetrics(namespaces, enabled && !!query.data?.installed)
  const fleet = useMemo<CNPGFleet | null>(
    () =>
      query.data?.installed
        ? applyCNPGFleetMetrics(
            applyCNPGDisk(buildCNPGFleet(query.data), disk.data?.clusters ?? (disk.error ? diskFailed(query.data.objects.clusters ?? [], disk.error) : undefined)),
            metrics.data?.clusters,
            metrics.data,
          )
        : null,
    [query.data, disk.data, disk.error, metrics.data],
  )
  return { query, fleet }
}

// A failed disk read is stated per cluster; left undefined it would render as
// still loading.
function diskFailed(clusters: any[], err: unknown): CNPGDiskReading[] {
  const reason = `Disk usage could not be read: ${err instanceof Error ? err.message : 'request failed'}`
  return clusters.map((c) => ({ namespace: c?.metadata?.namespace ?? '', name: c?.metadata?.name ?? '', state: 'error', reason, claims: 0, measured: 0 }))
}

function destinationCount(screen: CNPGScreen, fleet: CNPGFleet | null): { count?: number | null; lowerBound?: boolean; title?: string } {
  if (!fleet) return {}
  const partial = fleet.incompleteKinds.length > 0
  const note = partial ? ' Some CloudNativePG data is not readable, so this is a lower bound.' : ''
  const at = (count: number, what: string) => ({ count, lowerBound: partial, title: `${partial ? 'At least ' : ''}${count} ${what}.${note}` })
  switch (screen) {
    case 'overview':
      return at(fleet.attentionCount, 'clusters need attention')
    case 'protection':
      return at(fleet.categoryCounts.protection, 'clusters with failing backups or WAL archiving')
    case 'declarations':
      return at(fleet.categoryCounts.declarations, 'clusters with declarations that are not reconciled')
    case 'pooling':
      return at(fleet.categoryCounts.pooling, 'clusters with Pooler problems')
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
  // A Radar that predates the workspace serves none of its endpoints: no
  // destinations rather than ones that fail when opened.
  const { support } = useRadarFeature('cnpgWorkspace')
  const available = cnpgDiscovered(apiResources) && support !== 'unsupported'
  const { fleet } = useCNPGFleet(namespaces, available)
  const nsKey = namespaces.join(',')

  return useMemo(() => {
    if (!available) return undefined
    const destinations = CNPG_SCREENS.map((s) => {
      const { count, lowerBound, title } = destinationCount(s.id, fleet)
      return {
        id: s.id,
        label: s.label,
        icon: ICONS[s.id],
        count,
        countLowerBound: lowerBound,
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
  }, [available, fleet, active?.screen, active?.child?.label, active?.child?.title, navigate, nsKey]) // eslint-disable-line react-hooks/exhaustive-deps
}
