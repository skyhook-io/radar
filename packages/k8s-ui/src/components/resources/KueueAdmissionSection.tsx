import { Badge } from '../ui/Badge'
import { ResourceLink } from '../ui/drawer-components'
import { kindToPluralWithGroup } from '../../utils/navigation'
import type { KueueAdmissionResponse, SchedulingCondition, SchedulingObservation, SchedulingRef } from '../../types/scheduling'

interface KueueAdmissionSectionProps {
  data?: KueueAdmissionResponse
  loading: boolean
  error?: string
  hinted: boolean
  externalExecution: boolean
  onRetry?: () => void
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

const phases = { pending: 'Pending admission', quota_reserved: 'Quota reserved', admitted: 'Admitted', finished: 'Finished' }

export function KueueAdmissionSection({ data, loading, error, hinted, externalExecution, onRetry, onNavigate }: KueueAdmissionSectionProps) {
  if (!loading && !error && data && data.workloads.length === 0 && !hinted) return null
  const link = (name: string, ref?: SchedulingRef) => ref
    ? <ResourceLink {...ref} kind={kindToPluralWithGroup(ref.kind, ref.group ?? '')} label={name} onNavigate={onNavigate} />
    : name
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface p-4" aria-label="Kueue admission">
      <h3 className="text-sm font-semibold text-theme-text-primary">Kueue admission</h3>
      <p className="mt-1 text-xs text-theme-text-secondary">Admission and execution are separate observations. An admitted Workload does not prove that Jobs or Pods are running.</p>
      {loading ? <p className="mt-3 text-sm text-theme-text-secondary">Looking for controller-owned Workloads…</p>
        : error ? <div className="mt-3 text-sm text-theme-text-secondary"><p>Admission evidence unavailable: {error}</p>{onRetry && <button type="button" className="mt-2 text-accent-text hover:underline" onClick={onRetry}>Retry admission lookup</button>}</div>
          : data && !data.installed ? <p className="mt-3 text-sm text-theme-text-secondary">Kueue Workloads are not served by this cluster.</p>
            : data && data.workloads.length === 0 ? <p className="mt-3 text-sm text-theme-text-secondary">No controller-owned Kueue Workload observed in this namespace.{externalExecution ? ' This JobSet uses an external controller; local absence does not establish remote admission or execution state.' : ' Queue metadata alone does not establish an admission decision.'}</p>
              : data && <div className="mt-3 space-y-3">
                {data.total > 1 && <p className="text-xs text-theme-text-secondary">{data.truncated ? `${data.workloads.length} of ${data.total}` : data.total} Workloads shown, newest first. Each is a separate controller-owned record; order does not identify a current attempt.</p>}
                {data.workloads.map((workload) => <article key={workload.uid} className="card-inner space-y-2 text-sm">
                  <div className="flex flex-wrap items-center gap-2"><span className="font-medium">{link(workload.name, workload.ref)}</span>{workload.deleting && <Badge severity="alert">Deleting</Badge>}<span className="text-xs text-theme-text-tertiary">Generation {workload.generation}</span></div>
                  {workload.projection === 'unsupported' ? <p className="text-theme-text-secondary">Associated Workload found. Scheduling projection is unavailable for {workload.apiVersion}; inspect the resource for native evidence.</p>
                    : workload.projection === 'forbidden' ? <p className="text-theme-text-secondary">Associated Workload found, but permission to get this Workload is required for its scheduling detail.</p>
                      : workload.scheduling?.observations?.map((observation, index) => <AdmissionObservation key={index} observation={observation} link={link} />)}
                  {!!workload.omitted?.length && <p className="text-xs text-theme-text-tertiary">Some resource links or details are withheld by permissions. Names already reported by the Workload remain visible.</p>}
                </article>)}
              </div>}
    </section>
  )
}

function AdmissionObservation({ observation, link }: { observation: SchedulingObservation; link: (name: string, ref?: SchedulingRef) => React.ReactNode }) {
  const kueue = observation.kueue
  const condition = observation.primaryCondition
  const stale = !!condition?.observedGeneration && !!observation.subjectGeneration && condition.observedGeneration < observation.subjectGeneration
  return <div className="space-y-2">
    <div className="flex flex-wrap items-center gap-2">
      <Badge severity={stale || observation.decision === 'unknown' ? 'neutral' : observation.decision === 'satisfied' ? 'success' : 'warning'}>{kueue ? phases[kueue.phase] : 'Admission'}</Badge>
      <span className="text-theme-text-secondary">Decision: {observation.decision}</span>
      {kueue?.outcome && <span>Outcome: {kueue.outcome}</span>}
      {kueue?.active === false && <span>Workload inactive</span>}
    </div>
    {stale && <p className="text-sm text-theme-text-secondary">The primary condition describes generation {condition!.observedGeneration}; this Workload is now generation {observation.subjectGeneration}. Treat that condition as stale evidence.</p>}
    {condition ? <AdmissionCondition condition={condition} /> : <p className="text-theme-text-secondary">No primary admission condition reported.</p>}
    {!!observation.queues?.length && <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs">{observation.queues.map((queue, index) => <span key={index}>{queue.roles.join(' + ')} queue: {link(queue.name, queue.ref)}</span>)}</div>}
    {observation.gates?.map((gate, index) => <div key={index} className="border-l-2 border-theme-border pl-3 text-xs text-theme-text-secondary">
      <p>{gate.kind === 'preemption_gate' ? 'Preemption gate' : 'Admission check'}: {link(gate.name, gate.ref)} · {gate.nativeState || gate.decision}</p>
      {gate.message && <p className="whitespace-pre-wrap break-words">{gate.message}</p>}
      {gate.kind === 'preemption_gate' && <p>This gate governs preemption; it alone does not establish an admission blocker.</p>}
      {gate.retryCount != null && <p>Retries: {gate.retryCount}</p>}
      {gate.requeueAfterSeconds != null && <p>Requeue delay: {gate.requeueAfterSeconds}s</p>}
    </div>)}
    {observation.disruptions?.map((disruption, index) => <AdmissionCondition key={index} condition={disruption} />)}
    {kueue?.podsReady && <AdmissionCondition condition={kueue.podsReady} />}
    {kueue?.waitingForReplacementPods && <AdmissionCondition condition={kueue.waitingForReplacementPods} />}
    {kueue?.requeueState && <p className="text-xs text-theme-text-secondary">Requeues: {kueue.requeueState.count ?? 'Not reported'}{kueue.requeueState.requeueAt && ` · Eligible again: ${kueue.requeueState.requeueAt}`}</p>}
    {kueue?.concurrentAdmission && <p className="text-xs">Parent Workload: {link(kueue.concurrentAdmission.parentName, kueue.concurrentAdmission.parentRef)}</p>}
  </div>
}

function AdmissionCondition({ condition }: { condition: SchedulingCondition }) {
  return <div className="text-xs text-theme-text-secondary"><p>{condition.type}={condition.status}{condition.reason && ` · ${condition.reason}`}</p>{condition.message && <p className="mt-1 whitespace-pre-wrap break-words">{condition.message}</p>}</div>
}
