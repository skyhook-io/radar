import type { ReactNode } from 'react'
import { ListChecks } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { kueueAdmissionStateLabel, kueueAdmissionStatePresentation } from '../resource-utils-kueue'
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
  /**
   * Pages read so far, first page first. The first page's counts, coverage and
   * LocalQueues describe the list; later pages only add rows.
   */
  pages?: KueueQueueWorkloadsResponse[]
  loading?: boolean
  error?: string | null
  upgrade?: RadarUpgradeRequirement | null
  membership?: KueueQueueMembership
  onMembershipChange?: (membership: KueueQueueMembership | undefined) => void
  /** An admission state code to filter by. */
  state?: string
  onStateChange?: (state: string | undefined) => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  /** Why the next page could not be read, when the request itself failed. */
  nextPageError?: string | null
  /** The pages shown belong to the previous filter while the new one loads. */
  stale?: boolean
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
  kind, queue, pages, loading, error, upgrade, membership, onMembershipChange, state, onStateChange, hasMore, loadingMore, onLoadMore, nextPageError, stale, onRetry, onNavigate,
}: KueueQueueWorkloadsProps) {
  if (upgrade) return <RadarUpgradeNote requirement={upgrade} />
  const first = pages?.[0]
  if (!first) {
    if (error) return <Retryable message={`Could not read this queue's Workloads: ${error}`} onRetry={onRetry} />
    return <p className="text-sm text-theme-text-tertiary">{loading ? 'Reading Workloads…' : 'Workloads not read yet.'}</p>
  }
  const local = kind === 'LocalQueue'
  return (
    <div className="space-y-3">
      {first.workloads ? (
        <MemberList
          local={local}
          clusterQueue={first.queue.clusterQueue}
          members={first.workloads}
          rows={uniqueRows(pages ?? [])}
          paged={(pages?.length ?? 0) > 1}
          laterPageError={nextPageError ?? laterPageProblem(pages ?? [])}
          onRefresh={onRetry}
          membership={membership}
          onMembershipChange={onMembershipChange}
          state={state}
          onStateChange={onStateChange}
          hasMore={hasMore && !stale}
          loadingMore={loadingMore}
          stale={stale}
          onLoadMore={onLoadMore}
          onNavigate={onNavigate}
        />
      ) : (
        <WorkloadsUnread local={local} queue={queue} code={first.workloadsError?.error_code} message={first.workloadsError?.error} />
      )}
      {error && !nextPageError && <Retryable message={`Could not refresh: ${error}`} onRetry={onRetry} />}
      {!local && <LocalQueueList response={first} onNavigate={onNavigate} />}
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

/**
 * Why a page after the first couldn't be added to it. A later page that read
 * a different scope never arrives: the server refuses its cursor
 * (kueue_cursor_invalid) and the host rereads from the first page.
 */
export function laterPageProblem(pages: KueueQueueWorkloadsResponse[]): string | undefined {
  return pages.slice(1).find(page => page.workloadsError)?.workloadsError?.error
}

/**
 * Rows across pages, once each, in first-seen position with the latest data:
 * a member that finishes between pages is returned again among the finished.
 */
export function uniqueRows(pages: KueueQueueWorkloadsResponse[]): KueueQueueWorkload[] {
  const rows = new Map<string, KueueQueueWorkload>()
  for (const page of pages) {
    for (const row of page.workloads?.items ?? []) rows.set(row.uid, row)
  }
  return [...rows.values()]
}

/**
 * Whether the member counts are lower bounds: the Workload read was partial
 * or narrowed by Radar's collection, or LocalQueues weren't read somewhere,
 * so Workloads submitted there can't be placed. Reservation counts depend on
 * the Workload read alone.
 */
export function membersLowerBound(members: KueueQueueMembers, membership?: KueueQueueMembership): boolean {
  return isLowerBound(members.coverage) || (membership !== 'reservation' && members.submissionUnresolved != null)
}

/** The headline count, as a sourced fact: "≥N" when it is a lower bound. */
export function listedFact(members: KueueQueueMembers, local: boolean): Fact {
  const coverage = members.coverage
  const narrowedToYou = coverage.narrowedBy?.includes('user_access') ?? false
  const text = boundedCountLabel(members.counts.members, membersLowerBound(members))
  return {
    text: text === '–' ? 'None in what Radar read' : text,
    tone: 'neutral',
    source: local
      ? "Radar's read of this namespace's Workloads, finished ones included. Not the controller's count above."
      : `Radar's read of Workloads${narrowedToYou ? ' in the namespaces Radar shows you' : ''}, finished ones included. Not the controller's count above.`,
  }
}

/** Why the list is empty, from what the read covered. */
export function emptyMessage(members: KueueQueueMembers, local: boolean, filtered: boolean): string {
  if (filtered && members.counts.members > 0) return 'No Workloads match this filter.'
  const { status, narrowedBy } = members.coverage
  if (status === 'partial' || members.submissionUnresolved) return 'No Workloads found where Radar could read.'
  if (narrowedBy?.includes('namespace_collection')) return 'No Workloads in the namespaces Radar collects.'
  if (narrowedBy?.includes('namespace_pin')) return 'No Workloads in the namespace Radar is pinned to.'
  if (narrowedBy?.includes('user_access')) return 'No Workloads in the namespaces Radar shows you.'
  return local ? 'No Workloads are submitted to this queue.' : 'No Workloads belong to this queue.'
}

function confirmedEmptyNote(members: KueueQueueMembers, local: boolean): string | undefined {
  const coverage = members.coverage
  if (coverage.status !== 'complete' || coverage.narrowedBy?.length || members.submissionUnresolved) return undefined
  return local ? 'Radar read every Workload in this namespace.' : 'Radar read Workloads and LocalQueues in every namespace.'
}

/** Why the read covers fewer namespaces than the cluster has, when nothing else on screen says so. */
function scopeNotice(coverage: ResourceReadCoverage): string | undefined {
  if (coverage.narrowedBy?.includes('namespace_pin')) return "Radar is pinned to one namespace (--namespace-scope); Workloads elsewhere aren't read."
  return undefined
}

function MemberList({
  local, clusterQueue, members, rows, paged, laterPageError, onRefresh, membership, onMembershipChange, state, onStateChange, hasMore, loadingMore, onLoadMore, stale, onNavigate,
}: {
  local: boolean
  clusterQueue?: string
  members: KueueQueueMembers
  rows: KueueQueueWorkload[]
  paged?: boolean
  laterPageError?: string
  onRefresh?: () => void
  membership?: KueueQueueMembership
  onMembershipChange?: (membership: KueueQueueMembership | undefined) => void
  state?: string
  onStateChange?: (state: string | undefined) => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  stale?: boolean
  onNavigate?: (ref: NavigateRef) => void
}) {
  const filtered = !!membership || !!state
  const fact = listedFact(members, local)
  const notices = [
    resourceCoverageNotice(members.coverage, 'Workloads'),
    resourceNarrowedNotice(members.coverage),
    scopeNotice(members.coverage),
    unresolvedNotice(members),
  ].filter(Boolean) as string[]
  const lowerBound = membersLowerBound(members, membership)
  const chip = (count: number, bounded: boolean) => (bounded ? undefined : count)
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
          <FilterPill label={chipLabel('All', members.counts.members, membersLowerBound(members))} count={chip(members.counts.members, membersLowerBound(members))} active={!membership} onClick={() => onMembershipChange(undefined)} tooltip="Every Workload submitted here or whose admission names this queue" />
          <FilterPill label={chipLabel('Submitted here', members.counts.submission, membersLowerBound(members, 'submission'))} count={chip(members.counts.submission, membersLowerBound(members, 'submission'))} active={membership === 'submission'} onClick={() => onMembershipChange('submission')} tooltip="Workloads whose LocalQueue points at this ClusterQueue now" />
          <FilterPill label={chipLabel('Reserved here', members.counts.reservation, isLowerBound(members.coverage))} count={chip(members.counts.reservation, isLowerBound(members.coverage))} active={membership === 'reservation'} onClick={() => onMembershipChange('reservation')} tooltip="Workloads whose quota reservation is recorded in this ClusterQueue, admitted or not, finished ones included" />
        </div>
      )}
      {onStateChange && <StateFilter counts={members.counts.states} bounded={lowerBound} state={state} onStateChange={onStateChange} />}
      {rows.length === 0 ? (
        <div>
          <p className="text-sm text-theme-text-secondary">{emptyMessage(members, local, filtered)}</p>
          {!filtered && confirmedEmptyNote(members, local) && <p className="mt-1 text-xs text-theme-text-tertiary">{confirmedEmptyNote(members, local)}</p>}
        </div>
      ) : (
        <ul className={stale ? 'divide-y divide-theme-border opacity-60' : 'divide-y divide-theme-border'} aria-busy={stale || undefined}>
          {rows.map(row => (
            <WorkloadRow key={row.uid} row={row} local={local} clusterQueue={clusterQueue} onNavigate={onNavigate} />
          ))}
        </ul>
      )}
      {rows.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-theme-text-tertiary">
          <span>
            {showingText(rows.length, members.matching, lowerBound)} · sorted by {local ? 'name' : 'namespace and name'}, finished last; not admission order
          </span>
          <span className="flex gap-2">
            {(paged || laterPageError) && onRefresh && (
              <button type="button" className="btn-secondary px-2 py-1 text-xs" onClick={onRefresh} title="Pages are read at different moments; reread the list from the start">
                Refresh
              </button>
            )}
            {hasMore && onLoadMore && !laterPageError && (
              <button type="button" className="btn-secondary px-2 py-1 text-xs" disabled={loadingMore} onClick={onLoadMore}>
                {loadingMore ? 'Loading…' : 'Load more'}
              </button>
            )}
          </span>
        </div>
      )}
      {laterPageError && <p className="text-xs text-warning-text">Could not read the next page: {laterPageError}. Refresh to start over.</p>}
    </div>
  )
}

