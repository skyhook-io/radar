import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import { ChevronUp, ChevronDown, ArrowUpDown } from 'lucide-react'

export type SortDir = 'asc' | 'desc'

// Canonical dense-table header-cell styling, shared so the Applications,
// GitOps, and Helm tables read as one family with the Resources table.
export const TH_CLASS =
  'border-b border-theme-border px-4 py-3 text-left text-xs font-medium uppercase tracking-wide text-theme-text-secondary'

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
          // `uppercase` is repeated here on purpose: Tailwind Preflight resets
          // `button { text-transform: none }`, which would otherwise cancel the
          // inherited uppercase from TH_CLASS and render sortable headers in
          // title case while non-sortable <th> cells stay uppercase.
          'inline-flex items-center gap-1 select-none uppercase hover:text-theme-text-primary focus-visible:outline-none focus-visible:text-theme-text-primary',
          align === 'right' && 'w-full justify-end',
        )}
      >
        {label}
        <span className="shrink-0 text-theme-text-tertiary">
          {active ? (
            direction === 'asc' ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ArrowUpDown className="h-3 w-3 opacity-50" />
          )}
        </span>
      </button>
    </th>
  )
}
