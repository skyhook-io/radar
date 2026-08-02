import { useState, useCallback, useEffect, useRef } from 'react'
import {
  Copy,
  CopyPlus,
  Check,
  RefreshCw,
  Pencil,
  Save,
  Eye,
  XCircle,
  AlertTriangle,
} from 'lucide-react'
import { Download } from 'lucide-react'
import { stringify as yamlStringify } from 'yaml'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'
import type { YamlSchemaLoader } from '../ui/YamlEditor'
import { YamlReview, type YamlPreviewResult } from '../ui/YamlReview'
import { Tooltip } from '../ui/Tooltip'
import type { SelectedResource } from '../../types'
import { resourceToYaml } from '../../utils/yaml'
import { triggerDownload } from '../../utils/download'

// ============================================================================
// SUCCESS ANIMATION
// ============================================================================

export function SaveSuccessAnimation() {
  return (
    <div className="absolute top-0 left-0 right-0 z-50 pointer-events-none">
      <div className="flex justify-center animate-fade-in-out">
        <div className="mt-2 px-4 py-2 bg-green-600/90 dark:bg-green-500/90 backdrop-blur-sm rounded-lg shadow-lg flex items-center gap-2">
          <Check className="w-4 h-4 text-white" />
          <span className="text-white text-sm font-medium">Saved</span>
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// EDITABLE YAML VIEW
// ============================================================================

// Get edit warning for resource types with limited editability
function getEditWarning(
  kind: string,
): { message: string; tip: string; learnMoreUrl?: string } | null {
  const k = kind.toLowerCase()
  if (k === 'pods' || k === 'pod') {
    return {
      message: 'Pods have limited editability.',
      tip: 'Green highlighted lines can be changed. Edit the parent Deployment instead for other fields.',
      learnMoreUrl:
        'https://kubernetes.io/docs/concepts/workloads/pods/#pod-update-and-replacement',
    }
  }
  if (k === 'jobs' || k === 'job') {
    return {
      message: 'Jobs cannot be modified after creation.',
      tip: 'Delete and recreate the Job to make changes.',
      learnMoreUrl: 'https://kubernetes.io/docs/concepts/workloads/controllers/job/',
    }
  }
  return null
}

// Parse and simplify Kubernetes error messages
function formatSaveError(error: string): { summary: string; details?: string } {
  if (error.includes('is invalid:')) {
    const parts = error.split('is invalid:')
    const errorPart = parts[1]?.trim() || ''

    if (errorPart.includes('Forbidden:')) {
      const forbiddenAt = errorPart.indexOf(': Forbidden:')
      if (forbiddenAt > 0) {
        const target = errorPart.slice(0, forbiddenAt).trim()
        const messageStart = forbiddenAt + ': Forbidden:'.length
        const dotAt = errorPart.indexOf('.', messageStart)
        const braceAt = errorPart.indexOf('{', messageStart)
        const endCandidates = [dotAt, braceAt].filter((i) => i >= 0)
        const messageEnd = endCandidates.length ? Math.min(...endCandidates) : errorPart.length
        const message = errorPart.slice(messageStart, messageEnd).trim()
        return {
          summary: `Cannot update ${target}: ${message}`,
          details: error.length > 200 ? error : undefined,
        }
      }
    }

    const braceAt = errorPart.indexOf('{')
    const summary = (braceAt >= 0 ? errorPart.slice(0, braceAt) : errorPart).trim()
    if (summary) {
      return {
        summary,
        details: error.length > 200 ? error : undefined,
      }
    }
  }

  if (error.length > 150) {
    return {
      summary: error.substring(0, 150) + '...',
      details: error,
    }
  }

  return { summary: error }
}

// Safe sessionStorage wrappers — storage can throw QuotaExceededError or be
// blocked by browser security policies. Draft persistence is best-effort.
function safeSessionGet(key: string): string | null {
  try {
    return sessionStorage.getItem(key)
  } catch {
    return null
  }
}
function safeSessionSet(key: string, value: string): void {
  try {
    sessionStorage.setItem(key, value)
  } catch {
    /* best-effort */
  }
}
function safeSessionRemove(key: string): void {
  try {
    sessionStorage.removeItem(key)
  } catch {
    /* best-effort */
  }
}

export interface EditableYamlViewProps {
  resource: SelectedResource
  data: any
  onCopy: (text: string) => void
  copied: boolean
  /** Hide edit affordances when the host surface is read-only. */
  readOnly?: boolean
  /** Called after a successful save so the parent can refetch */
  onSaved?: () => void
  /** Save handler — injected by the platform wrapper */
  onSave?: (params: {
    kind: string
    namespace: string
    name: string
    yaml: string
    force: boolean
    reviewedResourceVersion?: string
    reviewedContext?: string
  }) => Promise<void>
  /** Whether a save is in progress */
  isSaving?: boolean
  /** Error message from the last save attempt */
  saveError?: string | null
  onPreview?: (params: {
    yaml: string
    mode: 'update'
    force: boolean
    target: { kind: string; namespace: string; name: string }
  }) => Promise<{
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  }>
  isPreviewing?: boolean
  previewError?: string | null
  schemaLoader?: YamlSchemaLoader
  /** Duplicate handler — opens create dialog with this resource's YAML */
  onDuplicate?: (params: { kind: string; namespace: string; name: string; yaml: string }) => void
  /**
   * Optional override for the download trigger — desktop builds inject a native save dialog here.
   * Falls back to a browser blob download when omitted.
   */
  onDownload?: (content: string, mime: string, filename: string) => void
}

export function EditableYamlView({
  resource,
  data,
  onCopy,
  copied,
  readOnly = false,
  onSaved,
  onSave,
  isSaving,
  saveError,
  onPreview,
  isPreviewing,
  previewError,
  schemaLoader,
  onDuplicate,
  onDownload,
}: EditableYamlViewProps) {
  const draftKey = `radar_yaml_draft:${resource.kind}/${resource.namespace}/${resource.name}`

  // Restore draft from sessionStorage (e.g., after session-expiry redirect).
  // All sessionStorage calls are wrapped in try-catch — storage can throw
  // QuotaExceededError or be blocked by browser security policies.
  const savedDraft = useRef(readOnly ? null : safeSessionGet(draftKey))
  const [isEditing, setIsEditing] = useState(savedDraft.current !== null)
  const [editedYaml, setEditedYaml] = useState(savedDraft.current ?? '')
  const [yamlErrors, setYamlErrors] = useState<string[]>([])
  const [showErrorDetails, setShowErrorDetails] = useState(false)
  // Default on: the editor resubmits the full live manifest, so an unforced
  // save would conflict on every field owned by Helm/Flux/Argo/a controller.
  const [force, setForce] = useState(true)
  const [preview, setPreview] = useState<{
    yaml: string
    force: boolean
    originalYaml: string
    documents: YamlPreviewResult[]
    nonAtomic: boolean
    context?: string
  } | null>(null)

  // Clean up restored draft flag
  useEffect(() => {
    if (typeof savedDraft.current === 'string') {
      safeSessionRemove(draftKey)
      savedDraft.current = null
    }
  }, [draftKey])

  // Autosave draft to sessionStorage while editing (best-effort)
  useEffect(() => {
    if (readOnly) {
      setIsEditing(false)
      setEditedYaml('')
      setYamlErrors([])
      safeSessionRemove(draftKey)
      return
    }
    if (isEditing && editedYaml) {
      safeSessionSet(draftKey, editedYaml)
    } else {
      safeSessionRemove(draftKey)
    }
  }, [isEditing, editedYaml, draftKey, readOnly])

  const handleDownload = useCallback(() => {
    const yaml = resourceToYaml(data)
    if (!yaml) return
    // Prefer the canonical singular Kind from the manifest (e.g. "Pod") over the URL plural ("pods").
    const kindForFile = (data?.kind || resource.kind || 'resource').toLowerCase()
    const slug = `${kindForFile}-${resource.name}`.replace(/[^a-z0-9._-]+/g, '-')
    triggerDownload(yaml, 'application/yaml', `${slug}.yaml`, onDownload)
  }, [data, resource.kind, resource.name, onDownload])

  const handleStartEdit = useCallback(() => {
    if (readOnly) return
    setEditedYaml(resourceToYaml(data))
    setYamlErrors([])
    setPreview(null)
    setIsEditing(true)
  }, [data, readOnly])

  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditedYaml('')
    setYamlErrors([])
    setPreview(null)
  }, [])

  const handleSaveEdit = useCallback(async () => {
    if (yamlErrors.length > 0 || !onSave) return

    try {
      if (onPreview) {
        const result = await onPreview({
          yaml: editedYaml,
          mode: 'update',
          force,
          target: {
            kind: resource.kind,
            namespace: resource.namespace,
            name: resource.name,
          },
        })
        setPreview({
          yaml: editedYaml,
          force,
          originalYaml: resourceToYaml(data),
          documents: result.documents,
          nonAtomic: result.nonAtomic,
          context: result.context,
        })
        return
      }
      await onSave({
        kind: resource.kind,
        namespace: resource.namespace,
        name: resource.name,
        yaml: editedYaml,
        force,
      })
      setIsEditing(false)
      setEditedYaml('')
      setTimeout(() => onSaved?.(), 1000)
    } catch {
      // Error handled by caller via saveError prop
    }
  }, [onSave, onPreview, resource, data, editedYaml, yamlErrors, onSaved, force])

  const handleApplyReviewed = useCallback(async () => {
    if (!preview || !onSave) return
    try {
      await onSave({
        kind: resource.kind,
        namespace: resource.namespace,
        name: resource.name,
        yaml: preview.yaml,
        force: preview.force,
        reviewedResourceVersion: preview.documents[0]?.reviewedResourceVersion,
        reviewedContext: preview.context,
      })
      setPreview(null)
      setIsEditing(false)
      setEditedYaml('')
      setTimeout(() => onSaved?.(), 1000)
    } catch {
      if (onPreview) {
        try {
          const refreshed = await onPreview({
            yaml: preview.yaml,
            mode: 'update',
            force: preview.force,
            target: {
              kind: resource.kind,
              namespace: resource.namespace,
              name: resource.name,
            },
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
  }, [preview, onSave, onPreview, resource, onSaved])

  const handleYamlValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlErrors(errors)
  }, [])

  const yamlContent = yamlStringify(data, { lineWidth: 0, indent: 2 })
  const editWarning = getEditWarning(resource.kind)
  const formattedError = saveError ? formatSaveError(saveError) : null
  const isPending = (isSaving ?? false) || (isPreviewing ?? false)

  if (isEditing) {
    if (preview) {
      return (
        <YamlReview
          submittedYaml={preview.yaml}
          originalYaml={preview.originalYaml}
          documents={preview.documents}
          nonAtomic={preview.nonAtomic}
          force={preview.force}
          isApplying={isSaving}
          applyError={saveError}
          onBack={() => setPreview(null)}
          onApply={handleApplyReviewed}
        />
      )
    }
    return (
      <div className="flex flex-col h-full">
        <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border bg-theme-elevated/50">
          <div className="flex items-center gap-2">
            <Pencil className="w-4 h-4 text-blue-400" />
            <span className="text-sm font-medium text-theme-text-primary">Editing Resource</span>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={handleCancelEdit}
              disabled={isPending}
              className="flex items-center gap-1 px-3 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-surface rounded border border-theme-border disabled:opacity-50"
            >
              <XCircle className="w-3.5 h-3.5" />
              Cancel
            </button>
            <Tooltip
              content={
                <div className="space-y-1.5 max-w-[19rem]">
                  <p>Override field ownership (server-side apply).</p>
                  <p>
                    <span className="font-medium text-theme-text-primary">On (default):</span> your
                    edits overwrite fields owned by Helm/Flux/Argo/kubectl — but an active
                    controller may reconcile them back on its next sync.
                  </p>
                  <p>
                    <span className="font-medium text-theme-text-primary">Off:</span> if another
                    manager owns a field you changed, the whole save is rejected with a conflict
                    (nothing is applied).
                  </p>
                </div>
              }
              position="bottom"
            >
              <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={force}
                  disabled={isPending}
                  onChange={(e) => setForce(e.target.checked)}
                  className="w-3.5 h-3.5 rounded border-theme-border bg-theme-base"
                />
                Force
              </label>
            </Tooltip>
            <button
              onClick={handleSaveEdit}
              disabled={isPending || yamlErrors.length > 0}
              className="flex items-center gap-1 px-3 py-1.5 text-xs btn-brand rounded disabled:cursor-not-allowed"
            >
              {isPending ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin" />
              ) : onPreview ? (
                <Eye className="w-3.5 h-3.5" />
              ) : (
                <Save className="w-3.5 h-3.5" />
              )}
              {isPreviewing
                ? 'Preparing review…'
                : isSaving
                  ? 'Saving…'
                  : onPreview
                    ? 'Review changes'
                    : 'Save'}
            </button>
          </div>
        </div>

        {editWarning && (
          <div className="px-4 py-2.5 bg-amber-500/10 dark:bg-yellow-500/10 border-b border-amber-300 dark:border-yellow-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-amber-600 dark:text-yellow-300 mt-0.5 shrink-0" />
              <div className="text-xs">
                <span className="font-medium text-amber-700 dark:text-yellow-300">
                  {editWarning.message}
                </span>
                <span className="text-amber-600 dark:text-yellow-300/80 ml-1">
                  {editWarning.tip}
                </span>
                {editWarning.learnMoreUrl && (
                  <a
                    href={editWarning.learnMoreUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="ml-1.5 text-blue-600 dark:text-blue-300 hover:underline"
                  >
                    Learn more →
                  </a>
                )}
              </div>
            </div>
          </div>
        )}

        {yamlErrors.length > 0 && (
          <div className="px-4 py-2 bg-red-500/10 border-b border-red-300 dark:border-red-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-300 mt-0.5 shrink-0" />
              <div className="text-xs text-red-600 dark:text-red-300">
                {yamlErrors.map((err, i) => (
                  <div key={i}>{err}</div>
                ))}
              </div>
            </div>
          </div>
        )}

        {(formattedError || previewError) && (
          <div className="px-4 py-2 bg-red-500/10 border-b border-red-300 dark:border-red-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-300 mt-0.5 shrink-0" />
              <div className="text-xs text-red-600 dark:text-red-300 flex-1">
                <div className="font-medium">{previewError ? 'Preview failed' : 'Save failed'}</div>
                <div className="mt-1">{previewError || formattedError?.summary}</div>
                {formattedError?.details && (
                  <button
                    onClick={() => setShowErrorDetails(!showErrorDetails)}
                    className="mt-1 text-red-500 dark:text-red-300 hover:text-red-700 dark:hover:text-red-200 underline"
                  >
                    {showErrorDetails ? 'Hide details' : 'Show details'}
                  </button>
                )}
                {showErrorDetails && formattedError?.details && (
                  <pre className="mt-2 p-2 bg-red-500/10 rounded text-[10px] whitespace-pre-wrap break-all max-h-40 overflow-auto">
                    {formattedError.details}
                  </pre>
                )}
              </div>
            </div>
          </div>
        )}

        <div className="flex-1 min-h-0">
          <YamlEditor
            value={editedYaml}
            onChange={setEditedYaml}
            onValidate={handleYamlValidate}
            schemaLoader={schemaLoader}
            height="100%"
            kind={resource.kind}
          />
        </div>
      </div>
    )
  }

  // Read-only mode
  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm font-medium text-theme-text-secondary">YAML</span>
        <div className="flex items-center gap-2">
          {!readOnly && (
            <button
              onClick={handleStartEdit}
              className="flex items-center gap-1 px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-theme-elevated rounded"
            >
              <Pencil className="w-3.5 h-3.5" />
              Edit
            </button>
          )}
          <button
            onClick={() => onCopy(yamlContent)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            {copied ? (
              <Check className="w-3.5 h-3.5 text-green-400" />
            ) : (
              <Copy className="w-3.5 h-3.5" />
            )}
            Copy
          </button>
          <Tooltip content="Download manifest as YAML (server-generated fields stripped)">
            <button
              onClick={handleDownload}
              className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
            >
              <Download className="w-3.5 h-3.5" />
              Download
            </button>
          </Tooltip>
          {onDuplicate && (
            <Tooltip content="Duplicate as new resource">
              <button
                onClick={() =>
                  onDuplicate({
                    kind: resource.kind,
                    namespace: resource.namespace,
                    name: resource.name,
                    yaml: yamlContent,
                  })
                }
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                <CopyPlus className="w-3.5 h-3.5" />
                Duplicate
              </button>
            </Tooltip>
          )}
        </div>
      </div>
      <CodeViewer
        code={yamlContent}
        language="yaml"
        showLineNumbers
        maxHeight="calc(100vh - 250px)"
      />
    </div>
  )
}
