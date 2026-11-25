import { useLocation } from 'react-router-dom'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'
import { useAPIResources } from '../../api/apiResources'
import { useLonghornWorkspace } from '../../api/longhorn'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { useVeleroSidebarWorkspace } from '../velero/useVeleroSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { LonghornOverview } from './LonghornOverview'
import { LonghornVolumes } from './LonghornVolumes'
import { LonghornScreenGate } from './shared'
import { longhornDiscovered, useLonghornSidebarWorkspace } from './useLonghornSidebarWorkspace'
import { parseLonghornRoute } from './routes'

/** The Longhorn Overview and Volumes screens, inside Resources. The namespace picker does not apply. */
export function LonghornView({ namespaces, selectedResource, onOpenResource, onCloseResource }: WorkspaceScreenProps) {
  const screen = parseLonghornRoute(useLocation().pathname)
  const compactNavigation = useMediaQuery('(max-width: 1100px)')
  const { data: apiResources, isPending: discoveryPending, isError: discoveryFailed } = useAPIResources()
  // Discovery that could not be read does not say Longhorn is absent; the server is asked then.
  const discovered = longhornDiscovered(apiResources) || discoveryFailed
  const query = useLonghornWorkspace({ enabled: discovered })
  const sidebar = useMergedSidebarWorkspaces(
    useCNPGSidebarWorkspace({ apiResources, namespaces }),
    useVeleroSidebarWorkspace({ apiResources }),
    useLonghornSidebarWorkspace({ apiResources, active: screen }),
  )
  const { drawerTarget, inspect } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebar} compactNavigation={compactNavigation}>
      <LonghornScreenGate query={query} discoveryPending={discoveryPending} discovered={discovered}>
        {(data) =>
          screen === 'volumes' ? (
            <LonghornVolumes data={data} onInspect={inspect} inspected={drawerTarget} />
          ) : (
            <LonghornOverview data={data} onInspect={inspect} inspected={drawerTarget} />
          )
        }
      </LonghornScreenGate>
    </WorkspaceFrame>
  )
}
