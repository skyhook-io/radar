import { useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useVeleroWorkspace } from '../../api/velero'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { parseVeleroRoute } from './routes'
import { VeleroScreenGate } from './shared'
import { VeleroOverview } from './VeleroOverview'
import { VeleroLocations } from './VeleroLocations'
import { useVeleroSidebarWorkspace, veleroDiscovered } from './useVeleroSidebarWorkspace'

/**
 * The Velero workspace. Lives inside Resources and renders the same Resources
 * sidebar, with the Velero destinations above the Velero kinds. It reads every
 * installation the caller can see; the namespace picker does not apply.
 */
export function VeleroView({ namespaces, selectedResource, onOpenResource, onCloseResource }: WorkspaceScreenProps) {
  const location = useLocation()
  const { screen } = parseVeleroRoute(location.pathname)
  const compactNavigation = useMediaQuery('(max-width: 1100px)')
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Velero is absent; the server is asked then.
  const discovered = veleroDiscovered(apiResources) || discoveryFailed
  const query = useVeleroWorkspace({ enabled: discovered })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources, active: screen })
  const cnpgSidebar = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar)
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  // The installation chips narrow what was read; a namespace that is no longer
  // read drops the narrowing rather than showing an empty screen.
  const [installation, setInstallation] = useState<string | null>(null)
  const installations = query.data?.installations
  useEffect(() => {
    if (installation !== null && installations && !installations.some((i) => i.namespace === installation)) setInstallation(null)
  }, [installation, installations])

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} compactNavigation={compactNavigation}>
      <VeleroScreenGate query={query} discoveryPending={discoveryPending} discovered={discovered}>
        {(data) => {
          const props = { data, installation, onInstallation: setInstallation, onInspect: inspect, inspected: drawerTarget }
          return screen === 'locations' ? <VeleroLocations {...props} /> : <VeleroOverview {...props} />
        }}
      </VeleroScreenGate>
    </WorkspaceFrame>
  )
}