function chipLabel(label: string, count: number, bounded: boolean): string {
  return bounded && count > 0 ? `${label} (≥${count.toLocaleString()})` : label
}

/** "Showing X of N", where N is as of the first page: rows loaded later can outnumber it when the list grew. */
export function showingText(shown: number, matching: number, lowerBound: boolean): string {
  if (shown > matching) return `Showing ${shown.toLocaleString()}; the list changed while paging`
  return `Showing ${shown.toLocaleString()} of ${lowerBound ? '≥' : ''}${matching.toLocaleString()}`
}

function unresolvedNotice(members: KueueQueueMembers): string | undefined {
  const set = members.submissionUnresolved
  if (!set) return undefined
  const names = set.names ?? []
  const where = set.extent === 'all'
    ? 'any namespace it read Workloads in'
    : names.length > 0 && names.length <= 3 && names.length === set.count ? names.join(', ') : 'some namespaces'
  return `Radar couldn't read LocalQueues in ${where}, so Workloads there are listed only when their reservation names this queue.`
}

/**
 * Members grouped by the state the shared classification gives them, largest
 * first. Counts are Radar's, over every member it read for you; each row's
 * badge carries how much attention its state needs.
 */
function StateFilter({ counts, bounded, state, onStateChange }: { counts: Record<string, number>; bounded: boolean; state?: string; onStateChange: (state: string | undefined) => void }) {
  const entries = Object.entries(counts ?? {}).filter(([, n]) => n > 0)
  if (state && !entries.some(([code]) => code === state)) entries.push([state, 0])
  if (entries.length < 2 && !state) return null
  entries.sort(([a, x], [b, y]) => y - x || a.localeCompare(b))
  return (
    <div className="flex flex-wrap gap-1.5" role="group" aria-label="Filter by admission state">
      {entries.map(([code, n]) => (
        <FilterPill key={code} label={chipLabel(kueueAdmissionStateLabel(code), n, bounded)} count={bounded ? undefined : n} active={state === code} onClick={() => onStateChange(state === code ? undefined : code)} />
      ))}
    </div>
  )
}

