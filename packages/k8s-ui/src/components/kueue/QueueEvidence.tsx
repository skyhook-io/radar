import type { ReactNode } from 'react'
import { Badge } from '../ui/Badge'
import { ResourceLink } from '../ui/drawer-components'
import { TimeValue } from '../ui/ScheduleValue'
import { FactGrid, FactRow, FactValue, type Fact } from '../facts'
import type { StatusBadge } from '../resources/resource-utils'
import { getAdmissionCheckStatus, getClusterQueueStatus, getKueueQueueCondition, isKueueConditionStale } from '../resources/resource-utils-kueue'
import { describeKueuePolicy } from './policy'
import { isDenied, unreadReason, type RelatedRead } from './related-read'
import { namespaceEligibility } from './eligibility'
import { KUEUE_GROUP } from './constants'

export type Navigate = (ref: { kind: string; namespace: string; name: string; group?: string }) => void

/** One related object's name with what Radar's read of it found. Without a read, the name alone. */
export function ReadState({ read, found }: { read?: RelatedRead; found?: ReactNode }) {
  if (!read) return null
  if (read.state === 'found') return <>{found ?? <Badge severity="neutral" size="sm">Found</Badge>}</>
  if (read.state === 'not_found') return <Badge severity="error" size="sm">Not found</Badge>
  if (read.state === 'unconfirmed') return <span className="text-xs text-theme-text-tertiary">Not in Radar’s list; this Radar can’t confirm it’s absent</span>
  return <span className={isDenied(read) ? 'text-xs text-theme-text-secondary' : 'text-xs text-theme-text-tertiary'}>{read.state === 'loading' ? 'Checking…' : `Not read: ${unreadReason(read)}`}</span>
}

export function StatusPill({ status }: { status: StatusBadge }) {
  return <Badge colorClass={status.color} size="sm">{status.text}</Badge>
}

/** The Active condition exactly as the controller reported it, with its age and generation. */
export function ReportedActive({ data, status, children }: { data: any; status: StatusBadge; children?: ReactNode }) {
  const condition = getKueueQueueCondition(data)
  const generation = data?.metadata?.generation
  if (!condition) {
    return <div className="space-y-2 text-sm">
      <div className="flex flex-wrap items-center gap-2"><StatusPill status={status} /></div>
      <p className="text-theme-text-secondary">No Active condition reported.</p>
      {children}
    </div>
  }
  return <div className="space-y-2 text-sm">
    <div className="flex flex-wrap items-center gap-2">
      <StatusPill status={status} />
      {condition.reason && !status.text.startsWith(condition.reason) && <code className="break-all text-xs text-theme-text-secondary">{condition.reason}</code>}
    </div>
    {condition.message && <p className="whitespace-pre-wrap break-words text-theme-text-primary">{condition.message}</p>}
    <p className="text-xs text-theme-text-tertiary">
      Active={condition.status}
      {condition.lastTransitionTime && <> since <TimeValue timestamp={condition.lastTransitionTime} /></>}
      {' · '}
      {condition.observedGeneration ? `observed generation ${condition.observedGeneration} of ${generation ?? '?'}` : 'observed generation not reported'}
    </p>
    {children}
  </div>
}

function count(value: unknown, source: string): Fact {
  return typeof value === 'number'
    ? { text: String(value), tone: 'neutral', source }
    : { text: 'Not reported', tone: 'unknown', source }
}

/** Controller-reported Workload counts. Reserving includes admitted, so they never add up. */
export function QueueCounts({ data, kind }: { data: any; kind: 'ClusterQueue' | 'LocalQueue' }) {
  const source = `Reported in the ${kind}'s status`
  return <div className="space-y-2">
    <FactGrid>
      <FactRow label="Pending"><FactValue fact={count(data?.status?.pendingWorkloads, source)} /></FactRow>
      <FactRow label="Reserving quota"><FactValue fact={count(data?.status?.reservingWorkloads, source)} /></FactRow>
      <FactRow label="Admitted"><FactValue fact={count(data?.status?.admittedWorkloads, source)} /></FactRow>
    </FactGrid>
    <p className="text-xs text-theme-text-tertiary">Reserving includes admitted Workloads, so these counts are not additive. Admission does not establish that Pods are running.</p>
  </div>
}

/** stopPolicy as configured, with what it means at this API version. */
export function ConfiguredStop({ data }: { data: any }) {
  const raw = data?.spec?.stopPolicy
  const policy = describeKueuePolicy(data?.apiVersion, 'stopPolicy', raw ?? 'None')
  return <div className="space-y-1 text-sm">
    <div className="flex flex-wrap items-baseline gap-2">
      <span className="text-theme-text-tertiary">Stop policy</span>
      <code className="text-theme-text-primary">{policy.raw}</code>
      {raw == null && <span className="text-xs text-theme-text-tertiary">(not set)</span>}
    </div>
    <p className="text-theme-text-secondary">{policy.meaning ?? `Radar has no description of this value for ${data?.apiVersion ?? 'this API version'}.`}</p>
  </div>
}

