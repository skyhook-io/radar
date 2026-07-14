import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { createTwoFilesPatch } from 'diff'
import { GitCompare, Maximize2, ShieldOff, X } from 'lucide-react'
import type { GitOpsResourceDiff } from '../../../types'
import { DiffLine, hasDiffBodyChange } from '../../shared/UnifiedDiff'

interface ArgoResourceDiffProps {
  diff?: GitOpsResourceDiff | null
  loading: boolean
  // Server error string ({"error"} body), surfaced inline — not a toast.
  error?: string | null
}

// ArgoResourceDiff renders the full Git-rendered desired-vs-live diff for a
// single Argo CD managed resource. Pure presentation: the host (web/) wires the
// fetch and passes data / loading / error. The inline view is a unified line
// diff; the maximize control opens the same content full-screen with side-by-
// side panes on wide viewports.
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

// Full-screen compare overlay. Side-by-side desired / live panes on wide
// viewports; unified diff when narrow. Portaled to body so it escapes the
// expanded row's overflow clipping.
function ArgoResourceDiffOverlay({ diff, onClose }: { diff: GitOpsResourceDiff; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [onClose])

  const unchanged = !docsDiffer(diff.desired, diff.live)

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
        {unchanged ? (
          <div className="p-6 text-sm text-theme-text-secondary">
            No differences between the Git-rendered desired state and live cluster state.
          </div>
        ) : (
          // Unified (not side-by-side) even full-screen: two unhighlighted
          // panes make the reader hunt for the drifted line by eye across a
          // whole manifest. The unified view marks every changed line — the
          // same default Argo's own UI makes. An aligned split-diff with
          // per-line highlighting can supersede this later.
          <div className="min-h-0 flex-1 overflow-auto bg-theme-base/50">
            <UnifiedDiffBody desired={diff.desired} live={diff.live} />
          </div>
        )}
      </div>
    </div>,
    document.body,
  )
}


// Renders a unified diff of two YAML documents using the `diff` package, via the
// shared DiffLine so this reads identically to the Helm manifest diff.
function UnifiedDiffBody({ desired, live }: { desired: string; live: string }) {
  const patch = createTwoFilesPatch('desired', 'live', desired, live, '', '', { context: 3 })
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
