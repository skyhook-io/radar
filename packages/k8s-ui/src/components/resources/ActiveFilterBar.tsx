import { clsx } from 'clsx'
import { AlertTriangle, History, ListFilter, Tag, X } from 'lucide-react'

export interface ActiveFilterBarProps {
  columnFilters: Record<string, string[]>
  columnFilterExcludes: Record<string, boolean>
  problemFilters: string[]
  labelSelector: string
  onClearColumn: (key: string) => void
  onClearProblems: () => void
  onClearLabels: () => void
  restored?: boolean
  onClearAll?: () => void
  className?: string
}

export function ActiveFilterBar({
  columnFilters,
  columnFilterExcludes,
  problemFilters,
  labelSelector,
  onClearColumn,
  onClearProblems,
  onClearLabels,
  restored = false,
  onClearAll,
  className,
}: ActiveFilterBarProps) {
  const activeColEntries = Object.entries(columnFilters).filter(([, vals]) => vals.length > 0)
  const hasChips = activeColEntries.length > 0 || problemFilters.length > 0 || !!labelSelector
  if (!hasChips && !restored) return null

  return (
    <div className={clsx('flex flex-wrap items-center gap-1.5', className)}>
      {restored && (
        <span className="flex items-center gap-1 text-xs text-theme-text-tertiary">
          <History className="w-3 h-3" />
          <span>Filters restored from your last visit</span>
          {onClearAll && (
            <button
              type="button"
              onClick={onClearAll}
              className="text-theme-text-secondary hover:text-theme-text-primary underline-offset-2 hover:underline"
            >
              Clear
            </button>
          )}
        </span>
      )}
      {activeColEntries.map(([key, vals]) => (
        <button
          type="button"
          key={key}
          onClick={() => onClearColumn(key)}
          className="flex items-center gap-1 px-2 py-1 text-xs selection selection-text rounded-md hover:selection-strong transition-colors"
        >
          <ListFilter className="w-3 h-3" />
          <span>{key}: {columnFilterExcludes[key] ? 'not ' : ''}{vals.join(', ')}</span>
          <X className="w-3 h-3" />
        </button>
      ))}
      {problemFilters.length > 0 && (
        <button
          type="button"
          onClick={onClearProblems}
          className="flex items-center gap-1 px-2 py-1 text-xs bg-red-500/15 text-red-700 dark:text-red-300 rounded-md hover:bg-red-500/25 transition-colors"
        >
          <AlertTriangle className="w-3 h-3" />
          <span>Problems: {problemFilters.join(', ')}</span>
          <X className="w-3 h-3" />
        </button>
      )}
      {labelSelector && (
        <button
          type="button"
          onClick={onClearLabels}
          className="flex items-center gap-1 px-2 py-1 text-xs bg-green-500/15 text-green-700 dark:text-green-300 rounded-md hover:bg-green-500/25 transition-colors"
        >
          <Tag className="w-3 h-3" />
          <span>{labelSelector}</span>
          <X className="w-3 h-3" />
        </button>
      )}
    </div>
  )
}
