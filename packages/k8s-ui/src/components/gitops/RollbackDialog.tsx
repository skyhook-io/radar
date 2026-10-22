import { useState, useEffect, useId } from 'react'
import { History, Loader2 } from 'lucide-react'

import { DialogPortal } from '../ui/DialogPortal'
import { Tooltip } from '../ui/Tooltip'

// =============================================================================
// RollbackDialog — Argo CD Application rollback confirmation. Shared
// between per-cluster Radar (OSS web/) and Radar Hub's fleet GitOps
// detail page. Presentational only; caller mints the historyId/revision
// from the clicked GitOpsHistoryItem and handles the POST to
// /api/argo/applications/{ns}/{name}/rollback in onConfirm.
//
// Defaults are conservative: no prune (avoid surprise deletions of
// resources added after the target revision), no dry-run (default is to
// actually roll back). Operators wanting a preview check Dry Run; the
// result lands in the Activity tab.
// =============================================================================

export interface RollbackDialogProps {
  open: boolean
  // Caller provides the user-visible revision (mono SHA / tag) and the
  // history id — Argo's API uses the id, the user reads the revision.
  appLabel: string
  revision: string
  historyId?: string
  source?: string
  multiSource?: boolean
  pending?: boolean
  onCancel: () => void
  onConfirm: (opts: { prune: boolean; dryRun: boolean }) => void
}

export function RollbackDialog({ open, appLabel, revision, historyId, source, multiSource, pending, onCancel, onConfirm }: RollbackDialogProps) {
  const titleId = useId()
  const [prune, setPrune] = useState(false)
  const [dryRun, setDryRun] = useState(false)

  useEffect(() => {
    if (open) {
      setPrune(false)
      setDryRun(false)
    }
  }, [open])

  return (
    <DialogPortal ariaLabelledBy={titleId} open={open} onClose={pending ? () => {} : onCancel} className="w-[440px]" closable={!pending}>
      <div className="border-b border-theme-border px-4 py-3">
        <h2 id={titleId} className="text-sm font-semibold text-theme-text-primary">Roll back application</h2>
        <p className="mt-0.5 text-xs text-theme-text-tertiary">{appLabel}</p>
      </div>
      <div className="space-y-4 px-4 py-4 text-sm">
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
          {dryRun
            ? 'Argo will preview syncing this application to a previous revision without applying changes.'
            : 'Argo will sync this application to a previous revision. Auto-sync is off. Later changes can still cause drift. Syncing or enabling auto-sync deploys the current revision and can undo the rollback.'}
        </div>
        {multiSource && (
          <p className="text-xs text-warning-text">
            Rolling back a multi-source Application needs Argo CD 2.12 or later. A 2.11 controller ignores the history entry’s sources and revisions and syncs the current ones.
          </p>
        )}
        <dl className="grid grid-cols-[80px_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
          <dt className="text-theme-text-tertiary">Revision</dt>
          <dd className="min-w-0">
            <Tooltip content={revision} delay={400} disabled={!revision} wrapperClassName="block max-w-full">
              <span className="block truncate font-mono text-theme-text-primary">{revision || '-'}</span>
            </Tooltip>
          </dd>
          {source && (
            <>
              <dt className="text-theme-text-tertiary">Source</dt>
              <dd className="min-w-0 break-words text-theme-text-primary">{source}</dd>
            </>
          )}
          {historyId && (
            <>
              <dt className="text-theme-text-tertiary">History ID</dt>
              <dd className="font-mono text-theme-text-primary">#{historyId}</dd>
            </>
          )}
        </dl>
        <div className="space-y-2">
          <p className="text-xs text-theme-text-tertiary">Uses the Application's current sync options. Sync hooks don't run.</p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={prune}
              onChange={(e) => setPrune(e.target.checked)}
              disabled={pending}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-sky-500 disabled:cursor-not-allowed"
            />
            <div className="min-w-0">
              <div className="text-xs text-theme-text-primary">Delete tracked resources that aren't in the rollback target</div>
              <div className="text-[11px] text-theme-text-tertiary">Off by default. The target's resources are still applied; other tracked resources are kept.</div>
            </div>
          </label>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={dryRun}
              onChange={(e) => setDryRun(e.target.checked)}
              disabled={pending}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-sky-500 disabled:cursor-not-allowed"
            />
            <div className="min-w-0">
              <div className="text-xs text-theme-text-primary">Dry run</div>
              <div className="text-[11px] text-theme-text-tertiary">Preview the rollback without applying it.</div>
            </div>
          </label>
        </div>
      </div>
      <div className="flex items-center justify-end gap-2 border-t border-theme-border bg-theme-base px-4 py-3">
        <button
          type="button"
          onClick={onCancel}
          disabled={pending}
          className="rounded-md border border-theme-border bg-theme-surface px-3 py-1.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={() => onConfirm({ prune, dryRun })}
          disabled={pending}
          className="inline-flex items-center gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-1.5 text-xs font-medium text-amber-700 hover:bg-amber-500/20 disabled:cursor-not-allowed disabled:opacity-50 dark:text-amber-400"
        >
          {pending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <History className="h-3.5 w-3.5" />}
          {dryRun ? 'Run dry-run' : 'Roll back'}
        </button>
      </div>
    </DialogPortal>
  )
}
