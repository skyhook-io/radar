import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Collapse, CollapseChevron, useDisclosure } from '@skyhook-io/k8s-ui/components/ui/Collapse'

// In-flow disclosure with the app-standard motion — the replacement for native
// <details>/<summary> on primary paths. Native details snaps open with no
// transition and its marker ignores the caret language; this pairs a button
// header (aria-expanded / aria-controls via useDisclosure) with CollapseChevron
// and an animated Collapse panel. Content stays mounted while closed, exactly
// like native details, so server-rendered markup and in-page search still see
// the collapsed copy.
export function Disclosure({
  summary,
  children,
  className,
  summaryClassName,
  chevronClassName = 'h-3.5 w-3.5',
  defaultOpen = false,
}: {
  summary: ReactNode
  children: ReactNode
  /** Wrapper (what the old <details> carried). */
  className?: string
  /** Header button (what the old <summary> carried). */
  summaryClassName?: string
  chevronClassName?: string
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const disclosure = useDisclosure(open)
  return (
    <div className={className}>
      <button
        type="button"
        {...disclosure.buttonProps}
        onClick={() => setOpen((value) => !value)}
        className={clsx('flex w-full cursor-pointer items-center gap-1.5 text-left', summaryClassName)}
      >
        <CollapseChevron open={open} className={chevronClassName} />
        {summary}
      </button>
      <Collapse open={open} id={disclosure.panelId}>
        {children}
      </Collapse>
    </div>
  )
}
