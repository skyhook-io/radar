import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
import { SectionHeading } from '../ui/FoldSection'
import { StatusDot, toneTextClass } from '../ui/status-tone'

export function WorkspaceCard({ title, children, className, framed = true }: { title?: ReactNode; children: ReactNode; className?: string; framed?: boolean }) {
  return (
    <section className={clsx(framed ? 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm' : 'border-t-subtle pt-3', className)}>
      {title && <SectionHeading>{title}</SectionHeading>}
      {children}
    </section>
  )
}

export function DimensionValue({
  tone,
  children,
  source,
  linkLabel,
  onOpen,
}: {
  tone: HealthLevel
  children: ReactNode
  source?: ReactNode
} & ({ linkLabel: string; onOpen?: () => void } | { linkLabel?: never; onOpen?: never })) {
  return (
    <div>
      <span className="inline-flex flex-wrap items-center gap-x-2">
        <StatusDot tone={tone} className="mt-[calc((1lh-0.375rem)/2)] shrink-0 self-start" />
        <span className={clsx('max-w-[calc(100%-0.875rem)] [overflow-wrap:anywhere]', toneTextClass(tone))}>{children}</span>
        {onOpen && (
          <button type="button" onClick={onOpen} className="text-xs text-accent-text hover:underline">
            {linkLabel} →
          </button>
        )}
      </span>
      {source && <div className="text-[11.5px] text-theme-text-tertiary">{source}</div>}
    </div>
  )
}
