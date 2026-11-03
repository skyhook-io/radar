import type { ReactNode } from 'react'
import { ListChecks } from 'lucide-react'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { ResourceLink, Section } from '../../ui/drawer-components'
import { RadarUpgradeNote } from '../../ui/RadarUpgradeNote'
import { FilterPill } from '../../ui/FilterPill'
import { FactGrid, FactRow, FactSource, FactValue, type Fact } from '../../facts'
import type { RadarUpgradeRequirement } from '../../../types/fetch-error'
import type { ResourceReadCoverage } from '../../../types/resource-coverage'
import type {
  KueueQueueLocalQueue,
  KueueQueueMembers,
  KueueQueueMembership,
  KueueQueueWorkload,
  KueueQueueWorkloadsResponse,
} from '../../../types/kueue-queues'
import { boundedCountLabel, isLowerBound, resourceCoverageNotice, resourceNarrowedNotice } from '../../../utils/resource-coverage'
import { pluralize } from '../../../utils/pluralize'
import { formatRelativeAgeTime } from '../../../utils/format'

const KUEUE_GROUP = 'kueue.x-k8s.io'

type NavigateRef = { kind: string; namespace: string; name: string; group?: string }

export interface KueueQueueWorkloadsProps {
  kind: 'ClusterQueue' | 'LocalQueue'
  /** The queue object itself: its own reported counts are the evidence shown when its Workloads can't be listed. */
  queue: any
  /** Pages read so far, oldest first. The newest page's counts and coverage describe the list. */
  pages?: KueueQueueWorkloadsResponse[]
  loading?: boolean
  error?: string | null
  upgrade?: RadarUpgradeRequirement | null
  membership?: KueueQueueMembership
  onMembershipChange?: (membership: KueueQueueMembership | undefined) => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  onRetry?: () => void
  onNavigate?: (ref: NavigateRef) => void
}

/**
 * The Workloads Radar read that belong to a ClusterQueue or LocalQueue, with
 * what the read covered. Its numbers are Radar's: they sit apart from the
 * counts Kueue reports for the queue, which count other things (Kueue leaves
 * finished Workloads out, Radar lists them) and can lag either way.
 */
export function KueueQueueWorkloads(props: KueueQueueWorkloadsProps) {
  return (
    <Section title="Workloads" icon={ListChecks} defaultExpanded>
      <KueueQueueWorkloadsBody {...props} />
    </Section>
  )
}

function KueueQueueWorkloadsBody({
  kind, queue, pages, loading, error, upgrade, membership, onMembershipChange, hasMore, loadingMore, onLoadMore, onRetry, onNavigate,
}: KueueQueueWorkloadsProps) {
  if (upgrade) return <RadarUpgradeNote requirement={upgrade} />
  const latest = pages?.[pages.length - 1]
  if (!latest) {
    if (error) return <Retryable message={`Could not read this queue's Workloads: ${error}`} onRetry={onRetry} />
    return <p className="text-sm text-theme-text-tertiary">{loading ? 'Reading Workloads…' : 'Workloads not read yet.'}</p>
  }
  const local = kind === 'LocalQueue'
  return (
    <div className="space-y-3">
      {latest.workloads ? (
        <MemberList
          local={local}
          namespace={latest.queue.namespace}
          clusterQueue={latest.queue.clusterQueue}
          members={latest.workloads}
          rows={uniqueRows(pages ?? [])}
          membership={membership}
          onMembershipChange={onMembershipChange}
          hasMore={hasMore}
          loadingMore={loadingMore}
          onLoadMore={onLoadMore}
          onNavigate={onNavigate}
        />
      ) : (
        <WorkloadsUnread local={local} queue={queue} code={latest.workloadsError?.error_code} message={latest.workloadsError?.error} />
      )}
      {error && <Retryable message={`Could not refresh: ${error}`} onRetry={onRetry} />}
      {!local && <LocalQueueList response={latest} onNavigate={onNavigate} />}
    </div>
  )
}

function Retryable({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-wrap items-baseline gap-2 text-sm text-warning-text">
      <span>{message}</span>
      {onRetry && <button type="button" className="text-xs text-theme-text-secondary underline hover:text-theme-text-primary" onClick={onRetry}>Retry</button>}
    </div>
  )
}

