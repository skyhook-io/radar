import { Cpu, Boxes } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'
import {
  getNvidiaClusterPolicyStatus,
  getNvidiaClusterPolicyEnabledComponents,
  getNvidiaClusterPolicyMigStrategy,
} from '../resource-utils-nvidia'
import { healthColors } from '../resource-utils'

interface NvidiaClusterPolicyRendererProps {
  data: any
}

export function NvidiaClusterPolicyRenderer({ data }: NvidiaClusterPolicyRendererProps) {
  const status = getNvidiaClusterPolicyStatus(data)
  const components = getNvidiaClusterPolicyEnabledComponents(data)
  const mig = getNvidiaClusterPolicyMigStrategy(data)

  return (
    <>
      {status.level === 'alert' && (
        <AlertBanner
          variant="warning"
          title="GPU Operator not ready"
          items={['One or more operator components have not reached ready state.']}
        />
      )}

      <Section title="Operator Status" icon={Cpu} defaultExpanded>
        <PropertyList>
          <Property
            label="State"
            value={<span className={clsx('badge', status.color)}>{status.text}</span>}
          />
          {data.status?.namespace && <Property label="Operator Namespace" value={data.status.namespace} />}
          {mig !== '-' && <Property label="MIG Strategy" value={mig} />}
        </PropertyList>
      </Section>

      {components.length > 0 && (
        <Section title={`Components (${components.length})`} icon={Boxes} defaultExpanded>
          <div className="flex flex-wrap gap-1.5">
            {components.map((c) => (
              <span
                key={c.label}
                className={clsx('badge', c.enabled ? healthColors.healthy : healthColors.neutral)}
              >
                {c.label}{c.enabled ? '' : ' (off)'}
              </span>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
