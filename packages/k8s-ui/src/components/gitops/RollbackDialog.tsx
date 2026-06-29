import { useState, useEffect } from 'react'
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
  pending?: boolean
  onCancel: () => void
  onConfirm: (opts: { prune: boolean; dryRun: boolean }) => void
}

export function RollbackDialog({ open, appLabel, revision, historyId, pending, onCancel, onConfirm }: RollbackDialogProps) {
  const [prune, setPrune] = useState(false)
  const [dryRun, setDryRun] = useState(false)

  useEffect(() => {
    if (open) {
      setPrune(false)
      setDryRun(false)
    }
  }, [open])

  return (
    <DialogPortal open={open} onClose={pending ? () => {} : onCancel} className="w-[440px]" closable={!pending}>
      <div className="border-b border-theme-border px-4 py-3">
        <h2 className="text-sm font-semibold text-theme-text-primary">Roll back application</h2>
        <p className="mt-0.5 text-xs text-theme-text-tertiary">{appLabel}</p>
      </div>
      <div className="space-y-4 px-4 py-4 text-sm">
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
          Argo will sync this application to a previous revision. This is a write operation on the cluster.
        </div>
        <dl className="grid grid-cols-[80px_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
          <dt className="text-theme-text-tertiary">Revision</dt>
          <dd className="min-w-0">
            <Tooltip content={revision} delay={400} disabled={!revision} wrapperClassName="block max-w-full">
              <span className="block truncate font-mono text-theme-text-primary">{revision || '-'}</span>
            </Tooltip>
          </dd>
          {historyId && (
            <>
              <dt className="text-theme-text-tertiary">History ID</dt>
              <dd className="font-mono text-theme-text-primary">#{historyId}</dd>
            </>
          )}
        </dl>
        <div className="space-y-2">
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={prune}
              onChange={(e) => setPrune(e.target.checked)}
              disabled={pending}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-sky-500 disabled:cursor-not-allowed"
            />
            <div className="min-w-0">
              <div className="text-xs text-theme-text-primary">Prune resources added since this revision</div>
              <div className="text-[11px] text-theme-text-tertiary">Off by default — leaves any resources created after this revision untouched.</div>
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
