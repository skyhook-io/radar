import { useAPIResources } from '../../api/apiResources'
import { useLocation, useSearchParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { getKindLabel } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import {
  decodeDrawerTrail,
  encodeDrawerTrail,
  sameSelectedResource,
} from '../../utils/drawer-trail'

export function WorkspaceDrawerTrailBack({
  resource,
}: {
  resource: SelectedResource
}) {
  const { data: apiResources } = useAPIResources()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  if (
    !location.pathname.startsWith('/cnpg') &&
    !location.pathname.startsWith('/datum')
  )
    return null
  const trail = decodeDrawerTrail(searchParams.get('drawer'))
  if (
    trail.length < 2 ||
    !sameSelectedResource(trail[trail.length - 1], resource)
  )
    return null
  const prev = trail[trail.length - 2]
  const back = () => {
    const params = new URLSearchParams(searchParams)
    params.set('drawer', encodeDrawerTrail(trail.slice(0, -1)))
    setSearchParams(params, { replace: true, state: location.state })
  }
  return (
    <div className="shrink-0 border-b border-theme-border px-4 py-2">
      <button
        type="button"
        onClick={back}
        className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {getKindLabel(
          apiResources?.find(
            (r) => r.group === (prev.group || '') && r.name === prev.kind,
          )?.kind || prev.kind,
        )}{' '}
        {prev.name}
      </button>
    </div>
  )
}
