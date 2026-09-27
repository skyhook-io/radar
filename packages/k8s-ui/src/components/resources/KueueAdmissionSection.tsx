import { admissionCheckSeverity, isKueueConditionStale } from './resource-utils-kueue'
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
                  {data.total > 1 && workload.createdAt && <p className="text-xs text-theme-text-tertiary">Created <time dateTime={workload.createdAt} title={workload.createdAt}>{formatRelativeAgeTime(workload.createdAt)}</time></p>}
                  {workload.projection !== 'available' && <div className="flex flex-wrap items-center gap-2">{link(workload.name, workload.ref)}{workload.deleting && <Badge severity="alert">Deleting</Badge>}</div>}
                  {workload.projection === 'unsupported' ? <p className="text-theme-text-secondary">Associated Workload found. Scheduling projection is unavailable for {workload.apiVersion}; inspect the resource for native evidence.</p>
                    : workload.projection === 'forbidden' ? <p className="text-theme-text-secondary">Associated Workload found, but permission to get this Workload is required for its scheduling detail.</p>
                      : workload.scheduling?.observations?.map((observation, index) => <AdmissionObservation key={index} presentation={presentation} observation={observation} identity={link(workload.name, workload.ref)} sourceLink={workload.ref && onNavigate ? link("View Workload details", workload.ref) : undefined} generation={workload.generation} deleting={workload.deleting} link={link} />)}
                  {workload.linksLimited && <p className="text-xs text-theme-text-tertiary">Some resource links are unavailable. Names already reported by the Workload remain visible.</p>}
                </article>)}
              </div>}
    </>
  return presentation === 'drawer'
    ? <Section title="Kueue admission">{content}</Section>
    : <section className="space-y-3" aria-label="Kueue admission"><h3 className="text-sm font-semibold text-theme-text-primary">Kueue admission</h3>{content}</section>
}

type Link = (name: string, ref?: SchedulingRef) => React.ReactNode


const evidenceCard = 'min-w-0 overflow-hidden rounded-lg border border-theme-border bg-theme-surface'
const cardHeader = 'border-b border-theme-border bg-theme-elevated px-3 py-2.5'
const groupHeading = 'text-xs font-medium uppercase tracking-wider text-theme-text-secondary'
type Gate = NonNullable<SchedulingObservation['gates']>[number]

function conditionLabel(condition: SchedulingCondition): string {
  const { type, status } = condition
  if (type === 'PodsReady') return `Pods ready: ${status === 'True' ? 'yes' : status === 'False' ? 'no' : status}`
  if (type === 'WaitingForReplacementPods') return `Replacement Pods: ${status === 'True' ? 'waiting' : status === 'False' ? 'not waiting' : status}`
  if (status === 'True') {
    if (type === 'Evicted') return 'Evicted'
    if (type === 'Preempted') return 'Preempted'
    if (type === 'DeactivationTarget') return 'Deactivation requested'
  }
  return `${type}=${status}`
}

function ConditionEvidence({ condition, generation, showAge }: { condition: SchedulingCondition; generation?: number; showAge?: boolean }) {
  return <div className="space-y-1 py-3 first:pt-0 last:pb-0">
    <p className="max-w-3xl whitespace-pre-wrap break-words text-theme-text-secondary">
      <span className="block font-medium text-theme-text-primary">{conditionLabel(condition)}{showAge && condition.lastTransitionTime && <span className="font-normal text-xs text-theme-text-tertiary"> · Status since {formatRelativeAgeTime(condition.lastTransitionTime)}</span>}</span>
      {condition.message || condition.reason || `${condition.type}=${condition.status}`}
    </p>
    {isKueueConditionStale(condition, generation) && <p className="text-xs text-warning-text">Stale evidence: condition generation {condition.observedGeneration}; Workload generation {generation}.</p>}
  </div>
}

