import { useState } from 'react'
import { AlertTriangle, XCircle, Loader2 } from 'lucide-react'
import type { VeleroMessage } from '../../../types/policy'

/**
 * The messages behind a run's error and warning counts.
 *
 * A Backup that reports "Warnings: 2" carries the number and nothing else — the
 * text is in a results file in object storage, and only Velero's controller can
 * hand out a link to it. So the counts rendered as two coloured digits with
 * nowhere to go: enough to worry an operator, not enough to act on.
 *
 * Fetched on a click rather than on page load. Reading them asks Velero to
 * create a DownloadRequest and then pulls an object out of storage, which is
 * real work to do on every drawer open, and the counts already answer "is
 * anything wrong". Same bargain as the network trace's probes.
 *
 * Both failure modes are rendered here rather than swallowed, because both are
 * ordinary and each points somewhere different: a controller that is not running
 * (nothing serves the request) and storage this process cannot reach (the link
 * resolves to somewhere only the cluster can see).
 */

/**
 * What the host supplies: the fetch and its outcome. Deliberately separate from
 * the counts, which the renderer already has from the object itself — a host
 * that had to pass those too would be restating data it was handed.
 */
export interface VeleroRunMessagesFetch {
  /** Undefined until fetched. */
  messages?: { warnings: VeleroMessage[]; errors: VeleroMessage[]; truncated?: boolean }
  loading?: boolean
  /** Rendered by the host when the fetch failed, so a denial is not silence and
   *  is not dressed up as a fault. Same slot the storage-location panel uses for
   *  its lookup, so both failures in this integration read the same way. */
  lookupNote?: React.ReactNode
  onFetch?: () => void
}

export interface VeleroRunMessagesProps extends VeleroRunMessagesFetch {
  errors: number
  warnings: number
}

/** How many messages Radar's server returns at most (`maxMessages` in velero_results.go). */
export const VELERO_MESSAGES_RETURNED = 500

/** Rows shown before "Show all": a partial failure records one error per failed item. */
const ROWS_SHOWN = 10

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** The button says what it shows: "Show 9 errors and 3 warnings". */
export function veleroMessagesLabel(errors: number, warnings: number): string {
  const total = errors + warnings
  if (total > VELERO_MESSAGES_RETURNED) return `Show the first ${VELERO_MESSAGES_RETURNED} of ${total} messages`
  const parts = [errors > 0 ? plural(errors, 'error') : '', warnings > 0 ? plural(warnings, 'warning') : ''].filter(Boolean)
  return `Show ${parts.join(' and ')}`
}

type Severity = 'error' | 'warning'

/** One line of the list: a message, or several that differ only in one word (an object's name). */
export interface VeleroMessageRow {
  severity: Severity
  scope: string
  /** The text, with `…` where the members differ. */
  text: string
  /** What differs, one per member; empty for a single message. */
  variants: string[]
}

