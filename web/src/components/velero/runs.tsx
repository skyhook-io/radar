import type { ReactNode } from 'react'
import { Tooltip, formatRelativeAgeTime, getBackupStatus, getRestoreStatus, veleroLocalTime } from '@skyhook-io/k8s-ui'
import type { VeleroRunsResponse } from '../../api/velero'
import { Notice } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageEmpty, coverageLabel } from '../workspace/coverage'

// How a Velero run (a Backup or Restore) reads in the lists and on the pages.

export function expired(expiration: string | undefined): boolean {
  return !!expiration && new Date(expiration).getTime() < Date.now()
}

/** How long until a time ahead, e.g. "21h left". */
export function leftText(iso: string): string {
  const s = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 1000))
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m left`
  if (s < 2 * 86400) return `${Math.round(s / 3600)}h left`
  return `${Math.round(s / 86400)} days left`
}

export function durationText(start?: string, end?: string): string | undefined {
  if (!start || !end) return undefined
  const s = Math.max(0, Math.round((new Date(end).getTime() - new Date(start).getTime()) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function Outcome({ badge }: { badge: { text: string; color: string } }) {
  return <span className={`badge whitespace-nowrap ${badge.color}`}>{badge.text}</span>
}

export function RunOutcome({ kind, phase }: { kind: 'Backup' | 'Restore'; phase?: string }) {
  const resource = { status: { phase } }
  return <Outcome badge={kind === 'Backup' ? getBackupStatus(resource) : getRestoreStatus(resource)} />
}

export const FAILED = new Set(['Failed', 'PartiallyFailed', 'FailedValidation', 'FinalizingPartiallyFailed', 'WaitingForPluginOperationsPartiallyFailed'])

export function When({ iso, fallback }: { iso?: string; fallback: string }) {
  if (!iso) return <span className="text-theme-text-tertiary">{fallback}</span>
  return (
    <Tooltip content={veleroLocalTime(iso)}>
      <span>{formatRelativeAgeTime(iso)}</span>
    </Tooltip>
  )
}

export function KeptUntil({ iso }: { iso?: string }) {
  if (!iso) return <>—</>
  if (expired(iso)) {
    return (
      <Tooltip content="Velero deletes it at its next garbage collection.">
        <span className="text-theme-text-tertiary">past retention</span>
      </Tooltip>
    )
  }
  return (
    <Tooltip content={veleroLocalTime(iso)}>
      <span>{leftText(iso)}</span>
    </Tooltip>
  )
}

/** A count over what was read: exact only when the read covered everything. */
export function boundedCount(n: number, query: { data?: VeleroRunsResponse }): string {
  const partial = !query.data || INCOMPLETE_COVERAGE_STATES.has(query.data.coverage.state) || query.data.truncated
  return partial && n > 0 ? `≥${n}` : String(n)
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

