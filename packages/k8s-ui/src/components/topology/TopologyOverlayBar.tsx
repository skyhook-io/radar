import type { ReactNode } from 'react'
import { clsx } from 'clsx'

interface TopologyOverlayBarProps {
  children: ReactNode
  /** Extra classes merged onto the bar container. */
  className?: string
}

/**
 * Top-anchored overlay bar for the topology canvas. Stacks children top-to-
 * bottom (flex column, source order); wrap items in your own `flex` div to put
 * them on one line. Everything stacks, so overlays never collide.
 *
 * `pointer-events-none` so the canvas stays pannable in the gaps — interactive
 * children opt back in with `pointer-events-auto`. Children are `items-start`
 * (content width); use `w-full` for a full-width row.
 */
export function TopologyOverlayBar({ children, className }: TopologyOverlayBarProps) {
  return (
    <div
      className={clsx(
        'pointer-events-none absolute inset-x-2 top-2 z-10 flex flex-col items-start gap-2',
        className,
      )}
    >
      {children}
    </div>
  )
}
