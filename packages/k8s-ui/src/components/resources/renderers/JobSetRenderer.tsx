import { Activity, Boxes, Network as NetworkIcon, ShieldCheck } from 'lucide-react'
import {
  ConditionsSection,
  ProblemAlerts,
  Property,
  PropertyList,
  Section,
  type ConditionTone,
  type Problem,
} from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { getJobSetStatus } from '../resource-utils-jobset-lws'

interface JobSetRendererProps {
  data: any
}

export function getJobSetConditionTone(condition: any): ConditionTone | undefined {
  if (condition?.status !== 'True' && condition?.status !== 'False') return 'unknown'

  switch (condition.type) {
    case 'Failed':
      return condition.status === 'True' ? 'fail' : 'ok'
    case 'Completed':
      return condition.status === 'True' ? 'ok' : 'unknown'
    case 'Suspended':
      return condition.status === 'True' ? 'warning' : 'ok'
    case 'RestartingJobSet':
      return condition.status === 'True' ? 'warning' : 'ok'
    case 'StartupPolicyInProgress':
      return condition.status === 'True' ? 'unknown' : 'ok'
    case 'StartupPolicyCompleted':
      return condition.status === 'True' ? 'ok' : 'unknown'
    default:
      return undefined
  }
}

function jobIndexRange(replicas: unknown): string | undefined {
  if (typeof replicas !== 'number') return undefined
  if (replicas <= 0) return 'None'
  return replicas === 1 ? '0' : `0–${replicas - 1}`
}

function sumCountArray(value: unknown): number | undefined {
  if (!Array.isArray(value)) return undefined
  return value.reduce((total: number, count: unknown) => total + (typeof count === 'number' ? count : 0), 0)
}

