import { Network } from 'lucide-react'
import { Property, PropertyList, Section } from '../../ui/drawer-components'

interface CalicoTierRendererProps {
  data: any
}

export function CalicoTierRenderer({ data }: CalicoTierRendererProps) {
  const spec = data?.spec ?? {}

  return (
    <Section title="Tier" icon={Network}>
      <PropertyList>
        <Property label="Default Action" value={spec.defaultAction ?? 'Deny'} />
        <Property label="Order" value={spec.order ?? 'Last (lowest precedence)'} />
      </PropertyList>
    </Section>
  )
}
