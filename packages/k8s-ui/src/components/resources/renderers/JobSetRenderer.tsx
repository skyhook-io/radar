import type { ReactNode } from 'react'
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
  admissionContent?: ReactNode
  mode?: 'detail' | 'overview' | 'configuration'
  shownMemberCounts?: ReadonlyMap<string, number>
  onSelectRole?: (role: string) => void
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

export function JobSetRenderer({ data, admissionContent, mode = 'detail', shownMemberCounts, onSelectRole }: JobSetRendererProps) {
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
  const globalCountedRestarts = status.restartsCountTowardsMax ?? (typeof status.restarts === 'number' ? 0 : undefined)
  const individualCountedRestarts = Array.isArray(status.replicatedJobsStatus)
    ? replicatedJobsStatus.reduce((total, entry) => total + (sumCountArray(entry?.jobRestartsCountTowardsMax) ?? 0), 0)
    : undefined
  const observedRoles = replicatedJobs.filter((role) => statusByName.has(role.name)).length
  const externalController = spec.managedBy && spec.managedBy !== 'jobset.sigs.k8s.io/jobset-controller'
  const showSummary = mode !== 'configuration'
  const showConfiguration = mode !== 'overview'
  const coordinator = spec.coordinator
  const network = spec.network

  return (
    <>
      {showSummary && <ProblemAlerts problems={problems} />}
      {showSummary && admissionContent}

      {showSummary && externalController && (
        <div className="mb-3 rounded border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
          Managed by <span className="font-mono">{spec.managedBy}</span>. The built-in JobSet controller does not create Jobs for this resource.
          {spec.managedBy === 'kueue.x-k8s.io/multikueue' && ' Execution may occur on another cluster; local member absence does not establish remote execution state.'}
        </div>
      )}

      {showSummary && <Section title="JobSet status" icon={Activity}>
        {mode === 'overview' ? (
          <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm text-theme-text-secondary">
            <span className={`badge ${displayedStatus.color}`}>{displayedStatus.text}</span>
            <span>Suspend requested: {spec.suspend ? 'Yes' : 'No'}</span>
            <span>Global restarts: {status.restarts ?? 'Not reported'}</span>
            {observedRoles < replicatedJobs.length && <span>Roles reporting status: {observedRoles} of {replicatedJobs.length}</span>}
          </div>
        ) : (
        <PropertyList>
          <Property label="State" value={<span className={`badge ${displayedStatus.color}`}>{displayedStatus.text}</span>} />
          <Property label="Terminal state" value={status.terminalState} />
          <Property label="Global restarts" value={status.restarts} />
          <Property label="Suspend requested" value={spec.suspend ? 'Yes' : 'No'} />
          <Property label="Managed by" value={spec.managedBy} />
          <Property
            label="Delete after finish"
            value={spec.ttlSecondsAfterFinished === undefined ? undefined : `${spec.ttlSecondsAfterFinished}s`}
          />
        </PropertyList>
        )}
      </Section>}

      {mode === 'overview' && replicatedJobs.length > 0 && (
        <Section title="Role progress" icon={Boxes}>
          <p className="mb-3 text-xs text-theme-text-secondary">Counts are controller-reported Jobs, not Pods. Active Jobs can have Pending Pods. Dependencies describe startup requirements.</p>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="text-theme-text-tertiary"><tr>
                {['Role', 'Declared Jobs', 'Ready', 'Active', 'Succeeded', 'Failed', 'Suspended', 'Starts after', ...(onSelectRole ? ['Members'] : [])].map((label) => <th key={label} className="whitespace-nowrap border-b border-theme-border px-2 py-2 font-medium">{label}</th>)}
              </tr></thead>
              <tbody>{replicatedJobs.map((role) => {
                const observation = statusByName.get(role.name)
                const shown = shownMemberCounts?.get(role.name) ?? 0
                return <tr key={role.name} className="border-b border-theme-border-subtle last:border-0">
                  <th scope="row" className="px-2 py-3 font-medium text-theme-text-primary">{role.name}{!observation && <span className="mt-1 block font-normal text-theme-text-tertiary">Status not reported</span>}</th>
                  <td className="px-2 py-3">{role.replicas ?? 1}</td>
                  {['ready', 'active', 'succeeded', 'failed', 'suspended'].map((field) => <td key={field} className="px-2 py-3" aria-label={`${role.name} ${field}: ${observation?.[field] ?? 'not reported'}`}>{observation?.[field] ?? '—'}</td>)}
                  <td className="px-2 py-3">{role.dependsOn?.map((dependency: any) => `${dependency.name} ${dependency.status}`).join(', ') || 'None'}</td>
                  {onSelectRole && <td className="px-2 py-3"><button type="button" className="whitespace-nowrap text-accent-text hover:underline" onClick={() => onSelectRole(role.name)}>Inspect members{shown > 0 ? ` (${shown} shown)` : ''}</button></td>}
                </tr>
              })}</tbody>
            </table>
          </div>
        </Section>
      )}

      {mode === 'configuration' && (spec.managedBy || spec.ttlSecondsAfterFinished !== undefined) && (
        <PropertyList>
          <Property label="Managed by" value={spec.managedBy} />
          <Property label="Delete after finish" value={spec.ttlSecondsAfterFinished === undefined ? undefined : `${spec.ttlSecondsAfterFinished}s`} />
        </PropertyList>
      )}

      {showConfiguration && replicatedJobs.length > 0 && (
        <Section title={`Replicated jobs (${replicatedJobs.length})`} icon={Boxes} defaultExpanded={mode === 'detail'}>
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

                  {mode === 'detail' && (counts.length > 0 ? (
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
                  ))}

                  {mode === 'detail' && dependencies.length > 0 && (
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

      {showConfiguration && (successPolicy || failurePolicy || spec.startupPolicy) && (
        <Section title="Completion and restart policies" icon={ShieldCheck} defaultExpanded={mode === 'detail'}>
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
            <Property label="Global restarts counted toward limit" value={globalCountedRestarts ?? 'Not reported'} />
            <Property label="Individual restarts counted toward limit" value={individualCountedRestarts == null ? 'Not reported' : `${individualCountedRestarts} (${observedRoles} of ${replicatedJobs.length} roles reported)`} />
            <Property label="Restart strategy" value={failurePolicy?.restartStrategy} />
            {failureRules.length > 0 && <Property label="Default action" value="RestartJobSet" />}
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

      {showConfiguration && (coordinator || network) && (
        <Section title="Coordination and network" icon={NetworkIcon} defaultExpanded={mode === 'detail'}>
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

      {showSummary && <ConditionsSection conditions={conditions} getConditionTone={getJobSetConditionTone} defaultExpanded={mode === 'overview' ? false : undefined} />}
    </>
  )
}
