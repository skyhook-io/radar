import { useQueries } from '@tanstack/react-query'
import { HTTPRouteRenderer as BaseHTTPRouteRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/HTTPRouteRenderer'
import { routeGateways, type GatewayLookup, type GatewayObject } from '@skyhook-io/k8s-ui/utils/host-url'
import { resourceQueryOptions } from '../../../api/client'
import type { ResourceRef } from '../../../types'

const GATEWAY_GROUP = 'gateway.networking.k8s.io'

interface HTTPRouteRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

/** Reads the route's parent Gateways so its hostnames can open with the
 * scheme their listeners serve. Shares the Gateway drawer's query. */
export function HTTPRouteRenderer({ data, onNavigate }: HTTPRouteRendererProps) {
  const refs = routeGateways(data)
  const parentGateways = useQueries({
    queries: refs.map(ref => resourceQueryOptions<GatewayObject>('gateways', ref.namespace, ref.name, GATEWAY_GROUP)),
    combine: (results): GatewayLookup[] => results.map((result, i) => ({
      ...refs[i],
      gateway: result.data?.resource,
      status: result.data?.resource ? 'loaded' : result.isPending ? 'loading' : 'unavailable',
    })),
  })
  return <BaseHTTPRouteRenderer data={data} onNavigate={onNavigate} parentGateways={parentGateways} />
}
