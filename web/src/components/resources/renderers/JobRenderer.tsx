import { Clock } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'
import { formatDuration } from '../resource-utils'

interface JobRendererProps {
  data: any
}

export function JobRenderer({ data }: JobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}

  const startTime = status.startTime ? new Date(status.startTime) : null
  const completionTime = status.completionTime ? new Date(status.completionTime) : null
  const duration = startTime && completionTime
    ? formatDuration(completionTime.getTime() - startTime.getTime(), true)
    : startTime
    ? formatDuration(Date.now() - startTime.getTime(), true) + ' (running)'
    : null

  return (
    <>
      <Section title="Status" icon={Clock}>
        <PropertyList>
          <Property label="Succeeded" value={status.succeeded || 0} />
          <Property label="Failed" value={status.failed || 0} />
          <Property label="Active" value={status.active || 0} />
          <Property label="Completions" value={`${status.succeeded || 0}/${spec.completions || 1}`} />
          {duration && <Property label="Duration" value={duration} />}
        </PropertyList>
      </Section>

      <Section title="Configuration">
        <PropertyList>
          <Property label="Parallelism" value={spec.parallelism || 1} />
          <Property label="Completions" value={spec.completions || 1} />
          <Property label="Backoff Limit" value={spec.backoffLimit ?? 6} />
          {spec.activeDeadlineSeconds && <Property label="Deadline" value={`${spec.activeDeadlineSeconds}s`} />}
          {spec.ttlSecondsAfterFinished !== undefined && (
            <Property label="TTL After Finish" value={`${spec.ttlSecondsAfterFinished}s`} />
          )}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
