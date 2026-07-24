import { useCallback, useEffect, useState } from 'react'
import { AlertTriangle, Check, ChevronDown, ChevronRight, Loader2, X } from 'lucide-react'
import { DialogPortal } from '../ui/DialogPortal'
import { YamlEditor, type YamlSchemaLoader } from '../ui/YamlEditor'
import {
  reviewedResourceVersionsForPreview,
  YamlReview,
  type YamlPreviewResult,
} from '../ui/YamlReview'
import { Tooltip } from '../ui/Tooltip'
import { formatApplyError } from '../../utils/k8s-errors'

export interface ApplyResult {
  name: string
  namespace: string
  kind: string
  created: boolean
}

export interface CreateResourceDialogProps {
  open: boolean
  onClose: () => void
  initialYaml?: string
  title?: string
  onApply: (params: {
    yaml: string
    mode: 'apply' | 'create'
    dryRun: boolean
    force: boolean
    reviewedResourceVersions?: Record<number, string>
    reviewedContext?: string
  }) => Promise<ApplyResult[]>
  isApplying: boolean
  onPreview?: (params: { yaml: string; mode: 'apply' | 'create'; force: boolean }) => Promise<{
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  }>
  isPreviewing?: boolean
  previewError?: string | null
  schemaLoader?: YamlSchemaLoader
  onCreated?: (result: ApplyResult) => void
}

