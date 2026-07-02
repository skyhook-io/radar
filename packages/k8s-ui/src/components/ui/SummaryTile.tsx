import { clsx } from 'clsx'

export type SummaryTone = 'neutral' | 'success' | 'warning' | 'error' | 'info'

// A compact count tile for a view's status summary — value over label, with a
// tone-colored value and (when clickable) an active border that doubles as a
// one-click filter. Lives in the shared list-view chassis: GitOps and
// Applications both surface their status rollup as a row of these in the header.
export function SummaryTile({
  label,
  value,
  tone = 'neutral',
  onClick,
  active = false,
  loading = false,
}: {
  label: string
  value: number
  tone?: SummaryTone
  onClick?: () => void
  active?: boolean
  /** First fetch in flight — render a pulse instead of the value. A tile
   *  that reads "0" while loading is a false zero, not a count. */
  loading?: boolean
}) {
  const toneClass = {
    neutral: 'text-theme-text-primary',
    success: 'text-emerald-600 dark:text-emerald-300',
    warning: 'text-amber-600 dark:text-amber-300',
    error: 'text-red-600 dark:text-red-300',
    info: 'text-sky-600 dark:text-sky-300',
  }[tone]
  const activeBorderClass = {
    neutral: 'border-skyhook-500',
    success: 'border-emerald-500',
    warning: 'border-amber-500',
    error: 'border-red-500',
    info: 'border-sky-500',
  }[tone]
  const value$ = loading ? (
    <div className="my-1 h-3.5 w-8 animate-pulse rounded bg-theme-hover" aria-hidden />
  ) : (
    <div className={`text-sm font-semibold ${toneClass}`}>{value}</div>
  )
  const label$ = <div className="text-xs text-theme-text-tertiary">{label}</div>
  if (!onClick) {
    return (
      <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2">
        {value$}
        {label$}
      </div>
    )
  }
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={clsx(
        'cursor-pointer rounded-md border bg-theme-base px-3 py-2 text-left transition-colors hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-skyhook-500',
        active ? activeBorderClass : 'border-theme-border',
      )}
    >
      {value$}
      {label$}
    </button>
  )
}
