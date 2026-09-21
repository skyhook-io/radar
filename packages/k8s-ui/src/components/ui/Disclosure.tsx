import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'

// Disclosure — in-flow expand/collapse with the app-standard motion; the
// replacement for native <details>/<summary> on primary paths. Native details
// snaps open with no transition and its marker ignores the caret language;
// this pairs a button header (aria-expanded / aria-controls via useDisclosure)
// with CollapseChevron and an animated Collapse panel. Shared by Radar and
// Radar Cloud so both open alike.
//
// Content stays mounted while closed (Collapse's default), exactly like native
// details — server-rendered markup and in-page search still see the collapsed
// copy. Sites whose panel polls or holds an editor should use
// <Collapse unmountOnExit> directly instead.
//
// Uncontrolled by default (`defaultOpen`); pass `open` + `onOpenChange` to
// control it from above (a form that keys other state on the disclosure).
// The chevron takes currentColor from the header by default: a summary's
// caret should read as part of its text, including on hover.
export function Disclosure({
  summary,
  children,
  className,
  summaryClassName,
  chevronClassName = 'h-3.5 w-3.5',
  inheritColor = true,
  defaultOpen = false,
  open: controlledOpen,
  onOpenChange,
}: {
  summary: ReactNode
  children: ReactNode
  /** Classes for the wrapper around header and panel. */
  className?: string
  /** Classes for the header button (layout, size and color of the summary). */
  summaryClassName?: string
  chevronClassName?: string
  /** False pins the caret to the tertiary grey regardless of the header's color. */
  inheritColor?: boolean
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(defaultOpen)
  const open = controlledOpen ?? uncontrolledOpen
  const disclosure = useDisclosure(open)
  const toggle = () => {
    const next = !open
    if (controlledOpen === undefined) setUncontrolledOpen(next)
    onOpenChange?.(next)
  }
  return (
    <div className={className}>
      <button
        type="button"
        {...disclosure.buttonProps}
        onClick={toggle}
        className={clsx('flex w-full cursor-pointer select-none items-center gap-1.5 text-left', summaryClassName)}
      >
        <CollapseChevron open={open} inheritColor={inheritColor} className={chevronClassName} />
        {summary}
      </button>
      <Collapse open={open} id={disclosure.panelId}>
        {children}
      </Collapse>
    </div>
  )
}
