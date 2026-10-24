import { Clock, Pause } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { cronToHuman } from '../resource-utils'
import { TimeValue } from '../../ui/ScheduleValue'
import { formatRelativeAgeTime } from '../../../utils/format'

interface CronJobRendererProps {
  data: {
    metadata?: { namespace?: string }
    spec?: {
      schedule?: string
      timeZone?: string
      suspend?: boolean
      concurrencyPolicy?: string
      startingDeadlineSeconds?: number
      successfulJobsHistoryLimit?: number
      failedJobsHistoryLimit?: number
    }
    status?: {
      lastScheduleTime?: string
      lastSuccessfulTime?: string
      active?: { name: string; namespace?: string }[]
    }
  }
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CronJobRenderer({ data, onNavigate }: CronJobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}

  const isSuspended = spec.suspend === true
  const hasNeverRun = !status.lastScheduleTime

  const activeJobs = status.active?.length || 0
  const lastSchedule = status.lastScheduleTime ? new Date(status.lastScheduleTime).getTime() : 0
  const lastSuccess = status.lastSuccessfulTime ? new Date(status.lastSuccessfulTime).getTime() : 0
  const completionUnverified = activeJobs === 0 && lastSchedule > 0 && lastSchedule > lastSuccess

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
          message="Kubernetes has not recorded a scheduled run. Check the schedule and controller Events."
        />
      )}

      {completionUnverified && !isSuspended && (
        <AlertBanner
          variant="info"
          title="Latest completion not verified"
          message={<>No successful completion is recorded after the latest scheduled run. Last success: {formatRelativeAgeTime(status.lastSuccessfulTime, 'never')}. Inspect the Job history to check the execution outcome.</>}
        />
      )}

      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property label="Schedule" value={spec.schedule} />
          <Property label="Human" value={cronToHuman(spec.schedule ?? '')} />
          <Property label="Time Zone" value={spec.timeZone ?? 'Controller local time (zone not reported)'} />
          <Property label="Suspend" value={spec.suspend ? 'Yes' : 'No'} />
          <Property label="Last Schedule" value={<TimeValue timestamp={status.lastScheduleTime} fallback="Never" />} />
          <Property label="Last Success" value={<TimeValue timestamp={status.lastSuccessfulTime} fallback="Never" />} />
          <Property label="Active Jobs" value={status.active?.length || 0} />
        </PropertyList>
      </Section>

      <Section title="Configuration">
        <PropertyList>
          <Property label="Concurrency" value={spec.concurrencyPolicy || 'Allow'} />
          <Property label="Starting Deadline" value={spec.startingDeadlineSeconds != null ? `${spec.startingDeadlineSeconds}s` : 'None'} />
          <Property label="Success History" value={spec.successfulJobsHistoryLimit ?? 3} />
          <Property label="Failed History" value={spec.failedJobsHistoryLimit ?? 1} />
        </PropertyList>
      </Section>

      {status.active && status.active.length > 0 && (
        <Section title="Active Jobs">
          <div className="space-y-1">
            {status.active.map((job) => (
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
