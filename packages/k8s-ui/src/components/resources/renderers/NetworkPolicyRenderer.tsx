import { GitFork } from 'lucide-react'
import { Section } from '../../ui/drawer-components'
import { NetworkPolicyDiagram } from './NetworkPolicyDiagram'

interface NetworkPolicyRendererProps {
  data: any
  staged?: boolean
}

export function NetworkPolicyRenderer({ data, staged = false }: NetworkPolicyRendererProps) {
  return (
    <Section title="Policy Flow" icon={GitFork} defaultExpanded>
      <NetworkPolicyDiagram spec={data.spec || {}} staged={staged} namespace={data.metadata?.namespace} />
    </Section>
  )
}
