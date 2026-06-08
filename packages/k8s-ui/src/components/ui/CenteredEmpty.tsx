import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { EmptyState, type EmptyStateTone } from './EmptyState'

// CenteredEmpty — the shared "whole panel is empty" state, centered in the
// available height (matches how the view components present their own
// empty/healthy states). Use for not-found / no-data-at-all; for "the filter
// excluded everything" render an inline EmptyState card where the rows would be.
export function CenteredEmpty({
  tone = 'neutral',
  icon,
  headline,
  body,
  action,
}: {
  tone?: EmptyStateTone
  icon?: LucideIcon
  headline: string
  body?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex min-h-[55vh] flex-1 items-center justify-center p-4">
      <EmptyState tone={tone} variant="card" icon={icon} headline={headline} body={body} action={action} className="border-none bg-transparent" />
    </div>
  )
}
