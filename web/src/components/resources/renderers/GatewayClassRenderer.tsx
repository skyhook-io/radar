import { Cpu, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'

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
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400">GatewayClass Not Accepted</div>
              <div className="text-xs text-red-300/80 mt-1">
                {acceptedCond.reason && <span className="font-medium">{acceptedCond.reason}: </span>}
                {acceptedCond.message || 'The gateway class has not been accepted by the controller.'}
              </div>
            </div>
          </div>
        </div>
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
                'px-2 py-0.5 rounded text-xs font-medium',
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
