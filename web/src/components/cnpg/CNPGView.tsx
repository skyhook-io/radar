import { useState } from 'react'
import { useLocation } from 'react-router-dom'
import type { SelectedResource } from '../../types'
import { useAPIResources } from '../../api/apiResources'
import { CNPGOverview } from './CNPGOverview'
import { CNPGProtection } from './CNPGProtection'
import { CNPGDeclarations } from './CNPGDeclarations'
import { CNPGPooling } from './CNPGPooling'
import { CNPGOperator } from './CNPGOperator'
import { CNPGScreenGate } from './shared'
import { CNPGCreateClusterDialog } from './CNPGCreateClusterDialog'
import { CNPGDetailPage } from './CNPGDetailPage'
import { parseCNPGRoute } from './routes'
import { useCNPGFleet, useCNPGSidebarWorkspace } from './useCNPGSidebarWorkspace'
import { useCNPGScreenParams } from './useCNPGScreenParams'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { useVeleroSidebarWorkspace } from '../velero/useVeleroSidebarWorkspace'
import { useSubstrateSidebarWorkspace } from '../substrate/useSubstrateSidebarWorkspace'
import { WorkspaceFrame } from '../workspace/WorkspaceFrame'
import { useMergedSidebarWorkspaces } from '../workspace/sidebar'
import { useAgentSandboxSidebarWorkspace } from '../agentsandbox/useAgentSandboxSidebarWorkspace'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'

interface CNPGViewProps {
  namespaces: string[]
  selectedResource: SelectedResource | null
  onOpenResource: (resource: SelectedResource) => void
  onCloseResource: () => void
  onClearNamespaces: () => void
}

/**
 * The CloudNativePG workspace. Lives inside Resources (the rail keeps
 * Resources highlighted) and renders the same Resources sidebar, with the
 * workspace destinations above the exact CNPG kinds.
 *
 * The drawer is the app's single resource drawer. `?drawer=` backs it so a
 * refresh, a shared link and browser Back restore the inspected object.
 */
export function CNPGView({ namespaces, selectedResource, onOpenResource, onCloseResource, onClearNamespaces }: CNPGViewProps) {
  const location = useLocation()
  const route = parseCNPGRoute(location.pathname)
  const laptop = useMediaQuery('(max-width: 1300px)')
  const narrow = useMediaQuery('(max-width: 1100px)')
  const compactNavigation = narrow || (laptop && !!route.detail)
  const { data: apiResources } = useAPIResources()
  const cnpgSidebar = useCNPGSidebarWorkspace({
    apiResources,
    namespaces,
    active: { screen: route.screen, child: route.detail ? { label: route.detail.name, title: `${route.detail.plural} ${route.detail.namespace}/${route.detail.name}` } : undefined },
  })
  const veleroSidebar = useVeleroSidebarWorkspace({ apiResources })
  const substrateSidebar = useSubstrateSidebarWorkspace({ apiResources })
  const sandboxSidebar = useAgentSandboxSidebarWorkspace({ apiResources, namespaces })
  const sidebarWorkspace = useMergedSidebarWorkspaces(cnpgSidebar, veleroSidebar, substrateSidebar, sandboxSidebar)
  const { query, fleet } = useCNPGFleet(namespaces)
  const { drawerTarget, inspect, searchParams } = useWorkspaceDrawer({ selectedResource, onOpenResource, onCloseResource })

  const [, setParams] = useCNPGScreenParams()
  const [creating, setCreating] = useState(false)

  return (
    <WorkspaceFrame namespaces={namespaces} categoryWorkspaces={sidebarWorkspace} compactNavigation={compactNavigation}>
      {route.detail ? (
        <CNPGDetailPage target={route.detail} namespaces={namespaces} onOpenResource={onOpenResource} />
      ) : (
      <CNPGScreenGate query={query} fleet={fleet}>
        {(data, readyFleet) => {
          const props = {
            data,
            fleet: readyFleet,
            namespaces,
            searchParams,
            onSetParams: setParams,
            onInspect: inspect,
            inspected: drawerTarget,
            onClearNamespaces,
            onCreate: () => setCreating(true),
          }
          switch (route.screen) {
            case 'protection':
              return <CNPGProtection {...props} />
            case 'declarations':
              return <CNPGDeclarations {...props} />
            case 'pooling':
              return <CNPGPooling {...props} />
            case 'operator':
              return <CNPGOperator {...props} />
            default:
              return <CNPGOverview {...props} />
          }
        }}
      </CNPGScreenGate>
      )}
      {creating && (
        <CNPGCreateClusterDialog namespaces={namespaces} onClose={() => setCreating(false)} onCreated={inspect} />
      )}
    </WorkspaceFrame>
  )
}
