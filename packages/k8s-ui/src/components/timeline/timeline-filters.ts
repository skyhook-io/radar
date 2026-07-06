import type { TimelineEvent } from '../../types'
import { isChangeEvent, isK8sEvent } from '../../types'
import { pluralize } from '../../utils/pluralize'
import type { TimelineSort } from './timeline-lane-sort'

// Legacy single-value activity filter. Retained for the home-page deep-link
// (`initialFilter`) compat path that seeds the multi-select from one preset.
export type ActivityTypeFilter = 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy'

// The individually-selectable activity-type chips (no 'all' — an empty selection
// means "everything", so 'all' has no key of its own).
export type ActivityFilterKey = 'changes' | 'k8s_events' | 'warnings' | 'unhealthy'

// Per-key semantics, mirroring the list view's original inline filter:
//   changes    → informer/historical resource mutations
//   k8s_events → native K8s Event objects (Normal + Warning)
//   warnings   → only K8s Warning events (matches the home-page count)
//   unhealthy  → changes whose health state is unhealthy or degraded (no K8s events)
function matchesActivityKey(event: TimelineEvent, key: ActivityFilterKey): boolean {
  switch (key) {
    case 'changes':
      return isChangeEvent(event)
    case 'k8s_events':
      return isK8sEvent(event)
    case 'warnings':
      return event.eventType === 'Warning'
    case 'unhealthy':
      return isChangeEvent(event) && (event.healthState === 'unhealthy' || event.healthState === 'degraded')
  }
}

// Predicate for the activity-type chips. Extracted so the list and swimlane can't
// drift — both filter their event stream through the exact same rules. Multi-select
// with union semantics: an empty selection matches everything; otherwise an event
// matches if it satisfies ANY selected key.
export function matchesActivityFilter(event: TimelineEvent, selected: readonly ActivityFilterKey[]): boolean {
  if (selected.length === 0) return true
  return selected.some((key) => matchesActivityKey(event, key))
}

// Free-text search predicate. Uses the list view's richer field set (adds the
// diff summary the swimlane previously ignored) so both views match identically.
export function matchesTimelineSearch(event: TimelineEvent, term: string): boolean {
  if (!term) return true
  const t = term.toLowerCase()
  return (
    event.name.toLowerCase().includes(t) ||
    event.kind.toLowerCase().includes(t) ||
    (event.namespace?.toLowerCase().includes(t) ?? false) ||
    (event.reason?.toLowerCase().includes(t) ?? false) ||
    (event.message?.toLowerCase().includes(t) ?? false) ||
    (event.diff?.summary?.toLowerCase().includes(t) ?? false)
  )
}

// Human-readable summary of the active content filters, for the filtered-empty
// state's reason line (e.g. `search "dfasdf" · 2 activity filters · 3 kinds ·
// deleted hidden`). Returns '' when nothing is filtering. Order mirrors the
// toolbar left-to-right. `showDeleted` defaults on, so only its hidden state
// counts as active.
export function describeActiveFilters(opts: {
  search: string
  activityFilter: readonly ActivityFilterKey[]
  kindFilter: readonly string[]
  showDeleted: boolean
}): string {
  const parts: string[] = []
  const search = opts.search.trim()
  if (search) parts.push(`search "${search}"`)
  if (opts.activityFilter.length > 0) parts.push(pluralize(opts.activityFilter.length, 'activity filter'))
  if (opts.kindFilter.length > 0) parts.push(pluralize(opts.kindFilter.length, 'kind'))
  if (!opts.showDeleted) parts.push('deleted hidden')
  return parts.join(' · ')
}

export interface ActivityStats {
  total: number
  changes: number
  warnings: number
  unhealthy: number
  deleted: number
}

// Chip counts. Derived from the full (unfiltered) events array so the badges
// show totals regardless of the active filter — identical in both views.
export function computeActivityStats(events: TimelineEvent[] | undefined): ActivityStats {
  if (!events || events.length === 0) {
    return { total: 0, changes: 0, warnings: 0, unhealthy: 0, deleted: 0 }
  }
  let changes = 0
  let warnings = 0
  let unhealthy = 0
  let deleted = 0
  for (const e of events) {
    if (isChangeEvent(e)) changes++
    if (e.eventType === 'Warning') warnings++
    if (isChangeEvent(e) && (e.healthState === 'unhealthy' || e.healthState === 'degraded')) unhealthy++
    if (e.eventType === 'delete') deleted++
  }
  return { total: events.length, changes, warnings, unhealthy, deleted }
}

// Count of non-default choices in the View menu — drives the badge on the "View"
// button. Deleted-visibility moved to its own toolbar toggle (shows its own
// state); defaults that count as zero: grouping by app, sort by
// importance. Kinds is deliberately excluded: it moved to its own toolbar chip
// with its own badge, so counting it here would double-count. `grouping`/`sort`
// are optional because the list view has neither; when undefined (or at their
// default) they never contribute.
export function countActiveViewOptions(opts: {
  grouping?: 'app' | 'owner' | 'flat'
  sort?: TimelineSort
}): number {
  let count = 0
  if (opts.grouping && opts.grouping !== 'app') count++
  if (opts.sort && opts.sort !== 'importance') count++
  return count
}

// Curated seed order for the Kind dropdown; discovered kinds (CRDs) append after.
export const TIMELINE_RESOURCE_KINDS = [
  'Deployment',
  'Pod',
  'Service',
  'ConfigMap',
  'Ingress',
  'Gateway',
  'HTTPRoute',
  'GRPCRoute',
  'TCPRoute',
  'TLSRoute',
  'ReplicaSet',
  'DaemonSet',
  'StatefulSet',
]

// Merge discovered kinds into the curated seed: seed keeps its order, extras are
// appended alphabetically. Shared so both toolbars offer the same option set.
export function mergeKindOptions(extraKinds: Iterable<string>): string[] {
  const seeded = new Set(TIMELINE_RESOURCE_KINDS)
  const extra = [...new Set(extraKinds)].filter((k) => !!k && !seeded.has(k)).sort()
  return [...TIMELINE_RESOURCE_KINDS, ...extra]
}
