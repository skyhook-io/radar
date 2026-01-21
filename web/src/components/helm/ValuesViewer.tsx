import { Copy, Check, Settings } from 'lucide-react'
import { clsx } from 'clsx'
import type { HelmValues } from '../../types'
import { CodeViewer } from '../ui/CodeViewer'

interface ValuesViewerProps {
  values?: HelmValues
  isLoading: boolean
  showAllValues: boolean
  onToggleAllValues: (show: boolean) => void
  onCopy: (text: string) => void
  copied: boolean
}

export function ValuesViewer({
  values,
  isLoading,
  showAllValues,
  onToggleAllValues,
  onCopy,
  copied,
}: ValuesViewerProps) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-32 text-theme-text-tertiary">
        Loading values...
      </div>
    )
  }

  const displayValues = showAllValues && values?.computed ? values.computed : values?.userSupplied
  const isEmpty = !displayValues || Object.keys(displayValues).length === 0

  if (isEmpty) {
    return (
      <div className="p-4">
        <div className="flex items-center justify-between mb-3">
          <span className="text-sm font-medium text-theme-text-secondary">Values</span>
          <ToggleButton showAll={showAllValues} onToggle={onToggleAllValues} />
        </div>
        <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
          <Settings className="w-8 h-8 text-theme-text-disabled" />
          <span>{showAllValues ? 'No computed values' : 'No user-supplied values'}</span>
        </div>
      </div>
    )
  }

  const yaml = jsonToYaml(displayValues)

  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-theme-text-secondary">
            {showAllValues ? 'All Values (Computed)' : 'User-Supplied Values'}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <ToggleButton showAll={showAllValues} onToggle={onToggleAllValues} />
          <button
            onClick={() => onCopy(yaml)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            Copy
          </button>
        </div>
      </div>

      <CodeViewer
        code={yaml}
        language="yaml"
        showLineNumbers
        maxHeight="calc(100vh - 300px)"
      />
    </div>
  )
}

function ToggleButton({ showAll, onToggle }: { showAll: boolean; onToggle: (show: boolean) => void }) {
  return (
    <div className="flex items-center bg-theme-elevated/50 rounded-md p-0.5 text-xs">
      <button
        onClick={() => onToggle(false)}
        className={clsx(
          'px-2 py-1 rounded transition-colors',
          !showAll ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
        )}
      >
        User
      </button>
      <button
        onClick={() => onToggle(true)}
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