function StateBadge({ row }: { row: KueueQueueWorkload }) {
  if (!row.admission?.state) return null
  const { label, severity } = kueueAdmissionStatePresentation(row.admission)
  const fact = row.admission?.blocker ?? row.admission?.inactive ?? row.admission?.disruption
  return (
    <span title={fact?.message || fact?.reason || undefined}>
      <Badge severity={severity} size="sm">{label}</Badge>
    </span>
  )
}

/** How this row belongs to the queue, in words, naming both queues when they disagree. */
export function membershipText(row: KueueQueueWorkload, local: boolean, clusterQueue: string | undefined): string {
  const reservedIn = row.reservation?.clusterQueue
  // A finished Workload's admission record says where quota was reserved,
  // not that it was admitted, nor that the quota is still held.
  const finished = row.admission?.state === 'finished' || row.phase === 'finished'
  const reservedWord = finished ? 'reservation recorded' : 'reserved'
  if (local) {
    if (!reservedIn) return 'No reservation'
    const cap = finished ? 'Reservation recorded' : 'Reserved'
    if (clusterQueue && reservedIn !== clusterQueue) return `${cap} in ${reservedIn} · this LocalQueue now points to ${clusterQueue}`
    return `${cap} in ${reservedIn}`
  }
  const parts: string[] = []
  const submitted = row.membership.includes('submission')
  const reserved = row.membership.includes('reservation')
  if (submitted) parts.push(`Submitted through ${row.queueName}`)
  if (reserved) parts.push(`${reservedWord} here`)
  else if (reservedIn) parts.push(`${reservedWord} in ${reservedIn}`)
  else parts.push('no reservation')
  if (!submitted && row.queueName) {
    if (row.submission) parts.push(`its LocalQueue ${row.queueName} now points to ${row.submission.clusterQueue}`)
    else if (row.localQueueUnread) parts.push(`LocalQueue ${row.queueName} not read`)
    else parts.push(`LocalQueue ${row.queueName} not found`)
  }
  const text = parts.join(' · ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

function WorkloadRow({ row, local, clusterQueue, onNavigate }: {
  row: KueueQueueWorkload
  local: boolean
  clusterQueue?: string
  onNavigate?: (ref: NavigateRef) => void
}) {
  return (
    <li className="py-2 first:pt-0">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm [&_button]:text-left">
        <span className="min-w-0 break-all font-medium">
          <ResourceLink name={row.name} kind="workloads" namespace={row.namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
        </span>
        {!local && <span className="break-all text-xs text-theme-text-tertiary">{row.namespace}</span>}
        <StateBadge row={row} />
        {row.deleting && <Badge severity="neutral" size="sm">Deleting</Badge>}
      </div>
      <p className="mt-0.5 break-words text-xs text-theme-text-secondary">
        {membershipText(row, local, clusterQueue)}
        {row.createdAt && <span className="text-theme-text-tertiary"> · created {formatRelativeAgeTime(row.createdAt)}</span>}
      </p>
    </li>
  )
}

/** Why a LocalQueue's Workloads aren't in the list, by the code of the gap that left them out. */
export function unreadWorkloadsText(lq: KueueQueueLocalQueue): string {
  const where = `in ${lq.namespace}`
  switch (lq.workloadsGap) {
    case 'user_forbidden': return `You can't list Workloads ${where}, so its Workloads aren't listed above.`
    case 'user_permission_check_failed': return `Radar hasn't confirmed you may list Workloads ${where} yet; refresh to check again.`
    case 'kind_sync_pending': return `Workloads ${where} are still loading.`
    case 'kind_sync_timed_out': return `Workloads ${where} haven't finished loading.`
    case 'radar_forbidden':
    case 'radar_credentials_rejected':
    case 'radar_namespace_not_collected': return `Radar's own access doesn't cover Workloads ${where}, so its Workloads aren't listed above.`
    default: return `Radar didn't read Workloads ${where}, so its Workloads aren't listed above.`
  }
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
    headline = local ? `You can't list Workloads in ${namespace}.` : "You can't list Workloads in any namespace Radar shows you."
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
        <p className="text-sm text-theme-text-secondary">{response.localQueuesError.error_code === 'user_forbidden' ? "You can't list LocalQueues in any namespace Radar shows you." : response.localQueuesError.error}</p>
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
              {!lq.workloadsRead && <p className="text-xs text-theme-text-tertiary">{unreadWorkloadsText(lq)}</p>}
            </li>
          ))}
        </ul>
      )}
      {lqs.items.length < lqs.total && <p className="mt-1 text-xs text-theme-text-tertiary">Showing {lqs.items.length} of {pluralize(lqs.total, 'LocalQueue')}.</p>}
    </div>
  )
}
