import { X, GitCompare } from 'lucide-react'
import { PaneLoader } from '@skyhook-io/k8s-ui'
import { clsx } from 'clsx'

interface ManifestDiffViewerProps {
  diff: string
  isLoading: boolean
  revision1: number
  revision2: number
  onClose: () => void
  title?: string
  emptyLabel?: string
}

export function ManifestDiffViewer({ diff, isLoading, revision1, revision2, onClose, title, emptyLabel }: ManifestDiffViewerProps) {
  if (isLoading) {
    return <PaneLoader label="Computing diff…" className="h-32" />
  }

  if (!hasDiffBodyChange(diff)) {
    return (
      <div className="p-4">
        <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
          <GitCompare className="w-8 h-8 text-theme-text-disabled" />
          <span>{emptyLabel || 'No differences found'}</span>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <GitCompare className="w-4 h-4 text-theme-text-secondary" />
          <span className="text-sm font-medium text-theme-text-secondary">
            {title || `Comparing Revision ${revision1} → ${revision2}`}
          </span>
        </div>
        <button
          onClick={onClose}
          className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          <X className="w-3.5 h-3.5" />
          Close
        </button>
      </div>

      <div className="rounded-lg max-h-[calc(100vh-300px)] overflow-auto bg-theme-base/50 font-mono text-xs">
        <div className="p-3">
          {diff.split('\n').map((line, index) => (
            <DiffLine key={index} line={line} />
          ))}
        </div>
      </div>

      {/* Legend */}
      <div className="flex items-center gap-4 mt-3 text-xs text-theme-text-tertiary">
        <div className="flex items-center gap-1">
          <span className="w-3 h-3 bg-red-500/20 border border-red-500/50 rounded" />
          <span>Removed</span>
        </div>
        <div className="flex items-center gap-1">
          <span className="w-3 h-3 bg-green-500/20 border border-green-500/50 rounded" />
          <span>Added</span>
        </div>
      </div>
    </div>
  )
}

export function hasDiffBodyChange(diff: string): boolean {
  return diff.split('\n').some((line) => {
    if (!line || line.startsWith('---') || line.startsWith('+++') || line.startsWith('@@')) {
      return false
    }
    return line.startsWith('+') || line.startsWith('-')
  })
}

export function DiffLine({ line }: { line: string }) {
  const isAddition = line.startsWith('+') && !line.startsWith('+++')
  const isRemoval = line.startsWith('-') && !line.startsWith('---')
  const isHeader = line.startsWith('---') || line.startsWith('+++') || line.startsWith('@@')

  return (
    <div
      className={clsx(
        'whitespace-pre',
        isAddition && 'bg-green-500/10 text-green-700 dark:text-green-400',
        isRemoval && 'bg-red-500/10 text-red-700 dark:text-red-400',
        isHeader && 'text-theme-text-tertiary font-bold',
        !isAddition && !isRemoval && !isHeader && 'text-theme-text-secondary'
      )}
    >
      {line || ' '}
    </div>
  )
}
