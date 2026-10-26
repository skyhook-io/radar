import type { ReadNameSet } from '../types/read-coverage'
import type { ResourceCoverage } from '../types/resource-coverage'
import { pluralNoun, pluralize } from './pluralize'

// Only a partial answer is qualified. Unknown coverage (an older Radar, or a
// host that passes none) keeps the presentation that predates coverage: plain
// counts, empty kinds hidden, and no notice.

// gapPhrases says why the rest of the scope was not read, most actionable first.
const gapPhrases: [string, (where: string) => string][] = [
  ['kind_sync_pending', where => `some ${where} are still loading`],
  ['user_permission_check_failed', where => `access to some ${where} could not be verified`],
  ['user_forbidden', where => `your permissions exclude some ${where}`],
  ['user_namespace_not_visible', where => `some ${where} aren't visible to you`],
  ['outside_namespace_pin', () => 'Radar is pinned to another namespace'],
  ['radar_credentials_rejected', where => `Kubernetes rejected Radar's credentials for some ${where}`],
  ['radar_forbidden', where => `Radar's collector could not read some ${where}`],
  ['kind_sync_timed_out', where => `some ${where} haven't finished loading`],
  ['radar_namespace_not_collected', where => `Radar doesn't collect this kind in some ${where}`],
]

/** The first few names of a set and how many more there are; empty when the set is too large to name. */
function namespacePreview({ names, count }: ReadNameSet): string {
  if (!names?.length) return ''
  const extra = count - Math.min(names.length, 3)
  return `${names.slice(0, 3).join(', ')}${extra > 0 ? ` and ${extra} more` : ''}`
}

export function resourceCoverageNotice(coverage: ResourceCoverage | undefined, kind = "resources"): string | undefined {
  if (coverage?.status !== 'partial') return undefined
  const read = coverage.read
  if (read?.type !== 'namespaces' || !read.namespaces) return 'Partial results. Radar could not read the whole requested scope.'
  const { count } = read.namespaces
  const scope = namespacePreview(read.namespaces) || `${count.toLocaleString()} ${pluralNoun(count, 'namespace')}`
  // A broad request named no namespaces, so none of them were "requested".
  const where = coverage.scope.type === 'namespaces' && !coverage.narrowedBy?.length ? 'requested namespaces' : 'namespaces'
  const codes = new Set(coverage.gaps.map(gap => gap.code))
  const phrase = gapPhrases.find(([code]) => codes.has(code))
  const affected = coverage.gaps.filter(gap => gap.code === phrase?.[0]).map(gap => gap.namespaces)
  const names = [...new Set(affected.flatMap(set => set?.names ?? []))]
  const canName = affected.length > 0 && names.length > 0 && names.length <= 3 && affected.every(set => set && !set.extent && set.names?.length === set.count)
  const namedScope = `${kind} in ${names.join(', ')}`
  const namedPhrases: Record<string, string> = {
    kind_sync_pending: `${namedScope} are still loading`,
    user_permission_check_failed: `access to ${namedScope} could not be verified`,
    user_forbidden: `your permissions exclude ${namedScope}`,
    user_namespace_not_visible: `${names.join(', ')} ${names.length === 1 ? "isn't" : "aren't"} visible to you`,
    radar_credentials_rejected: `Kubernetes rejected Radar's credentials for ${namedScope}`,
    radar_forbidden: `Radar's collector could not read ${namedScope}`,
    kind_sync_timed_out: `${namedScope} haven't finished loading`,
    radar_namespace_not_collected: `Radar doesn't collect ${namedScope}`,
  }
  const gap = (canName && phrase && namedPhrases[phrase[0]]) || phrase?.[1](where) || `other ${where} were not read`
  return `Partial results. Radar read ${scope}; ${gap}.`
}

/**
 * A complete answer whose scope Radar's own collection narrowed. The other
 * narrowings (the caller's access and Radar's pin) already show
 * in the namespace picker, so they get no note.
 */
export function resourceNarrowedNotice(coverage: ResourceCoverage | undefined): string | undefined {
  if (coverage?.status !== 'complete' || !coverage.narrowedBy?.includes('namespace_collection')) return undefined
  const namespaces = coverage.scope.namespaces
  if (!namespaces) return 'Radar collects this kind only in some namespaces.'
  const preview = namespacePreview(namespaces)
  // With the caller's access also narrowing it, the scope is the part of their
  // namespaces Radar collects, not everything Radar collects.
  const where = coverage.narrowedBy.includes('user_access')
    ? `${namespaces.count.toLocaleString()} of your namespaces`
    : pluralize(namespaces.count, 'namespace')
  return `Radar collects this kind only in ${where}${preview ? `: ${preview}` : ''}.`
}

export function resourceEmptyMessage(kind: string, coverage: ResourceCoverage | undefined, hasRows: boolean): string {
  if (hasRows) return `No ${kind} match the current filters`
  if (coverage?.status === 'complete' && coverage.narrowedBy?.includes('namespace_collection')) return `No ${kind} found in the namespaces Radar collects`
  if (coverage?.status === 'complete') return `No ${kind} found in the requested scope`
  if (coverage?.status === 'partial') return `No ${kind} found in the namespaces Radar could read`
  return `No ${kind} found`
}

/**
 * Whether a read's numbers are a lower bound for the cluster: it was partial,
 * or Radar's collection narrowed it to the namespaces it collects. Views show
 * cluster totals, which a read of only the collected namespaces can't
 * establish.
 */
export function isLowerBound(coverage: ResourceCoverage | undefined): boolean {
  return coverage?.status === 'partial' || (coverage?.status === 'complete' && coverage.narrowedBy?.includes('namespace_collection') === true)
}

/**
 * A zero that hides its kind as empty. A lower-bound zero proves nothing
 * about the namespaces Radar couldn't read or doesn't collect.
 */
export function isEmptyCount(count: number | null | undefined, coverage: ResourceCoverage | undefined): boolean {
  return count === 0 && !isLowerBound(coverage)
}

/** A lower-bound count reads "≥N", and a lower-bound zero is no count at all. */
export function boundedCountLabel(count: number, lowerBound: boolean): string {
  if (!lowerBound) return count.toLocaleString()
  return count === 0 ? '–' : `≥${count.toLocaleString()}`
}

/** One kind's count: "≥N" when the read is a lower bound for the cluster (see isLowerBound). */
export function resourceCountLabel(count: number, coverage: ResourceCoverage | undefined): string {
  return boundedCountLabel(count, isLowerBound(coverage))
}

/** Why a kind's count is a lower bound, or undefined when it is exact. */
export function resourceCountHint(coverage: ResourceCoverage | undefined): string | undefined {
  if (coverage?.status === 'partial') return 'Count in the namespaces Radar could read; more may exist outside them.'
  if (isLowerBound(coverage)) return 'Radar collects this kind only in some namespaces; more may exist elsewhere.'
  return undefined
}
