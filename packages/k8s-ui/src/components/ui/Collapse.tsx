import { useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'

// Animated show/hide using the grid-template-rows 0fr→1fr technique — the
// app-wide standard for expand/collapse (drawers, checks, audit, resources
// sidebar, port-forward). The inner overflow-hidden wrapper is load-bearing:
// it clips the content while the grid row animates between 0 and its natural
// height, so nothing spills before the row is fully open.
//
// Content stays mounted while closed (clipped to zero height) — that's what
// lets it animate. `inert` when closed keeps that clipped content out of the
// tab order and the accessibility tree, so keyboard/screen-reader users don't
// land on invisible rows (matching ChecksView's collapse).
//
// `mountLazily` defers rendering the children until the first open, then keeps
// them mounted so both the open and close animations still run. Use it when the
// collapsed content is heavy and there are many instances (e.g. hundreds of
// resource rows, each with a drift panel) — otherwise every collapsed instance
// pays its render cost up front. Off by default so the common single-instance
// case keeps the simplest behavior.
export function Collapse({
  open,
  children,
  className,
  mountLazily = false,
}: {
  open: boolean
  children: ReactNode
  className?: string
  mountLazily?: boolean
}) {
  // Latch: once opened, stay mounted. Conditional setState-in-render is the
  // supported pattern for deriving state from props without an extra commit.
  const [hasOpened, setHasOpened] = useState(open)
  if (mountLazily && open && !hasOpened) setHasOpened(true)
  const render = !mountLazily || hasOpened
  return (
    <div
      className={clsx('grid transition-[grid-template-rows] duration-200 ease-out', className)}
      style={{ gridTemplateRows: open ? '1fr' : '0fr' }}
    >
      <div className="overflow-hidden" inert={!open || undefined}>{render ? children : null}</div>
    </div>
  )
}

// CollapseChevron is the disclosure caret that pairs with <Collapse>: a single
// ChevronRight that rotates 90° when open, rather than swapping between two
// icons. Matches the drawer Section / ExpandableSection affordance so every
// collapsible surface animates the same way.
export function CollapseChevron({ open, className }: { open: boolean; className?: string }) {
  return (
    <ChevronRight
      aria-hidden="true"
      className={clsx('shrink-0 text-theme-text-tertiary transition-transform duration-200', open && 'rotate-90', className)}
    />
  )
}
