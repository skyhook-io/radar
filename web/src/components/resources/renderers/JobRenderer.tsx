import { Clock } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../drawer-components'
import { formatDuration } from '../resource-utils'

interface JobRendererProps {
  data: any
}

// Extract problems from Job status and conditions
function getJobProblems(data: any): string[] {
  const problems: string[] = []
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  // Check for Failed condition
  const failedCondition = conditions.find((c: any) => c.type === 'Failed' && c.status === 'True')
  if (failedCondition) {
    if (failedCondition.reason === 'BackoffLimitExceeded') {
      problems.push(`Job failed: reached backoff limit (${spec.backoffLimit ?? 6} retries)`)
    } else if (failedCondition.reason === 'DeadlineExceeded') {
      problems.push(`Job failed: exceeded active deadline (${spec.activeDeadlineSeconds}s)`)
    } else {
      problems.push(`Job failed: ${failedCondition.reason}${failedCondition.message ? ' - ' + failedCondition.message : ''}`)
    }
  }

  // Check for pod failures without terminal condition yet
  if (!failedCondition && status.failed > 0) {
    const remaining = (spec.backoffLimit ?? 6) - status.failed
    if (remaining > 0) {
      problems.push(`${status.failed} pod(s) failed — ${remaining} retries remaining`)
    } else {
      problems.push(`${status.failed} pod(s) failed — no retries remaining`)
    }
  }

  // Check for suspended
  if (spec.suspend) {
    problems.push('Job is suspended — pods will not be created')
  }

  return problems
}

export function JobRenderer({ data }: JobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const startTime = status.startTime ? new Date(status.startTime) : null
  const completionTime = status.completionTime ? new Date(status.completionTime) : null
  const duration = startTime && completionTime
    ? formatDuration(completionTime.getTime() - startTime.getTime(), true)
    : startTime
    ? formatDuration(Date.now() - startTime.getTime(), true) + ' (running)'
    : null

  // Check for problems
  const problems = getJobProblems(data)
  const hasProblems = problems.length > 0

  // Check if job completed successfully
  const isComplete = conditions.some((c: any) => c.type === 'Complete' && c.status === 'True')

  return (
    <>
      {/* Problems alert */}
      {hasProblems && (
        <AlertBanner variant="error" title="Job Issues" items={problems} />
      )}

      {/* Success banner */}
      {isComplete && !hasProblems && (
        <AlertBanner variant="success" title="Job Completed Successfully" />
      )}

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
