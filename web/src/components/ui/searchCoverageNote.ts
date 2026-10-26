import { englishPlural, isEnglishPlural } from '@skyhook-io/k8s-ui'
import type { SearchCoverage } from '@skyhook-io/k8s-ui/types/read-coverage'
import type { OmnibarNote, OmnibarNotePart } from './Omnibar'

// While a search reports kinds still loading, the open omnibar re-queries every
// PENDING_RETRY_MS for at most PENDING_RETRY_BUDGET_MS. Radar reports a kind
// failed 30 s after its watch starts, and on a collector-limited install a
// watch can start 20 s or more into warm-up, so the budget outlasts both and
// the note ends on the outcome rather than "still loading".
export const PENDING_RETRY_MS = 5000
export const PENDING_RETRY_BUDGET_MS = 60000

const FAILURE_REASONS = new Set(['kind_sync_timed_out', 'read_error', 'radar_credentials_rejected'])
const MAX_SENTENCES = 2
const MAX_NAMES = 3
const MAX_TARGET_NAMES = 5

export interface SearchCoverageNoteOptions {
  /** The search's hits: a kind that shares its name with a hit of another
   *  API group is named with its group. */
  hits?: { kind: string; group?: string }[]
  query?: string
  recheckKind?: (kind: string) => void
  /** Re-querying a still-loading search ran out of budget. */
  pendingExhausted?: boolean
  /** Returns how to open a kind's resource list, or undefined when the host can't. */
  openKindList?: (kind: string, group: string) => (() => void) | undefined
}

/** One kind or namespace a search couldn't cover, flattened from its gaps: a
 *  named kind, or `*` for namespaces, for kinds too many to name (`count`), or
 *  for an unknown set of kinds (no count). */
export interface SearchCoverageEntry {
  kind: string
  group: string
  reason: string
  namespaces?: string[]
  count?: number
  /** A namespace entry whose gap missed only some of the kinds searched. */
  someKinds?: boolean
  wholeNamespaceScope?: boolean
}

/** The entries of a search's coverage; CEL evaluation failures are left to the hits. */
export function searchCoverageEntries(coverage: SearchCoverage | undefined): SearchCoverageEntry[] {
  const out: SearchCoverageEntry[] = []
  for (const gap of coverage?.gaps ?? []) {
    if (gap.objects) continue
    // A gap lists one axis and gives the other as an extent.
    const kinds = gap.kinds && gap.kinds.extent === undefined ? gap.kinds : undefined
    const namespaces = gap.namespaces && gap.namespaces.extent === undefined ? gap.namespaces : undefined
    if (kinds?.names) {
      for (const k of kinds.names) out.push({ kind: k.kind, group: k.group ?? '', reason: gap.code, wholeNamespaceScope: gap.namespaces?.extent === 'all' })
    } else if (kinds) {
      out.push({ kind: '*', group: '', reason: gap.code, count: kinds.count })
    } else if (namespaces) {
      // A search names every namespace its query asked for.
      out.push({ kind: '*', group: '', reason: gap.code, namespaces: namespaces.names ?? [], someKinds: gap.kinds?.extent !== 'all' })
    } else {
      out.push({ kind: '*', group: '', reason: gap.code })
    }
  }
  return out
}

/** Plural display form of a Kind name ("Secrets", "Ingresses", "Endpoints"). */
export function kindPluralLabel(kind: string): string {
  return isEnglishPlural(kind) ? kind : englishPlural(kind)
}

