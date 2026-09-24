import type { ReactNode } from 'react'
import { clsx } from 'clsx'

// Light subheading separating field groups inside a settings pane.
export function SubHeading({ children }: { children: ReactNode }) {
  return (
    <h4 className="text-xs font-semibold uppercase tracking-wider text-theme-text-tertiary">
      {children}
    </h4>
  )
}

export function ConfigToggle({
  label,
  description,
  value,
  disabled,
  onChange,
}: {
  label: string
  description?: string
  value: boolean
  disabled?: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <label className={clsx('flex items-start justify-between gap-3 py-1', disabled ? 'cursor-not-allowed' : 'cursor-pointer')}>
      <span className="min-w-0">
        <span className={clsx('block text-sm', disabled ? 'text-theme-text-secondary' : 'text-theme-text-primary')}>{label}</span>
        {description && (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">{description}</span>
        )}
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={value}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!value)}
        className={clsx(
          'relative w-9 h-5 shrink-0 rounded-full transition-colors',
          value ? 'bg-skyhook-600' : 'bg-theme-elevated border border-theme-border',
          disabled && 'opacity-50'
        )}
      >
        <span
          className={clsx(
            'absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white transition-transform shadow-sm',
            value && 'translate-x-4'
          )}
        />
      </button>
    </label>
  )
}
