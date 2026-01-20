import { Clock } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'
import { formatAge, cronToHuman } from '../resource-utils'

interface CronJobRendererProps {
  data: any
}

export function CronJobRenderer({ data }: CronJobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}

  return (
    <>
      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property label="Schedule" value={spec.schedule} />
          <Property label="Human" value={cronToHuman(spec.schedule)} />
          <Property label="Suspend" value={spec.suspend ? 'Yes' : 'No'} />
          <Property label="Last Schedule" value={status.lastScheduleTime ? formatAge(status.lastScheduleTime) : 'Never'} />
          <Property label="Active Jobs" value={status.active?.length || 0} />
        </PropertyList>
      </Section>

      <Section title="Configuration">
        <PropertyList>
          <Property label="Concurrency" value={spec.concurrencyPolicy || 'Allow'} />
          <Property label="Starting Deadline" value={spec.startingDeadlineSeconds ? `${spec.startingDeadlineSeconds}s` : 'None'} />
          <Property label="Success History" value={spec.successfulJobsHistoryLimit ?? 3} />
          <Property label="Failed History" value={spec.failedJobsHistoryLimit ?? 1} />
        </PropertyList>
      </Section>

      {status.active?.length > 0 && (
        <Section title="Active Jobs">
          <div className="space-y-1">
            {status.active.map((job: any, i: number) => (
              <div key={i} className="text-sm text-blue-400">{job.name}</div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
