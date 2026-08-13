import { Network } from 'lucide-react'
import { Property, PropertyList, Section } from '../../ui/drawer-components'

interface CalicoIPPoolRendererProps {
  data: any
}

export function CalicoIPPoolRenderer({ data }: CalicoIPPoolRendererProps) {
  const spec = data?.spec ?? {}

  return (
    <Section title="IP Pool" icon={Network}>
      <PropertyList>
        <Property label="Allowed Uses" value={spec.allowedUses?.join(', ')} />
        <Property label="Assignment Mode" value={spec.assignmentMode} />
        <Property label="Block Size" value={spec.blockSize} />
        <Property label="CIDR" value={spec.cidr} />
        <Property label="NAT Outgoing" value={spec.natOutgoing == null ? undefined : spec.natOutgoing ? 'Yes' : 'No'} />
        <Property label="Node Selector" value={spec.nodeSelector} />
      </PropertyList>
    </Section>
  )
}
