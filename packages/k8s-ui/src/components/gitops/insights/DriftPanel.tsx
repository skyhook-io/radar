import type { GitOpsDrift, GitOpsDriftEntry } from '../../../types'

// DriftPanel renders the structured per-field diff. Format mimics a
// `diff -u` summary: removed paths in red, added in green, changed shown
// inline as "old → new". Path is monospace; values are JSON-encoded and
// pre-wrapped so structured values (objects, arrays) render readably.
export function DriftPanel({ drift }: { drift: GitOpsDrift }) {
  const sourceLabel = driftSourceLabel(drift.source)
  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Field diff</h4>
        <span className="text-[10px] text-theme-text-tertiary">
          desired (Git) → live ·
          {drift.truncated ? ' showing first 50 entries' : ` ${drift.entries.length} field${drift.entries.length === 1 ? '' : 's'}`}
          {sourceLabel && ` · ${sourceLabel}`}
        </span>
      </div>
      <div className="space-y-1 font-mono text-[11px]">
        {drift.entries.map((entry, i) => (
          <DriftEntryRow key={`${entry.path}-${i}`} entry={entry} />
        ))}
      </div>
    </div>
  )
}

// Provenance label for a Drift's field entries. Only Argo-sourced drift gets an
// explicit label; the built-in last-applied path renders without one (the
// "desired (Git) → live" copy already implies it). Unknown sources (a library
// consumer lagging the backend) fall through to no label rather than surfacing
// a raw enum string.
function driftSourceLabel(source: GitOpsDrift['source']): string {
  return source === 'argocd-api' ? 'via Argo CD API' : ''
}

function DriftEntryRow({ entry }: { entry: GitOpsDriftEntry }) {
  if (entry.op === 'removed') {
    return (
      <div>
        <span className="text-red-600 dark:text-red-400">- {entry.path}</span>
        {entry.desired && <span className="ml-2 text-theme-text-secondary">{entry.desired}</span>}
      </div>
    )
  }
  if (entry.op === 'added') {
    return (
      <div>
        <span className="text-emerald-700 dark:text-emerald-400">+ {entry.path}</span>
        {entry.live && <span className="ml-2 text-theme-text-secondary">{entry.live}</span>}
      </div>
    )
  }
  return (
    <div>
      <span className="text-amber-700 dark:text-amber-400">~ {entry.path}</span>
      <span className="ml-2 text-theme-text-tertiary">{entry.desired}</span>
      <span className="mx-1 text-theme-text-tertiary">→</span>
      <span className="text-theme-text-primary">{entry.live}</span>
    </div>
  )
}
