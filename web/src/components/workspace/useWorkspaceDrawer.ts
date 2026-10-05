import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import type { SelectedResource } from '../../types'
import {
  decodeDrawerTrail,
  encodeDrawerTrail,
  sameSelectedResource,
} from '../../utils/drawer-trail'

export function useWorkspaceDrawer(
  selectedResource: SelectedResource | null,
  onOpenResource: (ref: SelectedResource) => void,
  onCloseResource: () => void,
) {
  const location = useLocation()
  const [params, setParams] = useSearchParams()
  const drawerParam = params.get('drawer')
  const trail = useMemo(() => decodeDrawerTrail(drawerParam), [drawerParam])
  const target = trail.at(-1) ?? null
  const lastSynced = useRef<string | null>(null)
  const selectedKey = selectedResource
    ? encodeDrawerTrail([selectedResource])
    : ''
  const targetKey = target ? encodeDrawerTrail([target]) : ''
  useEffect(() => {
    if (targetKey !== (lastSynced.current ?? '')) {
      lastSynced.current = targetKey
      if (target && !sameSelectedResource(target, selectedResource))
        onOpenResource(target)
      else if (!target && selectedResource) onCloseResource()
      return
    }
    if (selectedKey !== targetKey) {
      lastSynced.current = selectedKey
      const next = new URLSearchParams(params)
      if (!selectedResource) next.delete('drawer')
      else {
        const idx = trail.findIndex((r) =>
          sameSelectedResource(r, selectedResource),
        )
        next.set(
          'drawer',
          encodeDrawerTrail(
            idx >= 0 ? trail.slice(0, idx + 1) : [...trail, selectedResource],
          ),
        )
      }
      setParams(next, { replace: true, state: location.state })
    }
  }, [targetKey, selectedKey]) // eslint-disable-line react-hooks/exhaustive-deps
  const inspect = useCallback(
    (resource: SelectedResource) => {
      const next = new URLSearchParams(params)
      next.set('drawer', encodeDrawerTrail([resource]))
      setParams(next, { replace: true, state: location.state })
    },
    [params, setParams, location.state],
  )
  return { drawerTarget: target, inspect }
}