export function CreateResourceDialog({
  open,
  onClose,
  initialYaml = '',
  title,
  onApply,
  isApplying,
  onPreview,
  isPreviewing = false,
  previewError,
  schemaLoader,
  onCreated,
}: CreateResourceDialogProps) {
  const [yaml, setYaml] = useState(initialYaml)
  const [mode, setMode] = useState<'apply' | 'create'>('apply')
  const [dryRun, setDryRun] = useState(false)
  const [force, setForce] = useState(false)
  const [yamlValid, setYamlValid] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [preview, setPreview] = useState<{
    yaml: string
    mode: 'apply' | 'create'
    force: boolean
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  } | null>(null)

  useEffect(() => {
    if (!open) return
    setYaml(initialYaml)
    setMode('apply')
    setDryRun(false)
    setForce(false)
    setYamlValid(true)
    setError(null)
    setSuccess(null)
    setPreview(null)
  }, [open, initialYaml])

  const pending = isApplying || isPreviewing
  const closeNow = useCallback(() => {
    setError(null)
    setSuccess(null)
    setPreview(null)
    setYaml('')
    onClose()
  }, [onClose])
  const handleClose = useCallback(() => {
    if (!pending) closeNow()
  }, [closeNow, pending])

  const handleValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlValid(errors.length === 0)
  }, [])

  const finishApply = useCallback(
    (results: ApplyResult[], appliedMode: 'apply' | 'create', wasDryRun: boolean) => {
      const action = appliedMode === 'create' ? 'Created' : 'Applied'
      const dryRunLabel = wasDryRun ? ' (dry run)' : ''
      if (results.length === 1) {
        const result = results[0]
        setSuccess(
          `${action} ${result.kind} ${result.namespace ? `${result.namespace}/` : ''}${result.name}${dryRunLabel}`,
        )
      } else {
        setSuccess(`${action} ${results.length} resources${dryRunLabel}`)
      }
      if (wasDryRun) return
      if (onCreated && results.length > 0) {
        closeNow()
        onCreated(results[0])
      } else {
        window.setTimeout(closeNow, 1200)
      }
    },
    [closeNow, onCreated],
  )

  const handleSubmit = useCallback(async () => {
    if (!yaml.trim()) {
      setError('YAML content is required')
      return
    }
    setError(null)
    setSuccess(null)
    try {
      if (onPreview) {
        const reviewedForce = mode === 'apply' && force
        const result = await onPreview({ yaml, mode, force: reviewedForce })
        setPreview({
          yaml,
          mode,
          force: reviewedForce,
          documents: result.documents,
          nonAtomic: result.nonAtomic,
          context: result.context,
        })
        return
      }
      const results = await onApply({
        yaml,
        mode,
        dryRun,
        force: mode === 'apply' && force,
      })
      finishApply(results, mode, dryRun)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Unknown error')
    }
  }, [yaml, mode, dryRun, force, onApply, onPreview, finishApply])

  const handleApplyReviewed = useCallback(async () => {
    if (!preview) return
    setError(null)
    try {
      const reviewedResourceVersions = reviewedResourceVersionsForPreview(preview.documents)
      const results = await onApply({
        yaml: preview.yaml,
        mode: preview.mode,
        dryRun: false,
        force: preview.force,
        reviewedResourceVersions,
        reviewedContext: preview.context,
      })
      finishApply(results, preview.mode, false)
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : 'Unknown error'
      const appliedResults =
        caught instanceof Error &&
        'appliedResults' in caught &&
        Array.isArray(caught.appliedResults)
          ? caught.appliedResults
          : []
      if (preview.mode === 'create' && appliedResults.length > 0) {
        setYaml(preview.yaml)
        setMode('apply')
        setPreview(null)
        setError(
          `${message} Radar switched to Apply mode so you can review and continue from the current cluster state.`,
        )
        return
      }
      setError(message)
      if (onPreview) {
        try {
          const refreshed = await onPreview({
            yaml: preview.yaml,
            mode: preview.mode,
            force: preview.force,
          })
          setPreview({
            ...preview,
            documents: refreshed.documents,
            nonAtomic: refreshed.nonAtomic,
            context: refreshed.context,
          })
        } catch {
          // Keep the last review visible when refresh is unavailable.
        }
      }
    }
  }, [preview, onApply, onPreview, finishApply])

  const dialogTitle = title || 'Create Resource'
  const submitLabel = onPreview ? 'Review' : mode === 'create' ? 'Create' : 'Apply'

  return (
    <DialogPortal
      open={open}
      onClose={handleClose}
      closable={!pending}
      className={
        preview
          ? 'w-[min(1200px,calc(100vw-2rem))] h-[min(860px,calc(100vh-2rem))] flex flex-col'
          : 'w-[700px] max-h-[85vh] flex flex-col'
      }
    >
      {preview ? (
        <YamlReview
          submittedYaml={preview.yaml}
          documents={preview.documents}
          nonAtomic={preview.nonAtomic}
          force={preview.force}
          isApplying={isApplying}
          applyError={error}
          applyLabel={
            preview.mode === 'create' ? 'Create reviewed resources' : 'Apply reviewed changes'
          }
          onClose={handleClose}
          onBack={() => {
            setError(null)
            setPreview(null)
          }}
          onApply={handleApplyReviewed}
        />
      ) : (
        <>
          <div className="flex shrink-0 items-center justify-between border-b border-theme-border px-5 py-3.5">
            <h2 className="text-sm font-semibold text-theme-text-primary">{dialogTitle}</h2>
            <Tooltip content="Close">
              <button
                type="button"
                onClick={handleClose}
                className="rounded p-1 text-theme-text-secondary transition-colors hover:bg-theme-hover"
              >
                <X className="h-4 w-4" />
              </button>
            </Tooltip>
          </div>

          <div className="min-h-0 flex-1 px-5 py-3">
            <YamlEditor
              value={yaml}
              onChange={setYaml}
              height="400px"
              onValidate={handleValidate}
              schemaLoader={schemaLoader}
            />
          </div>

          {(error || previewError) && (
            <ApplyErrorBanner error={error || previewError || 'Preview failed'} />
          )}
          {success && (
            <div className="mx-5 mb-2 flex items-center gap-2 rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3.5 w-3.5 shrink-0" />
              <span>{success}</span>
            </div>
          )}

          <div className="flex shrink-0 items-center justify-between border-t border-theme-border px-5 py-3">
            <div className="flex items-center gap-3">
              <Tooltip
                content="Apply: create or update (idempotent). Create: fail if exists."
                position="bottom"
              >
                <div
                  className="flex items-center rounded-md border border-theme-border bg-theme-base p-0.5"
                  role="radiogroup"
                  aria-label="Apply mode"
                >
                  {(['apply', 'create'] as const).map((option) => (
                    <button
                      type="button"
                      key={option}
                      onClick={() => setMode(option)}
                      role="radio"
                      aria-checked={mode === option}
                      className={`rounded px-2.5 py-1 text-xs font-medium capitalize transition-colors ${
                        mode === option
                          ? 'bg-theme-elevated text-theme-text-primary shadow-theme-sm'
                          : 'text-theme-text-tertiary hover:text-theme-text-secondary'
                      }`}
                    >
                      {option}
                    </button>
                  ))}
                </div>
              </Tooltip>

              {!onPreview && (
                <Tooltip
                  content="Validate against the cluster without persisting changes"
                  position="bottom"
                >
                  <label className="flex cursor-pointer items-center gap-1.5 text-xs text-theme-text-secondary">
                    <input
                      type="checkbox"
                      checked={dryRun}
                      onChange={(event) => setDryRun(event.target.checked)}
                      className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
                    />
                    Dry run
                  </label>
                </Tooltip>
              )}

              <Tooltip
                content="Override field ownership conflicts. An active controller may reconcile those fields back."
                position="bottom"
              >
                <label
                  className={`flex items-center gap-1.5 text-xs ${mode === 'apply' ? 'cursor-pointer text-theme-text-secondary' : 'cursor-not-allowed text-theme-text-tertiary'}`}
                >
                  <input
                    type="checkbox"
                    checked={mode === 'apply' && force}
                    disabled={mode !== 'apply'}
                    onChange={(event) => setForce(event.target.checked)}
                    className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
                  />
                  Force
                </label>
              </Tooltip>
            </div>

            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={handleClose}
                className="rounded-lg px-3 py-1.5 text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleSubmit}
                disabled={pending || !yaml.trim() || !yamlValid}
                className="btn-brand flex items-center gap-1.5 rounded-lg px-4 py-1.5 text-xs font-medium disabled:cursor-not-allowed"
              >
                {pending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {isPreviewing ? 'Previewing…' : isApplying ? `${submitLabel}…` : submitLabel}
              </button>
            </div>
          </div>
        </>
      )}
    </DialogPortal>
  )
}

function ApplyErrorBanner({ error }: { error: string }) {
  const [expanded, setExpanded] = useState(false)
  const parsed = formatApplyError(error)
  const hasFriendly = Boolean(parsed.suggestion)
  return (
    <div className="mx-5 mb-2 rounded-md border border-red-500/30 bg-red-500/10 text-xs">
      <div className="flex items-start gap-2 px-3 py-2 text-red-600 dark:text-red-400">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <span className="font-medium">{parsed.summary}</span>
          {parsed.suggestion && (
            <p className="mt-1 text-red-500/80 dark:text-red-400/80">{parsed.suggestion}</p>
          )}
        </div>
      </div>
      {hasFriendly && (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="flex items-center gap-1 px-3 pb-2 text-red-500/60 hover:text-red-500/80 dark:text-red-400/60 dark:hover:text-red-400/80"
        >
          {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          Details
        </button>
      )}
      {expanded && hasFriendly && (
        <div className="break-all px-3 pb-2 font-mono leading-relaxed text-red-500/60 dark:text-red-400/60">
          {parsed.raw}
        </div>
      )}
    </div>
  )
}
