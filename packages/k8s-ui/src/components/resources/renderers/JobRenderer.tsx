import { Clock } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
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

  // Check for pod failures without terminal condition yet. A Job that already
  // completed successfully (Complete condition) keeps its earlier failed pod
  // attempts in status.failed — those are retries, not a problem — so don't flag
  // them, or the drawer would read red while the table badge is calm neutral.
  const completeCondition = conditions.find((c: any) => c.type === 'Complete' && c.status === 'True')
  if (!failedCondition && !completeCondition && status.failed > 0) {
    const remaining = (spec.backoffLimit ?? 6) - status.failed
    if (remaining > 0) {
      problems.push(`${status.failed} pod(s) failed — ${remaining} retries remaining`)
    } else {
      problems.push(`${status.failed} pod(s) failed — no retries remaining`)
    }
  }

  return problems
}

export function JobRenderer({ data }: JobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const startTime = status.startTime ? new Date(status.startTime) : null
  const terminalCondition = conditions.find((c: any) => (c.type === 'Complete' || c.type === 'Failed') && c.status === 'True')
  const finishedAt = status.completionTime || terminalCondition?.lastTransitionTime
  const completionTime = finishedAt ? new Date(finishedAt) : null
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
  const isSuspended = spec.suspend === true

  return (
    <>
      {/* Problems alert */}
      {hasProblems && (
        <AlertBanner variant="error" title="Job Issues" items={problems} />
      )}

      {/* Suspended is an intentional state, not a fault — keep it informational. */}
      {isSuspended && !hasProblems && (
        <AlertBanner variant="info" title="Job Suspended" message="Pods will not be created until this Job is resumed." />
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
