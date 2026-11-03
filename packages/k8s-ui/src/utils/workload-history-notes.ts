import type { ReadGap, ReadKindRef } from '../types/read-coverage'
import { gapReasonText, readActionFor } from './read-actions'

/**
 * Whether Radar holds every change since a workload existed, as Radar's
 * issuesapi.HistoryCoverage serializes it. Present only when incomplete;
 * complete_since is sent only to callers who can read every namespace.
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

function gapClause(code: string): string {
  switch (readActionFor(code)) {
    case 'user_access':
      return "you don't have access to list them in this namespace"
    case 'radar_config':
      return "Radar doesn't collect them here"
    default:
      return gapReasonText(code)
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
    if (coverage.reasons.includes('observation_started_late')) why.push('Radar started recording after this workload was created')
    if (coverage.reasons.includes('events_evicted')) why.push('older history was deleted to stay within its limits')
    const head = coverage.complete_since
      ? `Changes before ${formatSince(coverage.complete_since)} may be missing`
      : 'Earlier changes may be missing'
    notes.push(why.length > 0 ? `${head}: ${joinWords(why)}.` : `${head}.`)
  }
  for (const gap of gaps ?? []) {
    const kinds = gap.kinds?.names?.map(kindLabel) ?? []
    const what = kinds.length > 0 ? `Changes to ${joinWords(kinds)}` : 'Some related changes'
    notes.push(`${what} aren't shown: ${gapClause(gap.code)}.`)
  }
  return notes
}
