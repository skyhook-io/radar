import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { DialogPortal, ResourcesSidebar, type SelectedKindInfo } from '@skyhook-io/k8s-ui'
import { PanelLeft, X } from 'lucide-react'
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
import { CNPGCreateClusterDialog } from './CNPGCreateClusterDialog'
import { CNPGDetailPage } from './CNPGDetailPage'
import { parseCNPGRoute } from './routes'
import { useCNPGFleet, useCNPGSidebarWorkspace } from './useCNPGSidebarWorkspace'
import { decodeDrawerTrail, encodeDrawerTrail, sameSelectedResource } from '../../utils/drawer-trail'
import { useCNPGNavigate } from './useCNPGNavigate'
import { useCNPGScreenParams } from './useCNPGScreenParams'
import { useMediaQuery } from '../../hooks/useMediaQuery'

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
  const laptop = useMediaQuery('(max-width: 1300px)')
  const narrow = useMediaQuery('(max-width: 1100px)')
  const compactNavigation = narrow || (laptop && !!route.detail)
  const [navigationOpen, setNavigationOpen] = useState(false)
  useEffect(() => setNavigationOpen(false), [location.pathname, location.search, compactNavigation])
  const { data: apiResources } = useAPIResources()
  const { data: counts } = useResourceCounts(namespaces)
  const { pinned, togglePin, isPinned } = usePinnedKinds()
  const sidebarWorkspace = useCNPGSidebarWorkspace({
    apiResources,
    namespaces,
    active: { screen: route.screen, child: route.detail ? { label: route.detail.name, title: `${route.detail.plural} ${route.detail.namespace}/${route.detail.name}` } : undefined },
  })
  const { query, fleet } = useCNPGFleet(namespaces)

  const drawerParam = searchParams.get('drawer')
  const trail = useMemo(() => decodeDrawerTrail(drawerParam), [drawerParam])
  const drawerTarget = trail.length > 0 ? trail[trail.length - 1] : null

  // Two-way sync between ?drawer= and the app drawer. Whichever side changed
  // since the last sync wins, so URL navigation (Back, a pasted link) opens the
  // drawer and drawer navigation (close, a link inside it) rewrites the URL.
  const lastSynced = useRef<string | null>(null)
  const selectedKey = selectedResource ? encodeDrawerTrail([selectedResource]) : ''
  const targetKey = drawerTarget ? encodeDrawerTrail([drawerTarget]) : ''
  useEffect(() => {
    if (targetKey !== (lastSynced.current ?? '')) {
      lastSynced.current = targetKey
      if (drawerTarget && !sameSelectedResource(drawerTarget, selectedResource)) onOpenResource(drawerTarget)
      else if (!drawerTarget && selectedResource) onCloseResource()
      return
    }
    if (selectedKey !== targetKey) {
      lastSynced.current = selectedKey
      const params = new URLSearchParams(searchParams)
      if (!selectedResource) {
        params.delete('drawer')
      } else {
        const idx = trail.findIndex((r) => sameSelectedResource(r, selectedResource))
        const next = idx >= 0 ? trail.slice(0, idx + 1) : [...trail, selectedResource]
        params.set('drawer', encodeDrawerTrail(next))
      }
      setSearchParams(params, { replace: true, state: location.state })
    }
  }, [targetKey, selectedKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const inspect = useCallback(
    (resource: SelectedResource) => {
      const params = new URLSearchParams(searchParams)
      params.set('drawer', encodeDrawerTrail([resource]))
      setSearchParams(params, { replace: true, state: location.state })
    },
    [searchParams, setSearchParams, location.state],
  )

  const [, setParams] = useCNPGScreenParams()
  const [creating, setCreating] = useState(false)

  const selectKind = useCallback(
    (kind: SelectedKindInfo) => {
      navigate(`/resources/${kind.name}${kind.group ? `?apiGroup=${encodeURIComponent(kind.group)}` : ''}`)
    },
    [navigate],
  )

  const sidebar = <ResourcesSidebar
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
        className={compactNavigation ? '!w-full !border-r-0' : undefined}
      />
  return (
    <div className="flex h-full min-h-0 w-full">
      {!compactNavigation && sidebar}
      <DialogPortal open={navigationOpen && compactNavigation} onClose={() => setNavigationOpen(false)} ariaLabel="Resource navigation" className="flex h-[min(80vh,44rem)] w-full max-w-md flex-col overflow-hidden">
        <div className="flex items-center justify-between border-b border-theme-border px-4 py-3"><h2 className="font-medium text-theme-text-primary">Resources</h2><button type="button" aria-label="Close resource navigation" onClick={() => setNavigationOpen(false)} className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover"><X className="h-4 w-4" /></button></div>
        <div className="flex min-h-0 flex-1">{sidebar}</div>
      </DialogPortal>
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
        {compactNavigation && <div className="border-b border-theme-border px-5 py-2"><button type="button" aria-haspopup="dialog" aria-expanded={navigationOpen} onClick={() => setNavigationOpen(true)} className="btn-secondary inline-flex items-center gap-1.5 px-2.5 py-1 text-xs"><PanelLeft className="h-3.5 w-3.5" />Resources</button></div>}
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
      </div>
      {creating && (
        <CNPGCreateClusterDialog namespaces={namespaces} onClose={() => setCreating(false)} onCreated={inspect} />
      )}
    </div>
  )
}
