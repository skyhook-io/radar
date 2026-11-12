import { useMemo } from 'react'
import { TraefikMiddlewareRenderer as BaseTraefikMiddlewareRenderer, type TraefikMiddlewareUsers } from '@skyhook-io/k8s-ui/components/resources/renderers/TraefikMiddlewareRenderer'
import type { ResourceRef, TraefikRoutesResponse } from '@skyhook-io/k8s-ui'
import { useTraefikMiddlewareUsers } from '../../../api/traefik'

// undefined on an older Radar or while loading; null when the request failed.
export function middlewareUsers(query: { data?: TraefikRoutesResponse; isError: boolean; support: string }): TraefikMiddlewareUsers | null | undefined {
  if (query.support === 'unsupported') return undefined
  if (query.isError) return null
  if (!query.data) return undefined
  return {
    routes: query.data.routes.map((r) => ({ group: r.ref.group, namespace: r.ref.namespace, name: r.ref.name })),
    truncated: query.data.truncated,
    partial: query.data.coverage?.ingressroutes?.state !== 'full',
  }
}

/** Host wrapper: adds which IngressRoutes use the Middleware. */
export function TraefikMiddlewareRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const query = useTraefikMiddlewareUsers(data?.metadata?.namespace ?? '', data?.metadata?.name ?? '')
  const usedBy = useMemo(() => middlewareUsers(query), [query.data, query.isError, query.support])
  return <BaseTraefikMiddlewareRenderer data={data} onNavigate={onNavigate} usedBy={usedBy} />
}
