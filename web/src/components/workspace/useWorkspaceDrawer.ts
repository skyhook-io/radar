import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import type { SelectedResource } from '../../types'
import { decodeDrawerTrail, encodeDrawerTrail, sameSelectedResource } from '../../utils/drawer-trail'

/**
 * A workspace's link to the app's single resource drawer. `?drawer=` backs it
 * so a refresh, a shared link and browser Back restore the inspected object,
 * and it carries the chain of objects opened from inside the drawer.
 */
export function useWorkspaceDrawer({
  selectedResource,
  onOpenResource,
  onCloseResource,
}: {
  selectedResource: SelectedResource | null
  onOpenResource: (resource: SelectedResource) => void
  onCloseResource: () => void
}) {
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
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

  return { drawerTarget, inspect, searchParams }
}
