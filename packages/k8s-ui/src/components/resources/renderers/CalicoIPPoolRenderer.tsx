import { Network } from 'lucide-react'
import { Property, PropertyList, Section } from '../../ui/drawer-components'

interface CalicoIPPoolRendererProps {
  data: any
}

export function CalicoIPPoolRenderer({ data }: CalicoIPPoolRendererProps) {
  const spec = data?.spec ?? {}
  const defaultBlockSize = typeof spec.cidr === 'string' ? spec.cidr.includes(':') ? 122 : 26 : undefined

  return (
    <Section title="IP Pool" icon={Network}>
      <PropertyList>
        <Property label="Allowed Uses" value={(spec.allowedUses ?? ['Workload', 'Tunnel']).join(', ')} />
        <Property label="Assignment Mode" value={spec.assignmentMode ?? 'Automatic'} />
        <Property label="Block Size" value={spec.blockSize ?? defaultBlockSize} />
        <Property label="CIDR" value={spec.cidr} />
        <Property label="IP-in-IP Mode" value={spec.ipipMode ?? 'Never'} />
        <Property label="VXLAN Mode" value={spec.vxlanMode ?? 'Never'} />
        <Property label="NAT Outgoing" value={spec.natOutgoing ? 'Yes' : 'No'} />
        <Property label="Disabled" value={spec.disabled ? 'Yes' : 'No'} />
        <Property label="BGP Export Disabled" value={spec.disableBGPExport ? 'Yes' : 'No'} />
        <Property label="Node Selector" value={spec.nodeSelector ?? 'all()'} />
        <Property label="Namespace Selector" value={spec.namespaceSelector} />
      </PropertyList>
    </Section>
  )
}
