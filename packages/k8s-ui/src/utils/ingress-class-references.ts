import type { ResourceRef } from '../types'

interface IngressClassParametersReference {
  name: string
  kind: string
  apiGroup?: string
  scope?: 'Cluster' | 'Namespace'
  namespace?: string
}

/** IngressClass parameters default to cluster scope and the core API group.
 * A namespaced reference requires its own namespace; the class has none.
 */
export function ingressClassParametersResourceRef(ref: IngressClassParametersReference): ResourceRef | null {
  if (ref.scope === 'Namespace' && !ref.namespace) return null
  return {
    kind: ref.kind,
    group: ref.apiGroup ?? '',
    namespace: ref.scope === 'Namespace' ? ref.namespace! : '',
    name: ref.name,
  }
}
