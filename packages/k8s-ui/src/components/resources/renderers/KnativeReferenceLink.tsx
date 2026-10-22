import { ResourceLink } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types/core'
import { getKnativeReference } from '../resource-utils-knative'

export function KnativeReferenceLink({ reference, namespace, onNavigate }: {
  reference: any
  namespace: string
  onNavigate?: (ref: ResourceRef) => void
}) {
  const resolved = getKnativeReference(reference, namespace)
  return resolved
    ? <ResourceLink {...resolved} onNavigate={onNavigate} />
    : <span className="text-theme-text-primary">{reference?.name || '-'}</span>
}
