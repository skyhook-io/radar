import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { createTwoFilesPatch, structuredPatch, type StructuredPatchHunk } from 'diff'
import { clsx } from 'clsx'
import { GitCompare, Maximize2, ShieldOff, X } from 'lucide-react'
import type { GitOpsResourceDiff } from '../../../types'
import { DiffLine, hasDiffBodyChange } from '../../shared/UnifiedDiff'
import { CodeViewer } from '../../ui/CodeViewer'

interface ArgoResourceDiffProps {
  diff?: GitOpsResourceDiff | null
  loading: boolean
  // Server error string ({"error"} body), surfaced inline — not a toast.
  error?: string | null
}

// ArgoResourceDiff renders the full Git-rendered desired-vs-live diff for a
// single Argo CD managed resource. Pure presentation: the host (web/) wires the
// fetch and passes data / loading / error. The inline view is a compact unified
// preview; the maximize control opens the full view (modeled on Argo CD's own
// resource diff modal: Diff / Live manifest / Desired manifest, with Compact
// diff and Inline diff toggles).
export function ArgoResourceDiff({ diff, loading, error }: ArgoResourceDiffProps) {
  const [maximized, setMaximized] = useState(false)

  if (loading) {
    // An inline row expansion, not a full pane — a quiet skeleton of the diff's
    // own shape reads as "content arriving here" without the page-level radar
    // loader shouting for attention.
    return (
      <div className="rounded-md border border-theme-border bg-theme-base/50 p-3" aria-busy="true" aria-label="Loading diff">
        <div className="mb-2 flex items-center gap-1.5 text-[11px] text-theme-text-tertiary">
          <GitCompare className="h-3.5 w-3.5 shrink-0" />
          <span>Loading diff…</span>
        </div>
        <div className="space-y-1.5">
          {[92, 68, 81, 55, 74].map((w, i) => (
            <div key={i} className="h-2.5 animate-pulse rounded bg-theme-hover" style={{ width: `${w}%` }} />
          ))}
        </div>
      </div>
    )
  }
  if (error) {
    return (
      <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-700 dark:text-red-400">
        {error}
      </div>
    )
  }
  if (!diff) return null

  const unchanged = !docsDiffer(diff.desired, diff.live)

  return (
    <div>
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <GitCompare className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
          <span className="text-[11px] text-theme-text-tertiary">
            desired (Git-rendered) <span className="mx-0.5">→</span> live (normalized)
          </span>
          {diff.redacted && <RedactedChip />}
        </div>
        <button
          type="button"
          onClick={() => setMaximized(true)}
          className="flex shrink-0 items-center gap-1 rounded border border-theme-border bg-theme-base px-1.5 py-0.5 text-[10px] text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <Maximize2 className="h-3 w-3" />
          Expand
        </button>
      </div>
      {unchanged ? (
        <p className="text-[11px] text-theme-text-tertiary">No differences between the Git-rendered desired state and live cluster state.</p>
      ) : (
        <div className="max-h-80 overflow-auto rounded-md border border-theme-border bg-theme-base/50">
          <UnifiedDiffBody desired={diff.desired} live={diff.live} />
        </div>
      )}

      {maximized && <ArgoResourceDiffOverlay diff={diff} onClose={() => setMaximized(false)} />}
    </div>
  )
}

function RedactedChip() {
  return (
    <span className="inline-flex items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[10px] text-theme-text-tertiary">
      <ShieldOff className="h-3 w-3" />
      Secret values masked
    </span>
  )
}

type ViewMode = 'diff' | 'live' | 'desired'

