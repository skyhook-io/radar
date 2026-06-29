// Pure helpers for GitOps insight rendering. Extracted from
// GitOpsInsightViews.tsx so they can be unit-tested without importing JSX.

import { SEVERITY_DOT, type Severity } from '../../../utils/badge-colors'
import type { GitOpsHistoryItem } from '../../../types'
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
