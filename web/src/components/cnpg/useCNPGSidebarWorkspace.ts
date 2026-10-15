import { useMemo } from 'react'
import { Database, FileCheck2, Settings2, ShieldCheck, Waypoints } from 'lucide-react'
import { applyCNPGDisk, applyCNPGFleetMetrics, buildCNPGFleet, type CNPGDiskReading, type CNPGFleet, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useCNPGWorkspace } from '../../api/cnpg'
import { useRadarFeature } from '../../api/client'
import { useCNPGFleetDisk } from '../../api/cnpg-storage'
import { useCNPGFleetMetrics } from '../../api/cnpg-history'
import { CNPG_SCREENS, type CNPGScreen } from './routes'
import { useCNPGNavigate } from './useCNPGNavigate'

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
// attention on every screen and sidebar badge that reads the enriched fleet;
// measured replication lag and disk growth join the same way. Both are
// Prometheus fan-outs per namespace, so callers outside the CloudNativePG views
// pass withMetrics=false.
export function useCNPGFleet(namespaces: string[], enabled = true, withMetrics = true) {
  const query = useCNPGWorkspace(namespaces, { enabled })
  const { support } = useRadarFeature('cnpgWorkspace')
  const enrich = enabled && withMetrics && support === 'supported' && !!query.data?.installed
  const disk = useCNPGFleetDisk(namespaces, enrich)
  const metrics = useCNPGFleetMetrics(namespaces, enrich)
  const fleet = useMemo<CNPGFleet | null>(
    () =>
      query.data?.installed
        ? applyCNPGFleetMetrics(
            applyCNPGDisk(buildCNPGFleet(query.data), enrich ? disk.data?.clusters ?? (disk.error ? diskFailed(query.data.objects.clusters ?? [], disk.error) : undefined) : undefined),
            enrich ? metrics.data?.clusters : undefined,
            enrich ? metrics.data : undefined,
          )
        : null,
    [query.data, disk.data, disk.error, metrics.data, enrich],
  )
  return { query, fleet }
}

// A failed disk read is stated per cluster; left undefined it would render as
// still loading.
function diskFailed(clusters: any[], err: unknown): CNPGDiskReading[] {
  const reason = `Disk usage could not be read: ${err instanceof Error ? err.message : 'request failed'}`
  return clusters.map((c) => ({ namespace: c?.metadata?.namespace ?? '', name: c?.metadata?.name ?? '', state: 'error', reason, claims: 0, measured: 0 }))
}

const UNMEASURED_NOTE = 'Disk use and replication lag are counted on the CloudNativePG views.'

function destinationCount(screen: CNPGScreen, fleet: CNPGFleet | null, withMetrics: boolean): { count?: number | null; lowerBound?: boolean; title?: string } {
  if (!fleet) return {}
  const partial = fleet.incompleteKinds.length > 0
  const note = partial ? ' Some CloudNativePG data is not readable, so this is a lower bound.' : ''
  // Disk use and measured lag only ever add problems, so a count without them
  // is a lower bound, and its zero is not "none".
  const at = (count: number, what: string, unmeasured?: string) => {
    const lowerBound = partial || !!unmeasured
    const extra = unmeasured ? ` ${unmeasured}` : ''
    if (lowerBound && count === 0) {
      return {
        count: null,
        lowerBound: true,
        title: partial ? `Unknown: some CloudNativePG data could not be read.${extra}` : `None found in the workspace read.${extra}`,
      }
    }
    return { count, lowerBound, title: `${lowerBound ? 'At least ' : ''}${count} ${what}.${note}${extra}` }
  }
  switch (screen) {
    case 'overview':
      return at(fleet.attentionCount, 'clusters need attention', withMetrics ? undefined : UNMEASURED_NOTE)
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
  withMetrics = !!active,
}: {
  apiResources: APIResource[] | undefined
  namespaces: string[]
  active?: { screen: CNPGScreen; child?: { label: string; title?: string } }
  /** Count disk use and replication lag too. Off by default outside the CloudNativePG views: every Resources page renders this sidebar. */
  withMetrics?: boolean
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useCNPGNavigate()
  const available = cnpgDiscovered(apiResources)
  const { fleet } = useCNPGFleet(namespaces, available, withMetrics)
  const nsKey = namespaces.join(',')

  return useMemo(() => {
    if (!available) return undefined
    const destinations = CNPG_SCREENS.map((s) => {
      const { count, lowerBound, title } = destinationCount(s.id, fleet, withMetrics)
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
  }, [available, fleet, withMetrics, active?.screen, active?.child?.label, active?.child?.title, navigate, nsKey]) // eslint-disable-line react-hooks/exhaustive-deps
}
