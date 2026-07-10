import type { TimelineEvent } from '../../types'
import { isChangeEvent, isK8sEvent } from '../../types'
import { pluralize } from '../../utils/pluralize'

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

// ---------------------------------------------------------------------------
// Two-axis view over the activity keys. The toolbar presents the filter as a
// SOURCE pick (Changes = watched resource mutations vs K8s Events = native
// Event objects) crossed with a PROBLEMS toggle (the severity slice of the
// picked source).
//
// The six reachable states are exactly expressible in the existing key
// vocabulary, so the URL param, the shared predicate, and both views carry
// ActivityFilterKey[] unchanged:
//   All        → []                          All+problems    → warnings+unhealthy
//   Changes    → [changes]                   Changes+problems → [unhealthy]
//   K8s Events → [k8s_events]                Events+problems  → [warnings]
// ---------------------------------------------------------------------------

export type ActivitySource = 'all' | 'changes' | 'k8s_events'

export interface ActivitySelection {
  source: ActivitySource
  problemsOnly: boolean
}

export function selectionToActivityKeys(sel: ActivitySelection): ActivityFilterKey[] {
  if (sel.problemsOnly) {
    if (sel.source === 'changes') return ['unhealthy']
    if (sel.source === 'k8s_events') return ['warnings']
    return ['warnings', 'unhealthy']
  }
  if (sel.source === 'changes') return ['changes']
  if (sel.source === 'k8s_events') return ['k8s_events']
  return []
}

// Inverse of selectionToActivityKeys for the six canonical states. Legacy
// multi-select key sets (pre-two-axis URLs) fall back to the widest reading —
// showing more than a stale link intended beats silently hiding activity.
export function activityKeysToSelection(keys: readonly ActivityFilterKey[]): ActivitySelection {
  const set = new Set(keys)
  const eq = (...want: ActivityFilterKey[]) => set.size === want.length && want.every((k) => set.has(k))
  if (keys.length === 0) return { source: 'all', problemsOnly: false }
  if (eq('changes')) return { source: 'changes', problemsOnly: false }
  if (eq('k8s_events')) return { source: 'k8s_events', problemsOnly: false }
  if (eq('unhealthy')) return { source: 'changes', problemsOnly: true }
  if (eq('warnings')) return { source: 'k8s_events', problemsOnly: true }
  if (eq('warnings', 'unhealthy')) return { source: 'all', problemsOnly: true }
  return { source: 'all', problemsOnly: false }
}

// Free-text search predicate. Matches name, kind, namespace, reason, message,
// and diff summary. Shared so the list and swimlane views match identically.
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
  // Count what the user set (source segment + Problems toggle), not the raw key
  // array: the single Problems toggle expands to two keys (warnings+unhealthy),
  // which would otherwise read as "2 activity filters" for one pick.
  const sel = activityKeysToSelection(opts.activityFilter)
  const activityCount = (sel.source !== 'all' ? 1 : 0) + (sel.problemsOnly ? 1 : 0)
  if (activityCount > 0) parts.push(pluralize(activityCount, 'activity filter'))
  if (opts.kindFilter.length > 0) parts.push(pluralize(opts.kindFilter.length, 'kind'))
  if (!opts.showDeleted) parts.push('deleted hidden')
  return parts.join(' · ')
}

export interface ActivityStats {
  total: number
  changes: number
  k8sEvents: number
  warnings: number
  unhealthy: number
  deleted: number
}

// Chip counts. Derived from the full (unfiltered) events array so the badges
// show totals regardless of the active filter — identical in both views.
export function computeActivityStats(events: TimelineEvent[] | undefined): ActivityStats {
  if (!events || events.length === 0) {
    return { total: 0, changes: 0, k8sEvents: 0, warnings: 0, unhealthy: 0, deleted: 0 }
  }
  let changes = 0
  let k8sEvents = 0
  let warnings = 0
  let unhealthy = 0
  let deleted = 0
  for (const e of events) {
    if (isChangeEvent(e)) changes++
    if (isK8sEvent(e)) k8sEvents++
    if (e.eventType === 'Warning') warnings++
    if (isChangeEvent(e) && (e.healthState === 'unhealthy' || e.healthState === 'degraded')) unhealthy++
    if (e.eventType === 'delete') deleted++
  }
  return { total: events.length, changes, k8sEvents, warnings, unhealthy, deleted }
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
