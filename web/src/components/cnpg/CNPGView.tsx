import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
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
import { decodeDrawerTrail, encodeDrawerTrail, parseCNPGRoute, sameResource } from './routes'
import { useCNPGFleet, useCNPGSidebarWorkspace } from './useCNPGSidebarWorkspace'

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
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const route = parseCNPGRoute(location.pathname)
  const { data: apiResources } = useAPIResources()
  const { data: counts } = useResourceCounts(namespaces)
  const { pinned, togglePin, isPinned } = usePinnedKinds()
  const sidebarWorkspace = useCNPGSidebarWorkspace({ apiResources, namespaces, active: { screen: route.screen } })
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
      if (drawerTarget && !sameResource(drawerTarget, selectedResource)) onOpenResource(drawerTarget)
      else if (!drawerTarget && selectedResource) onCloseResource()
      return
    }
    if (selectedKey !== targetKey) {
      lastSynced.current = selectedKey
      const params = new URLSearchParams(searchParams)
      if (!selectedResource) {
        params.delete('drawer')
      } else {
        const idx = trail.findIndex((r) => sameResource(r, selectedResource))
        const next = idx >= 0 ? trail.slice(0, idx + 1) : [...trail, selectedResource]
        params.set('drawer', encodeDrawerTrail(next))
      }
      setSearchParams(params, { replace: true })
    }
  }, [targetKey, selectedKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const inspect = useCallback(
    (resource: SelectedResource) => {
      const params = new URLSearchParams(searchParams)
      params.set('drawer', encodeDrawerTrail([resource]))
      setSearchParams(params, { replace: true })
    },
    [searchParams, setSearchParams],
  )

  const setParams = useCallback(
    (update: Record<string, string | null>) => {
      const params = new URLSearchParams(searchParams)
      for (const [k, v] of Object.entries(update)) {
        if (v === null || v === '') params.delete(k)
        else params.set(k, v)
      }
      setSearchParams(params, { replace: true })
    },
    [searchParams, setSearchParams],
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
      </div>
    </div>
  )
}