/** A related object's condition message; one describing an earlier generation says so. */
function ReportedMessage({ condition, generation }: { condition: any; generation?: number }) {
  return <p className="whitespace-pre-wrap break-words text-xs text-theme-text-secondary">
    {isKueueConditionStale(condition, generation) && <span className="text-theme-text-tertiary">Reported for generation {condition.observedGeneration} (now {generation}): </span>}
    {condition.message}
  </p>
}

/** A ClusterQueue as read from its own object: link, reported state and stop policy. */
export function ClusterQueueReadSummary({ name, read, onNavigate }: { name: string; read?: RelatedRead; onNavigate?: Navigate }) {
  const parent = read?.state === 'found' ? read.object : undefined
  const condition = parent ? getKueueQueueCondition(parent) : undefined
  return <div className="min-w-0 space-y-1 text-sm">
    <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
      <ResourceLink name={name} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} />
      <ReadState read={read} found={parent ? <StatusPill status={getClusterQueueStatus(parent)} /> : undefined} />
    </div>
    {parent && <p className="text-xs text-theme-text-secondary">
      Read from the ClusterQueue{condition?.reason && condition.status !== 'True' ? `: ${condition.reason}` : ''}{parent.spec?.stopPolicy && parent.spec.stopPolicy !== 'None' ? ` · stop policy ${parent.spec.stopPolicy}` : ''}
    </p>}
    {parent && condition?.message && condition.status !== 'True' && <ReportedMessage condition={condition} generation={parent.metadata?.generation} />}
  </div>
}

/**
 * Whether a namespace matches a ClusterQueue's namespaceSelector. The
 * selector is only known from a ClusterQueue that was read; the namespace's
 * labels are needed only when the selector has terms.
 */
export function namespaceEligibilityFact({ clusterQueue, parent, namespaceRead }: { clusterQueue: string; parent?: RelatedRead; namespaceRead?: RelatedRead }): Fact {
  const source = `ClusterQueue ${clusterQueue} namespaceSelector`
  if (parent?.state !== 'found') {
    const why = !parent ? 'Unknown: the ClusterQueue was not read'
      : parent.state === 'loading' ? 'Checking the ClusterQueue…'
        : parent.state === 'not_found' ? 'Unknown: the ClusterQueue was not found'
          : `Unknown: ClusterQueue not read (${unreadReason(parent)})`
    return { text: why, tone: 'unknown', source }
  }
  const selector = parent.object?.spec?.namespaceSelector
  const labels = namespaceRead?.state === 'found' ? (namespaceRead.object?.metadata?.labels ?? {}) : undefined
  const result = namespaceEligibility(selector, labels)
  switch (result.state) {
    case 'eligible':
      return { text: result.basis === 'all' ? 'Eligible: the selector is empty ({}), so every namespace matches' : 'Eligible: its labels match the selector', tone: 'healthy', source }
    case 'not_eligible':
      return { text: result.basis === 'none' ? 'Not eligible: the ClusterQueue sets no namespaceSelector, so no namespace matches' : 'Not eligible: its labels don’t match the selector', tone: 'unhealthy', source }
    default:
      if (result.why === 'unsupported_operator') return { text: `Unknown: the selector uses operator ${result.operator || '(none)'}, which Radar doesn't evaluate`, tone: 'unknown', source }
      if (namespaceRead?.state === 'loading') return { text: 'Checking the namespace…', tone: 'unknown', source }
      return { text: namespaceRead?.state === 'not_found' ? 'Unknown: the Namespace was not found' : `Unknown: its labels weren’t read (${unreadReason(namespaceRead)})`, tone: 'unknown', source }
  }
}

/** An AdmissionCheck's name, read state and its own Active report. */
export function AdmissionCheckRow({ name, onFlavors, read, onNavigate }: { name: string; onFlavors?: string[]; read?: RelatedRead; onNavigate?: Navigate }) {
  const check = read?.state === 'found' ? read.object : undefined
  const condition = check ? getKueueQueueCondition(check) : undefined
  return <div className="min-w-0 space-y-1 text-sm">
    <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
      <ResourceLink name={name} kind="admissionchecks" group={KUEUE_GROUP} onNavigate={onNavigate} />
      <ReadState read={read} found={check ? <StatusPill status={getAdmissionCheckStatus(check)} /> : undefined} />
    </div>
    <p className="break-words text-xs text-theme-text-secondary">{onFlavors?.length ? `Applies to flavors: ${onFlavors.join(', ')}` : 'Applies to all flavors'}</p>
    {condition?.message && condition.status !== 'True' && <ReportedMessage condition={condition} generation={check.metadata?.generation} />}
  </div>
}
