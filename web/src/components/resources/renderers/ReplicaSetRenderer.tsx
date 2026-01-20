import { Server } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList } from '../drawer-components'

interface ReplicaSetRendererProps {
  data: any
}

export function ReplicaSetRenderer({ data }: ReplicaSetRendererProps) {
  const ownerRef = data.metadata?.ownerReferences?.[0]

  return (
    <>
      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property label="Replicas" value={`${data.status?.readyReplicas || 0}/${data.spec?.replicas || 0}`} />
          <Property label="Available" value={data.status?.availableReplicas} />
          {ownerRef && (
            <Property label="Owner" value={`${ownerRef.kind}/${ownerRef.name}`} />
          )}
          <Property label="Revision" value={data.metadata?.annotations?.['deployment.kubernetes.io/revision']} />
        </PropertyList>
      </Section>

      <Section title="Selector">
        <KeyValueBadgeList items={data.spec?.selector?.matchLabels} />
      </Section>
    </>
  )
}
