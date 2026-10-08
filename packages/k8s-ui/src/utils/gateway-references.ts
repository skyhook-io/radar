import type { ResourceRef } from '../types'

/** Gateway references name namespaced objects. Unlike ObjectReference, their
 * namespace defaults to the referring Route and their group/kind have defaults.
 */
interface GatewayObjectReference {
  name: string
  group?: string
  kind?: string
  namespace?: string
}

export function gatewayBackendResourceRef(ref: GatewayObjectReference, routeNamespace: string): ResourceRef {
  return {
    kind: ref.kind ?? 'Service',
    group: ref.group ?? '',
    namespace: ref.namespace ?? routeNamespace,
    name: ref.name,
  }
}

export function gatewayParentResourceRef(ref: GatewayObjectReference, routeNamespace: string): ResourceRef {
  return {
    kind: ref.kind ?? 'Gateway',
    group: ref.group ?? 'gateway.networking.k8s.io',
    namespace: ref.namespace ?? routeNamespace,
    name: ref.name,
  }
}
