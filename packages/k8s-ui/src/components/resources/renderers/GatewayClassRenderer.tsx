import { Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'

interface GatewayClassRendererProps {
  data: any
}

export function GatewayClassRenderer({ data }: GatewayClassRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []

  const acceptedCond = conditions.find((c: any) => c.type === 'Accepted')
  const isAccepted = acceptedCond?.status === 'True'
  const isNotAccepted = acceptedCond?.status === 'False'

  return (
    <>
      {/* Problem detection alert */}
      {isNotAccepted && (
        <AlertBanner
          variant="error"
          title="GatewayClass Not Accepted"
          message={<>{acceptedCond.reason && <span className="font-medium">{acceptedCond.reason}: </span>}{acceptedCond.message || 'The gateway class has not been accepted by the controller.'}</>}
        />
      )}

      {/* Controller section */}
      <Section title="Gateway Class" icon={Cpu}>
        <PropertyList>
          <Property label="Controller" value={spec.controllerName} />
          {spec.description && <Property label="Description" value={spec.description} />}
          <Property
            label="Accepted"
            value={
              <span className={clsx(
                'badge',
                isAccepted
                  ? 'bg-green-500/20 text-green-400'
                  : isNotAccepted
                    ? 'bg-red-500/20 text-red-400'
                    : 'bg-gray-500/20 text-gray-400'
              )}>
                {isAccepted ? 'True' : isNotAccepted ? 'False' : 'Unknown'}
              </span>
            }
          />
        </PropertyList>
      </Section>

      {/* Parameter references (if any) */}
      {spec.parametersRef && (
        <Section title="Parameters Reference">
          <PropertyList>
            <Property label="Group" value={spec.parametersRef.group} />
            <Property label="Kind" value={spec.parametersRef.kind} />
            <Property label="Name" value={spec.parametersRef.name} />
            {spec.parametersRef.namespace && <Property label="Namespace" value={spec.parametersRef.namespace} />}
          </PropertyList>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