// Full-screen compare overlay, modeled directly on Argo CD's own resource diff
// modal (ui/src/app/applications/components/application-resources-diff): a
// Diff / Live manifest / Desired manifest mode switch, and — within Diff —
// two independent toggles matching Argo's own defaults (both off):
//   - Compact diff: 2 lines of hunk context vs. the whole file
//   - Inline diff: unified (single column) vs. split (side-by-side) rendering
// Portaled to body so it escapes the expanded row's overflow clipping.
function ArgoResourceDiffOverlay({ diff, onClose }: { diff: GitOpsResourceDiff; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        // Consumed: stop it here so it doesn't keep bubbling to window-level
        // Escape handlers (e.g. the bottom dock's maximize toggle) and
        // dismiss something behind this overlay in the same keystroke.
        e.stopPropagation()
      }
    }
    // Bubble phase, not capture: the embedded CodeViewer's own search bar
    // (Live/Desired manifest mode) handles Escape on its input and calls
    // stopPropagation to close just the search, not this whole overlay — a
    // capture-phase listener here would run before that input ever sees the
    // event and always win, closing the full-screen view out from under an
    // in-progress search instead. Search's own handler runs first (target
    // phase, before this document-level one) and stops propagation when it
    // acts, so this listener only ever fires when search isn't consuming
    // the key itself.
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const [viewMode, setViewMode] = useState<ViewMode>('diff')
  const [compactDiff, setCompactDiff] = useState(false)
  const [inlineDiff, setInlineDiff] = useState(false)

  const unchanged = !docsDiffer(diff.desired, diff.live)
  const context = compactDiff ? 2 : Number.MAX_SAFE_INTEGER

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="dialog relative flex max-h-[90vh] w-full max-w-6xl flex-col">
        <div className="flex items-center justify-between gap-2 border-b border-theme-border px-4 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <GitCompare className="h-4 w-4 shrink-0 text-theme-text-secondary" />
            <span className="truncate text-sm font-medium text-theme-text-primary">
              desired (Git-rendered) <span className="mx-0.5 text-theme-text-tertiary">→</span> live (normalized)
            </span>
            {diff.redacted && <RedactedChip />}
          </div>
          <button
            onClick={onClose}
            className="flex shrink-0 items-center gap-1 rounded px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
          >
            <X className="h-3.5 w-3.5" />
            Close
          </button>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-theme-border px-4 py-2">
          <div className="flex gap-1.5" role="tablist" aria-label="View">
            {([
              ['diff', 'Diff'],
              ['live', 'Live manifest'],
              ['desired', 'Desired manifest'],
            ] as const).map(([mode, label]) => (
              <button
                key={mode}
                type="button"
                role="tab"
                aria-selected={viewMode === mode}
                onClick={() => setViewMode(mode)}
                className={clsx(
                  'rounded-md border px-2.5 py-1 text-xs transition-colors',
                  viewMode === mode
                    ? 'selection selection-text selection-ring border-transparent'
                    : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover',
                )}
              >
                {label}
              </button>
            ))}
          </div>
          {viewMode === 'diff' && !unchanged && (
            <div className="flex items-center gap-4">
              <DiffToggle label="Compact diff" checked={compactDiff} onChange={setCompactDiff} />
              <DiffToggle label="Inline diff" checked={inlineDiff} onChange={setInlineDiff} />
            </div>
          )}
        </div>

        <div className="min-h-0 flex-1 overflow-auto bg-theme-base/50">
          {viewMode === 'live' ? (
            <CodeViewer code={diff.live} language="yaml" showLineNumbers showCopyButton maxHeight="calc(90vh - 180px)" />
          ) : viewMode === 'desired' ? (
            <CodeViewer code={diff.desired} language="yaml" showLineNumbers showCopyButton maxHeight="calc(90vh - 180px)" />
          ) : unchanged ? (
            <div className="p-6 text-sm text-theme-text-secondary">
              No differences between the Git-rendered desired state and live cluster state.
            </div>
          ) : inlineDiff ? (
            <UnifiedDiffBody desired={diff.desired} live={diff.live} context={context} />
          ) : (
            <SplitDiffBody desired={diff.desired} live={diff.live} context={context} />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

function DiffToggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="h-3.5 w-3.5 cursor-pointer accent-sky-500"
      />
      {label}
    </label>
  )
}

