import { Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, LabelSelectorDisplay } from '../../ui/drawer-components'
import { getNvidiaDriverStatus } from '../resource-utils-nvidia'

interface NvidiaDriverRendererProps {
  data: any
}

export function NvidiaDriverRenderer({ data }: NvidiaDriverRendererProps) {
  const status = getNvidiaDriverStatus(data)
  const spec = data.spec || {}

  return (
    <>
      {status.level === 'alert' && (
        <AlertBanner
          variant="warning"
          title="Driver rollout not ready"
          items={['The driver DaemonSet has not reached ready state on all selected nodes.']}
        />
      )}

      <Section title="Driver" icon={Cpu} defaultExpanded>
        <PropertyList>
          <Property
            label="State"
            value={<span className={clsx('badge', status.color)}>{status.text}</span>}
          />
          <Property label="Type" value={spec.driverType} />
          <Property label="Version" value={spec.version} />
          {spec.image && <Property label="Image" value={spec.image} />}
          {spec.repository && <Property label="Repository" value={spec.repository} />}
          {spec.usePrecompiled !== undefined && (
            <Property label="Precompiled" value={spec.usePrecompiled ? 'Yes' : 'No'} />
          )}
        </PropertyList>
      </Section>

      {spec.nodeSelector && Object.keys(spec.nodeSelector).length > 0 && (
        <Section title="Node Selector">
          <LabelSelectorDisplay selector={{ matchLabels: spec.nodeSelector }} />
        </Section>
      )}
    </>
  )
}
