import { useState, useEffect, type ComponentType } from 'react'
import { Loader2, RefreshCw } from 'lucide-react'

import { DialogPortal } from '../ui/DialogPortal'
import { Input } from '../ui/Input'

// =============================================================================
// SyncOptionsDialog — Argo CD Sync drawer, shared between per-cluster
// Radar (OSS web/) and Radar Hub's fleet GitOps detail page. Presentational
// only: caller controls open state, supplies the app label, and handles
// the onConfirm callback (POST to /api/argo/applications/{ns}/{name}/sync
// or the hub-proxied equivalent /c/{ctrl}/api/argo/...).
//
// Defaults match Argo's most-common path (prune true, no dry-run, no
// force) so the two-click sync stays cheap. Revision is optional — empty
// falls through to the Application's targetRevision.
// =============================================================================

// ArgoSyncOpts is the payload shape the dialog passes to its onConfirm
// callback (and that downstream POSTs to `/api/argo/applications/{ns}/{name}/sync`
// expect). Exported separately so consumers can type the mutationFn arg
// without redeclaring the structure — keeps OSS and Hub in lockstep on
// the wire shape.
export interface ArgoSyncOpts {
  revision?: string
  prune: boolean
  dryRun: boolean
  force: boolean
  applyOnly: boolean
  syncOptions: string[]
}

export interface SyncOptionsDialogProps {
  open: boolean
  appLabel: string
  pending?: boolean
  onCancel: () => void
  onConfirm: (opts: ArgoSyncOpts) => void
}

export function SyncOptionsDialog({ open, appLabel, pending, onCancel, onConfirm }: SyncOptionsDialogProps) {
  const [revision, setRevision] = useState('')
  const [prune, setPrune] = useState(true)
  const [dryRun, setDryRun] = useState(false)
  const [force, setForce] = useState(false)
  const [applyOnly, setApplyOnly] = useState(false)
  const [replace, setReplace] = useState(false)
  const [serverSideApply, setServerSideApply] = useState(false)

  // Reset on each open so a previous attempt's flags don't leak into the
  // next sync — easy footgun in modal-heavy flows.
  useEffect(() => {
    if (open) {
      setRevision('')
      setPrune(true)
      setDryRun(false)
      setForce(false)
      setApplyOnly(false)
      setReplace(false)
      setServerSideApply(false)
    }
  }, [open])

  function submit() {
    const syncOptions: string[] = []
    if (replace) syncOptions.push('Replace=true')
    if (serverSideApply) syncOptions.push('ServerSideApply=true')
    onConfirm({
      revision: revision.trim() || undefined,
      prune,
      dryRun,
      force,
      applyOnly,
      syncOptions,
    })
  }

  return (
    <DialogPortal open={open} onClose={pending ? () => {} : onCancel} className="w-[480px]" closable={!pending}>
      <div className="border-b border-theme-border px-4 py-3">
        <h2 className="text-sm font-semibold text-theme-text-primary">Sync application</h2>
        <p className="mt-0.5 text-xs text-theme-text-tertiary">{appLabel}</p>
      </div>
      <div className="space-y-4 px-4 py-4 text-sm">
        <label className="block">
          <span className="text-xs font-medium text-theme-text-secondary">Revision (optional)</span>
          <Input
            value={revision}
            onChange={(e) => setRevision(e.target.value)}
            placeholder="HEAD"
            disabled={pending}
            className="mt-1 w-full rounded-md border border-theme-border bg-theme-base px-2 py-1.5 font-mono text-xs text-theme-text-primary outline-none placeholder:text-theme-text-tertiary focus:border-sky-500"
          />
          <span className="mt-0.5 block text-[11px] text-theme-text-tertiary">
            Branch, tag, or commit SHA. Leave empty to use the Application's targetRevision.
          </span>
        </label>

        {/* Common (Prune / Dry run) sit above a divider; Advanced toggles
            stay accessible but visually subordinate so the common-case user
            can scan past them without parsing every helper line. */}
        <fieldset className="space-y-2">
          <legend className="mb-1 text-xs font-medium text-theme-text-secondary">Sync options</legend>
          <Toggle label="Prune" checked={prune} onChange={setPrune} disabled={pending} hint="Delete resources that are no longer in Git." />
          <Toggle label="Dry run" checked={dryRun} onChange={setDryRun} disabled={pending} hint="Preview only — Argo computes the diff but applies nothing." />
        </fieldset>
        <fieldset className="space-y-2 border-t border-theme-border pt-3">
          <legend className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Advanced</legend>
          <Toggle label="Apply only" checked={applyOnly} onChange={setApplyOnly} disabled={pending} hint="Skip PreSync / PostSync / SyncFail hooks." />
          <Toggle label="Force" checked={force} onChange={setForce} disabled={pending} hint="Use kubectl --force; required for some immutable-field changes." />
          <Toggle label="Replace" checked={replace} onChange={setReplace} disabled={pending} hint="kubectl replace instead of apply (drops fields not in source)." />
          <Toggle label="Server-side apply" checked={serverSideApply} onChange={setServerSideApply} disabled={pending} hint="Use the K8s server-side apply mechanism for ownership tracking." />
        </fieldset>
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
        <PrimaryButton onClick={submit} disabled={pending} icon={pending ? Loader2 : RefreshCw} loading={pending} label={dryRun ? 'Run dry-run' : 'Sync now'} />
      </div>
    </DialogPortal>
  )
}

function Toggle({ label, checked, onChange, disabled, hint }: { label: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; hint?: string }) {
  return (
    <label className="flex items-start gap-2">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        disabled={disabled}
        className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-sky-500 disabled:cursor-not-allowed"
      />
      <div className="min-w-0">
        <div className="text-xs text-theme-text-primary">{label}</div>
        {hint && <div className="text-[11px] text-theme-text-tertiary">{hint}</div>}
      </div>
    </label>
  )
}

function PrimaryButton({ onClick, disabled, icon: Icon, loading, label }: { onClick: () => void; disabled?: boolean; icon: ComponentType<{ className?: string }>; loading?: boolean; label: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="btn-brand inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50"
    >
      <Icon className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
      {label}
    </button>
  )
}