function GateEvidence({ gate, link }: { gate: Gate; link: Link }) {
  const retryMetadata = [gate.retryCount != null && gate.retryCount > 0 ? `Retries: ${gate.retryCount}` : null, gate.requeueAfterSeconds != null && gate.requeueAfterSeconds > 0 ? `Requeue delay: ${gate.requeueAfterSeconds}s` : null].filter(Boolean)
  return <div className="space-y-2 py-3">
    <div className="grid grid-cols-[minmax(0,1fr)_6rem] items-start gap-2">
      <span className="min-w-0 break-words font-medium [&_button]:text-left">{link(gate.name, gate.ref)}</span>
      <Badge className="justify-self-end" severity={gate.kind === 'preemption_gate' ? 'neutral' : admissionCheckSeverity(gate.nativeState)}>{gate.nativeState || 'Unknown'}</Badge>
    </div>
    {gate.lastTransitionTime && <p className="text-xs text-theme-text-tertiary">Status since <time dateTime={gate.lastTransitionTime} title={gate.lastTransitionTime}>{formatRelativeAgeTime(gate.lastTransitionTime)}</time></p>}
    {gate.message && <p className="max-w-3xl whitespace-pre-wrap break-words">{gate.message}</p>}
    {retryMetadata.length > 0 && <p className="text-xs text-theme-text-secondary">{retryMetadata.join(' · ')}</p>}
  </div>
}

