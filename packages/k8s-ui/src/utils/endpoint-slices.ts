import type { ResourceRef } from '../types'
import { apiVersionToGroup } from './navigation'

interface ServiceMetadata {
  namespace?: string
  name?: string
  uid?: string
}
interface EndpointSliceIdentity {
  apiVersion?: string
  kind?: string
  metadata?: ServiceMetadata & {
    labels?: Record<string, string>
    ownerReferences?: Array<{ apiVersion: string; kind: string; name: string; uid: string; controller?: boolean }>
  }
}

export interface EndpointSliceServiceAssociation extends ResourceRef {
  source: 'label' | 'ownerReference'
  uid?: string
}

/** Labels define the Service's published slice set. Exact core owner metadata
 * can identify an unlabeled slice, but ambiguous owners establish no association.
 */
export function endpointSliceServiceAssociation(slice: EndpointSliceIdentity): EndpointSliceServiceAssociation | null {
  const metadata = slice.metadata
  if (slice.kind !== 'EndpointSlice' || apiVersionToGroup(slice.apiVersion) !== 'discovery.k8s.io' || !metadata?.namespace) return null
  const label = metadata.labels?.['kubernetes.io/service-name']
  if (label) return { kind: 'Service', group: '', namespace: metadata.namespace, name: label, source: 'label' }
  const owners = (metadata.ownerReferences ?? []).filter(ref => ref.apiVersion === 'v1' && ref.kind === 'Service' && ref.name && ref.uid)
  const controllers = owners.filter(ref => ref.controller === true)
  const candidates = controllers.length ? controllers : owners
  const distinct = new Map(candidates.map(ref => [`${ref.name}/${ref.uid}`, ref]))
  if (distinct.size !== 1) return null
  const owner = distinct.values().next().value!
  return { kind: 'Service', group: '', namespace: metadata.namespace, name: owner.name, source: 'ownerReference', uid: owner.uid }
}

export function endpointSliceMatchesService(slice: EndpointSliceIdentity, service: { metadata?: ServiceMetadata }): boolean {
  const association = endpointSliceServiceAssociation(slice)
  const metadata = service.metadata
  return !!association && !!metadata && association.namespace === metadata.namespace && association.name === metadata.name
    && (association.source !== 'ownerReference' || !metadata.uid || association.uid === metadata.uid)
}