/** Rows across pages, once each: a member can move between pages as the list changes. */
export function uniqueRows(pages: KueueQueueWorkloadsResponse[]): KueueQueueWorkload[] {
  const seen = new Set<string>()
  const rows: KueueQueueWorkload[] = []
  for (const page of pages) {
    for (const row of page.workloads?.items ?? []) {
      if (seen.has(row.uid)) continue
      seen.add(row.uid)
      rows.push(row)
    }
  }
  return rows
}

/** The headline count, as a sourced fact: "≥N" when the read was partial or narrowed by Radar's collection. */
export function listedFact(members: KueueQueueMembers, local: boolean): Fact {
  const coverage = members.coverage
  const narrowedToYou = coverage.narrowedBy?.includes('user_access') ?? false
  const text = boundedCountLabel(members.counts.members, isLowerBound(coverage))
  return {
    text: text === '–' ? 'None in what Radar read' : text,
    tone: 'neutral',
    source: local
      ? "Radar's read of this namespace's Workloads. Not the controller's count above."
      : `Radar's read of Workloads${narrowedToYou ? ' in the namespaces you can see' : ''}. Not the controller's count above.`,
  }
}

/** Why the list is empty, from what the read covered. */
export function emptyMessage(members: KueueQueueMembers, local: boolean, filtered: boolean): string {
  if (filtered && members.counts.members > 0) return 'No Workloads match this filter.'
  const { status, narrowedBy } = members.coverage
  if (status === 'partial') return 'No Workloads found in the namespaces Radar could read.'
  if (narrowedBy?.includes('namespace_collection')) return 'No Workloads in the namespaces Radar collects.'
  if (narrowedBy?.includes('user_access')) return 'No Workloads in the namespaces you can see.'
  return local ? 'No Workloads are submitted to this queue.' : 'No Workloads belong to this queue.'
}

function confirmedEmptyNote(coverage: ResourceReadCoverage, local: boolean): string | undefined {
  if (coverage.status !== 'complete' || coverage.narrowedBy?.length) return undefined
  return local ? 'Radar read every Workload in this namespace.' : 'Radar read Workloads in every namespace.'
}

