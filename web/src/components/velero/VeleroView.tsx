import { useLocation } from 'react-router-dom'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useVeleroWorkspace } from '../../api/velero'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { VeleroScreenGate, useVeleroInstallation } from './shared'
import { VeleroOverview } from './VeleroOverview'
import { useVeleroSidebarWorkspace, veleroDiscovered } from './useVeleroSidebarWorkspace'
import { parseVeleroRoute } from './routes'
import { VeleroDetailPage } from './VeleroDetailPage'

/**
 * The Velero Overview, and each Velero object's page. Lives inside Resources and renders the same Resources
 * sidebar, with the Overview above the Velero kinds; the Schedule,
 * BackupStorageLocation, Backup and Restore kinds' lists are Radar's own
 * tables (VeleroKindLists).
 * It reads every installation the caller can see; the namespace picker does
 * not apply.
 */
export function VeleroView({ namespaces, selectedResource, onOpenResource, onCloseResource }: WorkspaceScreenProps) {
  const { detail } = parseVeleroRoute(useLocation().pathname)
  const narrow = useMediaQuery('(max-width: 1100px)')
  const laptop = useMediaQuery('(max-width: 1300px)')
  const compactNavigation = narrow || (laptop && !!detail)
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Velero is absent; the server is asked then.
  const discovered = veleroDiscovered(apiResources) || discoveryFailed
  const query = useVeleroWorkspace({ enabled: discovered })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources, active: true, child: detail ? { label: detail.name, title: `${detail.plural} ${detail.namespace}/${detail.name}` } : undefined })
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar)
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  const [installation, setInstallation] = useVeleroInstallation(query.data?.installations)

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} compactNavigation={compactNavigation}>
      {detail ? (
        <VeleroDetailPage target={detail} onOpenResource={onOpenResource} />
      ) : (
        <VeleroScreenGate query={query} discoveryPending={discoveryPending} discovered={discovered}>
          {(data) => <VeleroOverview data={data} installation={installation} onInstallation={setInstallation} onInspect={inspect} inspected={drawerTarget} />}
        </VeleroScreenGate>
      )}
    </WorkspaceFrame>
  )
}