function joinNames(names: string[], conj: string): string {
  if (names.length > MAX_TARGET_NAMES) return `${names.slice(0, MAX_TARGET_NAMES).join(', ')} and ${names.length - MAX_TARGET_NAMES} more`
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} ${conj} ${names[names.length - 1]}`
}

type KindLabel = (entry: SearchCoverageEntry, plural?: boolean) => string

// kindLabeler names a kind by its Kind, adding the API group when another kind
// of the same name but a different group is in the entries or the hits
// ("Certificate (networking.internal.knative.dev)" beside cert-manager's).
function kindLabeler(unsearched: SearchCoverageEntry[], hits: { kind: string; group?: string }[]): KindLabel {
  const groups = new Map<string, Set<string>>()
  const add = (kind: string, group: string) => groups.set(kind, (groups.get(kind) ?? new Set()).add(group))
  for (const e of unsearched) if (e.kind !== '*') add(e.kind, e.group)
  for (const h of hits) add(h.kind, h.group ?? '')
  return (e, plural = false) => {
    const name = plural ? kindPluralLabel(e.kind) : e.kind
    return e.group && (groups.get(e.kind)?.size ?? 0) > 1 ? `${name} (${e.group})` : name
  }
}

// The kinds entries name, once each by group and kind.
function distinctKinds(entries: SearchCoverageEntry[]): SearchCoverageEntry[] {
  return [...new Map(entries.filter((e) => e.kind !== '*').map((e) => [`${e.group}/${e.kind}`, e])).values()]
}

// The kinds a failure reason covers, in prose: up to MAX_NAMES names, then
// "N more kinds" for the rest (named beyond the cap, or folded into a counted
// "*" entry), or "some kinds" when a "*" entry carries no count.
function failedKinds(entries: SearchCoverageEntry[], label: (e: SearchCoverageEntry) => string): { text: string; one: boolean } {
  const names = distinctKinds(entries).map(label)
  const stars = entries.filter((e) => e.kind === '*')
  if (stars.some((e) => !e.count)) return { text: names.length ? `${names.slice(0, MAX_NAMES).join(', ')} and other kinds` : 'some kinds', one: false }
  const more = Math.max(names.length - MAX_NAMES, 0) + stars.reduce((n, e) => n + (e.count ?? 0), 0)
  const shown = names.slice(0, MAX_NAMES)
  if (!more) return { text: joinNames(shown, 'or'), one: shown.length === 1 }
  // Kinds too many to name are only counted.
  if (!shown.length) return { text: `${more} ${more === 1 ? 'kind' : 'kinds'}`, one: more === 1 }
  return { text: `${shown.join(', ')} and ${more} more ${more === 1 ? 'kind' : 'kinds'}`, one: false }
}

// Radar's ⌘K note for one search: what happened to the kinds and namespaces
// the query named, whether results are still loading, and which kinds failed
// to read. Broad searches keep access exclusions quiet; namespace collection
// is always explained, kind collection only for an empty result. Missing
// coverage gets no note.
export function searchCoverageNote(coverage: SearchCoverage | undefined, opts: SearchCoverageNoteOptions): OmnibarNote | undefined {
  const unsearched = searchCoverageEntries(coverage)
  if (!unsearched.length) {
    if (coverage?.gaps.length === 0) {
      // Collection limits come from how Radar is installed, and nothing else
      // in the omnibar shows them, so they are said with hits too.
      if (coverage.narrowedBy?.includes('namespace_collection')) {
        return { text: "Radar collects some kinds only in some namespaces; matches for them elsewhere won't show." }
      }
      if (coverage.narrowedBy?.includes('kind_collection') && !opts.hits?.length) {
        return { text: "Radar doesn't watch every kind; search kind:<name> to include one." }
      }
    }
    return undefined
  }
  const label = kindLabeler(unsearched, opts.hits ?? [])
  const named = (reason: string) => distinctKinds(unsearched.filter((e) => e.reason === reason))
  const names = (entries: SearchCoverageEntry[], conj: string, plural = true) => joinNames([...new Set(entries.map((e) => label(e, plural)))], conj)

  const sentences: OmnibarNotePart[] = []
  const say = (text: string) => sentences.push({ text })

  if (unsearched.some((e) => e.reason === 'kind_sync_pending')) {
    say(opts.pendingExhausted ? 'Still loading — try again shortly.' : 'Still loading — results may change.')
  }
  const unknown = named('kind_unknown')
  // A read_error "*" without a count beside an unknown kind: some API groups
  // failed discovery, so the kind may exist there.
  const groupsUnread = unknown.length > 0 && unsearched.some((e) => e.kind === '*' && e.reason === 'read_error' && !e.count)
  // Loading and failures lead: they are the facts a retry or a fix can change.
  const failed = unsearched.filter((e) => FAILURE_REASONS.has(e.reason) && !(groupsUnread && e.kind === '*' && !e.count))
  if (failed.length) {
    const { text, one } = failedKinds(failed, (e) => label(e))
    const warning = `Couldn't read ${text}; matches of ${one ? 'that kind' : 'those kinds'} may be missing.`
    const tokens = (opts.query ?? '').match(/(?:[^\s"]|"[^"]*(?:"|$))+/g) ?? []
    const namedKind = tokens.some((token) => /^(kind|k):.+/i.test(token.replaceAll('"', '')))
    const retry = !namedKind && failed.find((e) => e.kind !== '*' && e.reason === 'read_error')
    const unnamedRetry = !namedKind && !retry && failed.some((e) => e.kind === '*' && e.count && e.reason === 'read_error')
    if (unnamedRetry) {
      say(`${warning} Search kind:<name> to re-check one.`)
    } else if (retry) {
      const modifier = `kind:${retry.kind}`
      sentences.push(opts.recheckKind
        ? { text: `${warning} Search`, action: { label: modifier, suffix: ' to re-check.', keepOpen: true, onSelect: () => opts.recheckKind?.(retry.kind) } }
        : { text: `${warning} Search ${modifier} to re-check.` })
    } else {
      say(warning)
    }
  }
  const unchecked = unsearched.filter((e) => e.reason === 'user_permission_check_failed')
  if (unchecked.length) say(`Couldn't check your access to ${failedKinds(unchecked, (e) => label(e, true)).text}.`)
  if (unknown.length) {
    const noun = `No ${unknown.length === 1 ? 'kind' : 'kinds'} named ${names(unknown, 'or', false)}`
    say(groupsUnread ? `${noun} found, but some API groups couldn't be read.` : `${noun} here — not installed, or misspelled?`)
  }
  // Kinds Radar reads directly instead of watching: their list works.
  const unsupported = unsearched.filter((e) => e.kind !== '*' && e.reason === 'kind_not_searchable')
  if (unsupported.length) {
    const first = unsupported[0]
    const pointer = `Open the ${label(first, true)} list instead.`
    const openList = opts.openKindList?.(first.kind, first.group)
    const what = names(unsupported, 'and')
    sentences.push(openList
      ? { text: `${what} can't be searched.`, action: { label: pointer, onSelect: openList } }
      : { text: `${what} can't be searched. ${pointer}` })
  }
  const unwatchable = named('kind_not_watchable')
  if (unwatchable.length) say(`Radar can't collect ${names(unwatchable, 'or')}: the API doesn't support list and watch.`)
  // Namespace entries name the namespaces an ns: term asked for that weren't
  // searched; their reason says why.
  const namespaces = (reasons: string[]) => [...new Set(unsearched.filter((e) => reasons.includes(e.reason)).flatMap((e) => e.namespaces ?? []))].sort()
  const quoted = (list: string[]) => joinNames(list.map((ns) => `'${ns}'`), 'and')
  const namespaceSentence = (list: string[], one: string, many: string) => say(`${list.length === 1 ? 'Namespace' : 'Namespaces'} ${quoted(list)} ${list.length === 1 ? one : many}.`)
  const absent = namespaces(['namespace_not_found'])
  if (absent.length) namespaceSentence(absent, "doesn't exist on this cluster", "don't exist on this cluster")
  const unseen = namespaces(['user_namespace_not_visible'])
  if (unseen.length) namespaceSentence(unseen, "wasn't found in what you can see", "weren't found in what you can see")
  const pinned = namespaces(['outside_namespace_pin'])
  if (pinned.length) namespaceSentence(pinned, "wasn't searched: Radar is pinned to another namespace", "weren't searched: Radar is pinned to another namespace")
  const uncollected = namespaces(['radar_namespace_not_collected'])
  const someKindsUncollected = unsearched.some((e) => e.reason === 'radar_namespace_not_collected' && e.someKinds)
  if (uncollected.length && someKindsUncollected) say(`Some kinds weren't searched in ${uncollected.length === 1 ? 'namespace' : 'namespaces'} ${quoted(uncollected)}: Radar here watches only some namespaces.`)
  else if (uncollected.length) namespaceSentence(uncollected, "wasn't searched: Radar here watches only some namespaces", "weren't searched: Radar here watches only some namespaces")
  const findings = named('findings_scan_incomplete')
  if (findings.length) say(`Radar ran out of time searching the findings of ${names(findings, 'and')}, so a CVE or package missing here may be in a report it left out.`)
  const denied = named('user_forbidden')
  if (denied.length) {
    const suggestion = denied.some((e) => e.wholeNamespaceScope) ? ' Try narrowing with ns:<namespace>.' : ''
    say(`Your access doesn't include all ${names(denied, 'or')}.${suggestion}`)
  }
  const collector = named('radar_forbidden')
  if (collector.length) say(`Radar isn't allowed to read ${names(collector, 'or')} on this cluster.`)
  const unwatched = named('radar_kind_not_collected')
  if (unwatched.length) say(`Radar doesn't watch ${names(unwatched, 'or')} on this cluster, so they weren't searched.`)

  if (!sentences.length) return undefined
  const shown = sentences.slice(0, MAX_SENTENCES)
  if (shown.length > 1 && shown.some((s) => s.action)) {
    return {
      text: shown.map((s) => `${s.text}${s.action ? ` ${s.action.label}${s.action.suffix ?? ''}` : ''}`).join(' '),
      parts: shown,
    }
  }
  return { text: shown.map((s) => s.text).join(' '), action: shown.find((s) => s.action)?.action }
}
