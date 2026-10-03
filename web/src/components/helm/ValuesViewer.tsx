import { useState, useCallback, useEffect } from 'react'
import { Copy, Check, Settings, Pencil, X, Eye, Play, Loader2 } from 'lucide-react'
import { PaneLoader } from '@skyhook-io/k8s-ui'
import { clsx } from 'clsx'
import yaml from 'yaml'
import type { HelmValues, ValuesPreviewResponse } from '../../types'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'
import { useHelmPreviewValues, useHelmApplyValues } from '../../api/client'
import { useCanHelmAct } from '../../api/client'
import { ValuesDiffPreview } from './ValuesDiffPreview'
import { Tooltip } from '../ui/Tooltip'

interface ValuesViewerProps {
  values?: HelmValues
  isLoading: boolean
  error?: unknown
  showEffectiveValues: boolean
  onToggleEffectiveValues: (show: boolean) => void
  onCopy: (text: string) => void
  copied: boolean
  // Required for editing
  namespace?: string
  name?: string
  revision?: number
  currentRevision?: number
  onApplySuccess?: () => void
}
export function ValuesViewer({
  values,
  isLoading,
  error,
  showEffectiveValues,
  onToggleEffectiveValues,
  onCopy,
  copied,
  namespace,
  name,
  revision,
  currentRevision,
  onApplySuccess,
}: ValuesViewerProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editedYaml, setEditedYaml] = useState('')
  const [yamlError, setYamlError] = useState<string | null>(null)
  const [previewData, setPreviewData] = useState<ValuesPreviewResponse | null>(null)
  const [showPreview, setShowPreview] = useState(false)

  const previewMutation = useHelmPreviewValues()
  const applyMutation = useHelmApplyValues()
  const { allowed: canHelmWrite, reason: helmActReason } = useCanHelmAct()
  const isHistoricalRevision = typeof revision === 'number' && typeof currentRevision === 'number' && revision !== currentRevision

  const overridesLoaded = values?.userSuppliedLoaded === true
  const canOfferEdit = Boolean(namespace && name) && canHelmWrite && !isHistoricalRevision
  const canEdit = canOfferEdit && overridesLoaded

  const displayValues = showEffectiveValues ? values?.computed : values?.userSupplied
  const isEmpty = !displayValues || Object.keys(displayValues).length === 0

  // Start editing mode
  const handleStartEdit = useCallback(() => {
    if (!overridesLoaded) return
    // Allow editing even with no user-supplied values (start with empty YAML)
    const yamlStr = values?.userSupplied ? jsonToYaml(values.userSupplied) : ''
    setEditedYaml(yamlStr)
    setYamlError(null)
    setIsEditing(true)
    // Only user overrides are editable.
    if (showEffectiveValues) {
      onToggleEffectiveValues(false)
    }
  }, [values, overridesLoaded, showEffectiveValues, onToggleEffectiveValues])

  // Cancel editing
  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditedYaml('')
    setYamlError(null)
    setPreviewData(null)
    setShowPreview(false)
  }, [])

  useEffect(() => {
    if (isHistoricalRevision && isEditing) {
      handleCancelEdit()
    }
  }, [isHistoricalRevision, isEditing, handleCancelEdit])

  // Parse YAML and validate
  const parseYaml = useCallback((yamlStr: string): Record<string, unknown> | null => {
    try {
      const parsed = yaml.parse(yamlStr)
      setYamlError(null)
      return parsed || {}
    } catch (err) {
      setYamlError(err instanceof Error ? err.message : 'Invalid YAML')
      return null
    }
  }, [])

  // Preview changes
  const handlePreview = useCallback(async () => {
    if (!namespace || !name || isHistoricalRevision || !overridesLoaded) return
    const parsed = parseYaml(editedYaml)
    if (!parsed) return

    try {
      const result = await previewMutation.mutateAsync({
        namespace,
        name,
        values: parsed,
      })
      setPreviewData(result)
      setShowPreview(true)
    } catch {
      // Error is handled by mutation
    }
  }, [namespace, name, isHistoricalRevision, overridesLoaded, editedYaml, parseYaml, previewMutation])

  // Apply changes
  const handleApply = useCallback(async () => {
    if (!namespace || !name || isHistoricalRevision || !overridesLoaded) return
    const parsed = parseYaml(editedYaml)
    if (!parsed) return

    try {
      await applyMutation.mutateAsync({
        namespace,
        name,
        values: parsed,
      })
      handleCancelEdit()
      onApplySuccess?.()
    } catch {
      // Error is handled by mutation
    }
  }, [namespace, name, isHistoricalRevision, overridesLoaded, editedYaml, parseYaml, applyMutation, handleCancelEdit, onApplySuccess])

  // Apply from preview modal
  const handleApplyFromPreview = useCallback(async () => {
    if (!previewData || !namespace || !name || isHistoricalRevision || !overridesLoaded) return
    try {
      await applyMutation.mutateAsync({
        namespace,
        name,
        values: previewData.newValues,
      })
      setShowPreview(false)
      handleCancelEdit()
      onApplySuccess?.()
    } catch {
      // Error is handled by mutation
    }
  }, [previewData, namespace, name, isHistoricalRevision, overridesLoaded, applyMutation, handleCancelEdit, onApplySuccess])

  if (isLoading && !isEditing) {
    return <PaneLoader label="Loading values…" className="h-32" />
  }

  if (error) {
    return (
      <div role="alert" className="m-4 rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        Unable to load Helm values. Editing and applying user overrides are disabled.
      </div>
    )
  }

  if (isEmpty && !isEditing) {
    return (
      <div className="p-4">
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-theme-text-secondary">
              {showEffectiveValues ? 'Effective Values' : 'User Overrides'}
            </span>
            {isHistoricalRevision && (
              <span className="badge-sm bg-theme-hover/50 text-theme-text-secondary">revision {revision}</span>
            )}
          </div>
          <div className="flex items-center gap-2">
            <ValuesViewToggle showEffective={showEffectiveValues} onToggle={onToggleEffectiveValues} disabled={isEditing} />
            {canOfferEdit && (
              <button
                onClick={handleStartEdit}
                disabled={!canEdit}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Pencil className="w-3.5 h-3.5" />
                Edit Overrides
              </button>
            )}
          </div>
        </div>
        {isHistoricalRevision && (
          <div className="mb-3 rounded border border-theme-border bg-theme-elevated/40 px-3 py-2 text-xs text-theme-text-secondary">
            Viewing historical values. Switch back to the latest revision before editing or applying changes.
          </div>
        )}
        {!overridesLoaded && (
          <div role="alert" className="mb-3 rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
            User overrides could not be loaded. Editing and applying changes are disabled to preserve stored overrides.
          </div>
        )}
        <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
          <Settings className="w-8 h-8 text-theme-text-disabled" />
          <span>{showEffectiveValues ? 'No effective values' : overridesLoaded ? 'No user overrides' : 'User overrides unavailable'}</span>
        </div>
      </div>
    )
  }

  const yamlContent = isEditing ? editedYaml : jsonToYaml(displayValues || {})

  return (
    <div className="p-4 flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-theme-text-secondary">
            {isEditing ? 'Editing User Overrides' : showEffectiveValues ? 'Effective Values' : 'User Overrides'}
          </span>
          {isHistoricalRevision && !isEditing && (
            <span className="badge-sm bg-theme-hover/50 text-theme-text-secondary">revision {revision}</span>
          )}
          {isEditing && (
            <span className="badge-sm bg-amber-500/20 text-amber-400 border-amber-500/30">
              unsaved
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {!isEditing && (
            <>
              <ValuesViewToggle showEffective={showEffectiveValues} onToggle={onToggleEffectiveValues} disabled={isEditing} />
              <button
                onClick={() => onCopy(yamlContent)}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
                Copy
              </button>
              {canOfferEdit && (
                <button
                  onClick={handleStartEdit}
                  disabled={!canEdit}
                  className="flex items-center gap-1 px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-blue-500/10 rounded border border-blue-500/30 disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  <Pencil className="w-3.5 h-3.5" />
                  Edit Overrides
                </button>
              )}
            </>
          )}
          {isEditing && (
            <>
              <button
                onClick={handleCancelEdit}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                <X className="w-3.5 h-3.5" />
                Cancel
              </button>
              <button
                onClick={handlePreview}
                disabled={!!yamlError || previewMutation.isPending || isHistoricalRevision || !overridesLoaded || isLoading}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded border border-theme-border disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {previewMutation.isPending ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Eye className="w-3.5 h-3.5" />
                )}
                Preview
              </button>
              <Tooltip content={!canHelmWrite ? helmActReason : ''}>
              <button
                onClick={handleApply}
                disabled={!!yamlError || applyMutation.isPending || !canHelmWrite || isHistoricalRevision || !overridesLoaded || isLoading}
                className="flex items-center gap-1 px-2.5 py-1 text-xs btn-brand rounded disabled:cursor-not-allowed disabled:pointer-events-none"
              >
                {applyMutation.isPending ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Play className="w-3.5 h-3.5" />
                )}
                Apply
              </button>
              </Tooltip>
            </>
          )}
        </div>
      </div>

      {isHistoricalRevision && (
        <div className="mb-3 rounded border border-theme-border bg-theme-elevated/40 px-3 py-2 text-xs text-theme-text-secondary">
          Viewing historical values. Switch back to the latest revision before editing or applying changes.
        </div>
      )}

      {!overridesLoaded && (
        <div role="alert" className="mb-3 rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          User overrides could not be loaded. Editing and applying changes are disabled to preserve stored overrides.
        </div>
      )}

      {isEditing && isLoading && (
        <div className="mb-3 rounded border border-theme-border bg-theme-elevated/40 px-3 py-2 text-xs text-theme-text-secondary">
          Refreshing values. Your override edit session is preserved.
        </div>
      )}

      {/* Error message */}
      {yamlError && (
        <div className="mb-3 px-3 py-2 text-xs text-red-400 bg-red-500/10 border border-red-500/30 rounded">
          {yamlError}
        </div>
      )}

      {/* Mutation error */}
      {(previewMutation.error || applyMutation.error) && (
        <div className="mb-3 px-3 py-2 text-xs text-red-400 bg-red-500/10 border border-red-500/30 rounded">
          {previewMutation.error?.message || applyMutation.error?.message}
        </div>
      )}

      {/* Editor / Viewer */}
      <div className="flex-1 min-h-0">
        {isEditing ? (
          <YamlEditor
            value={editedYaml}
            onChange={setEditedYaml}
            showProblems={false}
            height="calc(100vh - 400px)"
            onValidate={(isValid, errors) => {
              setYamlError(isValid ? null : errors[0] || 'Invalid YAML')
            }}
          />
        ) : (
          <CodeViewer
            code={yamlContent}
            language="yaml"
            showLineNumbers
            maxHeight="calc(100vh - 300px)"
          />
        )}
      </div>

      {/* Preview Modal */}
      {showPreview && previewData && (
        <ValuesDiffPreview
          previewData={previewData}
          onClose={() => setShowPreview(false)}
          onApply={handleApplyFromPreview}
          isApplying={applyMutation.isPending}
        />
      )}
    </div>
  )
}

function ValuesViewToggle({
  showEffective,
  onToggle,
  disabled,
}: {
  showEffective: boolean
  onToggle: (show: boolean) => void
  disabled?: boolean
}) {
  return (
    <div className={clsx('flex items-center bg-theme-elevated/50 rounded-md p-0.5 text-xs', disabled && 'opacity-50 pointer-events-none')}>
      <button
        onClick={() => onToggle(true)}
        disabled={disabled}
        className={clsx(
          'px-2 py-1 rounded transition-colors',
          showEffective ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
        )}
      >
        Effective Values
      </button>
      <button
        onClick={() => onToggle(false)}
        disabled={disabled}
        className={clsx(
          'px-2 py-1 rounded transition-colors',
          !showEffective ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
        )}
      >
        User Overrides
      </button>
    </div>
  )
}

function jsonToYaml(obj: Record<string, unknown>): string {
  if (!obj || Object.keys(obj).length === 0) return ''
  return yaml.stringify(obj, { lineWidth: 0 })
}