export function JobSetRenderer({ data }: JobSetRendererProps) {
  const spec = data?.spec || {}
  const status = data?.status || {}
  const conditions: any[] = Array.isArray(status.conditions) ? status.conditions : []
  const replicatedJobs: any[] = Array.isArray(spec.replicatedJobs) ? spec.replicatedJobs : []
  const replicatedJobsStatus: any[] = Array.isArray(status.replicatedJobsStatus) ? status.replicatedJobsStatus : []
  const statusByName = new Map(replicatedJobsStatus.map((entry) => [entry?.name, entry]))
  const displayedStatus = getJobSetStatus(data)
  const failedCondition = conditions.find((condition) => condition?.type === 'Failed' && condition?.status === 'True')
  const problems: Problem[] =
    displayedStatus.level === 'unhealthy'
      ? [
          {
            color: 'red',
            message: failedCondition?.message || failedCondition?.reason || 'The JobSet reached a terminal failed state.',
          },
        ]
      : []
  const successPolicy = spec.successPolicy
  const failurePolicy = spec.failurePolicy
  const failureRules: any[] = Array.isArray(failurePolicy?.rules) ? failurePolicy.rules : []
  const countedRestarts =
    (typeof status.restartsCountTowardsMax === 'number' ? status.restartsCountTowardsMax : 0) +
    replicatedJobsStatus.reduce((total, entry) => total + (sumCountArray(entry?.jobRestartsCountTowardsMax) ?? 0), 0)
  const coordinator = spec.coordinator
  const network = spec.network

  return (
    <>
      <ProblemAlerts problems={problems} />

      <Section title="JobSet status" icon={Activity}>
        <PropertyList>
          <Property label="State" value={<span className={`badge ${displayedStatus.color}`}>{displayedStatus.text}</span>} />
          <Property label="Terminal state" value={status.terminalState} />
          <Property label="Global restarts" value={status.restarts} />
          <Property
            label="Restart budget used"
            value={typeof failurePolicy?.maxRestarts === 'number' ? `${countedRestarts} / ${failurePolicy.maxRestarts}` : undefined}
          />
          <Property label="Suspended" value={spec.suspend === undefined ? undefined : spec.suspend ? 'Yes' : 'No'} />
          <Property label="Managed by" value={spec.managedBy} />
          <Property
            label="Delete after finish"
            value={spec.ttlSecondsAfterFinished === undefined ? undefined : `${spec.ttlSecondsAfterFinished}s`}
          />
        </PropertyList>
      </Section>

      {replicatedJobs.length > 0 && (
        <Section title={`Replicated jobs (${replicatedJobs.length})`} icon={Boxes}>
          <div className="space-y-2">
            {replicatedJobs.map((replicatedJob, index) => {
              const name = replicatedJob?.name || `Role ${index + 1}`
              const roleStatus = statusByName.get(replicatedJob?.name)
              const template = replicatedJob?.template?.spec || {}
              const dependencies: any[] = Array.isArray(replicatedJob?.dependsOn) ? replicatedJob.dependsOn : []
              const replicas = replicatedJob?.replicas ?? 1
              const ready =
                typeof roleStatus?.ready === 'number' && typeof replicas === 'number'
                  ? `${roleStatus.ready}/${replicas}`
                  : roleStatus?.ready
              const counts = [
                { label: 'Ready', value: ready },
                { label: 'Active', value: roleStatus?.active },
                { label: 'Succeeded', value: roleStatus?.succeeded },
                { label: 'Failed', value: roleStatus?.failed },
                { label: 'Suspended', value: roleStatus?.suspended },
              ].filter((count) => count.value !== undefined && count.value !== null)
              const jobRestarts = sumCountArray(roleStatus?.jobRestarts)

              return (
                <div key={`${name}-${index}`} className="card-inner space-y-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium text-theme-text-primary">{name}</span>
                    {replicatedJob?.groupName && (
                      <Badge tone="structural" size="sm">
                        Group {replicatedJob.groupName}
                      </Badge>
                    )}
                  </div>

                  <PropertyList>
                    <Property label="Jobs" value={replicas} />
                    <Property label="Job indexes" value={jobIndexRange(replicas)} />
                    <Property label="Parallelism / Job" value={template.parallelism} />
                    <Property label="Completions / Job" value={template.completions} />
                    <Property label="Completion mode" value={template.completionMode} />
                    <Property label="Backoff limit" value={template.backoffLimit} />
                    <Property label="Individual restarts" value={jobRestarts} />
                  </PropertyList>

                  {counts.length > 0 ? (
                    <div className="flex flex-wrap gap-x-4 gap-y-1 border-t border-theme-border-subtle pt-2 text-xs text-theme-text-secondary">
                      {counts.map((count) => (
                        <span key={count.label}>
                          <span className="text-theme-text-tertiary">{count.label}</span>{' '}
                          <span className="font-medium text-theme-text-primary">{String(count.value)}</span>
                        </span>
                      ))}
                    </div>
                  ) : (
                    <div className="border-t border-theme-border-subtle pt-2 text-xs text-theme-text-tertiary">
                      Controller status has not been reported for this role.
                    </div>
                  )}

                  {dependencies.length > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5 text-xs">
                      <span className="text-theme-text-tertiary">Starts after</span>
                      {dependencies.map((dependency, dependencyIndex) => (
                        <Badge key={`${dependency?.name || 'dependency'}-${dependencyIndex}`} tone="structural" size="sm">
                          {dependency?.name || 'Unknown role'}
                          {dependency?.status ? ` · ${dependency.status}` : ''}
                        </Badge>
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {(successPolicy || failurePolicy || spec.startupPolicy) && (
        <Section title="Completion and restart policies" icon={ShieldCheck}>
          <PropertyList>
            {successPolicy?.operator && (
              <Property
                label="Success"
                value={
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <Badge tone="structural" size="sm">
                      {successPolicy.operator}
                    </Badge>
                    <span>
                      across{' '}
                      {Array.isArray(successPolicy.targetReplicatedJobs) && successPolicy.targetReplicatedJobs.length > 0
                        ? successPolicy.targetReplicatedJobs.join(', ')
                        : 'all replicated jobs'}
                    </span>
                  </span>
                }
              />
            )}
            <Property label="Restart limit" value={failurePolicy?.maxRestarts} />
            <Property label="Restart strategy" value={failurePolicy?.restartStrategy} />
            {failurePolicy && <Property label="No matching rule" value="RestartJobSet" />}
            <Property label="Startup order" value={spec.startupPolicy?.startupPolicyOrder} />
          </PropertyList>

          {failureRules.length > 0 && (
            <div className="mt-3 space-y-2">
              {failureRules.map((rule, index) => {
                const targets =
                  Array.isArray(rule?.targetReplicatedJobs) && rule.targetReplicatedJobs.length > 0
                    ? rule.targetReplicatedJobs.join(', ')
                    : 'All replicated jobs'
                const reasons =
                  Array.isArray(rule?.onJobFailureReasons) && rule.onJobFailureReasons.length > 0
                    ? rule.onJobFailureReasons.join(', ')
                    : 'Any Job failure reason'
                const messagePatterns =
                  Array.isArray(rule?.onJobFailureMessagePatterns) && rule.onJobFailureMessagePatterns.length > 0
                    ? rule.onJobFailureMessagePatterns.join(', ')
                    : 'Any failure message'

                return (
                  <div key={`${rule?.name || 'rule'}-${index}`} className="card-inner space-y-1.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-theme-text-primary">
                        {index + 1}. {rule?.name || `Rule ${index + 1}`}
                      </span>
                      {rule?.action && (
                        <Badge tone="structural" size="sm">
                          {rule.action}
                        </Badge>
                      )}
                    </div>
                    <div className="text-xs text-theme-text-secondary">
                      <span className="text-theme-text-tertiary">Applies to:</span> {targets}
                    </div>
                    <div className="text-xs text-theme-text-secondary">
                      <span className="text-theme-text-tertiary">Reasons:</span> {reasons}
                    </div>
                    <div className="break-words text-xs text-theme-text-secondary">
                      <span className="text-theme-text-tertiary">Messages:</span> {messagePatterns}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </Section>
      )}

      {(coordinator || network) && (
        <Section title="Coordination and network" icon={NetworkIcon}>
          <PropertyList>
            <Property label="Coordinator role" value={coordinator?.replicatedJob} />
            <Property label="Coordinator Job index" value={coordinator?.jobIndex} />
            <Property label="Coordinator Pod index" value={coordinator?.podIndex} />
            <Property label="Subdomain" value={network?.subdomain} />
            <Property
              label="DNS hostnames"
              value={network?.enableDNSHostnames === undefined ? undefined : network.enableDNSHostnames ? 'Enabled' : 'Disabled'}
            />
            <Property
              label="Publish before ready"
              value={network?.publishNotReadyAddresses === undefined ? undefined : network.publishNotReadyAddresses ? 'Yes' : 'No'}
            />
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} getConditionTone={getJobSetConditionTone} />
    </>
  )
}
