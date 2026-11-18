import { useMemo } from 'react'
import { TraefikMiddlewareRenderer as BaseTraefikMiddlewareRenderer, type TraefikMiddlewareUsers } from '@skyhook-io/k8s-ui/components/resources/renderers/TraefikMiddlewareRenderer'
import type { ResourceRef, TraefikRoutesResponse } from '@skyhook-io/k8s-ui'
import { isRadarFeatureUnsupported } from '../../../api/radarFeatures'
import { useTraefikMiddlewareUsers } from '../../../api/traefik'

// undefined on an older Radar or while loading (IngressRoutes still syncing
// included); null when the request failed or Radar couldn't read
// IngressRoutes at all, so an unread answer never reads as "no route".
export function middlewareUsers(query: { data?: TraefikRoutesResponse; isError: boolean; error?: unknown; support: string }): TraefikMiddlewareUsers | null | undefined {
  if (query.support === 'unsupported' || isRadarFeatureUnsupported(query.error, 'traefikRoutes')) return undefined
  if (query.isError) return null
  if (!query.data) return undefined
  const state = query.data.coverage?.ingressroutes?.state
  if (state === 'syncing') return undefined
  if (state !== 'full' && state !== 'partial') return null
  return {
    routes: query.data.routes.map((r) => ({ group: r.ref.group, namespace: r.ref.namespace, name: r.ref.name })),
    truncated: query.data.truncated,
    partial: state === 'partial',
  }
}

/** Host wrapper: adds which IngressRoutes use the Middleware. */
export function TraefikMiddlewareRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const { data: users, isError, error, support } = useTraefikMiddlewareUsers(data?.metadata?.namespace ?? '', data?.metadata?.name ?? '')
  const usedBy = useMemo(() => middlewareUsers({ data: users, isError, error, support }), [users, isError, error, support])
  return <BaseTraefikMiddlewareRenderer data={data} onNavigate={onNavigate} usedBy={usedBy} />
}
