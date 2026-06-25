import { useState } from 'react'
import { History, Eye, GitCompare, Check, RotateCcw } from 'lucide-react'
import { clsx } from 'clsx'
import type { HelmOperation, HelmRevision } from '../../types'
import { getStatusColor, formatDate, formatAge } from './helm-utils'
import { SEVERITY_BADGE } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'

interface RevisionHistoryProps {
  history: HelmRevision[]
  currentRevision: number
  operations?: HelmOperation[]
  onViewRevision: (revision: number) => void
  onCompare: (rev1: number, rev2: number) => void
  onRollback?: (revision: number) => void
}

export function RevisionHistory({ history, currentRevision, operations = [], onViewRevision, onCompare, onRollback }: RevisionHistoryProps) {
  const [selectedForCompare, setSelectedForCompare] = useState<number | null>(null)

  const handleCompareClick = (revision: number) => {
    if (selectedForCompare === null) {
      setSelectedForCompare(revision)
    } else if (selectedForCompare === revision) {
      setSelectedForCompare(null)
    } else {
      // Compare the two revisions (older first)
      const rev1 = Math.min(selectedForCompare, revision)
      const rev2 = Math.max(selectedForCompare, revision)
      onCompare(rev1, rev2)
      setSelectedForCompare(null)
    }
  }

  if (!history || history.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
        <History className="w-8 h-8 text-theme-text-disabled" />
        <span>No revision history</span>
      </div>
    )
  }

  return (
    <div className="p-4">
      {/* Compare mode indicator */}
      {selectedForCompare !== null && (
        <div className="mb-4 p-3 bg-blue-500/10 border border-blue-500/30 rounded-lg flex items-center justify-between">
          <div className="flex items-center gap-2 text-sm text-blue-300">
            <GitCompare className="w-4 h-4" />
            <span>Select another revision to compare with revision {selectedForCompare}</span>
          </div>
          <button
            onClick={() => setSelectedForCompare(null)}
            className="text-xs text-theme-text-secondary hover:text-theme-text-primary"
          >
            Cancel
          </button>
        </div>
      )}

      {/* History timeline */}
      <div className="space-y-2">
        {history.map((revision, index) => {
          const isCurrent = revision.revision === currentRevision
          const isSelectedForCompare = selectedForCompare === revision.revision
          const annotations = operationAnnotationsForRevision(operations, revision.revision)

          return (
            <div
              key={revision.revision}
              className={clsx(
                'relative bg-theme-elevated/30 rounded-lg p-4 border transition-colors',
                isCurrent
                  ? 'border-green-500/50'
                  : isSelectedForCompare
                  ? 'border-blue-500/50 bg-blue-500/10'
                  : 'border-transparent hover:border-theme-border-light'
              )}
            >
              {/* Timeline connector */}
              {index < history.length - 1 && (
                <div className="absolute left-7 top-14 bottom-0 w-0.5 bg-theme-hover -mb-2" />
              )}

              <div className="flex items-start gap-3">
                {/* Revision circle */}
                <div
                  className={clsx(
                    'w-8 h-8 rounded-full flex items-center justify-center text-xs font-bold shrink-0 z-10',
                    isCurrent
                      ? 'bg-green-500 text-theme-text-primary'
                      : 'bg-theme-hover text-theme-text-secondary'
                  )}
                >
                  {revision.revision}
                </div>

                {/* Revision details */}
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-sm font-medium text-theme-text-primary">{revision.chart}</span>
                    <span className={clsx('badge-sm', getStatusColor(revision.status))}>
                      {revision.status}
                    </span>
                    {isCurrent && (
                      <span className={clsx('badge-sm', SEVERITY_BADGE.success)}>
                        Current
                      </span>
                    )}
                    {annotations.map((annotation) => (
                      <span key={annotation.label} className={clsx('badge-sm', annotation.className)}>
                        {annotation.label}
                      </span>
                    ))}
                  </div>

                  <div className="flex items-center gap-4 mt-1 text-xs text-theme-text-tertiary">
                    <Tooltip content={revision.updated}><span>{formatDate(revision.updated)}</span></Tooltip>
                    <span>{formatAge(revision.updated)} ago</span>
                  </div>

                  {revision.description && (
                    <p className="mt-2 text-sm text-theme-text-secondary truncate">{revision.description}</p>
                  )}

                  {/* Actions */}
                  <div className="flex items-center gap-2 mt-3">
                    <button
                      onClick={() => onViewRevision(revision.revision)}
                      className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                    >
                      <Eye className="w-3.5 h-3.5" />
                      View Manifest
                    </button>
                    <button
                      onClick={() => handleCompareClick(revision.revision)}
                      className={clsx(
                        'flex items-center gap-1 px-2 py-1 text-xs rounded',
                        isSelectedForCompare
                          ? 'bg-blue-500 text-theme-text-primary'
                          : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                      )}
                    >
                      {isSelectedForCompare ? (
                        <>
                          <Check className="w-3.5 h-3.5" />
                          Selected
                        </>
                      ) : (
                        <>
                          <GitCompare className="w-3.5 h-3.5" />
                          Compare
                        </>
                      )}
                    </button>
                    {!isCurrent && onRollback && (
                      <button
                        onClick={() => onRollback(revision.revision)}
                        className="flex items-center gap-1 px-2 py-1 text-xs text-amber-400 hover:text-amber-300 hover:bg-amber-500/10 rounded"
                      >
                        <RotateCcw className="w-3.5 h-3.5" />
                        Rollback
                      </button>
                    )}
                  </div>
                </div>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function operationAnnotationsForRevision(operations: HelmOperation[], revision: number): Array<{ label: string; className: string }> {
  const annotations: Array<{ label: string; className: string }> = []
  const seen = new Set<string>()
  const add = (label: string, className: string) => {
    if (seen.has(label)) return
    seen.add(label)
    annotations.push({ label, className })
  }

  for (const op of operations) {
    if (op.failedRevision === revision) {
      add('Failed upgrade', SEVERITY_BADGE.error)
    }
    if (op.rollbackRevision === revision) {
      add('Rollback revision', SEVERITY_BADGE.warning)
    }
    if (op.revision === revision) {
      switch (op.kind) {
        case 'upgrade_failed':
          add('Failed upgrade', SEVERITY_BADGE.error)
          break
        case 'release_failed':
          add('Failed', SEVERITY_BADGE.error)
          break
        case 'rollback':
          add('Rollback', SEVERITY_BADGE.warning)
          break
        case 'pending':
          add('Pending', SEVERITY_BADGE.warning)
          break
      }
    }
  }

  return annotations
}
