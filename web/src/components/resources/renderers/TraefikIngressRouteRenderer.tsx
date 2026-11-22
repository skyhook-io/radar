import { useMemo } from 'react'
import { TraefikIngressRouteRenderer as BaseTraefikIngressRouteRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/TraefikIngressRouteRenderer'
import type { ResourceRef, TraefikRoutesResponse } from '@skyhook-io/k8s-ui'
import { isRadarFeatureUnsupported } from '../../../api/radarFeatures'
import { useTraefikRouteFacts } from '../../../api/traefik'

// The answer as the drawer takes it: undefined on an older Radar or while
// loading (the drawer looks as before), null when the request failed.
export function routeFactsProps(query: { data?: TraefikRoutesResponse; isError: boolean; error?: unknown; support: string }) {
  if (query.support === 'unsupported' || isRadarFeatureUnsupported(query.error, 'traefikRoutes')) return { routeFacts: undefined, middlewareUse: undefined }
  if (query.isError) return { routeFacts: null, middlewareUse: undefined }
  if (!query.data) return { routeFacts: undefined, middlewareUse: undefined }
  const middlewareUse: Record<string, number> = {}
  for (const m of query.data.middlewares) middlewareUse[`${m.namespace}/${m.name}`] = m.usedBy
  return { routeFacts: query.data.routes, middlewareUse }
}

/** Host wrapper: adds each rule's request path to the IngressRoute drawer. */
export function TraefikIngressRouteRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const { data: facts, isError, error, support } = useTraefikRouteFacts(data?.metadata?.namespace ?? '', data?.metadata?.name ?? '', String(data?.apiVersion ?? '').split('/')[0])
  const props = useMemo(() => routeFactsProps({ data: facts, isError, error, support }), [facts, isError, error, support])
  return <BaseTraefikIngressRouteRenderer data={data} onNavigate={onNavigate} {...props} />
}
