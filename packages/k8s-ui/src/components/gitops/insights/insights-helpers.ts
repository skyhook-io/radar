// Pure helpers for GitOps insight rendering. Extracted from
// GitOpsInsightViews.tsx so they can be unit-tested without importing JSX.

import { SEVERITY_DOT, type Severity } from '../../../utils/badge-colors'
import type { GitOpsChange, GitOpsHistoryItem } from '../../../types'
import type { SyncStatus, GitOpsHealthStatus } from '../../../types/gitops'

const SYNC_STATUS_SET = new Set<SyncStatus>(['Synced', 'OutOfSync', 'Reconciling', 'Unknown'])
const HEALTH_STATUS_SET = new Set<GitOpsHealthStatus>(['Healthy', 'Progressing', 'Degraded', 'Suspended', 'Missing', 'Unknown'])

// normalizeSyncStatus narrows an arbitrary string (e.g. a GitOpsChange.category
// or .sync from the backend) onto the SyncStatusBadge's expected union. Unknown
// values fall back to "Unknown" rather than rendering whatever the badge's
// default-case branch happens to do — silently rendering wrong was the failure
// mode of the `as any` casts this replaces.
export function normalizeSyncStatus(value: string | undefined | null): SyncStatus {
  if (value && (SYNC_STATUS_SET as Set<string>).has(value)) return value as SyncStatus
  return 'Unknown'
}

export function normalizeHealthStatus(value: string | undefined | null): GitOpsHealthStatus {
  if (value && (HEALTH_STATUS_SET as Set<string>).has(value)) return value as GitOpsHealthStatus
  return 'Unknown'
}

// --- Resources table: filtering + sorting -------------------------------
// Pure predicates + rank functions backing the Resources list's search box,
// status facets, and sortable columns. Kept here (not inline in the view) so
// the sort/filter behavior is unit-testable without rendering.

// The status facets an operator filters the Resources list by — the three
// states worth isolating on a broadly-drifted app. (These replaced the
// per-resource OutOfSync "issues" that used to restate the whole table.)
export type ResourceStatusFacet = 'outOfSync' | 'degraded' | 'missing'

export type ResourceSortKey = 'order' | 'name' | 'sync' | 'health'

// changeSync/changeHealth normalize a change's raw backend strings onto the
// badge unions. `sync` falls back to `category` to match how ChangeRow and
// the sync badge resolve it.
export function changeSync(change: GitOpsChange): SyncStatus {
  return normalizeSyncStatus(change.sync ?? change.category)
}

export function changeHealth(change: GitOpsChange): GitOpsHealthStatus {
  return normalizeHealthStatus(change.health)
}

export function isArgoResourceSyncEligible(change: GitOpsChange, declared: boolean, hook?: string): boolean {
  if (!declared || hook || change.hookPhase) return false
  return changeSync(change) === 'OutOfSync' || changeHealth(change) === 'Missing' || change.category === 'Missing'
}

// Rank sync/health so an ascending sort surfaces the states an operator cares
// about first (drift, then failures). Mirrors the GitOps application table's
// syncRank/healthRank so the two tables order status the same way.
export function syncStatusRank(sync: SyncStatus): number {
  return ({ OutOfSync: 0, Reconciling: 1, Unknown: 2, Synced: 3 } as Record<SyncStatus, number>)[sync]
}

export function healthStatusRank(health: GitOpsHealthStatus): number {
  return ({ Missing: 0, Degraded: 1, Progressing: 2, Unknown: 3, Suspended: 4, Healthy: 5 } as Record<GitOpsHealthStatus, number>)[health]
}

export function changeMatchesSearch(change: GitOpsChange, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const { kind, name, namespace, group } = change.ref
  return [kind, name, namespace, group].some((s) => s?.toLowerCase().includes(q))
}

// Union match: a change passes when it satisfies ANY active facet (empty set
// = no filter). OutOfSync keys off sync status; Degraded/Missing off health.
export function changeMatchesFacets(change: GitOpsChange, facets: Set<ResourceStatusFacet>): boolean {
  if (facets.size === 0) return true
  if (facets.has('outOfSync') && changeSync(change) === 'OutOfSync') return true
  if (facets.has('degraded') && changeHealth(change) === 'Degraded') return true
  if (facets.has('missing') && changeHealth(change) === 'Missing') return true
  return false
}

