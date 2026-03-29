import { useState, useCallback } from 'react'
import {
  Copy,
  Check,
  RefreshCw,
  Pencil,
  Save,
  XCircle,
  AlertTriangle,
} from 'lucide-react'
import { stringify as yamlStringify } from 'yaml'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'
import type { SelectedResource } from '../../types'

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
function getEditWarning(kind: string): { message: string; tip: string; learnMoreUrl?: string } | null {
  const k = kind.toLowerCase()
  if (k === 'pods' || k === 'pod') {
    return {
      message: 'Pods have limited editability.',
      tip: 'Green highlighted lines can be changed. Edit the parent Deployment instead for other fields.',
      learnMoreUrl: 'https://kubernetes.io/docs/concepts/workloads/pods/#pod-update-and-replacement'
    }
  }
  if (k === 'jobs' || k === 'job') {
    return {
      message: 'Jobs cannot be modified after creation.',
      tip: 'Delete and recreate the Job to make changes.',
      learnMoreUrl: 'https://kubernetes.io/docs/concepts/workloads/controllers/job/'
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
      const forbiddenMatch = errorPart.match(/([^:]+):\s*Forbidden:\s*([^.{]+)/)
      if (forbiddenMatch) {
        return {
          summary: `Cannot update ${forbiddenMatch[1]}: ${forbiddenMatch[2].trim()}`,
          details: error.length > 200 ? error : undefined
        }
      }
    }

    const summaryMatch = errorPart.match(/^([^{]+)/)
    if (summaryMatch) {
      return {
        summary: summaryMatch[1].trim(),
        details: error.length > 200 ? error : undefined
      }
    }
  }

  if (error.length > 150) {
    return {
      summary: error.substring(0, 150) + '...',
      details: error
    }
  }

  return { summary: error }
}

interface EditableYamlViewProps {
  resource: SelectedResource
  data: any
  onCopy: (text: string) => void
  copied: boolean
  /** Called after a successful save so the parent can refetch */
  onSaved?: () => void
  /** Save handler — injected by the platform wrapper */
  onSave?: (params: { kind: string; namespace: string; name: string; yaml: string }) => Promise<void>
  /** Whether a save is in progress */
  isSaving?: boolean
  /** Error message from the last save attempt */
  saveError?: string | null
}

export function EditableYamlView({ resource, data, onCopy, copied, onSaved, onSave, isSaving, saveError }: EditableYamlViewProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editedYaml, setEditedYaml] = useState('')
  const [yamlErrors, setYamlErrors] = useState<string[]>([])
  const [showErrorDetails, setShowErrorDetails] = useState(false)

  // Convert resource to YAML for editing
  const convertToYaml = useCallback((d: any) => {
    if (!d) return ''
    const cleaned = { ...d }
    delete cleaned.status
    if (cleaned.metadata) {
      delete cleaned.metadata.managedFields
      delete cleaned.metadata.resourceVersion
      delete cleaned.metadata.uid
      delete cleaned.metadata.creationTimestamp
      delete cleaned.metadata.generation
    }
    return yamlStringify(cleaned, { lineWidth: 0, indent: 2 })
  }, [])

  const handleStartEdit = useCallback(() => {
    setEditedYaml(convertToYaml(data))
    setYamlErrors([])
    setIsEditing(true)
  }, [data, convertToYaml])

  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditedYaml('')
    setYamlErrors([])
  }, [])

  const handleSaveEdit = useCallback(async () => {
    if (yamlErrors.length > 0 || !onSave) return

    try {
      await onSave({
        kind: resource.kind,
        namespace: resource.namespace,
        name: resource.name,
        yaml: editedYaml,
      })
      setIsEditing(false)
      setEditedYaml('')
      setTimeout(() => onSaved?.(), 1000)
    } catch {
      // Error handled by caller via saveError prop
    }
  }, [onSave, resource, editedYaml, yamlErrors, onSaved])

  const handleYamlValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlErrors(errors)
  }, [])

  const yamlContent = yamlStringify(data, { lineWidth: 0, indent: 2 })
  const editWarning = getEditWarning(resource.kind)
  const formattedError = saveError ? formatSaveError(saveError) : null
  const isPending = isSaving ?? false

  if (isEditing) {
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
            <button
              onClick={handleSaveEdit}
              disabled={isPending || yamlErrors.length > 0}
              className="flex items-center gap-1 px-3 py-1.5 text-xs btn-brand rounded disabled:cursor-not-allowed"
            >
              {isPending ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin" />
              ) : (
                <Save className="w-3.5 h-3.5" />
              )}
              {isPending ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>

        {editWarning && (
          <div className="px-4 py-2.5 bg-amber-500/10 dark:bg-yellow-500/10 border-b border-amber-300 dark:border-yellow-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-amber-600 dark:text-yellow-300 mt-0.5 shrink-0" />
              <div className="text-xs">
                <span className="font-medium text-amber-700 dark:text-yellow-300">{editWarning.message}</span>
                <span className="text-amber-600 dark:text-yellow-300/80 ml-1">{editWarning.tip}</span>
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

        {formattedError && (
          <div className="px-4 py-2 bg-red-500/10 border-b border-red-300 dark:border-red-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-300 mt-0.5 shrink-0" />
              <div className="text-xs text-red-600 dark:text-red-300 flex-1">
                <div className="font-medium">Save failed</div>
                <div className="mt-1">{formattedError.summary}</div>
                {formattedError.details && (
                  <button
                    onClick={() => setShowErrorDetails(!showErrorDetails)}
                    className="mt-1 text-red-500 dark:text-red-300 hover:text-red-700 dark:hover:text-red-200 underline"
                  >
                    {showErrorDetails ? 'Hide details' : 'Show details'}
                  </button>
                )}
                {showErrorDetails && formattedError.details && (
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
          <button
            onClick={handleStartEdit}
            className="flex items-center gap-1 px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-theme-elevated rounded"
          >
            <Pencil className="w-3.5 h-3.5" />
            Edit
          </button>
          <button
            onClick={() => onCopy(yamlContent)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            Copy
          </button>
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
