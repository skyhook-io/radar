import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import { ChevronUp, ChevronDown } from 'lucide-react'

export type SortDir = 'asc' | 'desc'

// Canonical dense-table header-cell styling, shared so the Applications and
// GitOps tables (modeled on the Resources table) read as one table family.
export const TH_CLASS =
  'border-b border-theme-border px-3 py-2 text-left text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary'

// A clickable, sortable column header. Clicking fires onSort(sortKey); the
// consumer owns the cycle (toggle dir, or asc→desc→off). One chevron marks the
// active column + its direction — the same affordance as the Resources table,
// replacing per-view sort dropdowns.
export function SortableTh<K extends string>({
  label,
  sortKey,
  activeKey,
  direction,
  onSort,
  align = 'left',
  className,
}: {
  label: ReactNode
  sortKey: K
  activeKey: K | null
  direction: SortDir
  onSort: (key: K) => void
  align?: 'left' | 'right'
  className?: string
}) {
  const active = activeKey === sortKey
  // Sort lives on a real <button> (keyboard-focusable, Enter/Space native) while
  // aria-sort stays on the <th> — accessible sorting, not a mouse-only header.
  return (
    <th
      aria-sort={active ? (direction === 'asc' ? 'ascending' : 'descending') : 'none'}
      className={clsx(TH_CLASS, align === 'right' && 'text-right', className)}
    >
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        className={clsx(
          'inline-flex items-center gap-1 select-none hover:text-theme-text-primary focus-visible:outline-none focus-visible:text-theme-text-primary',
          align === 'right' && 'w-full justify-end',
        )}
      >
        {label}
        {active ? (
          direction === 'asc' ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />
        ) : (
          <span className="w-3" />
        )}
      </button>
    </th>
  )
}
