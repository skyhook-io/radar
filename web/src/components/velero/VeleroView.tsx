import { useMemo } from 'react'
import { useLocation } from 'react-router-dom'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useVeleroWorkspace } from '../../api/velero'
import { useNamespaces } from '../../api/client'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { useSubstrateSidebarWorkspace } from '../substrate/useSubstrateSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useAgentSandboxSidebarWorkspace } from '../agentsandbox/useAgentSandboxSidebarWorkspace'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { VeleroScreenGate, useVeleroInstallation } from './shared'
import { VeleroOverview } from './VeleroOverview'
import { useVeleroSidebarWorkspace, veleroDiscovered } from './useVeleroSidebarWorkspace'
import { VELERO_DETAIL_KINDS, VELERO_GROUP, parseVeleroRoute } from './routes'
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
  // An object's page highlights its kind's row; the Overview row only the Overview.
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources, active: !detail })
  const plural = detail?.plural
  const selectedKind = useMemo(() => (plural ? { name: plural, kind: VELERO_DETAIL_KINDS[plural].kind, group: VELERO_GROUP } : null), [plural])
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const substrateSidebar = useSubstrateSidebarWorkspace({ apiResources })
  const sandboxSidebar = useAgentSandboxSidebarWorkspace({ apiResources, namespaces })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar, substrateSidebar, sandboxSidebar)
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  const [installation, setInstallation] = useVeleroInstallation(query.data?.installations)
  const namespaceList = useNamespaces()
  const namespaceNames = useMemo(() => (namespaceList.isError ? 'error' as const : namespaceList.data?.map((n) => n.name)), [namespaceList.isError, namespaceList.data])

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} selectedKind={selectedKind} compactNavigation={compactNavigation}>
      {detail ? (
        <VeleroDetailPage target={detail} onOpenResource={onOpenResource} />
      ) : (
        <VeleroScreenGate query={query} discoveryPending={discoveryPending} discovered={discovered}>
          {(data) => <VeleroOverview data={data} installation={installation} onInstallation={setInstallation} onInspect={inspect} inspected={drawerTarget} namespaces={namespaceNames} />}
        </VeleroScreenGate>
      )}
    </WorkspaceFrame>
  )
}
