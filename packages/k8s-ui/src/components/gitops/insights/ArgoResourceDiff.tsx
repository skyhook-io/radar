import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { createTwoFilesPatch } from 'diff'
import { clsx } from 'clsx'
import { FileText, GitCompare, Maximize2, Rows, ShieldOff, X } from 'lucide-react'
import type { GitOpsResourceDiff } from '../../../types'
import { DiffLine, hasDiffBodyChange } from '../../shared/UnifiedDiff'
import { CodeViewer } from '../../ui/CodeViewer'
import { YamlDiffEditor } from '../../ui/YamlEditor'
import { Tooltip } from '../../ui/Tooltip'

interface ArgoResourceDiffProps {
  diff?: GitOpsResourceDiff | null
  loading: boolean
  // Server error string ({"error"} body), surfaced inline — not a toast.
  error?: string | null
}

// ArgoResourceDiff renders the full Git-rendered desired-vs-live diff for a
// single Argo CD managed resource. Pure presentation: the host (web/) wires the
// fetch and passes data / loading / error. The inline view is a compact unified
// preview; the maximize control opens the full view (Diff / Live manifest /
// Desired manifest, with the Monaco-based YamlDiffEditor powering the Diff tab).
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

// Full-screen compare overlay: a Diff / Live manifest / Desired manifest mode
// switch, and — within Diff — the same Monaco-backed YamlDiffEditor
// ResourceCompareView uses, with its "Diff only" (collapse unchanged regions,
// on by default) and "Unified" (side-by-side vs single-column) toggles.
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
  const [unified, setUnified] = useState(false)
  const [hideUnchanged, setHideUnchanged] = useState(true)

  const unchanged = !docsDiffer(diff.desired, diff.live)

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="dialog relative flex h-[90vh] w-full max-w-6xl flex-col">
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
            <div className="flex items-center gap-1">
              <ToggleButton active={hideUnchanged} onClick={() => setHideUnchanged((v) => !v)} icon={<FileText className="h-3.5 w-3.5" />} label="Diff only" tooltip="Collapse unchanged regions" />
              <ToggleButton active={unified} onClick={() => setUnified((v) => !v)} icon={<Rows className="h-3.5 w-3.5" />} label="Unified" tooltip="Switch between side-by-side and single-column" />
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
          ) : (
            <YamlDiffEditor
              original={diff.desired}
              modified={diff.live}
              unified={unified}
              hideUnchanged={hideUnchanged}
              height="100%"
              bleed
            />
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

function ToggleButton({
  active,
  onClick,
  icon,
  label,
  tooltip,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  tooltip: string
}) {
  return (
    <Tooltip content={tooltip}>
      <button
        type="button"
        onClick={onClick}
        aria-pressed={active}
        className={clsx(
          'flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors whitespace-nowrap',
          active
            ? 'border-skyhook-400/50 bg-skyhook-500/15 text-skyhook-300'
            : 'border-transparent text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
        )}
      >
        {icon}
        {label}
      </button>
    </Tooltip>
  )
}

// Renders a unified diff of two YAML documents using the `diff` package, via the
// shared DiffLine so this reads identically to the Helm manifest diff — used
// only for the compact inline row preview above; the full-view overlay's Diff
// tab renders through YamlDiffEditor instead.
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

// True when the desired and live documents differ in a body line (ignoring the
// patch's own file headers). Cheaper than rendering for the "no differences"
// short-circuit.
function docsDiffer(desired: string, live: string): boolean {
  if (desired === live) return false
  const patch = createTwoFilesPatch('desired', 'live', desired, live, '', '', { context: 0 })
  return hasDiffBodyChange(patch)
}
