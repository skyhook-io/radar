import { Clock, Pause } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../drawer-components'
import { formatAge, cronToHuman } from '../resource-utils'

interface CronJobRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CronJobRenderer({ data, onNavigate }: CronJobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}

  // Check for issues or notable states
  const isSuspended = spec.suspend === true
  const hasNeverRun = !status.lastScheduleTime

  // Calculate time since last success vs last schedule
  const lastScheduleAge = status.lastScheduleTime ? new Date().getTime() - new Date(status.lastScheduleTime).getTime() : 0
  const lastSuccessAge = status.lastSuccessfulTime ? new Date().getTime() - new Date(status.lastSuccessfulTime).getTime() : 0
  const recentFailures = lastScheduleAge > 0 && lastSuccessAge > lastScheduleAge

  return (
    <>
      {/* Suspended warning */}
      {isSuspended && (
        <AlertBanner
          variant="warning"
          icon={Pause}
          title="CronJob Suspended"
          message="No new jobs will be scheduled until this CronJob is resumed."
        />
      )}

      {/* Never run warning */}
      {hasNeverRun && !isSuspended && (
        <AlertBanner
          variant="info"
          title="Never Scheduled"
          message="This CronJob has never run. Check the schedule and starting deadline settings."
        />
      )}

      {/* Recent failures warning */}
      {recentFailures && (
        <AlertBanner
          variant="error"
          title="Recent Jobs Failing"
          message={<>Jobs have been scheduled but haven't succeeded recently. Last success: {formatAge(status.lastSuccessfulTime)}. Check job history and pod logs.</>}
        />
      )}

      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property label="Schedule" value={spec.schedule} />
          <Property label="Human" value={cronToHuman(spec.schedule)} />
          <Property label="Suspend" value={spec.suspend ? 'Yes' : 'No'} />
          <Property label="Last Schedule" value={status.lastScheduleTime ? formatAge(status.lastScheduleTime) : 'Never'} />
          <Property label="Last Success" value={status.lastSuccessfulTime ? formatAge(status.lastSuccessfulTime) : 'Never'} />
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
            {status.active.map((job: any) => (
              <div key={job.name} className="text-sm">
                <ResourceLink name={job.name} kind="jobs" namespace={job.namespace || data.metadata?.namespace || ''} onNavigate={onNavigate} />
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
