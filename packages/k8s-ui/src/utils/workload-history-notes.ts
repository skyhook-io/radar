import type { ReadGap, ReadKindRef } from '../types/read-coverage'
import { canonicalReadCode, gapReasonText, READ_ACTIONS, readActionFor } from './read-actions'

/**
 * Whether Radar holds every change since a workload existed, as Radar's
 * issuesapi.HistoryCoverage serializes it. Present only when incomplete;
 * complete_since is sent only to callers who can read every namespace.
 * An absent coverage value means complete history or an older Radar; it
 * does not establish completeness on its own.
 */
export interface WorkloadHistoryCoverage {
  incomplete: boolean
  complete_since?: string
  reasons: string[]
}

// Kinds a history gap names, as a reader knows them.
const KIND_LABELS: Record<string, string> = {
  'kueue.x-k8s.io/Workload': 'Kueue Workloads',
  'autoscaling.x-k8s.io/ProvisioningRequest': 'ProvisioningRequests',
}

function kindLabel(ref: ReadKindRef): string {
  return KIND_LABELS[`${ref.group ?? ''}/${ref.kind}`] ?? `${ref.kind} resources`
}

function joinWords(words: string[]): string {
  if (words.length <= 1) return words[0] ?? ''
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
}

function gapClause(gap: ReadGap): string {
  const code = canonicalReadCode(gap.code)
  switch (code) {
    case 'radar_forbidden':
      return "Radar's Kubernetes identity may not list them"
    case 'radar_credentials_rejected':
      return "Kubernetes rejected Radar's credentials"
    case 'radar_kind_not_collected':
    case 'radar_namespace_not_collected':
      return "Radar doesn't collect them here"
  }
  if (typeof code === 'string' && Object.hasOwn(READ_ACTIONS, code)) {
    return readActionFor(code) === 'user_access'
      ? "you don't have access to list them in this namespace"
      : gapReasonText(code)
  }
  switch (gap.action) {
    case 'user_access':
      return "you don't have access to list them in this namespace"
    case 'radar_config':
      return "Radar's connection or collection settings need attention"
    case 'retry':
      return 'retry the read'
    case 'wait':
      return 'wait and retry the read'
    case 'check_target':
      return 'check the requested target and retry'
    case 'change_request':
      return 'change the read request'
    default:
      return gapReasonText(gap.code)
  }
}

function formatSince(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

/**
 * Sentences for a workload Timeline's note line: since when Radar's history
 * is complete, and which related kinds it can't include for this viewer.
 * Unknown reasons and codes from a newer Radar still produce a sentence.
 */
export function workloadHistoryNotes(coverage?: WorkloadHistoryCoverage | null, gaps?: ReadGap[] | null): string[] {
  const notes: string[] = []
  if (coverage?.incomplete) {
    const why: string[] = []
    if (coverage.reasons.includes('observation_started_late')) why.push('Radar began recording part of this history later')
    if (coverage.reasons.includes('events_evicted')) why.push('older history was deleted to stay within its limits')
    const head = coverage.complete_since
      ? `Changes before ${formatSince(coverage.complete_since)} may be missing`
      : 'Earlier changes may be missing'
    notes.push(why.length > 0 ? `${head}: ${joinWords(why)}.` : `${head}.`)
  }
  for (const gap of gaps ?? []) {
    const kinds = gap.kinds?.names?.map(kindLabel) ?? []
    const what = kinds.length > 0 ? `Changes to ${joinWords(kinds)}` : 'Some related changes'
    notes.push(`${what} aren't shown: ${gapClause(gap)}.`)
  }
  return notes
}
