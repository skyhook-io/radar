import { useCallback, useEffect, useRef, useState, type DragEvent as ReactDragEvent } from 'react'
import { AlertTriangle, Check, Loader2, Upload, X } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { DialogPortal } from '../ui/DialogPortal'
import { YamlEditor, type YamlSchemaLoader } from '../ui/YamlEditor'
import {
  reviewedResourceVersionsForPreview,
  YamlReview,
  type YamlPreviewResult,
} from '../ui/YamlReview'
import { Tooltip } from '../ui/Tooltip'
import { formatApplyError } from '../../utils/k8s-errors'
import {
  describeFileDrag,
  needsReplaceConfirmation,
  readYamlFile,
  YAML_FILE_ACCEPT,
  type FileDragState,
} from '../../utils/yaml-file-import'

// A frame this close to the last one means the main thread is keeping up; a
// blocked editor produces gaps far larger.
const responsiveFrameMs = 100

export interface ApplyResult {
  name: string
  namespace: string
  kind: string
  apiVersion?: string
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
  const [drag, setDrag] = useState<FileDragState>('none')
  const [pendingImport, setPendingImport] = useState<{ fileName: string; yaml: string } | null>(null)
  const [importing, setImporting] = useState<string | null>(null)
  // An import is several awaits and several frames long. Anything that changes
  // what the result should land on — a newer import, a close, a reopen — bumps
  // the generation, and every deferred step checks it before touching state.
  const importGeneration = useRef(0)
  const scheduledFrames = useRef(new Set<number>())
  // The buffer as it stands now, not as it stood when a read began.
  const yamlRef = useRef(yaml)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // dragenter/dragleave also fire for the editor's own descendants, so the
  // overlay tracks depth rather than the first dragleave it sees.
  const dragDepth = useRef(0)

  useEffect(() => {
    yamlRef.current = yaml
  }, [yaml])

  const retireDeferredImports = useCallback(() => {
    importGeneration.current += 1
    for (const id of scheduledFrames.current) cancelAnimationFrame(id)
    scheduledFrames.current.clear()
  }, [])

  useEffect(() => retireDeferredImports, [retireDeferredImports])