function MemberList({
  local, namespace, clusterQueue, members, rows, membership, onMembershipChange, hasMore, loadingMore, onLoadMore, onNavigate,
}: {
  local: boolean
  namespace?: string
  clusterQueue?: string
  members: KueueQueueMembers
  rows: KueueQueueWorkload[]
  membership?: KueueQueueMembership
  onMembershipChange?: (membership: KueueQueueMembership | undefined) => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  onNavigate?: (ref: NavigateRef) => void
}) {
  const fact = listedFact(members, local)
  const notices = [
    resourceCoverageNotice(members.coverage, 'Workloads'),
    resourceNarrowedNotice(members.coverage),
    unresolvedNotice(members),
  ].filter(Boolean) as string[]
  const unresolved = new Set(members.submissionUnresolved?.names ?? [])
  return (
    <div className="space-y-3">
      <FactGrid>
        <FactRow label="Listed by Radar">
          <FactValue fact={fact} />
          <FactSource fact={fact} />
        </FactRow>
      </FactGrid>
      {notices.map(notice => <p key={notice} className="text-xs text-warning-text">{notice}</p>)}
      {!local && onMembershipChange && members.counts.members > 0 && (
        <div className="flex flex-wrap gap-1.5" role="group" aria-label="Filter by membership">
          <FilterPill label="All" count={members.counts.members} active={!membership} onClick={() => onMembershipChange(undefined)} tooltip="Every Workload submitted here or reserving quota here" />
          <FilterPill label="Submitted here" count={members.counts.submission} active={membership === 'submission'} onClick={() => onMembershipChange('submission')} tooltip="Workloads whose LocalQueue points at this ClusterQueue now" />
          <FilterPill label="Reserved here" count={members.counts.reservation} active={membership === 'reservation'} onClick={() => onMembershipChange('reservation')} tooltip="Workloads that report their admission in this ClusterQueue" />
        </div>
      )}
      {rows.length === 0 ? (
        <div>
          <p className="text-sm text-theme-text-secondary">{emptyMessage(members, local, !!membership)}</p>
          {!membership && confirmedEmptyNote(members.coverage, local) && <p className="mt-1 text-xs text-theme-text-tertiary">{confirmedEmptyNote(members.coverage, local)}</p>}
        </div>
      ) : (
        <ul className="divide-y divide-theme-border">
          {rows.map(row => (
            <WorkloadRow key={row.uid} row={row} local={local} namespace={namespace} clusterQueue={clusterQueue} unresolved={unresolved.has(row.namespace)} onNavigate={onNavigate} />
          ))}
        </ul>
      )}
      {rows.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-theme-text-tertiary">
          <span>
            Showing {rows.length.toLocaleString()} of {members.matching.toLocaleString()} · sorted by {local ? 'name' : 'namespace and name'}, not admission order
          </span>
          {hasMore && onLoadMore && (
            <button type="button" className="btn-secondary px-2 py-1 text-xs" disabled={loadingMore} onClick={onLoadMore}>
              {loadingMore ? 'Loading…' : 'Load more'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

function unresolvedNotice(members: KueueQueueMembers): string | undefined {
  const set = members.submissionUnresolved
  if (!set) return undefined
  const names = set.names ?? []
  const where = names.length > 0 && names.length <= 3 && names.length === set.count ? names.join(', ') : 'some namespaces'
  return `Radar couldn't read LocalQueues in ${where}, so Workloads there are listed only when their reservation names this queue.`
}

const phases: Record<string, { text: string; severity: BadgeSeverity }> = {
  pending: { text: 'Pending', severity: 'neutral' },
  quota_reserved: { text: 'Quota reserved', severity: 'info' },
  admitted: { text: 'Admitted', severity: 'success' },
  finished: { text: 'Finished', severity: 'neutral' },
}

function PhaseBadge({ row }: { row: KueueQueueWorkload }) {
  if (!row.phase) return null
  const phase = phases[row.phase] ?? { text: row.phase, severity: 'neutral' as const }
  return (
    <span title={row.phaseStale ? 'Reported for an older generation of this Workload' : undefined}>
      <Badge severity={row.phaseStale ? 'neutral' : phase.severity} size="sm">{phase.text}{row.phaseStale ? ' (older generation)' : ''}</Badge>
    </span>
  )
}

/** How this row belongs to the queue, in words, naming both queues when they disagree. */
export function membershipText(row: KueueQueueWorkload, local: boolean, clusterQueue: string | undefined, unresolved: boolean): string {
  const reservedIn = row.reservation?.clusterQueue
  if (local) {
    if (!reservedIn) return 'No reservation'
    if (clusterQueue && reservedIn !== clusterQueue) return `Reserved in ${reservedIn} · this LocalQueue now points to ${clusterQueue}`
    return `Reserved in ${reservedIn}`
  }
  const parts: string[] = []
  const submitted = row.membership.includes('submission')
  const reserved = row.membership.includes('reservation')
  if (submitted) parts.push(`Submitted through ${row.queueName}`)
  if (reserved) parts.push('reserved here')
  else if (reservedIn) parts.push(`reserved in ${reservedIn}`)
  else parts.push('no reservation')
  if (!submitted && row.queueName) {
    if (row.submission) parts.push(`its LocalQueue ${row.queueName} now points to ${row.submission.clusterQueue}`)
    else if (unresolved) parts.push(`LocalQueue ${row.queueName} not read`)
    else parts.push(`LocalQueue ${row.queueName} not found`)
  }
  const text = parts.join(' · ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

function WorkloadRow({ row, local, clusterQueue, unresolved, onNavigate }: {
  row: KueueQueueWorkload
  local: boolean
  namespace?: string
  clusterQueue?: string
  unresolved: boolean
  onNavigate?: (ref: NavigateRef) => void
}) {
  return (
    <li className="py-2 first:pt-0">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm [&_button]:text-left">
        <span className="min-w-0 break-all font-medium">
          <ResourceLink name={row.name} kind="workloads" namespace={row.namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
        </span>
        {!local && <span className="break-all text-xs text-theme-text-tertiary">{row.namespace}</span>}
        <PhaseBadge row={row} />
        {row.deleting && <Badge severity="neutral" size="sm">Deleting</Badge>}
      </div>
      <p className="mt-0.5 break-words text-xs text-theme-text-secondary">
        {membershipText(row, local, clusterQueue, unresolved)}
        {row.createdAt && <span className="text-theme-text-tertiary"> · created {formatRelativeAgeTime(row.createdAt)}</span>}
      </p>
    </li>
  )
}

function reportedText(reported: KueueQueueLocalQueue['reported']): string {
  const part = (value: number | null, label: string) => (value == null ? `${label} not reported` : `${value.toLocaleString()} ${label}`)
  return [part(reported.pending, 'pending'), part(reported.reserving, 'reserving'), part(reported.admitted, 'admitted')].join(' · ')
}

function queueReported(queue: any): KueueQueueLocalQueue['reported'] {
  const value = (v: unknown) => (typeof v === 'number' ? v : null)
  return { pending: value(queue?.status?.pendingWorkloads), reserving: value(queue?.status?.reservingWorkloads), admitted: value(queue?.status?.admittedWorkloads) }
}

function WorkloadsUnread({ local, queue, code, message }: { local: boolean; queue: any; code?: string; message?: string }) {
  const namespace = queue?.metadata?.namespace
  let headline: ReactNode = message ?? 'Radar could not read Workloads for this queue.'
  if (code === 'user_forbidden') {
    headline = local ? `You can't list Workloads in ${namespace}.` : "You can't list Workloads in any namespace you can see."
  } else if (code === 'user_no_namespace_access') {
    headline = "You don't have access to any namespace, so Radar can't list this queue's Workloads for you."
  }
  const reported = local ? queueReported(queue) : null
  return (
    <div className="space-y-1">
      <p className="text-sm text-theme-text-secondary">{headline}</p>
      {reported && Object.values(reported).some(v => v != null) && (
        <p className="text-xs text-theme-text-tertiary">
          The controller reports {reportedText(reported)} for this LocalQueue. That is Kueue's own count, not a list Radar can show you.
        </p>
      )}
    </div>
  )
}

function LocalQueueList({ response, onNavigate }: { response: KueueQueueWorkloadsResponse; onNavigate?: (ref: NavigateRef) => void }) {
  if (response.localQueuesError) {
    return (
      <div className="border-t border-theme-border pt-3">
        <h4 className="mb-1 text-xs font-medium uppercase tracking-wider text-theme-text-tertiary">LocalQueues pointing here</h4>
        <p className="text-sm text-theme-text-secondary">{response.localQueuesError.error_code === 'user_forbidden' ? "You can't list LocalQueues in the namespaces you can see." : response.localQueuesError.error}</p>
      </div>
    )
  }
  const lqs = response.localQueues
  if (!lqs) return null
  const notice = resourceCoverageNotice(lqs.coverage, 'LocalQueues')
  return (
    <div className="border-t border-theme-border pt-3">
      <h4 className="mb-1 text-xs font-medium uppercase tracking-wider text-theme-text-tertiary">
        LocalQueues pointing here ({boundedCountLabel(lqs.total, isLowerBound(lqs.coverage))})
      </h4>
      {notice && <p className="mb-1 text-xs text-warning-text">{notice}</p>}
      {lqs.items.length === 0 ? (
        <p className="text-sm text-theme-text-secondary">
          {lqs.coverage.status === 'complete' && !lqs.coverage.narrowedBy?.length ? 'No LocalQueue points at this ClusterQueue.' : 'None found in the namespaces Radar could read.'}
        </p>
      ) : (
        <ul className="space-y-2">
          {lqs.items.map(lq => (
            <li key={`${lq.namespace}/${lq.name}`} className="text-sm [&_button]:text-left">
              <span className="break-all">
                <ResourceLink name={lq.name} label={`${lq.namespace}/${lq.name}`} kind="localqueues" namespace={lq.namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
              </span>
              <p className="text-xs text-theme-text-secondary">Controller reports {reportedText(lq.reported)}</p>
              {!lq.workloadsRead && (
                <p className="text-xs text-theme-text-tertiary">
                  {lq.workloadsGap === 'user_forbidden'
                    ? `You can't list Workloads in ${lq.namespace}, so its Workloads aren't listed above.`
                    : `Radar didn't read Workloads in ${lq.namespace}, so its Workloads aren't listed above.`}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
      {lqs.items.length < lqs.total && <p className="mt-1 text-xs text-theme-text-tertiary">Showing {lqs.items.length} of {pluralize(lqs.total, 'LocalQueue')}.</p>}
    </div>
  )
}