// A word that names an object by explicit syntax: name=x, or a slash-joined
// reference (persistentvolumeclaims/data-0, ns/pod). Only these fold: a bare
// word, quoted or not ("access-denied", E1001, Forbidden), may be a reason, and
// two reasons are two rows.
const OBJECT_IDENTITY_PATTERNS = [
  /^\(?name=["']?[a-z0-9][a-z0-9_.-]*["']?[,;:)]*$/,
  /^["']?(?:[a-z0-9][a-z0-9_.-]*\/)+[a-z0-9][a-z0-9_.-]*["']?[,;:)]*$/,
]

// Folding bounds. A message longer than these is shown as written (exact
// repeats still fold); at most this many identity positions per message are
// indexed, and once the index holds this many characters the remaining
// messages fold only exact repeats.
const MAX_FOLD_TOKENS = 400
const MAX_FOLD_BYTES = 4096
const MAX_IDENTITIES_PER_MESSAGE = 4
const MAX_INDEX_CHARS = 512 * 1024

/** What folding indexed, for tests that hold it to its bounds. */
export interface VeleroMessageFoldStats {
  keys: number
  chars: number
}

function commonAffixes(values: string[]): [number, number] {
  let pre = 0
  while (values.every((v) => pre < v.length && v[pre] === values[0][pre])) pre++
  let suf = 0
  while (values.every((v) => suf < v.length - pre && v[v.length - 1 - suf] === values[0][values[0].length - 1 - suf])) suf++
  return [pre, suf]
}

const encoder = new TextEncoder()

function foldable(message: string, tokens: string[]): boolean {
  return tokens.length <= MAX_FOLD_TOKENS && message.length <= MAX_FOLD_BYTES && encoder.encode(message).byteLength <= MAX_FOLD_BYTES
}

/** The positions of the words that name an object, at most MAX_IDENTITIES_PER_MESSAGE. */
function identityPositions(tokens: string[]): number[] {
  const out: number[] = []
  for (let i = 0; i < tokens.length && out.length < MAX_IDENTITIES_PER_MESSAGE; i++) {
    if (OBJECT_IDENTITY_PATTERNS.some((pattern) => pattern.test(tokens[i]))) out.push(i)
  }
  return out
}

const templateKey = (tokens: string[], at: number) => `${at}\u0000${tokens.slice(0, at).join(' ')}\u0000${tokens.slice(at + 1).join(' ')}`

/**
 * Groups by scope, errors first. The same message repeated is one row with a
 * count, and messages that differ only in one object's identity fold into one
 * row naming them: Velero records one error per item that failed, so a failed
 * volume step across forty PVCs is forty lines that say the same thing.
 */
export function veleroMessageRows(messages: { errors: VeleroMessage[]; warnings: VeleroMessage[] }, stats?: VeleroMessageFoldStats): VeleroMessageRow[] {
  const entries = [
    ...messages.errors.map((m) => ({ ...m, severity: 'error' as Severity })),
    ...messages.warnings.map((m) => ({ ...m, severity: 'warning' as Severity })),
  ]
  const scopes = [...new Set(entries.map((e) => e.scope))]
  const rows: VeleroMessageRow[] = []
  const budget = stats ?? { keys: 0, chars: 0 }
  for (const scope of scopes) {
    for (const severity of ['error', 'warning'] as Severity[]) {
      const here = entries.filter((e) => e.scope === scope && e.severity === severity).map((e) => ({ text: e.message, tokens: e.message.split(' ') }))
      // Pass 1: index each message's identity positions, within the budget.
      const indexed = here.map((m) => {
        if (!foldable(m.text, m.tokens)) return []
        const keys: { at: number; key: string }[] = []
        for (const at of identityPositions(m.tokens)) {
          if (budget.chars + m.text.length > MAX_INDEX_CHARS) break
          const key = templateKey(m.tokens, at)
          budget.chars += key.length
          budget.keys++
          keys.push({ at, key })
        }
        return keys
      })
      const counts = new Map<string, number>()
      for (const keys of indexed) for (const { key } of keys) counts.set(key, (counts.get(key) ?? 0) + 1)
      // Pass 2: each message joins the template most messages share, else its exact repeats.
      const groups = new Map<string, { tokens: string[]; at: number; variants: string[] }>()
      const order: string[] = []
      here.forEach((m, n) => {
        let best: { at: number; key: string } | undefined
        for (const k of indexed[n]) if ((counts.get(k.key) ?? 0) > (best ? counts.get(best.key)! : 1)) best = k
        const key = best ? `t\u0000${best.key}` : `x\u0000${m.text}`
        const g = groups.get(key)
        if (g) g.variants.push(best ? m.tokens[best.at] : '')
        else {
          groups.set(key, { tokens: m.tokens, at: best ? best.at : -1, variants: [best ? m.tokens[best.at] : ''] })
          order.push(key)
        }
      })
      for (const key of order) {
        const g = groups.get(key)!
        if (g.variants.length === 1) {
          rows.push({ severity, scope, text: g.tokens.join(' '), variants: [] })
          continue
        }
        const distinct = [...new Set(g.variants)]
        if (g.at < 0 || distinct.length === 1) {
          rows.push({ severity, scope, text: g.tokens.join(' '), variants: g.variants })
          continue
        }
        const [pre, suf] = commonAffixes(distinct)
        const word = g.tokens[g.at]
        rows.push({
          severity,
          scope,
          text: g.tokens.map((t, j) => (j === g.at ? `${word.slice(0, pre)}…${word.slice(word.length - suf)}` : t)).join(' '),
          variants: g.variants.map((v) => v.slice(pre, v.length - suf)),
        })
      }
    }
  }
  return rows
}

function scopeTitle(scope: string) {
  if (scope === 'velero') return 'Velero'
  if (scope === 'cluster') return 'Cluster-scoped resources'
  return `Namespace ${scope}`
}

function MessageRow({ row }: { row: VeleroMessageRow }) {
  const [open, setOpen] = useState(false)
  const repeated = row.variants.length > 1
  // The same message several times has nothing to list.
  const named = new Set(row.variants).size > 1
  return (
    <li className="flex items-start gap-2 text-xs">
      {row.severity === 'error' ? (
        <XCircle className="w-3.5 h-3.5 mt-0.5 shrink-0 text-red-500 dark:text-red-400" aria-hidden />
      ) : (
        <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0 text-amber-500 dark:text-amber-400" aria-hidden />
      )}
      {/* The icon and its colour were the only thing separating an error from
          a warning, which is the distinction that decides whether anyone acts. */}
      <span className="sr-only">{row.severity === 'error' ? 'Error: ' : 'Warning: '}</span>
      <div className="min-w-0 space-y-0.5">
        <span className="text-theme-text-secondary break-words">{row.text}</span>
        {repeated && !named && <span className="whitespace-nowrap text-theme-text-tertiary"> × {row.variants.length}</span>}
        {named && (
          <>
            {' '}
            <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="whitespace-nowrap text-accent-text hover:underline">
              × {row.variants.length}
            </button>
            {open && <div className="break-words font-mono text-theme-text-tertiary">{row.variants.join(', ')}</div>}
          </>
        )}
      </div>
    </li>
  )
}

export function VeleroRunMessages({
  errors,
  warnings,
  messages,
  loading,
  lookupNote,
  onFetch,
}: VeleroRunMessagesProps) {
  const [all, setAll] = useState(false)
  const total = errors + warnings
  // Nothing to explain, and no button for a run that reported nothing.
  if (total === 0) return null

  const count = messages ? messages.errors.length + messages.warnings.length : 0
  const rows = messages ? veleroMessageRows(messages) : []
  const shown = all ? rows : rows.slice(0, ROWS_SHOWN)
  const scopes = [...new Set(shown.map((r) => r.scope))]
  const tally = (scope: string) => {
    const e = messages?.errors.filter((m) => m.scope === scope).length ?? 0
    const w = messages?.warnings.filter((m) => m.scope === scope).length ?? 0
    return [e ? plural(e, 'error') : '', w ? plural(w, 'warning') : ''].filter(Boolean).join(', ')
  }

  return (
    // aria-live so the outcome reaches a screen reader: this region swaps a
    // button for a list (or a failure) with no navigation.
    <div className="mt-2 space-y-2" aria-live="polite">
      {!messages && (
        <button
          type="button"
          onClick={onFetch}
          disabled={loading || !onFetch}
          className="btn-brand text-xs px-2 py-1 disabled:opacity-50"
        >
          {loading ? (
            <span className="flex items-center gap-1.5">
              <Loader2 className="w-3 h-3 animate-spin" />
              Asking Velero…
            </span>
          ) : (
            veleroMessagesLabel(errors, warnings)
          )}
        </button>
      )}

      {lookupNote}

      {/* The count comes from the object; the messages come from a file
          written separately, so they can disagree. Truncation has its own line. */}
      {messages && !messages.truncated && count !== total && (
        <div className="text-xs text-theme-text-tertiary">
          {count === 0
            ? `Velero reported ${total} ${total === 1 ? 'message' : 'messages'} on this run, but the results file it returned holds none.`
            : `Velero reported ${total} on this run and returned ${count}.`}
        </div>
      )}

      {scopes.map((scope) => (
        <section key={scope} className="space-y-1">
          <h5 className="text-[11px] font-medium text-theme-text-tertiary">
            {scopeTitle(scope)} · {tally(scope)}
          </h5>
          <ul className="space-y-1">
            {shown.filter((r) => r.scope === scope).map((row, i) => (
              <MessageRow key={`${row.severity}-${i}-${row.text}`} row={row} />
            ))}
          </ul>
        </section>
      ))}

      {rows.length > ROWS_SHOWN && (
        <button type="button" onClick={() => setAll((v) => !v)} className="text-xs text-accent-text hover:underline">
          {all ? 'Show fewer' : `Show all ${rows.length}`}
        </button>
      )}

      {messages?.truncated && (
        <div className="text-xs text-theme-text-tertiary">Only the first {count} are shown.</div>
      )}
    </div>
  )
}
