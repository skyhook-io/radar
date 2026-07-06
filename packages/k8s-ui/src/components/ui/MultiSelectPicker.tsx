import { useMemo } from 'react'
import type { ReactNode } from 'react'
import { Search, X } from 'lucide-react'

export interface MultiSelectPickerProps {
  /** Full option list. The host sorts/orders it; the picker filters by search only. */
  items: string[]
  /** Currently-selected values. Empty = no filter (summary shows {@link summaryEmptyLabel}). */
  selected: ReadonlySet<string>
  /** Called with the next selection whenever a row or the visible-bulk button toggles. */
  onSelectionChange: (next: Set<string>) => void
  /**
   * The left "Clear all" button. Separate from {@link onSelectionChange} so the
   * host can attach side effects (e.g. commit + close) rather than only clearing.
   */
  onClearAll: () => void
  /** Footer "Done" button. */
  onDone: () => void

  /** Controlled search term (kept in the host so the body stays SSR-testable). */
  search: string
  onSearchChange: (value: string) => void

  /** Placeholder for the filter input, e.g. "Filter kinds". */
  searchPlaceholder: string
  /** Footer summary when nothing is selected, e.g. "All kinds". */
  summaryEmptyLabel: string
  /** List message when there are no items at all, e.g. "No kinds available." */
  noItemsLabel: string

  /** Disables the left "Clear all" button (nothing to clear / not permitted). */
  clearAllDisabled?: boolean
  clearAllAriaLabel?: string
  /** Trailing per-item content (e.g. a "kubeconfig" tag). */
  renderItemMeta?: (item: string) => ReactNode
  /** Show the filter input only once the list is longer than this. Default 6. */
  searchThreshold?: number
}

/**
 * Presentational multi-select picker body: filter input, Clear all / Select all
 * row, a scrollable checkbox list, and a summary + Done footer. The shared core
 * behind the namespace scope picker and the timeline Kinds filter — controlled
 * (selection + search injected) so both hosts drive it their own way and it
 * stays SSR-testable.
 */
export function MultiSelectPicker({
  items,
  selected,
  onSelectionChange,
  onClearAll,
  onDone,
  search,
  onSearchChange,
  searchPlaceholder,
  summaryEmptyLabel,
  noItemsLabel,
  clearAllDisabled = false,
  clearAllAriaLabel = 'Clear selection',
  renderItemMeta,
  searchThreshold = 6,
}: MultiSelectPickerProps) {
  const filteredItems = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return items
    return items.filter(item => item.toLowerCase().includes(q))
  }, [items, search])

  const toggle = (item: string) => {
    const next = new Set(selected)
    if (next.has(item)) next.delete(item)
    else next.add(item)
    onSelectionChange(next)
  }

  const selectVisible = () => {
    const next = new Set(selected)
    for (const item of filteredItems) next.add(item)
    onSelectionChange(next)
  }

  const clearVisible = () => {
    const next = new Set(selected)
    for (const item of filteredItems) next.delete(item)
    onSelectionChange(next)
  }

  // Counts computed against the visible (filtered) set so the bulk-action label
  // matches what the button will affect.
  const visibleSelectedCount = filteredItems.reduce((n, item) => n + (selected.has(item) ? 1 : 0), 0)
  const allVisibleSelected = filteredItems.length > 0 && visibleSelectedCount === filteredItems.length

  return (
    <>
      {items.length > searchThreshold && (
        <div className="flex items-center gap-2 px-2 py-1.5 border-b border-theme-border">
          <Search className="w-3.5 h-3.5 text-theme-text-tertiary" />
          <input
            autoFocus
            value={search}
            onChange={e => onSearchChange(e.target.value)}
            placeholder={searchPlaceholder}
            className="flex-1 bg-transparent text-sm outline-none text-theme-text-primary placeholder:text-theme-text-tertiary"
          />
        </div>
      )}

      <div className="flex items-center justify-between px-2 py-1.5 border-b border-theme-border text-xs text-theme-text-secondary">
        <button
          onClick={clearAllDisabled ? undefined : onClearAll}
          disabled={clearAllDisabled}
          className="flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-theme-hover disabled:opacity-50 disabled:hover:bg-transparent"
          aria-label={clearAllAriaLabel}
        >
          <X className="w-3 h-3" />
          Clear all
        </button>
        <button
          onClick={allVisibleSelected ? clearVisible : selectVisible}
          disabled={filteredItems.length === 0}
          className="px-1.5 py-0.5 rounded hover:bg-theme-hover disabled:opacity-50 disabled:hover:bg-transparent"
        >
          {allVisibleSelected
            ? `Clear ${filteredItems.length} visible`
            : search.trim()
              ? `Select ${filteredItems.length} visible`
              : 'Select all'}
        </button>
      </div>

      <ul className="max-h-80 overflow-y-auto py-1">
        {filteredItems.length === 0 && (
          <li className="px-3 py-2 text-xs text-theme-text-tertiary">
            {search ? 'No matches.' : noItemsLabel}
          </li>
        )}

        {filteredItems.map(item => (
          <li key={item}>
            <label className="w-full flex items-center justify-between px-3 py-1.5 text-sm hover:bg-theme-hover text-left text-theme-text-primary cursor-pointer">
              <span className="flex items-center gap-2 min-w-0">
                <input
                  type="checkbox"
                  checked={selected.has(item)}
                  onChange={() => toggle(item)}
                  className="shrink-0 accent-current"
                />
                <span className="truncate">{item}</span>
                {renderItemMeta?.(item)}
              </span>
            </label>
          </li>
        ))}
      </ul>

      <div className="flex items-center justify-between px-3 py-1.5 border-t border-theme-border text-[11px] text-theme-text-tertiary">
        <span>{selected.size === 0 ? summaryEmptyLabel : `${selected.size} selected`}</span>
        <button
          onClick={onDone}
          className="px-2 py-0.5 rounded bg-theme-elevated hover:bg-theme-hover text-theme-text-primary"
        >
          Done
        </button>
      </div>
    </>
  )
}
