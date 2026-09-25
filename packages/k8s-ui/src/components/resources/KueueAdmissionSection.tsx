import { isKueueConditionStale } from './resource-utils-kueue'
import { Disclosure } from '../ui/Disclosure'
import { formatRelativeAgeTime } from '../../utils/format'
import { Badge } from '../ui/Badge'
import { AlertBanner, ResourceLink, Section } from '../ui/drawer-components'
import { kindToPluralWithGroup } from '../../utils/navigation'
import type { KueueAdmissionResponse, SchedulingCondition, SchedulingObservation, SchedulingRef } from '../../types/scheduling'

interface KueueAdmissionSectionProps {
  presentation?: 'card' | 'drawer'
  data?: KueueAdmissionResponse
  loading: boolean
  error?: string
  forbidden?: boolean
  hinted: boolean
  externalExecution: boolean
  hasOwner?: boolean
  onRetry?: () => void
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

const phases = { pending: 'Pending admission', quota_reserved: 'Quota reserved', admitted: 'Admitted', finished: 'Finished' }

export function KueueAdmissionSection({ presentation = 'card', data, loading, error, forbidden, hinted, externalExecution, hasOwner, onRetry, onNavigate }: KueueAdmissionSectionProps) {
  if (!loading && !error && data && data.workloads.length === 0 && !hinted) return null
  const link = (name: string, ref?: SchedulingRef) => ref
    ? <ResourceLink {...ref} kind={kindToPluralWithGroup(ref.kind, ref.group ?? '')} label={name} onNavigate={onNavigate} />
    : name
  const spacing = presentation === 'drawer' ? '' : 'mt-3'
  const content = <>
      {loading ? <p className={`${spacing} text-sm text-theme-text-secondary`}>Looking for controller-owned Workloads…</p>
        : error ? <div className={`${spacing} [&>div]:mb-0`}><AlertBanner variant={forbidden ? 'info' : 'warning'} title={forbidden ? 'Admission lookup requires permission' : 'Admission evidence unavailable'} message={error}>{!forbidden && onRetry && <button type="button" className="mt-2 text-accent-text hover:underline" onClick={onRetry}>Retry admission lookup</button>}</AlertBanner></div>
          : data && !data.installed ? <p className={`${spacing} text-sm text-theme-text-secondary`}>Kueue Workloads are not served by this cluster.</p>
            : data && data.workloads.length === 0 ? <p className={`${spacing} text-sm text-theme-text-secondary`}>No controller-owned Kueue Workload observed in this namespace.{hasOwner ? ' Admission may be tracked on the owning resource; use the existing owner link to inspect it.' : externalExecution ? ' This workload uses an external controller; local absence does not establish remote admission or execution state.' : ' Queue metadata alone does not establish an admission decision.'}</p>
              : data && <div className={`${spacing} space-y-3`}>
                {data.total > 1 && <p className="text-xs text-theme-text-secondary">{data.truncated ? `${data.workloads.length} of ${data.total}` : data.total} Workloads shown, newest first. Each is a separate controller-owned record; order does not identify a current attempt.</p>}
                {data.workloads.map((workload) => <article key={workload.uid} className="space-y-2 text-sm border-t border-theme-border pt-3 first:border-0 first:pt-0">
                  {workload.projection !== 'available' && <div className="flex flex-wrap items-center gap-2">{link(workload.name, workload.ref)}{workload.deleting && <Badge severity="alert">Deleting</Badge>}</div>}
                  {workload.projection === 'unsupported' ? <p className="text-theme-text-secondary">Associated Workload found. Scheduling projection is unavailable for {workload.apiVersion}; inspect the resource for native evidence.</p>
                    : workload.projection === 'forbidden' ? <p className="text-theme-text-secondary">Associated Workload found, but permission to get this Workload is required for its scheduling detail.</p>
                      : workload.scheduling?.observations?.map((observation, index) => <AdmissionObservation key={index} observation={observation} identity={link(workload.name, workload.ref)} generation={workload.generation} deleting={workload.deleting} link={link} />)}
                  {workload.linksLimited && <p className="text-xs text-theme-text-tertiary">Some resource links are unavailable. Names already reported by the Workload remain visible.</p>}
                </article>)}
              </div>}
    </>
  return presentation === 'drawer'
    ? <Section title="Kueue admission">{content}</Section>
    : <section className="rounded-lg border border-theme-border bg-theme-surface p-4" aria-label="Kueue admission"><h3 className="text-sm font-semibold text-theme-text-primary">Kueue admission</h3>{content}</section>
}

type Link = (name: string, ref?: SchedulingRef) => React.ReactNode

function ConditionEvidence({ condition, generation, label }: { condition: SchedulingCondition; generation?: number; label?: string }) {
  const stale = isKueueConditionStale(condition, generation)
  return <div className="space-y-1">
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-theme-text-secondary">
      {label && <span>{label}: {condition.type}={condition.status}</span>}
      {condition.reason && <code className="break-all">{condition.reason}</code>}
      {!condition.message && !label && <span>{condition.type}={condition.status}</span>}
      {condition.lastTransitionTime && <span>Status since {formatRelativeAgeTime(condition.lastTransitionTime)}</span>}
    </div>
    {stale && <p className="text-xs text-warning-text">Stale evidence: condition generation {condition.observedGeneration}; Workload generation {generation}.</p>}
    {condition.message && <p className="whitespace-pre-wrap break-words">{condition.message}</p>}
  </div>
}

function GateEvidence({ gate, link }: { gate: NonNullable<SchedulingObservation['gates']>[number]; link: Link }) {
  return <div className="space-y-1">
    <p className="flex flex-wrap items-center gap-2">{link(gate.name, gate.ref)}<Badge severity={gate.kind === 'preemption_gate' ? 'neutral' : gate.nativeState === 'Ready' ? 'success' : gate.nativeState === 'Rejected' ? 'error' : 'warning'}>{gate.nativeState || gate.decision}</Badge></p>
    {gate.message && <p className="whitespace-pre-wrap break-words">{gate.message}</p>}
    {(gate.retryCount != null || gate.requeueAfterSeconds != null) && <p className="text-xs text-theme-text-secondary">{gate.retryCount != null && `Retries: ${gate.retryCount}`}{gate.retryCount != null && gate.requeueAfterSeconds != null && ' · '}{gate.requeueAfterSeconds != null && `Requeue delay: ${gate.requeueAfterSeconds}s`}</p>}
  </div>
}

function AdmissionObservation({ observation, identity, generation, deleting, link }: { observation: SchedulingObservation; identity: React.ReactNode; generation: number; deleting: boolean; link: Link }) {
  const kueue = observation.kueue
  const condition = observation.primaryCondition
  const stale = isKueueConditionStale(condition, observation.subjectGeneration)
  const secondary = [...(observation.disruptions ?? []), kueue?.podsReady, kueue?.waitingForReplacementPods]
    .filter((entry) => entry != null)
    .filter((entry) => entry.type !== condition?.type)
    .filter((entry, index, entries) => entries.findIndex((candidate) => candidate.type === entry.type) === index)
  const nominal = (entry: SchedulingCondition) => !isKueueConditionStale(entry, observation.subjectGeneration) && ((entry.type === 'PodsReady' && entry.status === 'True') || (entry.type === 'WaitingForReplacementPods' && entry.status === 'False'))
  const supporting = secondary.filter(nominal)
  const visible = secondary.filter((entry) => !nominal(entry))
  const checks = (observation.gates ?? []).filter((gate) => gate.kind !== 'preemption_gate')
  const pendingChecks = checks.filter((gate) => gate.nativeState !== 'Ready')
  const readyChecks = checks.filter((gate) => gate.nativeState === 'Ready')
  const preemption = (observation.gates ?? []).filter((gate) => gate.kind === 'preemption_gate')
  const severity = stale || observation.decision === 'unknown' ? 'neutral'
    : kueue?.phase === 'finished' ? kueue.outcome === 'failed' ? 'error' : kueue.outcome === 'succeeded' ? 'success' : 'neutral'
      : observation.decision === 'satisfied' ? 'success' : 'warning'
  return <div className="space-y-2">
    <div className="flex flex-wrap items-center gap-2">
      <Badge severity={severity}>{kueue ? phases[kueue.phase] : 'Admission'}</Badge>
      <span className="font-medium break-all">{identity}</span>
      {deleting && <Badge severity="alert">Deleting</Badge>}
      {kueue?.outcome && <span>Outcome: {kueue.outcome}</span>}
    </div>
    {(observation.decision === 'held' || observation.decision === 'unknown' || kueue?.active === false) && <p className="text-xs text-theme-text-secondary">{[observation.decision === 'held' ? 'Admission held' : observation.decision === 'unknown' ? 'Admission unknown' : null, kueue?.active === false ? 'Workload inactive' : null].filter(Boolean).join(' · ')}</p>}
    {condition ? <ConditionEvidence condition={condition} generation={observation.subjectGeneration} /> : <p className="text-theme-text-secondary">No primary admission condition reported.</p>}
    {(kueue?.phase === 'admitted' || kueue?.phase === 'quota_reserved') && <p className="text-xs text-theme-text-secondary">Admission status; execution is shown separately.</p>}
    {visible.map((entry) => <ConditionEvidence key={entry.type} condition={entry} generation={observation.subjectGeneration} label={observation.disruptions?.some((item) => item.type === entry.type) ? 'Reported disruption' : 'Reported readiness'} />)}
    {pendingChecks.length > 0 && <div className="space-y-2"><p className="text-xs font-medium text-theme-text-secondary">Admission checks</p>{pendingChecks.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}</div>}
    {preemption.length > 0 && <div className="space-y-2"><p className="text-xs font-medium text-theme-text-secondary">Preemption gates</p><p className="text-xs text-theme-text-secondary">This gate family governs preemption; it alone does not establish an admission blocker.</p>{preemption.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}</div>}
    {!!observation.queues?.length && <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs">{observation.queues.map((queue, index) => <span key={index}><span>{queue.roles.join(' + ').replace(/^./, (letter) => letter.toUpperCase())} queue</span>: {link(queue.name, queue.ref)}</span>)}</div>}
    {kueue?.requeueState && <p className="text-xs text-theme-text-secondary">Requeues: {kueue.requeueState.count ?? 'Not reported'}{kueue.requeueState.requeueAt && ` · Eligible again: ${kueue.requeueState.requeueAt}`}</p>}
    {kueue?.concurrentAdmission && <p className="text-xs">Parent Workload: {link(kueue.concurrentAdmission.parentName, kueue.concurrentAdmission.parentRef)}</p>}
    <Disclosure summary={<span>Technical details{supporting.length > 0 && ` · ${supporting.length} additional condition${supporting.length === 1 ? '' : 's'}`}{readyChecks.length > 0 && ` · ${readyChecks.length} ready check${readyChecks.length === 1 ? '' : 's'}`}</span>} summaryClassName="text-xs text-theme-text-secondary">
      <div className="space-y-2 pt-2 text-xs text-theme-text-secondary">
        <p>Decision: {observation.decision} · Generation {generation}</p>
        {[condition, ...visible].filter((entry) => entry != null).map((entry) => <p key={entry.type}>{entry.type}={entry.status}{entry.observedGeneration != null && ` · Observed generation ${entry.observedGeneration}`}</p>)}
        {supporting.map((entry) => <div key={entry.type}><ConditionEvidence condition={entry} generation={observation.subjectGeneration} label="Reported readiness" />{entry.observedGeneration != null && <p>Observed generation {entry.observedGeneration}</p>}</div>)}
        {readyChecks.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}
      </div>
    </Disclosure>
  </div>
}
