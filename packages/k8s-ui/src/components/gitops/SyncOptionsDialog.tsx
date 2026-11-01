import { useState, useEffect, type ComponentType, useId } from 'react'
import { AlertTriangle, CheckCircle2, CircleAlert, Loader2, RefreshCw, ShieldCheck, XCircle } from 'lucide-react'

import { DialogPortal } from '../ui/DialogPortal'
import { Input } from '../ui/Input'
import { AlertBanner } from '../ui/drawer-components'
import { Tooltip } from '../ui/Tooltip'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { SEVERITY_TEXT } from '../../utils/badge-colors'
import type { GitOpsInsightRef } from '../../types/gitops-insights'

// =============================================================================
// SyncOptionsDialog — Argo CD Sync drawer, shared between per-cluster
// Radar (OSS web/) and Radar Hub's fleet GitOps detail page. Presentational
// only: caller controls open state, supplies the app label, and handles
// the onConfirm callback (POST to /api/argo/applications/{ns}/{name}/sync
// or the hub-proxied equivalent /c/{ctrl}/api/argo/...).
//
// Prune starts off, as in Argo CD's UI and CLI, and is always sent. Resource
// sync does not prune and omits revision/apply-only.
//
// Sync options start from the Application's own (applicationSyncOptions). A
// sync whose options the user leaves alone sends none, so Radar applies the
// Application's current ones; flipping Replace or Server-side apply sends the
// Application's options with that edit, keeping all other options.
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
  // Omitted: sync with the Application's own spec.syncPolicy.syncOptions.
  // Present: the options for this sync, replacing the Application's ([] = none).
  // Only a Radar advertising `argoSyncInheritance` reads an omission that
  // way; see argoSyncOptionsForRequest.
  syncOptions?: string[]
}

export interface SyncOptionsDialogProps {
  open: boolean
  appLabel: string
  multiSource?: boolean
  comparedRevision?: string
  // An omitted revision will use the last compared one: the serving Radar
  // pins (argoSyncInheritance) and the comparison matches the current
  // source(s) (argoComparedRevisionCurrent). Otherwise the target is resolved
  // when the sync runs.
  pinsComparedRevision?: boolean
  resource?: GitOpsInsightRef
  disabledReason?: string
  // The Application's spec.syncPolicy.syncOptions. Without it the toggles
  // are disabled and the submitted syncOptions are omitted.
  applicationSyncOptions?: string[]
  pending?: boolean
  autoSyncEnabled?: boolean
  validationPending?: boolean
  operationInProgress?: boolean
  validationResult?: ResourceValidationResult | null
  validationError?: string | null
  validationRawError?: string
  // Why the last confirm failed, for a host that keeps the dialog open to retry.
  syncError?: string | null
  onCancel: () => void
  onConfirm: (opts: ArgoSyncOpts) => void
  onValidate?: (opts: ArgoSyncOpts) => void
  onValidationReset?: () => void
}

export interface ResourceValidationResult {
  outcome: 'succeeded' | 'failed' | 'inconclusive'
  message: string
  resource?: {
    status?: string
    message?: string
  }
}

