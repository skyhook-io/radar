import { useState, type ReactNode } from 'react'
import { Tooltip, formatDuration, formatRelativeAgeTime, getBackupStatus, getRestoreStatus, toneTextClass, veleroLocalTime, veleroMomentText, veleroPlainReason, VELERO_FAILED_PHASES } from '@skyhook-io/k8s-ui'
import type { VeleroRunRow, VeleroRunsResponse } from '../../api/velero'
import { WholeNames } from './shared'
import { Notice } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageEmpty, coverageLabel } from '../workspace/coverage'

// How a Velero run (a Backup or Restore) reads in the lists and on the pages.

export function expired(expiration: string | undefined): boolean {
  return !!expiration && new Date(expiration).getTime() < Date.now()
}

export function durationText(start?: string, end?: string): string | undefined {
  if (!start || !end) return undefined
  return formatDuration(Math.max(0, Date.parse(end) - Date.parse(start)), true)
}

export function Outcome({ badge }: { badge: { text: string; color: string } }) {
  return <span className={`badge whitespace-nowrap ${badge.color}`}>{badge.text}</span>
}

export function RunOutcome({ kind, phase }: { kind: 'Backup' | 'Restore'; phase?: string }) {
  const resource = { status: { phase } }
  return <Outcome badge={kind === 'Backup' ? getBackupStatus(resource) : getRestoreStatus(resource)} />
}

export const FAILED = VELERO_FAILED_PHASES
// Velero refused the run or it failed outright; the rest of FAILED partly failed.
const FAILED_OUTRIGHT = new Set(['Failed', 'FailedValidation'])

/** Why a run failed, under its outcome: Radar's plain reading when it has one, Velero's text on hover. */
export function RunReason({ run }: { run: Pick<VeleroRunRow, 'phase' | 'reason'> }) {
  if (!run.reason || !FAILED.has(run.phase ?? '')) return null
  const plain = veleroPlainReason(run.reason)
  return (
    <Tooltip content={run.reason}>
      <div className={`mt-0.5 line-clamp-2 min-w-0 break-words text-xs ${toneTextClass(FAILED_OUTRIGHT.has(run.phase ?? '') ? 'unhealthy' : 'degraded')}`}><WholeNames text={plain ?? run.reason} /></div>
    </Tooltip>
  )
}

export function When({ iso, fallback }: { iso?: string; fallback: string }) {
  if (!iso) return <span className="text-theme-text-tertiary">{fallback}</span>
  return (
    <Tooltip content={veleroLocalTime(iso)}>
      <span>{formatRelativeAgeTime(iso)}</span>
    </Tooltip>
  )
}

/** When Velero deletes a run: "in 21h", or "expired" once its retention passed. */
export function Expires({ iso }: { iso?: string }) {
  if (!iso) return <span className="text-theme-text-tertiary">—</span>
  if (expired(iso)) {
    return (
      <Tooltip content={`${veleroLocalTime(iso)} · Velero deletes it at its next garbage collection`}>
        <span className="text-theme-text-tertiary">expired</span>
      </Tooltip>
    )
  }
  return (
    <Tooltip content={veleroLocalTime(iso)}>
      <span>{veleroMomentText(iso)}</span>
    </Tooltip>
  )
}

/** "0 · 2": errors and warnings, each coloured only when there are some. */
export function RunCounts({ errors, warnings }: { errors: number; warnings: number }) {
  return (
    <span className="tabular-nums">
      <span className={errors > 0 ? toneTextClass('unhealthy') : 'text-theme-text-tertiary'}>{errors}</span>
      <span className="text-theme-text-tertiary"> · </span>
      <span className={warnings > 0 ? toneTextClass('degraded') : 'text-theme-text-tertiary'}>{warnings}</span>
    </span>
  )
}

/** All runs, newest first, or only the failed ones. Opens on all, so a run in progress is in view. */
export function useRunView<R extends VeleroRunRow>(rows: R[]) {
  const failed = rows.filter((r) => FAILED.has(r.phase ?? ''))
  const [view, setView] = useState<'all' | 'failed'>('all')
  return { failed, view, setView, shown: view === 'failed' ? failed : rows }
}

/** A count over what was read: exact only when the read covered everything. */
export function boundedCount(n: number, query: { data?: VeleroRunsResponse }): string {
  const partial = !query.data || INCOMPLETE_COVERAGE_STATES.has(query.data.coverage.state) || query.data.truncated
  return partial && n > 0 ? `≥${n}` : String(n)
}

/** The All and Failed segments; the failed count is red when there are any. */
export function runViewOptions(failed: number, all: number, query: { data?: VeleroRunsResponse }) {
  const failedCount = boundedCount(failed, query)
  return [
    { id: 'all' as const, label: 'All', count: boundedCount(all, query) },
    { id: 'failed' as const, label: 'Failed', count: failed > 0 ? <span className={toneTextClass('unhealthy')}>{failedCount}</span> : failedCount },
  ]
}

/** Why a run list is empty, or that what was read holds no match. */
export function runsEmpty(query: { data?: VeleroRunsResponse; isPending: boolean; error: unknown }, noun: string, filtered: string): string {
  if (query.isPending) return `Loading ${noun}…`
  if (!query.data) return `${noun[0].toUpperCase()}${noun.slice(1)} could not be read: ${(query.error as Error | null)?.message ?? 'unknown error'}`
  const cov = query.data.coverage
  if (INCOMPLETE_COVERAGE_STATES.has(cov.state)) return `${filtered} in what Radar could read (${coverageLabel(cov)}).`
  return query.data.runs.length === 0 ? coverageEmpty(cov, noun) : `${filtered}.`
}

/** A read that failed or is partial, said above the rows it leaves. */
export function RunsReadNotice({ query, noun }: { query: { data?: VeleroRunsResponse; error: unknown }; noun: string }) {
  const notes: ReactNode[] = []
  if (query.error && query.data) notes.push(`The last refresh of ${noun} failed (${(query.error as Error).message}); what is shown is from before it.`)
  if (query.data && INCOMPLETE_COVERAGE_STATES.has(query.data.coverage.state)) notes.push(`Some ${noun} were not read: ${coverageLabel(query.data.coverage)}. Counts are lower bounds.`)
  if (query.data?.truncated) notes.push(`Only the newest ${query.data.runs.length} of ${query.data.total} ${noun} are listed.`)
  if (notes.length === 0) return null
  return <Notice>{notes.map((n, i) => <div key={i}>{n}</div>)}</Notice>
}
