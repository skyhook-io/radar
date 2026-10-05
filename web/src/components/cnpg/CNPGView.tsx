import { useCallback } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { ResourcesSidebar, type SelectedKindInfo } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useAPIResources } from '../../api/apiResources'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useResourceCounts } from '../../hooks/useResourceCounts'
import { CNPGOverview } from './CNPGOverview'
import { CNPGProtection } from './CNPGProtection'
import { CNPGDeclarations } from './CNPGDeclarations'
import { CNPGPooling } from './CNPGPooling'
import { CNPGOperator } from './CNPGOperator'
import { CNPGScreenGate } from './shared'
import { CNPGDetailPage } from './CNPGDetailPage'
import { parseCNPGRoute } from './routes'
import { useCNPGFleet, useCNPGSidebarWorkspace } from './useCNPGSidebarWorkspace'
import { useWorkspaceDrawer } from '../workspace/useWorkspaceDrawer'
import { useCNPGNavigate } from './useCNPGNavigate'

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
  const navigate = useCNPGNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const route = parseCNPGRoute(location.pathname)
  const { data: apiResources } = useAPIResources()
  const { data: counts } = useResourceCounts(namespaces)
  const { pinned, togglePin, isPinned } = usePinnedKinds()
  const sidebarWorkspace = useCNPGSidebarWorkspace({
    apiResources,
    namespaces,
    active: { screen: route.screen, child: route.detail ? { label: route.detail.name, title: `${route.detail.plural} ${route.detail.namespace}/${route.detail.name}` } : undefined },
  })
  const { query, fleet } = useCNPGFleet(namespaces)

  const { drawerTarget, inspect } = useWorkspaceDrawer(selectedResource, onOpenResource, onCloseResource)

  const setParams = useCallback(
    (update: Record<string, string | null>) => {
      const params = new URLSearchParams(searchParams)
      for (const [k, v] of Object.entries(update)) {
        if (v === null || v === '') params.delete(k)
        else params.set(k, v)
      }
      setSearchParams(params, { replace: true, state: location.state })
    },
    [searchParams, setSearchParams, location.state],
  )

  const selectKind = useCallback(
    (kind: SelectedKindInfo) => {
      navigate(`/resources/${kind.name}${kind.group ? `?apiGroup=${encodeURIComponent(kind.group)}` : ''}`)
    },
    [navigate],
  )

  return (
    <div className="flex h-full min-h-0 w-full">
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={selectKind}
        apiResources={apiResources}
        resourceCounts={counts?.counts}
        resourceForbidden={counts?.forbidden}
        resourceUnavailable={counts?.unavailable}
        pinned={pinned}
        togglePin={togglePin}
        isPinned={(kind: string, group?: string) => isPinned(kind, group ?? '')}
        categoryWorkspaces={sidebarWorkspace}
      />
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
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
      </div>
    </div>
  )
}
