import { useLocation, useSearchParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import type { SelectedResource } from '../../types'
import { decodeDrawerTrail, encodeDrawerTrail, sameSelectedResource } from '../../utils/drawer-trail'

/**
 * On a workspace screen the drawer URL carries the chain of objects opened
 * from inside the drawer. This is the step back to the previous one, shown for
 * whatever kind is open. `kindLabel` names the previous object's kind from its
 * plural; unnamed plurals show as they are.
 */
export function DrawerTrailBack({ resource, pathPrefix, kindLabel }: { resource: SelectedResource; pathPrefix: string; kindLabel: (plural: string) => string | undefined }) {
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  if (location.pathname !== pathPrefix && !location.pathname.startsWith(`${pathPrefix}/`)) return null
  const trail = decodeDrawerTrail(searchParams.get('drawer'))
  if (trail.length < 2 || !sameSelectedResource(trail[trail.length - 1], resource)) return null
  const prev = trail[trail.length - 2]
  const back = () => {
    const params = new URLSearchParams(searchParams)
    params.set('drawer', encodeDrawerTrail(trail.slice(0, -1)))
    setSearchParams(params, { replace: true, state: location.state })
  }
  return (
    <div className="shrink-0 border-b border-theme-border px-4 py-2">
      <button type="button" onClick={back} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
        <ArrowLeft className="h-3.5 w-3.5" />
        {kindLabel(prev.kind) ?? prev.kind} {prev.name}
      </button>
    </div>
  )
}