export function SyncOptionsDialog({ open, appLabel, multiSource, comparedRevision, pinsComparedRevision, resource, disabledReason, applicationSyncOptions, pending, autoSyncEnabled, validationPending, operationInProgress, validationResult, validationError, validationRawError, syncError, onCancel, onConfirm, onValidate, onValidationReset }: SyncOptionsDialogProps) {
  const titleId = useId()
  const syncOptionsUnknown = applicationSyncOptions === undefined
  const liveAppOptions = applicationSyncOptions ?? EMPTY_OPTIONS
  // Joined so a caller passing a fresh array each render isn't a change.
  const liveAppOptionsKey = liveAppOptions.join('\n')
  // The options the toggles started from, which the request is built from.
  // They follow the Application until the user edits an option, so a refetch
  // while the dialog is open never replaces what the user sees or chose.
  const [appOptions, setAppOptions] = useState(liveAppOptions)
  const [optionsEdited, setOptionsEdited] = useState(false)
  const optionsChangedWhileEditing = optionsEdited && liveAppOptionsKey !== appOptions.join('\n')
  const appReplace = appOptions.includes(REPLACE_OPTION)
  const appServerSideApply = appOptions.includes(SERVER_SIDE_APPLY_OPTION)
  const otherAppOptions = appOptions.filter((option) => !isToggledOption(option))
  const comparedRevisionLabel = comparedRevision && /^[0-9a-f]{40}$/i.test(comparedRevision) ? comparedRevision.slice(0, 7) : comparedRevision
  const [revision, setRevision] = useState('')
  const [prune, setPrune] = useState(false)
  const [dryRun, setDryRun] = useState(false)
  const [force, setForce] = useState(false)
  const [applyOnly, setApplyOnly] = useState(false)
  const [replace, setReplace] = useState(appReplace)
  const [serverSideApply, setServerSideApply] = useState(appServerSideApply)
  const richResourceValidation = !!resource && !!onValidate
  const busy = !!pending || !!validationPending
  const optionsDisabled = busy || !!operationInProgress || !!disabledReason
  const revisionDisabled = !!autoSyncEnabled && !dryRun
  const targetLabel = pinsComparedRevision
    ? `the revision Argo last compared${comparedRevisionLabel ? ` (${comparedRevisionLabel})` : ''}`
    : 'the target revision, resolved when the sync runs'

  function startFromApplicationOptions(options: string[]) {
    setAppOptions(options)
    setReplace(options.includes(REPLACE_OPTION))
    setServerSideApply(options.includes(SERVER_SIDE_APPLY_OPTION))
  }

  // Reset on each open so a previous attempt's flags don't leak into the
  // next sync — easy footgun in modal-heavy flows.
  useEffect(() => {
    if (open) {
      setRevision('')
      setPrune(false)
      setDryRun(false)
      setForce(false)
      setApplyOnly(false)
      setOptionsEdited(false)
      startFromApplicationOptions(liveAppOptions)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, resource])

  // A revision that no longer applies is cleared rather than left on screen
  // while the sync deploys something else.
  useEffect(() => {
    if (revisionDisabled || multiSource) setRevision('')
  }, [revisionDisabled, multiSource])

  // The Application's options changed while the dialog is open and the user
  // hasn't edited one: start from the new ones, and drop a dry-run result
  // that was computed with the old ones.
  useEffect(() => {
    if (!open || optionsEdited || liveAppOptionsKey === appOptions.join('\n')) return
    startFromApplicationOptions(liveAppOptions)
    onValidationReset?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveAppOptionsKey])

  // Omitting the options means "the Application's current ones", which is
  // what the toggles show only while the snapshot is still current.
  const optionEdits = syncOptionsUnknown
    ? { replace: false, serverSideApply: false }
    : { replace, serverSideApply, applicationSyncOptions: appOptions, sendUnchanged: appOptions.join('\n') !== liveAppOptionsKey }

  // Replace and Force can take workloads down, so a real sync using either says
  // so beside the button and in its label: Argo CD's own panel asks for a
  // confirmation on each. Replace counts when inherited from the Application.
  const disruptive = !dryRun || richResourceValidation
    ? [replace && 'replace', force && 'force'].filter((mode): mode is string => !!mode)
    : []
  const syncLabel = resource ? 'Sync resource' : 'Sync now'

  function submit() {
    if (optionsDisabled) return
    onConfirm(buildArgoSyncOpts({ resourceMode: !!resource, revision: multiSource || revisionDisabled ? '' : revision, prune, dryRun: richResourceValidation ? false : dryRun, force, applyOnly, ...optionEdits }))
  }

  function validate() {
    if (optionsDisabled) return
    onValidate?.(buildArgoSyncOpts({ resourceMode: true, revision, prune, dryRun: true, force, applyOnly, ...optionEdits }))
  }

  function updateValidationOption(setter: (value: boolean) => void, value: boolean) {
    setter(value)
    onValidationReset?.()
  }

  function updateSyncOption(setter: (value: boolean) => void, value: boolean) {
    setOptionsEdited(true)
    updateValidationOption(setter, value)
  }

  return (
    <DialogPortal ariaLabelledBy={titleId} open={open} onClose={busy ? () => {} : onCancel} className="flex max-h-[calc(100vh-2rem)] w-[480px] flex-col" closable={!busy}>
      <div className="shrink-0 border-b border-theme-border px-4 py-3">
        <h2 id={titleId} className="text-sm font-semibold text-theme-text-primary">{resource ? 'Sync resource' : 'Sync application'}</h2>
        <p className="mt-0.5 break-all text-xs text-theme-text-tertiary">{appLabel}</p>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4 text-sm">
        {resource && (
          <div className="rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2.5">
            <div className="flex items-start gap-2">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
              <div className="min-w-0">
                <div className="break-all text-xs font-medium text-theme-text-primary">
                  {resource.kind} / {resource.namespace ? `${resource.namespace}/` : ''}{resource.name}
                </div>
                <p className="mt-1 text-[11px] leading-relaxed text-theme-text-secondary">
                  Syncs only this resource, without hooks. Resources it depends on aren't synced; namespace creation still applies. Uses {multiSource && pinsComparedRevision ? 'the revisions Argo last compared' : targetLabel}.
                </p>
                {autoSyncEnabled && (
                  <p className="mt-1 text-[11px] leading-relaxed text-theme-text-secondary">
                    Auto-sync stays on during the dry run, so Argo can still sync the Application meanwhile.
                  </p>
                )}
              </div>
            </div>
          </div>
        )}
        {!resource && !multiSource && (
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">Revision (optional)</span>
            <Input
              value={revision}
              onChange={(e) => setRevision(e.target.value)}
              placeholder={pinsComparedRevision ? 'Last compared revision' : 'Target revision'}
              disabled={optionsDisabled || revisionDisabled}
              className="mt-1 w-full rounded-md border border-theme-border bg-theme-base px-2 py-1.5 font-mono text-xs text-theme-text-primary outline-none placeholder:text-theme-text-tertiary focus:border-sky-500"
            />
            <span className="mt-0.5 block text-[11px] text-theme-text-tertiary">
              {revisionDisabled
                ? `Auto-sync is on, so this sync uses ${targetLabel}. Turn off auto-sync to deploy another revision; a dry run can preview one.`
                : `Branch, tag, or commit SHA. Leave empty for ${targetLabel}.`}
            </span>
          </label>
        )}
        {!resource && multiSource && (
          <p className="text-xs text-theme-text-tertiary">{pinsComparedRevision ? 'Syncs each source at the revision Argo last compared.' : "Syncs each source's target revision, resolved when the sync runs."}</p>
        )}

        {/* Common (Prune / Dry run) sit above a divider; Advanced toggles
            stay accessible but visually subordinate so the common-case user
            can scan past them without parsing every helper line. */}
        {!richResourceValidation && (
          <fieldset className="space-y-2">
            <legend className="mb-1 text-xs font-medium text-theme-text-secondary">Sync options</legend>
            {!resource && <Toggle label="Prune" checked={prune} onChange={setPrune} disabled={optionsDisabled} hint="Delete resources that are no longer in Git." />}
            <Toggle label="Dry run" checked={dryRun} onChange={setDryRun} disabled={optionsDisabled} hint="Preview only — Argo computes the diff but applies nothing." />
          </fieldset>
        )}
        <fieldset className="space-y-2 border-t border-theme-border pt-3">
          <legend className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Advanced</legend>
          {!resource && <Toggle label="Apply only" checked={applyOnly} onChange={setApplyOnly} disabled={optionsDisabled} hint="Skip PreSync / PostSync / SyncFail hooks." />}
          <Toggle label="Force" checked={force} onChange={(value) => updateValidationOption(setForce, value)} disabled={optionsDisabled} hint="Use kubectl --force; required for some immutable-field changes." />
          <Toggle label="Replace" checked={replace} onChange={(value) => updateSyncOption(setReplace, value)} disabled={optionsDisabled || syncOptionsUnknown} hint={`kubectl replace instead of apply (drops fields not in source).${appReplace ? FROM_APPLICATION : ''}`} />
          <Toggle label="Server-side apply" checked={serverSideApply} onChange={(value) => updateSyncOption(setServerSideApply, value)} disabled={optionsDisabled || syncOptionsUnknown} hint={`Use the K8s server-side apply mechanism for ownership tracking.${appServerSideApply ? FROM_APPLICATION : ''}`} />
          {syncOptionsUnknown && (
            <p className="text-xs text-theme-text-tertiary">This view can't show or change the Application's sync options, so Replace and Server-side apply are unavailable here.</p>
          )}
          {optionsChangedWhileEditing && (
            <p className="text-xs text-theme-text-secondary">The Application's sync options changed while this dialog was open. This sync uses the options shown here. Cancel and reopen to load the latest.</p>
          )}
          {otherAppOptions.length > 0 && (
            <div>
              <div className="text-xs font-medium text-theme-text-secondary">Also used for this sync</div>
              <ul className="mt-1 space-y-1 text-[11px] text-theme-text-tertiary">
                {otherAppOptions.map((option, index) => <li key={index} className="break-all font-mono">{option}</li>)}
              </ul>
            </div>
          )}
        </fieldset>
        {syncError && !pending && (
          <p role="alert" className={`flex items-start gap-1.5 text-xs ${SEVERITY_TEXT.error}`}>
            <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span className="max-h-20 min-w-0 overflow-auto break-words">Sync failed: {syncError}</span>
          </p>
        )}
        {disruptive.length > 0 && (
          <p className={`flex items-start gap-1.5 text-xs ${SEVERITY_TEXT.warning}`}>
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              {replace && 'Replace deletes and recreates resources instead of updating them, which can cause downtime. '}
              {force && 'Force removes resources immediately, skipping graceful deletion, which can lose data.'}
            </span>
          </p>
        )}
        {richResourceValidation && (validationPending || validationResult || validationError) && (
          <ValidationResult pending={!!validationPending} result={validationResult} error={validationError} rawError={validationRawError} />
        )}
        {richResourceValidation && !validationPending && operationInProgress && (
          <p className="text-[11px] leading-relaxed text-theme-text-secondary">
            Argo is finishing the current operation. Sync becomes available when it releases the operation slot.
          </p>
        )}
      </div>
      <div className="border-t border-theme-border bg-theme-base px-4 py-3">
        {disabledReason && <AlertBanner variant="warning" title="Action unavailable" message={disabledReason} />}
        <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={busy}
          className="rounded-md border border-theme-border bg-theme-surface px-3 py-1.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
        >
          Cancel
        </button>
        {richResourceValidation && (
          <Tooltip content="Runs an Argo selective dry-run. Applies nothing; API admission can still reject the real sync." wrapperClassName="inline-flex">
            <button
              type="button"
              onClick={validate}
              disabled={optionsDisabled}
              className="inline-flex items-center gap-1.5 rounded-md border border-theme-border bg-theme-surface px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
            >
              {validationPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5" />}
              {validationPending ? 'Running dry-run…' : 'Dry-run'}
            </button>
          </Tooltip>
        )}
        <PrimaryButton onClick={submit} disabled={optionsDisabled} icon={pending ? Loader2 : RefreshCw} loading={pending} label={dryRun && !richResourceValidation ? 'Run dry-run' : disruptive.length > 0 ? `${syncLabel === 'Sync now' ? 'Sync' : syncLabel} with ${disruptive.join(' and ')}` : syncLabel} warning={disruptive.length > 0} />
        </div>
      </div>
    </DialogPortal>
  )
}

function ValidationResult({ pending, result, error, rawError }: { pending: boolean; result?: ResourceValidationResult | null; error?: string | null; rawError?: string }) {
  const [detailsOpen, setDetailsOpen] = useState(false)
  const disclosure = useDisclosure(detailsOpen)
  if (pending) {
    return (
      <div className="card-inner flex items-start gap-2 px-3 py-2.5">
        <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-theme-text-secondary" />
        <div>
          <div className="text-xs font-medium text-theme-text-primary">Running selective dry-run</div>
          <p className="mt-0.5 text-[11px] text-theme-text-secondary">Waiting for the selective dry-run result. No resource changes will be applied.</p>
        </div>
      </div>
    )
  }
  if (error) {
    return (
      <div className="rounded-md border border-red-500/30 bg-red-500/5 px-3 py-2.5">
        <div className="flex items-start gap-2">
          <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-500" />
          <div>
            <div className="text-xs font-medium text-theme-text-primary">Dry-run could not start or complete</div>
            <p className="mt-0.5 max-h-20 overflow-auto break-words text-[11px] text-theme-text-secondary">{error}</p>
            {rawError && (
              <div className="mt-2 text-theme-text-secondary">
                <button type="button" onClick={() => setDetailsOpen(value => !value)} aria-expanded={detailsOpen} aria-controls={disclosure.panelId} className="flex items-center gap-1 text-xs">
                  <CollapseChevron open={detailsOpen} /> Error details
                </button>
                <Collapse open={detailsOpen} id={disclosure.panelId}>
                  <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs">{rawError}</pre>
                </Collapse>
              </div>
            )}
          </div>
        </div>
      </div>
    )
  }
  if (!result) return null

  const succeeded = result.outcome === 'succeeded'
  const inconclusive = result.outcome === 'inconclusive'
  const Icon = succeeded ? CheckCircle2 : inconclusive ? CircleAlert : XCircle
  const title = succeeded ? 'Dry-run passed' : inconclusive ? 'Dry-run was inconclusive' : 'Dry-run failed'
  const surface = succeeded
    ? 'border-emerald-500/30 bg-emerald-500/5'
    : inconclusive
      ? 'border-amber-500/30 bg-amber-500/5'
      : 'border-red-500/30 bg-red-500/5'
  const iconTone = succeeded ? 'text-emerald-500' : inconclusive ? 'text-amber-500' : 'text-red-500'
  return (
    <div className={`rounded-md border px-3 py-2.5 ${surface}`}>
      <div className="flex items-start gap-2">
        <Icon className={`mt-0.5 h-4 w-4 shrink-0 ${iconTone}`} />
        <div className="min-w-0">
          <div className="text-xs font-medium text-theme-text-primary">{title}</div>
          <p className="mt-0.5 max-h-20 overflow-auto break-words text-[11px] text-theme-text-secondary">{result.message}</p>
          {(result.resource?.status || result.resource?.message) && (
            <div className="mt-2 max-h-28 overflow-auto break-all font-mono text-[10px] text-theme-text-secondary">
              {result.resource.status}{result.resource.status && result.resource.message ? ' — ' : ''}{result.resource.message}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

const EMPTY_OPTIONS: string[] = []
const REPLACE_OPTION = 'Replace=true'
const SERVER_SIDE_APPLY_OPTION = 'ServerSideApply=true'
const FROM_APPLICATION = " On in the Application's sync policy."

// The dialog's Replace and Server-side apply toggles own these keys, whatever
// value the Application gives them.
function isToggledOption(option: string): boolean {
  const key = option.split('=')[0].trim()
  return key === 'Replace' || key === 'ServerSideApply'
}

export function buildArgoSyncOpts({ resourceMode, revision, prune, dryRun, force, applyOnly, replace, serverSideApply, applicationSyncOptions = EMPTY_OPTIONS, sendUnchanged = false }: {
  resourceMode: boolean
  revision: string
  prune: boolean
  dryRun: boolean
  force: boolean
  applyOnly: boolean
  replace: boolean
  serverSideApply: boolean
  applicationSyncOptions?: string[]
  // Send the options even when the toggles match applicationSyncOptions,
  // because those are no longer the Application's current ones.
  sendUnchanged?: boolean
}): ArgoSyncOpts {
  const opts: ArgoSyncOpts = {
    revision: resourceMode ? undefined : revision.trim() || undefined,
    prune: resourceMode ? false : prune,
    dryRun,
    force,
    applyOnly: resourceMode ? false : applyOnly,
  }
  const unchanged = replace === applicationSyncOptions.includes(REPLACE_OPTION) &&
    serverSideApply === applicationSyncOptions.includes(SERVER_SIDE_APPLY_OPTION)
  if (unchanged && !sendUnchanged) return opts

  const syncOptions = applicationSyncOptions.filter((option) => !isToggledOption(option))
  if (replace) syncOptions.push(REPLACE_OPTION)
  if (serverSideApply) syncOptions.push(SERVER_SIDE_APPLY_OPTION)
  return { ...opts, syncOptions }
}

// argoSyncOptionsForRequest is the syncOptions to send for a sync whose
// options are `syncOptions` (undefined = the Application's own). Only a Radar
// advertising `argoSyncInheritance` reads an omitted list as the
// Application's own; an older one syncs with none, so it is sent the
// Application's options as the caller last read them.
export function argoSyncOptionsForRequest(syncOptions: string[] | undefined, applicationSyncOptions: string[] | undefined, serverInheritsOptions: boolean): string[] | undefined {
  if (syncOptions !== undefined || serverInheritsOptions) return syncOptions
  return applicationSyncOptions && applicationSyncOptions.length > 0 ? applicationSyncOptions : undefined
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

function PrimaryButton({ onClick, disabled, icon: Icon, loading, label, warning }: { onClick: () => void; disabled?: boolean; icon: ComponentType<{ className?: string }>; loading?: boolean; label: string; warning?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`${warning ? 'bg-amber-600 text-theme-text-primary hover:bg-amber-700' : 'btn-brand'} inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50`}
    >
      <Icon className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
      {label}
    </button>
  )
}
