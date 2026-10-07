import { Globe } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ResourceLink } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import { ingressClassParametersResourceRef } from '../../../utils/ingress-class-references'
import { BADGE_INACTIVE } from '../../../utils/badge-colors'

interface IngressClassRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function IngressClassRenderer({ data, onNavigate }: IngressClassRendererProps) {
  const spec = data.spec || {}
  const parametersRef = spec.parameters ? ingressClassParametersResourceRef(spec.parameters) : null
  const annotations = data.metadata?.annotations || {}

  const isDefault = annotations['ingressclass.kubernetes.io/is-default-class'] === 'true'

  return (
    <>
      <Section title="Ingress Class" icon={Globe}>
        <PropertyList>
          <Property label="Controller" value={spec.controller} />
          <Property
            label="Default"
            value={
              <span className={clsx(
                'badge',
                isDefault
                  ? 'status-green'
                  : BADGE_INACTIVE
              )}>
                {isDefault ? 'Yes' : 'No'}
              </span>
            }
          />
        </PropertyList>
      </Section>

      {spec.parameters && (
        <Section title="Parameters Reference">
          <PropertyList>
            {spec.parameters.apiGroup && <Property label="API Group" value={spec.parameters.apiGroup} />}
            <Property label="Kind" value={spec.parameters.kind} />
            <Property label="Name" value={parametersRef
              ? <ResourceLink {...parametersRef} onNavigate={onNavigate} />
              : spec.parameters.name
            } />
            {spec.parameters.namespace && <Property label="Namespace" value={spec.parameters.namespace} />}
            {spec.parameters.scope && <Property label="Scope" value={spec.parameters.scope} />}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
