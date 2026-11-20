import { useState, useCallback, useEffect, useMemo } from 'react'
import { Copy, Check, Settings, Pencil, X, Eye, Play, Loader2 } from 'lucide-react'
import { PaneLoader, usePhoneLayout } from '@skyhook-io/k8s-ui'
import { clsx } from 'clsx'
import yaml from 'yaml'
import type { HelmValues, ValuesPreviewResponse } from '../../types'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'
import { useHelmPreviewValues, useHelmApplyValues, useHelmValues } from '../../api/client'
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
  onRetry?: () => void
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
  onRetry,
}: ValuesViewerProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editedYaml, setEditedYaml] = useState('')
  const [yamlError, setYamlError] = useState<string | null>(null)
  const [previewData, setPreviewData] = useState<ValuesPreviewResponse | null>(null)
  const [showPreview, setShowPreview] = useState(false)

  const previewMutation = useHelmPreviewValues()
  const applyMutation = useHelmApplyValues()
  const { allowed: canHelmWrite, reason: helmActReason } = useCanHelmAct(namespace)
  const isHistoricalRevision = typeof revision === 'number' && typeof currentRevision === 'number' && revision !== currentRevision

  // Edits replace every stored override on apply, so they start from, and wait for, a fresh
  // overrides-only read. An older Radar's all=true response can carry empty overrides after a
  // failed second read, and a cached copy can predate the last apply.
  const overridesQuery = useHelmValues(namespace ?? '', name ?? '', false, Boolean(namespace && name), revision)
  const overridesData = overridesQuery.data
  const overridesBaseline = useMemo(() => (overridesData ? (overridesData.userSupplied ?? {}) : undefined), [overridesData])
  const overridesReady = overridesBaseline !== undefined && !overridesQuery.error && !overridesQuery.isFetching
  // Phones read values but don't edit them: authoring YAML by touch is error-prone.
  const isPhone = usePhoneLayout()
  const canOfferEdit = Boolean(namespace && name) && canHelmWrite && !isHistoricalRevision && !isPhone
  const canEdit = canOfferEdit && overridesReady

  const displayValues = showEffectiveValues ? values?.computed : overridesBaseline
  const isEmpty = !displayValues || Object.keys(displayValues).length === 0

  // Start editing mode
  const handleStartEdit = useCallback(() => {
    if (!overridesReady) return
    // Allow editing even with no user-supplied values (start with empty YAML)
    const yamlStr = overridesBaseline && Object.keys(overridesBaseline).length > 0 ? jsonToYaml(overridesBaseline) : ''
    setEditedYaml(yamlStr)
    setYamlError(null)
    setIsEditing(true)
    // Only user overrides are editable.
    if (showEffectiveValues) {
      onToggleEffectiveValues(false)
    }
  }, [overridesBaseline, overridesReady, showEffectiveValues, onToggleEffectiveValues])

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
    if (!namespace || !name || isHistoricalRevision || !overridesReady) return
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
  }, [namespace, name, isHistoricalRevision, overridesReady, editedYaml, parseYaml, previewMutation])

  // Apply changes
  const handleApply = useCallback(async () => {
    if (!namespace || !name || isHistoricalRevision || !overridesReady) return
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
  }, [namespace, name, isHistoricalRevision, overridesReady, editedYaml, parseYaml, applyMutation, handleCancelEdit, onApplySuccess])

  // Apply from preview modal
  const handleApplyFromPreview = useCallback(async () => {
    if (!previewData || !namespace || !name || isHistoricalRevision || !overridesReady) return
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
  }, [previewData, namespace, name, isHistoricalRevision, overridesReady, applyMutation, handleCancelEdit, onApplySuccess])

  if (!isEditing && (showEffectiveValues ? isLoading : overridesQuery.isLoading)) {
    return <PaneLoader label="Loading values…" className="h-32" />
  }

  const readProblems = valuesReadProblems({
    showEffectiveValues,
    effectiveLoaded: values !== undefined,
    effectiveFailed: Boolean(error),
    overridesLoaded: overridesBaseline !== undefined,
    overridesFailed: Boolean(overridesQuery.error),
    editingAffected: canOfferEdit,
  })
  const viewLoadFailed = showEffectiveValues ? Boolean(error) && !values : Boolean(overridesQuery.error) && !overridesBaseline
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
              {!isEmpty && (
                <button
                  onClick={() => onCopy(yamlContent)}
                  className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                >
                  {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
                  Copy
                </button>
              )}
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
                disabled={!!yamlError || previewMutation.isPending || isHistoricalRevision || !overridesReady}
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
                disabled={!!yamlError || applyMutation.isPending || !canHelmWrite || isHistoricalRevision || !overridesReady}
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

      {!isEditing && readProblems.map((problem) => (
        <div key={problem.read} role="alert" className="mb-3 rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {problem.message}{' '}
          {(problem.read === 'overrides' || onRetry) && (
            <button
              type="button"
              onClick={() => (problem.read === 'overrides' ? void overridesQuery.refetch() : onRetry?.())}
              className="underline hover:text-red-300"
            >
              Try again
            </button>
          )}
        </div>
      ))}

      {isEditing && !overridesReady && (
        <div
          role={overridesQuery.error ? 'alert' : undefined}
          className={clsx(
            'mb-3 rounded border px-3 py-2 text-xs',
            overridesQuery.error
              ? 'border-red-500/30 bg-red-500/10 text-red-400'
              : 'border-theme-border bg-theme-elevated/40 text-theme-text-secondary',
          )}
        >
          {overridesQuery.error ? (
            <>
              Couldn't reload user overrides, so Apply is off. Your edits are kept.{' '}
              <button type="button" onClick={() => void overridesQuery.refetch()} className="underline hover:text-red-300">
                Try again
              </button>
            </>
          ) : (
            'Reloading user overrides. Your edits are kept.'
          )}
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
        {!isEditing && viewLoadFailed ? null : !isEditing && isEmpty ? (
          <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
            <Settings className="w-8 h-8 text-theme-text-disabled" />
            <span>{showEffectiveValues ? 'No effective values' : 'No user overrides'}</span>
          </div>
        ) : isEditing ? (
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

export interface ValuesReadProblem {
  read: 'effective' | 'overrides'
  message: string
}

// The two reads fail independently, so each view only reports its own read,
// plus the overrides read wherever it switches editing off. "Refresh" wording
// is only used when the last loaded data for that view is still on screen.
export function valuesReadProblems({
  showEffectiveValues,
  effectiveLoaded,
  effectiveFailed,
  overridesLoaded,
  overridesFailed,
  editingAffected,
}: {
  showEffectiveValues: boolean
  effectiveLoaded: boolean
  effectiveFailed: boolean
  overridesLoaded: boolean
  overridesFailed: boolean
  editingAffected: boolean
}): ValuesReadProblem[] {
  const problems: ValuesReadProblem[] = []
  if (showEffectiveValues && effectiveFailed) {
    problems.push({
      read: 'effective',
      message: effectiveLoaded
        ? "Couldn't refresh effective values. Showing the last ones that loaded."
        : "Couldn't load effective values.",
    })
  }
  if (overridesFailed && !showEffectiveValues) {
    problems.push({
      read: 'overrides',
      message: overridesLoaded
        ? "Couldn't refresh your overrides. Showing the last ones that loaded."
          + (editingAffected ? ' Editing is off until they load again.' : '')
        : "Couldn't load your overrides." + (editingAffected ? ' Editing is off until they load.' : ''),
    })
  } else if (overridesFailed && editingAffected) {
    problems.push({
      read: 'overrides',
      message: overridesLoaded
        ? "Couldn't refresh your overrides, so editing is off."
        : "Couldn't load your overrides, so editing is off.",
    })
  }
  return problems
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
