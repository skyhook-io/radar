import { useEffect, useRef } from 'react'
import { X, Play, Loader2, FileText, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import type { ValuesPreviewResponse } from '../../types'

interface ValuesDiffPreviewProps {
  previewData: ValuesPreviewResponse
  onClose: () => void
  onApply: () => void
  isApplying: boolean
}

export function ValuesDiffPreview({
  previewData,
  onClose,
  onApply,
  isApplying,
}: ValuesDiffPreviewProps) {
  const dialogRef = useRef<HTMLDivElement>(null)

  // Handle ESC key
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !isApplying) {
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose, isApplying])

  // Focus trap
  useEffect(() => {
    if (dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [])

  const hasChanges = previewData.manifestDiff.trim().length > 0 &&
    previewData.manifestDiff.split('\n').some(line => line.startsWith('+') || line.startsWith('-'))

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={isApplying ? undefined : onClose}
      />

      {/* Dialog */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl max-w-4xl w-full mx-4 max-h-[85vh] flex flex-col outline-none"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-2">
            <FileText className="w-5 h-5 text-blue-400" />
            <h3 className="text-lg font-semibold text-theme-text-primary">Preview Changes</h3>
          </div>
          <button
            onClick={onClose}
            disabled={isApplying}
            className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-auto p-4">
          {hasChanges ? (
            <div className="space-y-4">
              <p className="text-sm text-theme-text-secondary">
                The following manifest changes will be applied:
              </p>
              <DiffView diff={previewData.manifestDiff} />
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center py-12 text-theme-text-tertiary">
              <AlertTriangle className="w-12 h-12 mb-3 text-amber-400/50" />
              <p className="text-lg font-medium text-theme-text-secondary">No Manifest Changes</p>
              <p className="text-sm mt-1">
                The new values will not change the rendered manifest.
              </p>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-4 py-3 border-t border-theme-border shrink-0 bg-theme-surface/50">
          <div className="text-xs text-theme-text-tertiary">
            {hasChanges ? (
              <span className="flex items-center gap-1">
                <span className="text-green-400">+</span> additions
                <span className="mx-2">|</span>
                <span className="text-red-400">-</span> deletions
              </span>
            ) : (
              <span>Values will be updated without manifest changes</span>
            )}
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={onClose}
              disabled={isApplying}
              className="px-4 py-2 text-sm font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              onClick={onApply}
              disabled={isApplying}
              className="flex items-center gap-2 px-4 py-2 text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors disabled:opacity-50"
            >
              {isApplying ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                <Play className="w-4 h-4" />
              )}
              Apply Changes
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

interface DiffViewProps {
  diff: string
}

function DiffView({ diff }: DiffViewProps) {
  const lines = diff.split('\n')

  return (
    <div className="rounded-lg border border-theme-border overflow-hidden">
      <div className="bg-theme-elevated/50 px-3 py-2 text-xs font-medium text-theme-text-secondary border-b border-theme-border">
        Manifest Diff
      </div>
      <div className="overflow-auto max-h-[50vh]">
        <pre className="text-xs font-mono p-0 m-0">
          {lines.map((line, index) => (
            <DiffLine key={index} line={line} lineNumber={index + 1} />
          ))}
        </pre>
      </div>
    </div>
  )
}

interface DiffLineProps {
  line: string
  lineNumber: number
}

function DiffLine({ line, lineNumber }: DiffLineProps) {
  const isAddition = line.startsWith('+') && !line.startsWith('+++')
  const isDeletion = line.startsWith('-') && !line.startsWith('---')
  const isHeader = line.startsWith('@@') || line.startsWith('---') || line.startsWith('+++')

  return (
    <div
      className={clsx(
        'flex',
        isAddition && 'bg-green-500/10',
        isDeletion && 'bg-red-500/10',
        isHeader && 'bg-blue-500/10'
      )}
    >
      <span className="w-12 shrink-0 text-right pr-3 py-0.5 text-theme-text-disabled select-none border-r border-theme-border/50">
        {lineNumber}
      </span>
      <span
        className={clsx(
          'flex-1 px-3 py-0.5 whitespace-pre',
          isAddition && 'text-green-400',
          isDeletion && 'text-red-400',
          isHeader && 'text-blue-400 font-medium',
          !isAddition && !isDeletion && !isHeader && 'text-theme-text-secondary'
        )}
      >
        {line}
      </span>
    </div>
  )
}