  const scheduleFrame = useCallback((generation: number, run: () => void) => {
    const id = requestAnimationFrame(() => {
      scheduledFrames.current.delete(id)
      if (generation !== importGeneration.current) return
      run()
    })
    scheduledFrames.current.add(id)
  }, [])

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
    setDrag('none')
    setPendingImport(null)
    setImporting(null)
    dragDepth.current = 0
    retireDeferredImports()
  }, [open, initialYaml, retireDeferredImports])

  const pending = isApplying || isPreviewing
  const closeNow = useCallback(() => {
    retireDeferredImports()
    setError(null)
    setSuccess(null)
    setPreview(null)
    setPendingImport(null)
    setImporting(null)
    setYaml('')
    onClose()
  }, [onClose, retireDeferredImports])
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

  // Handing a document to the editor parses the whole buffer and blocks the
  // main thread, so the status has to be on screen before that starts. Two
  // frames: the first commits it, the second lets the browser paint it. A
  // spinner would only freeze mid-turn, so this says what is happening instead
  // of pretending to animate.
  const loadIntoEditor = useCallback(
    (fileName: string, content: string, generation: number) => {
      setImporting(fileName)
      scheduleFrame(generation, () => {
        scheduleFrame(generation, () => {
          setYaml(content)
          // Clearing it in this same callback would batch it into the very
          // render that blocks, retiring the status while the editor is still
          // unusable. Wait for a frame to arrive on time instead: that is the
          // editor answering again, which is what the status was promising.
          let previous = performance.now()
          const clearWhenResponsive = () => {
            const now = performance.now()
            if (now - previous < responsiveFrameMs) {
              setImporting(null)
              return
            }
            previous = now
            scheduleFrame(generation, clearWhenResponsive)
          }
          scheduleFrame(generation, clearWhenResponsive)
        })
      })
    },
    [scheduleFrame],
  )

  const loadFiles = useCallback(
    async (files: File[]) => {
      const generation = ++importGeneration.current
      const result = await readYamlFile(files)
      // A newer import, a close or a reopen happened while this one was
      // reading; its result describes an editor that has moved on.
      if (generation !== importGeneration.current) return
      // This import owns the status from here. Taking the generation retired
      // the frames that would have cleared a previous import's overlay, so
      // every path out of this function has to leave it in a sane state — the
      // ones that return early below would otherwise strand it on screen.
      setImporting(null)
      if (!result.ok) {
        setSuccess(null)
        setError(result.message)
        return
      }
      setError(null)
      setSuccess(null)
      if (needsReplaceConfirmation(yamlRef.current, initialYaml)) {
        setPendingImport({ fileName: result.fileName, yaml: result.yaml })
        return
      }
      loadIntoEditor(result.fileName, result.yaml, generation)
    },
    [initialYaml, loadIntoEditor],
  )

  const handleDragEnter = useCallback((event: ReactDragEvent) => {
    const state = describeFileDrag(event.dataTransfer)
    if (state === 'none') return
    event.preventDefault()
    dragDepth.current += 1
    setDrag(state)
  }, [])

  const handleDragOver = useCallback((event: ReactDragEvent) => {
    if (describeFileDrag(event.dataTransfer) === 'none') return
    // Without this the browser navigates away to the dropped file.
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const handleDragLeave = useCallback((event: ReactDragEvent) => {
    if (describeFileDrag(event.dataTransfer) === 'none') return
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDrag('none')
  }, [])

  const handleDrop = useCallback(
    (event: ReactDragEvent) => {
      if (describeFileDrag(event.dataTransfer) === 'none') return
      event.preventDefault()
      dragDepth.current = 0
      setDrag('none')
      // The DataTransfer is neutered once this handler returns, so take the
      // files off it before anything awaits.
      void loadFiles(Array.from(event.dataTransfer.files))
    },
    [loadFiles],
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
            <div className="mb-2 flex items-center gap-2">
              <button
                type="button"
                onClick={() => fileInputRef.current?.click()}
                className="flex items-center gap-1.5 rounded-md border border-theme-border px-2.5 py-1 text-xs font-medium text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
              >
                <Upload className="h-3.5 w-3.5" />
                Import YAML file
              </button>
              <span className="text-xs text-theme-text-tertiary">
                or drop a YAML file into the editor
              </span>
              <input
                ref={fileInputRef}
                type="file"
                accept={YAML_FILE_ACCEPT}
                className="hidden"
                onChange={(event) => {
                  const picked = Array.from(event.target.files ?? [])
                  // Clearing the input empties its FileList, so read it first.
                  event.target.value = ''
                  void loadFiles(picked)
                }}
              />
            </div>

            <div
              className="relative h-[400px]"
              onDragEnter={handleDragEnter}
              onDragOver={handleDragOver}
              onDragLeave={handleDragLeave}
              onDrop={handleDrop}
            >
              <YamlEditor
                value={yaml}
                onChange={setYaml}
                height="400px"
                onValidate={handleValidate}
                schemaLoader={schemaLoader}
              />
              {importing && (
                <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md border border-theme-border bg-theme-base/85">
                  <span className="text-sm font-medium text-theme-text-primary">
                    Loading {importing}…
                  </span>
                </div>
              )}
              {drag !== 'none' && (
                <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md border-2 border-dashed border-accent bg-theme-base/85">
                  <span className="flex items-center gap-2 text-sm font-medium text-theme-text-primary">
                    <Upload className="h-4 w-4" />
                    {drag === 'multiple-files'
                      ? 'Drop a single YAML file'
                      : 'Drop YAML file to load'}
                  </span>
                </div>
              )}
            </div>
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

      <ConfirmDialog
        open={pendingImport !== null}
        onClose={() => setPendingImport(null)}
        onConfirm={() => {
          // Confirming is its own decision about the editor as it stands now,
          // so it loads under a fresh generation rather than the one the read
          // started under, which anything since may have retired.
          if (pendingImport) {
            const generation = ++importGeneration.current
            loadIntoEditor(pendingImport.fileName, pendingImport.yaml, generation)
          }
          setPendingImport(null)
        }}
        variant="warning"
        showWarning={false}
        title="Replace editor contents?"
        message={`Loading ${pendingImport?.fileName ?? 'this file'} discards the YAML currently in the editor.`}
        confirmLabel="Replace"
      />
    </DialogPortal>
  )
}

function ApplyErrorBanner({ error }: { error: string }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)
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
          {...buttonProps}
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="flex items-center gap-1 px-3 pb-2 text-red-500/60 hover:text-red-500/80 dark:text-red-400/60 dark:hover:text-red-400/80"
        >
          <CollapseChevron open={expanded} inheritColor className="h-3 w-3" />
          Details
        </button>
      )}
      <Collapse open={expanded && hasFriendly} id={panelId}>
        <div className="break-all px-3 pb-2 font-mono leading-relaxed text-red-500/60 dark:text-red-400/60">
          {parsed.raw}
        </div>
      </Collapse>
    </div>
  )
}
