import type { ComponentType, ReactNode } from 'react'
import { clsx } from 'clsx'
import { Info } from 'lucide-react'
import { Tooltip } from './Tooltip'

// Shared faceted-filter primitives for sidebar filter rails. One implementation
// behind the GitOps and Applications rails so the "this filter is on" language
// stays identical everywhere — active = the skyhook accent, the same signal the
// nav rail and header status tiles use.

export type FacetTone = 'neutral' | 'success' | 'warning' | 'error' | 'info'

const DOT_CLASS: Record<FacetTone, string> = {
  neutral: 'bg-theme-text-tertiary',
  success: 'bg-emerald-500',
  warning: 'bg-amber-500',
  error: 'bg-red-500',
  info: 'bg-sky-500',
}

// A titled group in a filter rail: optional leading icon, optional info tooltip,
// and a bottom divider so stacked sections read as distinct facets.
export function FacetSection({
  icon: Icon,
  title,
  info,
  children,
}: {
  icon?: ComponentType<{ className?: string }>
  title: string
  info?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="border-b border-theme-border px-3 py-2">
      <div className="mb-1.5 flex items-center gap-1.5">
        {Icon && <Icon className="h-3.5 w-3.5 text-theme-text-tertiary" />}
        <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{title}</span>
        {info && (
          <Tooltip content={info} delay={150} position="right">
            <Info className="h-3 w-3 cursor-default text-theme-text-tertiary/70 hover:text-theme-text-secondary" aria-label={`About ${title}`} />
          </Tooltip>
        )}
      </div>
      <div className="space-y-0.5">{children}</div>
    </section>
  )
}

// A single multi-select filter toggle: leading status dot (tone-colored), label,
// trailing count. Active uses the skyhook accent.
export function FacetButton({
  label,
  count,
  active,
  tone = 'neutral',
  tooltip,
  onClick,
}: {
  label: string
  count?: number
  active: boolean
  tone?: FacetTone
  tooltip?: ReactNode
  onClick: () => void
}) {
  const btn = (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={clsx(
        'flex w-full items-center gap-2 rounded px-2 py-1 text-left text-[11px] transition-colors',
        active
          ? 'bg-skyhook-500/15 text-skyhook-700 dark:text-skyhook-300'
          : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
      )}
    >
      <span className={clsx('h-2 w-2 shrink-0 rounded-full', DOT_CLASS[tone])} />
      <span className="min-w-0 flex-1 truncate font-medium">{label}</span>
      {count != null && count > 0 && <span className="tabular-nums text-theme-text-tertiary">{count}</span>}
    </button>
  )
  return tooltip ? (
    <Tooltip content={tooltip} delay={300} position="right" wrapperClassName="w-full">
      {btn}
    </Tooltip>
  ) : (
    btn
  )
}

// Convenience wrapper: a FacetSection of multi-select FacetButtons driven by a
// Set. Auto-hides zero-count options (quieter than a hard slice cap) unless
// hideZero is turned off.
export function Facet<T extends string>({
  title,
  icon,
  info,
  options,
  selected,
  onToggle,
  hideZero = true,
}: {
  title: string
  icon?: ComponentType<{ className?: string }>
  info?: ReactNode
  options: { value: T; label: string; count: number; tone?: FacetTone; tooltip?: ReactNode }[]
  selected: Set<T>
  onToggle: (v: T) => void
  hideZero?: boolean
}) {
  const visible = hideZero ? options.filter((o) => o.count > 0) : options
  if (visible.length === 0) return null
  return (
    <FacetSection icon={icon} title={title} info={info}>
      {visible.map((o) => (
        <FacetButton
          key={o.value}
          label={o.label}
          count={o.count}
          active={selected.has(o.value)}
          tone={o.tone}
          tooltip={o.tooltip}
          onClick={() => onToggle(o.value)}
        />
      ))}
    </FacetSection>
  )
}
