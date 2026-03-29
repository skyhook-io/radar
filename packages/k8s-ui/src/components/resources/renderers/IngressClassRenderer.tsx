import { Globe } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface IngressClassRendererProps {
  data: any
}

export function IngressClassRenderer({ data }: IngressClassRendererProps) {
  const spec = data.spec || {}
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
                  ? 'bg-green-500/20 text-green-400'
                  : 'bg-gray-500/20 text-gray-400'
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
            <Property label="Name" value={spec.parameters.name} />
            {spec.parameters.namespace && <Property label="Namespace" value={spec.parameters.namespace} />}
            {spec.parameters.scope && <Property label="Scope" value={spec.parameters.scope} />}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