export interface ResourceStatusCounts {
  outOfSync: number
  degraded: number
  missing: number
}

export function resourceStatusCounts(changes: GitOpsChange[]): ResourceStatusCounts {
  const counts: ResourceStatusCounts = { outOfSync: 0, degraded: 0, missing: 0 }
  for (const c of changes) {
    if (changeSync(c) === 'OutOfSync') counts.outOfSync++
    const health = changeHealth(c)
    if (health === 'Degraded') counts.degraded++
    if (health === 'Missing') counts.missing++
  }
  return counts
}

// Map GitOps-flavored vocabulary (Argo/Flux phase strings, insight Issue
// severities) onto the canonical Severity tokens used by SEVERITY_BADGE /
// SEVERITY_TEXT / SEVERITY_DOT. Centralizing this keeps call sites from
// hand-rolling Tailwind color literals (which bypass theme overrides and
// drift from the rest of the OSS surface).
export function gitopsToSeverity(value: string | undefined): Severity {
  const v = (value || '').toLowerCase()
  if (!v) return 'neutral'
  if (v === 'critical' || v.includes('fail') || v.includes('error')) return 'error'
  if (v === 'alert') return 'alert'
  if (v === 'warning' || v.includes('terminat') || v.includes('pending') || v.includes('wait')) return 'warning'
  if (v === 'info' || v.includes('progress') || v.includes('running') || v.includes('reconcil')) return 'info'
  if (v.includes('succeed') || v === 'healthy' || v === 'ok') return 'success'
  return 'neutral'
}

// Map a phase string to its dot color, or null if the phase carries no
// meaningful signal (caller decides whether to fall back to inference).
export function phaseToTone(phase?: string): string | null {
  const sev = gitopsToSeverity(phase)
  return sev === 'neutral' ? null : SEVERITY_DOT[sev]
}

// Best-effort phase recovery from message text. Argo only populates the
// phase field on the most recent revision; older entries lose their
// outcome signal unless we read it from the human-readable message.
export function messageToPhase(message?: string): string | undefined {
  if (!message) return undefined
  const m = message.toLowerCase()
  if (m.includes('successfully') || m.includes('succeeded')) return 'succeeded'
  if (m.includes('failed') || m.includes('error')) return 'failed'
  if (m.includes('progressing') || m.includes('reconciling')) return 'progressing'
  return undefined
}

export interface EntryTone {
  dot: string
  inferredFrom?: string
}

// Pick a dot color via the canonical SEVERITY_DOT palette. Argo only
// populates phase on the most recent revision; older entries fall back to
// inference from the message string so the timeline still encodes outcome
// at a glance instead of degenerating into a column of neutral dots.
export function entryTone(item: GitOpsHistoryItem): EntryTone {
  const explicit = phaseToTone(item.phase)
  if (explicit) return { dot: explicit }
  const inferred = phaseToTone(messageToPhase(item.message))
  if (inferred) return { dot: inferred, inferredFrom: 'inferred from message' }
  // No signal at all — keep the dot visible but neutral. Coloring it green
  // would be a guess (a failed revision can sit at history's head with no
  // successor), and a wrong-color dot is worse than no information.
  return { dot: SEVERITY_DOT.neutral, inferredFrom: 'no phase information' }
}

// Compact a source string for inline display. Argo emits the full GitHub URL
// followed by " · path/within/repo", which dominates the timeline row when
// rendered raw. Strip the protocol+host (full string still shown on hover via
// title), and shorten deep paths to "head/…/leaf" form.
export function compactSource(source?: string): string {
  if (!source) return ''
  const [repoPart, ...pathParts] = source.split(' · ')
  const repo = repoPart
    .replace(/^https?:\/\/(www\.)?github\.com\//, '')
    .replace(/^https?:\/\//, '')
    .replace(/\/$/, '')
  const path = pathParts.join(' · ').trim()
  if (!path) return repo
  const segments = path.split('/').filter(Boolean)
  const shortPath = segments.length > 3
    ? `${segments[0]}/…/${segments[segments.length - 1]}`
    : path
  return `${repo} · ${shortPath}`
}
