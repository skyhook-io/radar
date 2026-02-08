import { useState, useCallback } from 'react'
import { Copy, Check, Settings, Pencil, X, Eye, Play, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import yaml from 'yaml'
import type { HelmValues, ValuesPreviewResponse } from '../../types'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'
import { useHelmPreviewValues, useHelmApplyValues } from '../../api/client'
import { useCanHelmWrite } from '../../contexts/CapabilitiesContext'
import { ValuesDiffPreview } from './ValuesDiffPreview'

interface ValuesViewerProps {
  values?: HelmValues
  isLoading: boolean
  showAllValues: boolean
  onToggleAllValues: (show: boolean) => void
  onCopy: (text: string) => void
  copied: boolean
  // Required for editing
  namespace?: string
  name?: string
  onApplySuccess?: () => void
}

export function ValuesViewer({
  values,
  isLoading,
  showAllValues,
  onToggleAllValues,
  onCopy,
  copied,
  namespace,
  name,
  onApplySuccess,
}: ValuesViewerProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editedYaml, setEditedYaml] = useState('')
  const [yamlError, setYamlError] = useState<string | null>(null)
  const [previewData, setPreviewData] = useState<ValuesPreviewResponse | null>(null)
  const [showPreview, setShowPreview] = useState(false)

  const previewMutation = useHelmPreviewValues()
  const applyMutation = useHelmApplyValues()
  const canHelmWrite = useCanHelmWrite()

  const canEdit = Boolean(namespace && name) && canHelmWrite

  const displayValues = showAllValues && values?.computed ? values.computed : values?.userSupplied
  const isEmpty = !displayValues || Object.keys(displayValues).length === 0

  // Start editing mode
  const handleStartEdit = useCallback(() => {
    // Allow editing even with no user-supplied values (start with empty YAML)
    const yamlStr = values?.userSupplied ? jsonToYaml(values.userSupplied) : ''
    setEditedYaml(yamlStr)
    setYamlError(null)
    setIsEditing(true)
    // Switch to user-supplied view when editing
    if (showAllValues) {
      onToggleAllValues(false)
    }
  }, [values?.userSupplied, showAllValues, onToggleAllValues])

  // Cancel editing
  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditedYaml('')
    setYamlError(null)
    setPreviewData(null)
    setShowPreview(false)
  }, [])

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
    if (!namespace || !name) return
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
  }, [namespace, name, editedYaml, parseYaml, previewMutation])

  // Apply changes
  const handleApply = useCallback(async () => {
    if (!namespace || !name) return
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
  }, [namespace, name, editedYaml, parseYaml, applyMutation, handleCancelEdit, onApplySuccess])

  // Apply from preview modal
  const handleApplyFromPreview = useCallback(async () => {
    if (!previewData || !namespace || !name) return
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
  }, [previewData, namespace, name, applyMutation, handleCancelEdit, onApplySuccess])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-32 text-theme-text-tertiary">
        Loading values...
      </div>
    )
  }

  if (isEmpty && !isEditing) {
    return (
      <div className="p-4">
        <div className="flex items-center justify-between mb-3">
          <span className="text-sm font-medium text-theme-text-secondary">Values</span>
          <div className="flex items-center gap-2">
            <ToggleButton showAll={showAllValues} onToggle={onToggleAllValues} disabled={isEditing} />
            {canEdit && (
              <button
                onClick={handleStartEdit}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                <Pencil className="w-3.5 h-3.5" />
                Edit
              </button>
            )}
          </div>
        </div>
        <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
          <Settings className="w-8 h-8 text-theme-text-disabled" />
          <span>{showAllValues ? 'No computed values' : 'No user-supplied values'}</span>
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
            {isEditing ? 'Editing Values' : showAllValues ? 'All Values (Computed)' : 'User-Supplied Values'}
          </span>
          {isEditing && (
            <span className="px-1.5 py-0.5 text-xs rounded bg-amber-500/20 text-amber-400 border border-amber-500/30">
              unsaved
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {!isEditing && (
            <>
              <ToggleButton showAll={showAllValues} onToggle={onToggleAllValues} disabled={isEditing} />
              <button
                onClick={() => onCopy(yamlContent)}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
                Copy
              </button>
              {canEdit && (
                <button
                  onClick={handleStartEdit}
                  className="flex items-center gap-1 px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-blue-500/10 rounded border border-blue-500/30"
                >
                  <Pencil className="w-3.5 h-3.5" />
                  Edit
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
                disabled={!!yamlError || previewMutation.isPending}
                className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded border border-theme-border disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {previewMutation.isPending ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Eye className="w-3.5 h-3.5" />
                )}
                Preview
              </button>
              <button
                onClick={handleApply}
                disabled={!!yamlError || applyMutation.isPending || !canHelmWrite}
                className="flex items-center gap-1 px-2.5 py-1 text-xs text-white bg-blue-600 hover:bg-blue-700 rounded disabled:opacity-50 disabled:cursor-not-allowed"
                title={!canHelmWrite ? 'Helm write permissions required (rbac.helm=true)' : undefined}
              >
                {applyMutation.isPending ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Play className="w-3.5 h-3.5" />
                )}
                Apply
              </button>
            </>
          )}
        </div>
      </div>

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

function ToggleButton({ showAll, onToggle, disabled }: { showAll: boolean; onToggle: (show: boolean) => void; disabled?: boolean }) {
  return (
    <div className={clsx('flex items-center bg-theme-elevated/50 rounded-md p-0.5 text-xs', disabled && 'opacity-50 pointer-events-none')}>
      <button
        onClick={() => onToggle(false)}
        disabled={disabled}
        className={clsx(
          'px-2 py-1 rounded transition-colors',
          !showAll ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
        )}
      >
        User
      </button>
      <button
        onClick={() => onToggle(true)}
        disabled={disabled}
        className={clsx(
          'px-2 py-1 rounded transition-colors',
          showAll ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
        )}
      >
        All
      </button>
    </div>
  )
}

// Simple JSON to YAML converter for display
function jsonToYaml(obj: Record<string, unknown>, indent = 0): string {
  const spaces = '  '.repeat(indent)
  let result = ''

  for (const [key, value] of Object.entries(obj)) {
    if (value === null || value === undefined) {
      result += `${spaces}${key}: null\n`
    } else if (typeof value === 'object' && !Array.isArray(value)) {
      result += `${spaces}${key}:\n`
      result += jsonToYaml(value as Record<string, unknown>, indent + 1)
    } else if (Array.isArray(value)) {
      result += `${spaces}${key}:\n`
      for (const item of value) {
        if (typeof item === 'object' && item !== null) {
          result += `${spaces}- \n`
          const itemYaml = jsonToYaml(item as Record<string, unknown>, indent + 2)
          result += itemYaml
        } else {
          result += `${spaces}- ${formatValue(item)}\n`
        }
      }
    } else {
      result += `${spaces}${key}: ${formatValue(value)}\n`
    }
  }

  return result
}

function formatValue(value: unknown): string {
  if (typeof value === 'string') {
    // Quote strings that contain special characters
    if (value.includes(':') || value.includes('#') || value.includes('\n') || value.startsWith(' ') || value.endsWith(' ')) {
      return `"${value.replace(/"/g, '\\"')}"`
    }
    return value
  }
  return String(value)
}