function AdmissionObservation({ observation, identity, sourceLink, generation, deleting, presentation, link }: { observation: SchedulingObservation; identity: React.ReactNode; sourceLink?: React.ReactNode; generation: number; deleting: boolean; presentation: 'card' | 'drawer'; link: Link }) {
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
  const isDisruption = (entry: SchedulingCondition) => observation.disruptions?.some((item) => item.type === entry.type)
  const disruptions = visible.filter(isDisruption)
  const readiness = visible.filter((entry) => !isDisruption(entry))
  const checks = (observation.gates ?? []).filter((gate) => gate.kind !== 'preemption_gate')
  const pendingChecks = checks.filter((gate) => gate.nativeState !== 'Ready')
  const readyChecks = checks.filter((gate) => gate.nativeState === 'Ready')
  const preemption = (observation.gates ?? []).filter((gate) => gate.kind === 'preemption_gate')
  const split = presentation === 'card' && (visible.length > 0 || preemption.length > 0)
  const severity = stale || observation.decision === 'unknown' ? 'neutral'
    : kueue?.phase === 'finished' ? kueue.outcome === 'failed' ? 'error' : kueue.outcome === 'succeeded' ? 'success' : 'neutral'
      : observation.decision === 'satisfied' ? 'success' : 'warning'
  const qualifier = observation.decision === 'held' && kueue?.active !== false ? 'Admission held' : observation.decision === 'unknown' ? 'Admission unknown' : null
  return <div className="@container/admission space-y-4">
    <div className={split ? 'grid min-w-0 items-start gap-4 @min-[1024px]/admission:grid-cols-[minmax(0,1.7fr)_minmax(320px,0.9fr)]' : 'space-y-4'}>
    <div className="min-w-0 space-y-4">
    <section aria-label="Admission" className={evidenceCard}>
      <h4 className={`${groupHeading} ${cardHeader}`}>Admission</h4>
      <div className="space-y-2 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium break-all">{identity}</span>
        <Badge severity={severity}>{kueue ? phases[kueue.phase] : 'Admission'}</Badge>
        {qualifier && <span className="text-xs text-theme-text-secondary">{qualifier}</span>}
        {kueue?.active === false && <Badge severity="neutral">Workload inactive</Badge>}
        {deleting && <Badge severity="alert">Deleting</Badge>}
        {kueue?.outcome && <span>Outcome: {kueue.outcome}</span>}
      </div>
        {stale && <p className="text-xs text-warning-text">Stale evidence: condition generation {condition!.observedGeneration}; Workload generation {observation.subjectGeneration}.</p>}
        {condition ? <p className="max-w-3xl whitespace-pre-wrap break-words">{condition.message || <><code>{condition.reason}</code>{condition.reason && ' · '}{condition.type}={condition.status}</>}</p> : <p className="text-theme-text-secondary">No primary admission condition reported.</p>}
        {condition?.lastTransitionTime && <p className="text-xs text-theme-text-tertiary">{condition.type}={condition.status} since {formatRelativeAgeTime(condition.lastTransitionTime)}</p>}
      <div className="empty:hidden flex flex-wrap gap-x-5 gap-y-2 border-t border-theme-border pt-2 text-xs text-theme-text-secondary">
      {sourceLink && <span>{sourceLink}</span>}
      {(!!observation.queues?.length || kueue?.concurrentAdmission) && <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-theme-text-secondary">
        {observation.queues?.map((queue, index) => <span key={index}>{queue.roles.join(' + ').replace(/^./, (letter) => letter.toUpperCase())} queue: {link(queue.name, queue.ref)}</span>)}
        {kueue?.concurrentAdmission && <span>Parent Workload: {link(kueue.concurrentAdmission.parentName, kueue.concurrentAdmission.parentRef)}</span>}
      </div>}
      {kueue?.requeueState && ((kueue.requeueState.count ?? 0) > 0 || kueue.requeueState.requeueAt) && <p className="text-xs text-theme-text-secondary">{kueue.requeueState.count != null && `Requeues: ${kueue.requeueState.count}`}{kueue.requeueState.requeueAt && `${kueue.requeueState.count != null ? ' · ' : ''}Eligible again: ${kueue.requeueState.requeueAt}`}</p>}
      {(kueue?.phase === 'admitted' || kueue?.phase === 'quota_reserved') && <p className="text-xs text-theme-text-tertiary">Admission status; execution is shown separately.</p>}
      </div>
      </div>
      <div className="border-t border-theme-border p-3">
    <Disclosure summary={<span>Technical details{supporting.length > 0 && ` · ${supporting.length} additional condition${supporting.length === 1 ? '' : 's'}`}{readyChecks.length > 0 && ` · ${readyChecks.length} ready check${readyChecks.length === 1 ? '' : 's'}`}</span>} summaryClassName="text-xs text-theme-text-secondary">
      <div className="space-y-2 pt-2 text-xs text-theme-text-secondary">
        <p>Decision: {observation.decision} · Generation {generation}</p>
        {[condition, ...secondary].filter((entry) => entry != null).map((entry) => <div key={entry.type}>
          <p>{entry.type}={entry.status}{entry.reason && ` · Reason: ${entry.reason}`}{entry.observedGeneration != null && ` · Observed generation ${entry.observedGeneration}`}{entry.lastTransitionTime && ` · Status since ${formatRelativeAgeTime(entry.lastTransitionTime)}`}</p>
          {supporting.includes(entry) && entry.message && <p className="whitespace-pre-wrap break-words">{entry.message}</p>}
        </div>)}
        {(observation.gates ?? []).filter((gate) => gate.retryCount === 0 || gate.requeueAfterSeconds === 0).map((gate, index) => <p key={index}>{gate.kind === 'preemption_gate' ? 'Preemption gate' : 'Admission check'} {gate.name}{gate.retryCount === 0 && ' · Retries: 0'}{gate.requeueAfterSeconds === 0 && ' · Requeue delay: 0s'}</p>)}
        {kueue?.requeueState?.count === 0 && !kueue.requeueState.requeueAt && <p>Requeues: 0</p>}
        {readyChecks.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}
      </div>
    </Disclosure>
      </div>
    </section>
    {pendingChecks.length > 0 && <section aria-label="Admission checks" className={evidenceCard}>
      <h4 className={`${groupHeading} ${cardHeader}`}>Admission checks · {pendingChecks.length} not ready</h4>
      <div className="divide-y divide-theme-border [&>div]:p-3">{pendingChecks.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}</div>
    </section>}
    </div>
    {(visible.length > 0 || preemption.length > 0) && <section aria-label="Supporting evidence" className={`${evidenceCard} self-start`}>
      <h4 className={`${groupHeading} ${cardHeader}`}>Supporting evidence</h4>
      <div className="divide-y divide-theme-border px-3">
        {disruptions.length > 0 && <div className="space-y-3 py-3"><h5 className={groupHeading}>Reported disruptions · {disruptions.length}</h5><div className="divide-y divide-theme-border">{disruptions.map((entry) => <ConditionEvidence key={entry.type} condition={entry} generation={observation.subjectGeneration} showAge />)}</div></div>}
        {readiness.length > 0 && <div className="space-y-3 py-3"><h5 className={groupHeading}>Pod readiness · {readiness.length}</h5><div className="divide-y divide-theme-border">{readiness.map((entry) => <ConditionEvidence key={entry.type} condition={entry} generation={observation.subjectGeneration} />)}</div></div>}
        {preemption.length > 0 && <div className="space-y-2 py-3"><h5 className={groupHeading}>Preemption gates · {preemption.length}</h5><p className="text-xs text-theme-text-tertiary">Governs preemption; it alone does not establish an admission blocker.</p><div className="divide-y divide-theme-border">{preemption.map((gate, index) => <GateEvidence key={index} gate={gate} link={link} />)}</div></div>}
      </div>
    </section>}
    </div>

  </div>
}
