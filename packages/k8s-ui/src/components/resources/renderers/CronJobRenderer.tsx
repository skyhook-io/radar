import { Clock, Pause } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
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

  // "Recent failures" means the latest scheduled run didn't reach success.
  // Only meaningful once nothing is running — while a job is active the most
  // recent run is simply still in flight, not failing. With concurrencyPolicy
  // Allow an older failed run can overlap a new active one; CronJob status only
  // carries aggregate timestamps, so we accept missing that rare case here (the
  // failed Job still surfaces in the job list / topology) rather than reviving
  // the false positive that fired on every normal in-flight run.
  const activeJobs = status.active?.length || 0
  const lastSchedule = status.lastScheduleTime ? new Date(status.lastScheduleTime).getTime() : 0
  const lastSuccess = status.lastSuccessfulTime ? new Date(status.lastSuccessfulTime).getTime() : 0
  const recentFailures = activeJobs === 0 && lastSchedule > 0 && lastSchedule > lastSuccess

  return (
    <>
      {/* Suspended is an intentional operator state, not a fault — keep it informational. */}
      {isSuspended && (
        <AlertBanner
          variant="info"
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
      {recentFailures && !isSuspended && (
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
