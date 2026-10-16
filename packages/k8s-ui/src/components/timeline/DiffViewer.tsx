import { memo } from 'react'
import { clsx } from 'clsx'
import { ArrowRight, EyeOff, Plus, Minus, RefreshCw } from 'lucide-react'
import type { DiffInfo, FieldChange } from '../../types'

interface DiffViewerProps {
  diff: DiffInfo
  compact?: boolean
}

export const DiffViewer = memo(function DiffViewer({ diff, compact = false }: DiffViewerProps) {
  if (!diff || diff.fields.length === 0) {
    return null
  }

  if (compact) {
    return (
      <div className="text-xs text-theme-text-secondary flex items-center gap-1">
        <RefreshCw className="w-3 h-3" aria-hidden />
        <span>{diff.summary || `${diff.fields.length} field(s) changed`}</span>
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {/* Summary */}
      {diff.summary && (
        <div className="text-sm font-medium text-theme-text-secondary flex items-center gap-2">
          <RefreshCw className="w-4 h-4 text-blue-400" aria-hidden />
          {diff.summary}
        </div>
      )}

      {/* Field changes */}
      <div className="space-y-1.5">
        {diff.fields.map((field, idx) => (
          <FieldChangeRow key={`${field.path}-${idx}`} field={field} />
        ))}
      </div>
    </div>
  )
})

interface FieldChangeRowProps {
  field: FieldChange
}

// The backend never sends values it keeps out of the feed (secrets, config
// blobs, large structures); it sends one of these markers instead. Older
// events carry a bare "changed" on both sides.
const HIDDEN_VALUE = '(value hidden)'
const HIDDEN_SET = '(set)'

function hiddenChange(field: FieldChange): 'changed' | 'set' | 'removed' | null {
  const { oldValue, newValue } = field
  const absent = (v: unknown) => v === null || v === undefined
  if ((oldValue === HIDDEN_VALUE && newValue === HIDDEN_VALUE) || (oldValue === 'changed' && newValue === 'changed')) {
    return 'changed'
  }
  if (absent(oldValue) && newValue === HIDDEN_SET) return 'set'
  if (oldValue === HIDDEN_SET && absent(newValue)) return 'removed'
  return null
}

function FieldChangeRow({ field }: FieldChangeRowProps) {
  const hidden = hiddenChange(field)
  if (hidden) {
    const Icon = hidden === 'set' ? Plus : hidden === 'removed' ? Minus : RefreshCw
    return (
      <div className="rounded bg-theme-surface/50 border border-theme-border px-3 py-2">
        <div className="text-xs font-mono text-theme-text-tertiary mb-1 break-words [overflow-wrap:anywhere]">{field.path}</div>
        <div className="flex flex-wrap items-center gap-2 text-sm text-theme-text-secondary">
          <Icon className="w-3.5 h-3.5 shrink-0" aria-hidden />
          <span>{hidden === 'set' ? 'Set' : hidden === 'removed' ? 'Removed' : 'Changed'}</span>
          <span className="inline-flex items-center gap-1 rounded-full bg-theme-elevated px-2 py-0.5 text-xs text-theme-text-tertiary">
            <EyeOff className="w-3 h-3" aria-hidden />
            value hidden
          </span>
        </div>
      </div>
    )
  }

  const isAdded = field.oldValue === null || field.oldValue === undefined
  const isRemoved = field.newValue === null || field.newValue === undefined
  const isModified = !isAdded && !isRemoved

  return (
    <div className="rounded bg-theme-surface/50 border border-theme-border px-3 py-2">
      {/* Field path */}
      <div className="text-xs font-mono text-theme-text-tertiary mb-1 break-words [overflow-wrap:anywhere]">{field.path}</div>

      {/* Values */}
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
        {isAdded ? (
          <>
            <Plus className="w-3.5 h-3.5 text-green-400 shrink-0" aria-hidden />
            <span className="sr-only">Added value: </span>
            <span className="min-w-0 break-words text-green-400 [overflow-wrap:anywhere]">
              {formatValue(field.newValue)}
            </span>
          </>
        ) : isRemoved ? (
          <>
            <Minus className="w-3.5 h-3.5 text-red-400 shrink-0" aria-hidden />
            <span className="sr-only">Removed value: </span>
            <span className="min-w-0 break-words text-red-400 line-through [overflow-wrap:anywhere]">
              {formatValue(field.oldValue)}
            </span>
          </>
        ) : isModified ? (
          <>
            <span className="sr-only">Old value: </span>
            <span className="min-w-0 break-words text-red-400 line-through [overflow-wrap:anywhere]">
              {formatValue(field.oldValue)}
            </span>
            <ArrowRight className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" aria-hidden />
            <span className="sr-only">New value: </span>
            <span className="min-w-0 break-words text-green-400 [overflow-wrap:anywhere]">
              {formatValue(field.newValue)}
            </span>
          </>
        ) : null}
      </div>
    </div>
  )
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined) {
    return 'null'
  }
  if (typeof value === 'object') {
    try {
      const str = JSON.stringify(value)
      // Truncate long values
      if (str.length > 100) {
        return str.slice(0, 97) + '...'
      }
      return str
    } catch {
      return String(value)
    }
  }
  return String(value)
}

// Inline diff badge for use in event cards
interface DiffBadgeProps {
  diff: DiffInfo
}

export const DiffBadge = memo(function DiffBadge({ diff }: DiffBadgeProps) {
  if (!diff || !diff.summary) {
    return null
  }

  return (
    <span
      className={clsx(
        'badge',
        'bg-skyhook-500/10 text-skyhook-400 border border-skyhook-500/20'
      )}
    >
      <RefreshCw className="w-3 h-3" aria-hidden />
      {diff.summary}
    </span>
  )
})
