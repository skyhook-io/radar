import { Server } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList, ConditionsSection, AlertBanner } from '../drawer-components'

interface ReplicaSetRendererProps {
  data: any
}

// Extract problems from ReplicaSet status
function getReplicaSetProblems(data: any): string[] {
  const problems: string[] = []
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const ready = status.readyReplicas || 0
  const desired = spec.replicas || 0
  const available = status.availableReplicas || 0

  // Check replica counts
  if (desired > 0 && ready < desired) {
    problems.push(`${desired - ready} of ${desired} pods are not ready`)
  }

  if (desired > 0 && available < desired) {
    problems.push(`${desired - available} pods are not available`)
  }

  // Check conditions
  for (const cond of conditions) {
    if (cond.status === 'False' && cond.message) {
      problems.push(`${cond.type}: ${cond.message}`)
    }
    if (cond.status === 'True' && cond.type === 'ReplicaFailure' && cond.message) {
      problems.push(cond.message)
    }
  }

  return problems
}

export function ReplicaSetRenderer({ data }: ReplicaSetRendererProps) {
  const ownerRef = data.metadata?.ownerReferences?.[0]
  const problems = getReplicaSetProblems(data)
  const hasProblems = problems.length > 0

  return (
    <>
      {/* Problems alert */}
      {hasProblems && (
        <AlertBanner variant="error" title="Issues Detected" items={problems} />
      )}

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

      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}
