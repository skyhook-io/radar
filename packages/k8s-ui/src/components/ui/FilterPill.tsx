import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from './Tooltip'

// FilterPill is a single-button toggle filter — clickable pill that
// communicates an active/inactive state. Used in horizontal filter rows
// where each pill toggles one filter on/off (no dropdown — this is the
// toggle pattern, not a combobox).
//
// Tone-encoded active state: when tone='danger' and active=true, the
// pill bg+text use rose; tone='warn' uses amber; etc. This is a
// filter-UI-scoped vocabulary (neutral/danger/warn/ok/brand) — distinct
// from the canonical HealthLevel vocabulary (healthy/degraded/alert/
// unhealthy/neutral/unknown), since "an active filter for danger
// problems" reads better as `tone='danger'` than `tone='unhealthy'`.
// Useful for filter rows that mix severity-bearing categories with
// neutral ones (Critical filters and Warning filters get visually
// distinct active states).
//
// Accessibility: every pill renders aria-pressed automatically, so
// screen readers announce pressed/unpressed correctly. Optional tooltip
// describes the toggle action ("Click to stop filtering by danger").

export type FilterPillTone = 'neutral' | 'danger' | 'warn' | 'ok' | 'brand'

interface Props {
  label: ReactNode
  active: boolean
  onClick: () => void
  /** Active-state color encoding. Default: neutral (Radar's existing style). */
  tone?: FilterPillTone
  /** Optional leading icon. */
  icon?: LucideIcon
  /** Optional count badge — renders " (N)" after label. */
  count?: number
  /** Tooltip explaining the toggle. Wraps button in Tooltip if set. */
  tooltip?: string
  /** Override the accessible name. Defaults to label + active state. */
  'aria-label'?: string
  className?: string
}

// Always-bordered chip: same border-width in both states keeps geometry stable
// when toggling, and a visible inactive border is what makes pills read as
// pressable chips instead of plain links. Active states fill the chip and
// promote the border to a tone-matched ring.
const TONE_ACTIVE: Record<FilterPillTone, string> = {
  neutral: 'bg-theme-text-primary/10 border-theme-text-primary/25 text-theme-text-primary',
  danger:  'bg-red-500/15 border-red-500/40 text-red-700 dark:text-red-300',
  warn:    'bg-amber-500/15 border-amber-500/40 text-amber-800 dark:text-amber-300',
  ok:      'bg-emerald-500/15 border-emerald-500/40 text-emerald-700 dark:text-emerald-300',
  brand:   'bg-[var(--color-brand-50)] border-[var(--color-radar-accent)] text-theme-text-primary dark:bg-[var(--color-brand-950)]',
}

const INACTIVE = 'border-theme-border-light text-theme-text-secondary hover:border-theme-border hover:text-theme-text-primary hover:bg-theme-hover/50'

export function FilterPill({
  label,
  active,
  onClick,
  tone = 'neutral',
  icon: Icon,
  count,
  tooltip,
  className,
  ...rest
}: Props) {
  const ariaLabel = rest['aria-label']

  const button = (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      aria-label={ariaLabel}
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors',
        'focus-visible:ring-2 focus-visible:ring-theme-text-primary/20 focus-visible:outline-none',
        active ? TONE_ACTIVE[tone] : INACTIVE,
        className,
      )}
    >
      {Icon && <Icon className="h-3.5 w-3.5" aria-hidden />}
      <span>{label}</span>
      {count !== undefined && (
        <span className="text-theme-text-tertiary">({count})</span>
      )}
    </button>
  )

  if (!tooltip) return button
  return (
    <Tooltip content={tooltip} delay={200}>
      {button}
    </Tooltip>
  )
}
