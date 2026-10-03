import { useLocation, useSearchParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { CNPG_KIND_BY_KEY } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { decodeDrawerTrail, encodeDrawerTrail, sameSelectedResource } from '../../utils/drawer-trail'

const KIND_BY_PLURAL: Record<string, string> = Object.fromEntries(
  Object.values(CNPG_KIND_BY_KEY).map((k) => [k.plural, k.kind]),
)

/**
 * On a CloudNativePG workspace screen the drawer URL carries the chain of
 * objects opened from inside the drawer. This is the step back to the previous
 * one, shown for whatever kind is open (a Pod or Secret reached from a CNPG
 * object included).
 */
export function CNPGDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  if (!location.pathname.startsWith('/cnpg')) return null
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
        {KIND_BY_PLURAL[prev.kind] ?? prev.kind} {prev.name}
      </button>
    </div>
  )
}