// Renders a unified diff of two YAML documents using the `diff` package, via the
// shared DiffLine so this reads identically to the Helm manifest diff. `context`
// defaults to the original fixed 3 lines used by the inline row preview; the
// full-view overlay drives it from the Compact diff toggle.
function UnifiedDiffBody({ desired, live, context = 3 }: { desired: string; live: string; context?: number }) {
  const patch = createTwoFilesPatch('desired', 'live', desired, live, '', '', { context })
  const lines = patch.split('\n').filter((line) => !line.startsWith('===') && !line.startsWith('Index:'))
  return (
    <div className="p-3 font-mono text-[11px]">
      {lines.map((line, index) => (
        <DiffLine key={index} line={line} />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Split (side-by-side) diff — Argo CD's own default rendering (inlineDiff:
// false → viewType 'split'). Built from `diff`'s structuredPatch rather than a
// new dependency: react-diff-view (what Argo's UI itself uses) needs its own
// unidiff-formatted text; `structuredPatch`'s hunks already give aligned
// old/new line numbers directly, which is all a split view needs.
// ---------------------------------------------------------------------------

export interface SplitCellData {
  num: number
  text: string
  type: 'context' | 'removal' | 'addition'
}

export interface SplitRow {
  left?: SplitCellData
  right?: SplitCellData
}

// A row with neither side set renders as the "⋯" gap between two hunks that
// aren't adjacent — only possible when Compact diff (context: 2) is on.
export function isHunkGap(row: SplitRow): boolean {
  return row.left === undefined && row.right === undefined
}

// Walks a hunk's unified lines and zips consecutive removal/addition runs into
// side-by-side rows (the standard split-diff pairing: a block of N removals
// followed by M additions becomes max(N, M) rows, blank-padding the shorter
// side) — context lines pass straight through unchanged on both sides.
export function buildSplitRows(hunks: readonly StructuredPatchHunk[]): SplitRow[] {
  const rows: SplitRow[] = []
  hunks.forEach((hunk, hunkIndex) => {
    if (hunkIndex > 0) rows.push({})
    let oldNum = hunk.oldStart
    let newNum = hunk.newStart
    let i = 0
    while (i < hunk.lines.length) {
      const line = hunk.lines[i]
      const marker = line[0]
      if (marker === ' ') {
        const text = line.slice(1)
        rows.push({
          left: { num: oldNum++, text, type: 'context' },
          right: { num: newNum++, text, type: 'context' },
        })
        i++
        continue
      }
      if (marker === '-' || marker === '+') {
        const removals: string[] = []
        while (i < hunk.lines.length && hunk.lines[i][0] === '-') {
          removals.push(hunk.lines[i].slice(1))
          i++
        }
        const additions: string[] = []
        while (i < hunk.lines.length && hunk.lines[i][0] === '+') {
          additions.push(hunk.lines[i].slice(1))
          i++
        }
        const max = Math.max(removals.length, additions.length)
        for (let j = 0; j < max; j++) {
          rows.push({
            left: j < removals.length ? { num: oldNum++, text: removals[j], type: 'removal' } : undefined,
            right: j < additions.length ? { num: newNum++, text: additions[j], type: 'addition' } : undefined,
          })
        }
        continue
      }
      // e.g. "\ No newline at end of file" — not a real line, skip.
      i++
    }
  })
  return rows
}

function SplitDiffBody({ desired, live, context }: { desired: string; live: string; context: number }) {
  const patch = structuredPatch('desired', 'live', desired, live, '', '', { context })
  const rows = buildSplitRows(patch.hunks)
  return (
    // Each side is its OWN scroll container spanning every row, not one per
    // row: a long line's horizontal scroll must move every row on that side
    // together. Giving each row its own overflow-x-auto (the previous
    // approach) let an ordinary trackpad scroll gesture — which almost
    // always carries a little horizontal delta even when scrolling mostly
    // vertically — scroll whichever single row the cursor happened to be
    // over, leaving that one row's text offset from every other row instead
    // of moving with them.
    <div className="flex font-mono text-[11px]">
      <div className="min-w-0 flex-1 overflow-x-auto border-r border-theme-border">
        {rows.map((row, index) => (isHunkGap(row) ? <HunkGapRow key={index} /> : <SplitCell key={index} cell={row.left} />))}
      </div>
      <div className="min-w-0 flex-1 overflow-x-auto">
        {rows.map((row, index) => (isHunkGap(row) ? <HunkGapRow key={index} /> : <SplitCell key={index} cell={row.right} />))}
      </div>
    </div>
  )
}

function HunkGapRow() {
  return (
    <div className="select-none border-y border-theme-border bg-theme-elevated px-3 py-0.5 text-center text-theme-text-tertiary">
      ⋯
    </div>
  )
}

function SplitCell({ cell }: { cell?: SplitCellData }) {
  return (
    <div
      className={clsx(
        'flex whitespace-pre',
        !cell && 'bg-theme-hover/30',
        cell?.type === 'removal' && 'bg-red-500/10 text-red-700 dark:text-red-400',
        cell?.type === 'addition' && 'bg-green-500/10 text-green-700 dark:text-green-400',
        cell?.type === 'context' && 'text-theme-text-secondary',
      )}
    >
      <span className="w-10 shrink-0 select-none px-2 text-right text-theme-text-tertiary">{cell?.num ?? ''}</span>
      <span className="pr-2">{cell ? cell.text || ' ' : ' '}</span>
    </div>
  )
}

// True when the desired and live documents differ in a body line (ignoring the
// patch's own file headers). Cheaper than rendering for the "no differences"
// short-circuit.
function docsDiffer(desired: string, live: string): boolean {
  if (desired === live) return false
  const patch = createTwoFilesPatch('desired', 'live', desired, live, '', '', { context: 0 })
  return hasDiffBodyChange(patch)
}
