import { useState, useCallback, useEffect } from 'react'
import { X, Loader2, Check, AlertTriangle, ChevronDown, ChevronRight } from 'lucide-react'
import { DialogPortal } from '../ui/DialogPortal'
import { YamlEditor } from '../ui/YamlEditor'
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
  // Injected by platform (decouples from data-fetching hooks)
  onApply: (params: { yaml: string; mode: 'apply' | 'create'; dryRun: boolean; force: boolean }) => Promise<ApplyResult[]>
  isApplying: boolean
  /** Called after a successful non-dry-run apply with the first created resource */
  onCreated?: (result: ApplyResult) => void
}

export function CreateResourceDialog({ open, onClose, initialYaml = '', title, onApply, isApplying, onCreated }: CreateResourceDialogProps) {
  const [yaml, setYaml] = useState(initialYaml)
  const [mode, setMode] = useState<'apply' | 'create'>('apply')
  const [dryRun, setDryRun] = useState(false)
  const [force, setForce] = useState(false)
  const [yamlValid, setYamlValid] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)

  // Reset state when dialog opens or initialYaml changes
  useEffect(() => {
    if (open) {
      setYaml(initialYaml)
      setMode('apply')
      setDryRun(false)
      setForce(false)
      setError(null)
      setSuccess(null)
    }
  }, [open, initialYaml])

  const handleClose = useCallback(() => {
    if (isApplying) return
    setError(null)
    setSuccess(null)
    setYaml('')
    onClose()
  }, [onClose, isApplying])

  const handleValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlValid(errors.length === 0)
  }, [])

  const handleSubmit = useCallback(async () => {
    if (!yaml.trim()) {
      setError('YAML content is required')
      return
    }
    setError(null)
    setSuccess(null)

    try {
      const results = await onApply({ yaml, mode, dryRun, force: mode === 'apply' && force })
      const action = mode === 'create' ? 'Created' : 'Applied'
      const dryRunLabel = dryRun ? ' (dry run)' : ''

      if (results.length === 1) {
        const r = results[0]
        setSuccess(`${action} ${r.kind} ${r.namespace ? r.namespace + '/' : ''}${r.name}${dryRunLabel}`)
      } else {
        setSuccess(`${action} ${results.length} resources${dryRunLabel}`)
      }

      if (!dryRun) {
        if (onCreated && results.length > 0) {
          // Close immediately and navigate to the created resource
          handleClose()
          onCreated(results[0])
        } else {
          setTimeout(handleClose, 1200)
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    }
  }, [yaml, mode, dryRun, force, onApply, onCreated, handleClose])

  const dialogTitle = title || 'Create Resource'
  const submitLabel = mode === 'create' ? 'Create' : 'Apply'

  return (
    <DialogPortal open={open} onClose={handleClose} closable={!isApplying} className="w-[700px] max-h-[85vh] flex flex-col">
      {/* Header */}
      <div className="flex items-center justify-between px-5 py-3.5 border-b border-theme-border shrink-0">
        <h2 className="text-sm font-semibold text-theme-text-primary">{dialogTitle}</h2>
        <Tooltip content="Close">
          <button
            onClick={handleClose}
            className="p-1 rounded hover:bg-theme-hover text-theme-text-secondary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </Tooltip>
      </div>

      {/* Editor */}
      <div className="flex-1 min-h-0 px-5 py-3">
        <YamlEditor
          value={yaml}
          onChange={setYaml}
          height="400px"
          onValidate={handleValidate}
        />
      </div>

      {/* Status messages — single location for feedback (no toast) */}
      {error && <ApplyErrorBanner error={error} />}
      {success && (
        <div className="mx-5 mb-2 px-3 py-2 rounded-md bg-emerald-500/10 border border-emerald-500/30 text-emerald-400 text-xs flex items-center gap-2">
          <Check className="w-3.5 h-3.5 shrink-0" />
          <span>{success}</span>
        </div>
      )}

      {/* Footer */}
      <div className="flex items-center justify-between px-5 py-3 border-t border-theme-border shrink-0">
        <div className="flex items-center gap-3">
          {/* Mode toggle — pill segmented control */}
          <Tooltip content="Apply: create or update (idempotent). Create: fail if exists." position="bottom">
            <div className="flex items-center rounded-md bg-theme-base border border-theme-border p-0.5" role="radiogroup" aria-label="Apply mode">
              <button
                onClick={() => setMode('apply')}
                role="radio"
                aria-checked={mode === 'apply'}
                className={`px-2.5 py-1 rounded text-xs font-medium transition-colors ${
                  mode === 'apply'
                    ? 'bg-theme-elevated text-theme-text-primary shadow-sm'
                    : 'text-theme-text-tertiary hover:text-theme-text-secondary'
                }`}
              >
                Apply
              </button>
              <button
                onClick={() => setMode('create')}
                role="radio"
                aria-checked={mode === 'create'}
                className={`px-2.5 py-1 rounded text-xs font-medium transition-colors ${
                  mode === 'create'
                    ? 'bg-theme-elevated text-theme-text-primary shadow-sm'
                    : 'text-theme-text-tertiary hover:text-theme-text-secondary'
                }`}
              >
                Create
              </button>
            </div>
          </Tooltip>

          {/* Dry run checkbox */}
          <Tooltip content="Validate against the cluster without persisting changes" position="bottom">
            <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={dryRun}
              onChange={(e) => setDryRun(e.target.checked)}
              className="w-3.5 h-3.5 rounded border-theme-border bg-theme-base"
            />
            Dry run
          </label>
          </Tooltip>

          {/* Force checkbox — only meaningful for server-side apply */}
          <Tooltip
            content={
              <div className="space-y-1.5 max-w-[19rem]">
                <p>Override field ownership (server-side apply).</p>
                <p>
                  <span className="font-medium text-theme-text-primary">Off (default):</span> if a field is owned by Helm/Flux/Argo/kubectl, the whole apply is rejected with a conflict instead of overwriting.
                </p>
                <p>
                  <span className="font-medium text-theme-text-primary">On:</span> your manifest overwrites those fields — but an active controller may reconcile them back on its next sync.
                </p>
              </div>
            }
            position="bottom"
          >
            <label className={`flex items-center gap-1.5 text-xs cursor-pointer ${mode === 'apply' ? 'text-theme-text-secondary' : 'text-theme-text-tertiary cursor-not-allowed'}`}>
            <input
              type="checkbox"
              checked={mode === 'apply' && force}
              disabled={mode !== 'apply'}
              onChange={(e) => setForce(e.target.checked)}
              className="w-3.5 h-3.5 rounded border-theme-border bg-theme-base"
            />
            Force
          </label>
          </Tooltip>
        </div>

        <div className="flex items-center gap-2">
          <button
            onClick={handleClose}
            className="px-3 py-1.5 text-xs rounded-lg hover:bg-theme-hover text-theme-text-secondary transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={isApplying || !yaml.trim() || !yamlValid}
            className="px-4 py-1.5 text-xs rounded-lg btn-brand font-medium disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {isApplying ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                {submitLabel === 'Apply' ? 'Applying...' : 'Creating...'}
              </>
            ) : (
              submitLabel
            )}
          </button>
        </div>
      </div>
    </DialogPortal>
  )
}

function ApplyErrorBanner({ error }: { error: string }) {
  const [expanded, setExpanded] = useState(false)
  const parsed = formatApplyError(error)
  const hasFriendly = !!parsed.suggestion

  return (
    <div className="mx-5 mb-2 rounded-md bg-red-500/10 border border-red-500/30 text-xs">
      <div className="px-3 py-2 flex items-start gap-2 text-red-400">
        <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <span className="font-medium">{parsed.summary}</span>
          {parsed.suggestion && (
            <p className="mt-1 text-red-400/80">{parsed.suggestion}</p>
          )}
        </div>
      </div>
      {hasFriendly && (
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-1 px-3 pb-2 text-red-400/60 hover:text-red-400/80 transition-colors"
        >
          {expanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
          <span>Details</span>
        </button>
      )}
      {(expanded || !hasFriendly) && hasFriendly && (
        <div className="px-3 pb-2 text-red-400/60 break-all font-mono leading-relaxed">
          {parsed.raw}
        </div>
      )}
    </div>
  )
}
